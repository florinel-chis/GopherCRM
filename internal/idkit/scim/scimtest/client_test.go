package scimtest_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/idkit/scim"
	"github.com/florinel-chis/gophercrm/internal/idkit/scim/scimtest"
)

// recorder keeps the last request it served.
type recorder struct {
	header http.Header
	remote string
	body   string
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	r.header = request.Header.Clone()
	r.remote = request.RemoteAddr
	buf := make([]byte, 64)
	n, _ := request.Body.Read(buf)
	r.body = string(buf[:n])
	w.Header().Set("Content-Type", scim.ContentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"id":"u-1"}`))
}

func TestClientHeaders(t *testing.T) {
	cases := []struct {
		name, token, body string
	}{
		{"token_and_body", "secret-token", `{"userName":"a"}`},
		{"token_no_body", "secret-token", ""},
		{"no_token_body", "", `{"userName":"a"}`},
		{"no_token_no_body", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := &recorder{}
			client := scimtest.New(handler, tc.token)
			client.Prepare = func(request *http.Request) {
				request.RemoteAddr = "192.0.2.7:1234"
				request.Header.Set("X-Request-ID", "req-1")
			}
			response := client.Do(http.MethodPost, "/scim/v2/Users", tc.body)
			require.Equal(t, http.StatusOK, response.Status)
			assert.Equal(t, scim.ContentType, response.Header.Get("Content-Type"))
			var decoded struct {
				ID string `json:"id"`
			}
			require.NoError(t, response.Decode(&decoded))
			assert.Equal(t, "u-1", decoded.ID)

			assert.Equal(t, scim.ContentType, handler.header.Get("Accept"))
			if tc.token != "" {
				assert.Equal(t, "Bearer "+tc.token, handler.header.Get("Authorization"))
			} else {
				assert.NotContains(t, handler.header, "Authorization")
			}
			if tc.body != "" {
				assert.Equal(t, scim.ContentType, handler.header.Get("Content-Type"))
			} else {
				assert.NotContains(t, handler.header, "Content-Type")
			}
			assert.Equal(t, tc.body, handler.body)
			assert.Equal(t, "192.0.2.7:1234", handler.remote)
			assert.Equal(t, "req-1", handler.header.Get("X-Request-ID"))
			_, isError := response.SCIMError()
			assert.False(t, isError, "a resource is not a SCIM error")
		})
	}
}

func TestResponseSCIMError(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		scim.WriteErrorType(w, http.StatusBadRequest, "invalidFilter", "Only one filter")
	})
	response := scimtest.New(handler, "t").Do(http.MethodGet, "/scim/v2/Users?filter=x", "")
	assert.Equal(t, http.StatusBadRequest, response.Status)
	body, ok := response.SCIMError()
	require.True(t, ok)
	assert.Equal(t, []string{scim.ErrorSchema}, body.Schemas)
	assert.Equal(t, "400", body.Status)
	assert.Equal(t, "invalidFilter", body.ScimType)
	assert.Equal(t, "Only one filter", body.Detail)

	plain := scimtest.Response{Body: []byte("not json")}
	_, ok = plain.SCIMError()
	assert.False(t, ok)
}
