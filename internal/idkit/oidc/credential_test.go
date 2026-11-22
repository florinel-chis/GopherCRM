package oidc

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"regexp"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientAssertion(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "client assertion test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)

	now := time.Date(2026, 11, 20, 10, 30, 0, 0, time.FixedZone("plus2", 2*60*60))
	signed, err := ClientAssertion(&ClientCertificate{Certificate: cert, PrivateKey: key}, "client-id", "https://idp.example.test/token", now)
	require.NoError(t, err)

	claims := &jwt.RegisteredClaims{}
	token, err := jwt.ParseWithClaims(signed, claims, func(*jwt.Token) (any, error) { return &key.PublicKey, nil },
		jwt.WithValidMethods([]string{"PS256"}), jwt.WithTimeFunc(func() time.Time { return now }))
	require.NoError(t, err)
	require.True(t, token.Valid)

	sha1Sum := sha1.Sum(der)
	sha256Sum := sha256.Sum256(der)
	assert.Equal(t, "JWT", token.Header["typ"])
	assert.Equal(t, base64.RawURLEncoding.EncodeToString(sha1Sum[:]), token.Header["x5t"])
	assert.Equal(t, base64.RawURLEncoding.EncodeToString(sha256Sum[:]), token.Header["x5t#S256"])
	assert.Equal(t, "client-id", claims.Issuer)
	assert.Equal(t, "client-id", claims.Subject)
	assert.Equal(t, jwt.ClaimStrings{"https://idp.example.test/token"}, claims.Audience)
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{32}$`), claims.ID)
	require.NotNil(t, claims.IssuedAt)
	require.NotNil(t, claims.NotBefore)
	require.NotNil(t, claims.ExpiresAt)
	assert.True(t, claims.IssuedAt.Equal(now.Truncate(time.Second)))
	assert.True(t, claims.NotBefore.Equal(now.Truncate(time.Second)))
	assert.True(t, claims.ExpiresAt.Equal(now.Add(5*time.Minute).Truncate(time.Second)))
	assert.Equal(t, 5*time.Minute, ClientAssertionLifetime)

	other, err := ClientAssertion(&ClientCertificate{Certificate: cert, PrivateKey: key}, "client-id", "https://idp.example.test/token", now)
	require.NoError(t, err)
	otherClaims := &jwt.RegisteredClaims{}
	_, _, err = jwt.NewParser().ParseUnverified(other, otherClaims)
	require.NoError(t, err)
	assert.NotEqual(t, claims.ID, otherClaims.ID, "a fresh jti per assertion")
}
