package oidc

import (
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func TestAuthRequest(t *testing.T) {
	base64URL := regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	first, err := NewAuthRequest()
	require.NoError(t, err)
	second, err := NewAuthRequest()
	require.NoError(t, err)
	for _, req := range []AuthRequest{first, second} {
		assert.Regexp(t, base64URL, req.State)
		assert.Regexp(t, base64URL, req.Nonce)
		assert.NotEmpty(t, req.Verifier)
		assert.NotEqual(t, req.State, req.Nonce)
	}
	assert.NotEqual(t, first.State, second.State)
	assert.NotEqual(t, first.Nonce, second.Nonce)
	assert.NotEqual(t, first.Verifier, second.Verifier)

	cfg := &oauth2.Config{
		ClientID:    "client",
		RedirectURL: "https://app.example.test/auth/callback",
		Endpoint:    oauth2.Endpoint{AuthURL: "https://idp.example.test/authorize"},
	}
	parsed, err := url.Parse(AuthCodeURL(cfg, first))
	require.NoError(t, err)
	query := parsed.Query()
	assert.Equal(t, first.State, query.Get("state"))
	assert.Equal(t, first.Nonce, query.Get("nonce"))
	assert.Equal(t, "S256", query.Get("code_challenge_method"))
	sum := sha256.Sum256([]byte(first.Verifier))
	assert.Equal(t, base64.RawURLEncoding.EncodeToString(sum[:]), query.Get("code_challenge"))
	assert.Empty(t, query.Get("code_verifier"), "the verifier itself is never sent")

	assert.False(t, NonceMatches("", ""), "an absent nonce never matches")
	assert.True(t, NonceMatches("a", "a"))
	assert.False(t, NonceMatches("a", "b"))
	assert.True(t, BindingMatches("a", "a"))
	assert.False(t, BindingMatches("a", "b"))
}
