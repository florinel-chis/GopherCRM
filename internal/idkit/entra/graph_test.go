package entra

import (
	"context"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/idkit/oidc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testClient is the production issuer client trusting server's certificate.
func testClient(server *httptest.Server) *http.Client {
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	return oidc.NewHTTPClient(pool)
}

// TestOIDCResolveEntraGroups covers the Graph failure modes the sign-in
// subtests cannot tell apart: an http nextLink on the Graph host is refused
// before any request, a value that is not an array of strings and a page
// over 1 MiB fail, and an empty access token sends nothing.
func TestOIDCResolveEntraGroups(t *testing.T) {
	var (
		mu       sync.Mutex
		requests int
		answer   string
	)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		requests++
		body := answer
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	resolver := GroupResolver{BaseURL: server.URL, Timeout: 2 * time.Second, Client: testClient(server)}
	plainHost := "http://" + strings.TrimPrefix(server.URL, "https://")

	cases := []struct {
		name     string
		token    string
		answer   string
		want     []string
		requests int
		err      error
	}{
		{"ids", "t", `{"value":["a","b"]}`, []string{"a", "b"}, 1, nil},
		{"empty_list", "t", `{"value":[]}`, []string{}, 1, nil},
		{"no_access_token", "", `{"value":["a"]}`, nil, 0, ErrNoAccessToken},
		{"http_next_link_same_host", "t", `{"value":["a"],"@odata.nextLink":"` + plainHost + `/v1.0/me/getMemberObjects?page=1"}`, nil, 1, ErrNextLink},
		{"value_numbers", "t", `{"value":[1]}`, nil, 1, ErrBody},
		{"value_null_element", "t", `{"value":[null]}`, nil, 1, ErrBody},
		{"value_null", "t", `{"value":null}`, nil, 1, ErrBody},
		{"value_missing", "t", `{}`, nil, 1, ErrBody},
		{"value_object", "t", `{"value":{"a":"b"}}`, nil, 1, ErrBody},
		{"not_json", "t", `<html>`, nil, 1, ErrBody},
		{"over_one_mib", "t", `{"value":["` + strings.Repeat("a", 1<<20) + `"]}`, nil, 1, ErrTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mu.Lock()
			requests, answer = 0, tc.answer
			mu.Unlock()
			got, err := resolver.MemberObjects(context.Background(), tc.token, 10)
			mu.Lock()
			assert.Equal(t, tc.requests, requests, "requests sent")
			mu.Unlock()
			if tc.want == nil {
				assert.Error(t, err)
				assert.ErrorIs(t, err, tc.err)
				assert.Nil(t, got)
				if err != nil {
					assert.NotContains(t, err.Error(), server.URL, "the error names no URL")
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestEntraGroupResolverLimits pins the page count, the client, origin,
// status and deadline limits of MemberObjects against a Graph whose every
// page links to a next one on the same origin.
func TestEntraGroupResolverLimits(t *testing.T) {
	var (
		mu       sync.Mutex
		requests int
		status   = http.StatusOK
	)
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		requests++
		page, code := requests, status
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = fmt.Fprintf(w, `{"value":["g%d"],"@odata.nextLink":"%s%s?page=%d"}`, page, server.URL, memberObjectsPath, page+1)
	}))
	t.Cleanup(server.Close)
	client := testClient(server)
	reset := func(code int) {
		mu.Lock()
		requests, status = 0, code
		mu.Unlock()
	}
	sent := func() int {
		mu.Lock()
		defer mu.Unlock()
		return requests
	}

	for _, tc := range []struct {
		name     string
		maxPages int
		requests int
	}{
		{"max_pages_2", 2, 2},
		{"max_pages_0_default", 0, DefaultMaxPages},
		{"max_pages_51_default", MaxPagesLimit + 1, DefaultMaxPages},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reset(http.StatusOK)
			got, err := GroupResolver{BaseURL: server.URL, Timeout: 5 * time.Second, Client: client}.MemberObjects(context.Background(), "t", tc.maxPages)
			assert.ErrorIs(t, err, ErrTooManyPages)
			assert.Nil(t, got)
			assert.Equal(t, tc.requests, sent(), "requests sent")
		})
	}

	t.Run("nil_client", func(t *testing.T) {
		reset(http.StatusOK)
		_, err := GroupResolver{BaseURL: server.URL, Timeout: 5 * time.Second}.MemberObjects(context.Background(), "t", 2)
		assert.ErrorIs(t, err, ErrRequest)
		assert.Equal(t, 0, sent(), "requests sent")
	})

	t.Run("http_base_url", func(t *testing.T) {
		reset(http.StatusOK)
		plain := "http://" + strings.TrimPrefix(server.URL, "https://")
		_, err := GroupResolver{BaseURL: plain, Timeout: 5 * time.Second, Client: client}.MemberObjects(context.Background(), "t", 2)
		assert.ErrorIs(t, err, ErrNextLink)
		assert.Equal(t, 0, sent(), "requests sent")
	})

	t.Run("non_200", func(t *testing.T) {
		reset(http.StatusForbidden)
		_, err := GroupResolver{BaseURL: server.URL, Timeout: 5 * time.Second, Client: client}.MemberObjects(context.Background(), "t", 2)
		assert.ErrorIs(t, err, ErrStatus)
		assert.Equal(t, 1, sent(), "requests sent")
	})

	t.Run("deadline", func(t *testing.T) {
		release := make(chan struct{})
		blocking := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-release:
			}
		}))
		// Registered after Close, so it runs first: the handler returns
		// before Close waits for it.
		t.Cleanup(blocking.Close)
		t.Cleanup(func() { close(release) })

		started := time.Now()
		_, err := GroupResolver{BaseURL: blocking.URL, Timeout: 200 * time.Millisecond, Client: testClient(blocking)}.MemberObjects(context.Background(), "t", 2)
		assert.ErrorIs(t, err, ErrRequest)
		assert.Less(t, time.Since(started), 2*time.Second, "the deadline ends the call")
	})
}
