package oidc

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// authRequestValueBytes is the number of random bytes of a state or a nonce.
const authRequestValueBytes = 32

// AuthRequest holds the per-sign-in values of an authorization request: the
// state, the nonce and the PKCE verifier.
type AuthRequest struct {
	State, Nonce, Verifier string
}

// NewAuthRequest draws fresh values: State and Nonce are each 32 random
// bytes in raw base64url, Verifier is an x/oauth2 PKCE verifier.
func NewAuthRequest() (AuthRequest, error) {
	state, err := randomValue()
	if err != nil {
		return AuthRequest{}, err
	}
	nonce, err := randomValue()
	if err != nil {
		return AuthRequest{}, err
	}
	return AuthRequest{State: state, Nonce: nonce, Verifier: oauth2.GenerateVerifier()}, nil
}

func randomValue() (string, error) {
	buf := make([]byte, authRequestValueBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("failed to generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// AuthCodeURL is the issuer's authorization URL carrying the request's
// state, nonce and S256 code challenge.
func AuthCodeURL(cfg *oauth2.Config, req AuthRequest) string {
	return cfg.AuthCodeURL(req.State, gooidc.Nonce(req.Nonce), oauth2.S256ChallengeOption(req.Verifier))
}

// NonceMatches reports whether an ID token's nonce is present and equals the
// expected one, compared in constant time.
func NonceMatches(tokenNonce, expected string) bool {
	return tokenNonce != "" && subtle.ConstantTimeCompare([]byte(tokenNonce), []byte(expected)) == 1
}

// BindingMatches reports whether the browser binding equals the state,
// compared in constant time.
func BindingMatches(binding, state string) bool {
	return subtle.ConstantTimeCompare([]byte(binding), []byte(state)) == 1
}
