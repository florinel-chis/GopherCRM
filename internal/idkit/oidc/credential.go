package oidc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"
)

// The client assertion of RFC 7523 (private_key_jwt).
const (
	ClientAssertionType     = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"
	ClientAssertionLifetime = 5 * time.Minute
)

// ClientCertificate is the certificate and RSA key a client authenticates
// with instead of a secret.
type ClientCertificate struct {
	Certificate *x509.Certificate
	PrivateKey  *rsa.PrivateKey
}

// Client is the OAuth 2.0 client of a sign-in: its id, its secret or
// certificate, the redirect URL and the requested scopes.
type Client struct {
	ClientID, ClientSecret, RedirectURL string
	Scopes                              []string
	Certificate                         *ClientCertificate
}

// OAuth2Config is the OAuth 2.0 configuration of c against p. With a client
// certificate it carries no secret and sends client_id in the form, so the
// token request authenticates only with the client assertion.
func (c Client) OAuth2Config(p *gooidc.Provider) *oauth2.Config {
	out := &oauth2.Config{
		ClientID:     c.ClientID,
		ClientSecret: c.ClientSecret,
		Endpoint:     p.Endpoint(),
		RedirectURL:  c.RedirectURL,
		Scopes:       c.Scopes,
	}
	if c.Certificate != nil {
		out.ClientSecret = ""
		out.Endpoint.AuthStyle = oauth2.AuthStyleInParams
	}
	return out
}

// Exchange redeems the authorization code with the PKCE verifier. With a
// client certificate the request carries a fresh client assertion signed at
// now; a signing failure fails the exchange.
func (c Client) Exchange(ctx context.Context, p *gooidc.Provider, code, verifier string, now time.Time) (*oauth2.Token, error) {
	options := []oauth2.AuthCodeOption{oauth2.VerifierOption(verifier)}
	if c.Certificate != nil {
		assertion, err := ClientAssertion(c.Certificate, c.ClientID, p.Endpoint().TokenURL, now)
		if err != nil {
			return nil, err
		}
		options = append(options,
			oauth2.SetAuthURLParam("client_assertion_type", ClientAssertionType),
			oauth2.SetAuthURLParam("client_assertion", assertion))
	}
	return c.OAuth2Config(p).Exchange(ctx, code, options...)
}

// ClientAssertion signs the client assertion of one code exchange: PS256
// over the certificate's key, the certificate named by its x5t and x5t#S256
// thumbprints, aud the token endpoint, iss and sub the client id, a fresh
// jti, valid from now for ClientAssertionLifetime.
func ClientAssertion(cert *ClientCertificate, clientID, tokenURL string, now time.Time) (string, error) {
	jti := make([]byte, 16)
	if _, err := rand.Read(jti); err != nil {
		return "", fmt.Errorf("generate the client assertion id: %w", err)
	}
	now = now.UTC()
	token := jwt.NewWithClaims(jwt.SigningMethodPS256, jwt.RegisteredClaims{
		Issuer:    clientID,
		Subject:   clientID,
		Audience:  jwt.ClaimStrings{tokenURL},
		ID:        hex.EncodeToString(jti),
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(ClientAssertionLifetime)),
	})
	sha1Sum := sha1.Sum(cert.Certificate.Raw)
	sha256Sum := sha256.Sum256(cert.Certificate.Raw)
	token.Header["typ"] = "JWT"
	token.Header["x5t"] = base64.RawURLEncoding.EncodeToString(sha1Sum[:])
	token.Header["x5t#S256"] = base64.RawURLEncoding.EncodeToString(sha256Sum[:])
	signed, err := token.SignedString(cert.PrivateKey)
	if err != nil {
		return "", fmt.Errorf("sign the client assertion: %w", err)
	}
	return signed, nil
}
