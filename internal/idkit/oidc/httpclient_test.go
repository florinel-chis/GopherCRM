package oidc

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOIDCHTTPClient(t *testing.T) {
	client := NewHTTPClient(nil)
	assert.Greater(t, client.Timeout, time.Duration(0), "a non-zero timeout")
	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Nil(t, transport.Proxy, "HTTP(S)_PROXY is ignored")

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	t.Cleanup(redirecting.Close)
	response, err := client.Get(redirecting.URL)
	require.NoError(t, err)
	_ = response.Body.Close()
	assert.Equal(t, http.StatusFound, response.StatusCode, "the redirect is not followed")
}
