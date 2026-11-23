package oidc_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	idkitoidc "github.com/florinel-chis/gophercrm/internal/idkit/oidc"
	"github.com/florinel-chis/gophercrm/internal/idkit/oidctest"
)

func discover(t *testing.T, issuer *oidctest.Issuer) *idkitoidc.Discovery {
	t.Helper()
	cache := idkitoidc.NewProviderCache(idkitoidc.NewHTTPClient(issuer.RootCAs()), time.Now)
	discovered, err := cache.Get(context.Background(), idkitoidc.ProviderKey{Issuer: issuer.Issuer()})
	require.NoError(t, err)
	return discovered
}

// TestEndSessionEndpoint: only an absolute https URL without a fragment
// counts; anything else is "".
func TestEndSessionEndpoint(t *testing.T) {
	issuer := oidctest.New(t)
	assert.Equal(t, issuer.URL()+oidctest.EndSessionPath, discover(t, issuer).EndSessionEndpoint())
	for raw, want := range map[string]string{
		"https://idp.example.test/logout?tenant=t1": "https://idp.example.test/logout?tenant=t1",
		"http://idp.example.test/logout":            "",
		"https://idp.example.test/logout#frag":      "",
		"/logout":                                   "",
		"https:opaque":                              "",
		"https://":                                  "",
	} {
		issuer.SetEndSessionEndpoint(raw)
		assert.Equal(t, want, discover(t, issuer).EndSessionEndpoint(), raw)
	}
	issuer.OmitEndSessionEndpoint(true)
	assert.Equal(t, "", discover(t, issuer).EndSessionEndpoint(), "absent")
}

// TestVerifyHint: an expired hint for the client verifies; another audience,
// an unpublished key or another issuer does not.
func TestVerifyHint(t *testing.T) {
	issuer := oidctest.New(t)
	discovered := discover(t, issuer)
	expired := map[string]interface{}{"exp": time.Now().Add(-time.Hour).Unix(), "iat": time.Now().Add(-2 * time.Hour).Unix()}
	ctx := context.Background()

	token, err := discovered.VerifyHint(ctx, issuer.ClientID(), issuer.IDToken(t, oidctest.Claims{Subject: "s1", Extra: expired}))
	require.NoError(t, err, "an expired hint verifies")
	assert.Equal(t, "s1", token.Subject)

	_, err = discovered.VerifyHint(ctx, issuer.ClientID(), issuer.IDToken(t, oidctest.Claims{Subject: "s1", Audience: "other"}))
	assert.Error(t, err, "another audience")
	_, err = discovered.VerifyHint(ctx, issuer.ClientID(), issuer.IDToken(t, oidctest.Claims{Subject: "s1", UnpublishedKey: true}))
	assert.Error(t, err, "another key")
	_, err = discovered.VerifyHint(ctx, issuer.ClientID(), issuer.IDToken(t, oidctest.Claims{Subject: "s1",
		Extra: map[string]interface{}{"iss": "https://other.example.test"}}))
	assert.Error(t, err, "another issuer")
}
