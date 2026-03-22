package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strconv"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newCSRFAuthService() *authService {
	return &authService{
		jwtConfig: config.JWTConfig{
			Secret:      "test-secret-key-for-csrf-testing-min32chars!",
			ExpiryHours: 24,
		},
	}
}

func TestCSRF_ValidTokenRoundtrip(t *testing.T) {
	svc := newCSRFAuthService()

	token, err := svc.GenerateCSRFToken()
	require.NoError(t, err)
	assert.NotEmpty(t, token)

	assert.True(t, svc.ValidateCSRFToken(token), "freshly generated token should be valid")
}

func TestCSRF_ExpiredToken(t *testing.T) {
	svc := newCSRFAuthService()

	// Manually craft a token with a timestamp 25 hours in the past
	nonce := "deadbeef12345678deadbeef12345678"
	pastTime := time.Now().Add(-25 * time.Hour).Unix()
	timestamp := strconv.FormatInt(pastTime, 10)
	message := nonce + ":" + timestamp

	mac := hmac.New(sha256.New, []byte(svc.jwtConfig.Secret))
	mac.Write([]byte(message))
	sig := hex.EncodeToString(mac.Sum(nil))

	raw := message + "." + sig
	token := base64.RawURLEncoding.EncodeToString([]byte(raw))

	assert.False(t, svc.ValidateCSRFToken(token), "expired token (>24h) should be rejected")
}

func TestCSRF_TamperedToken(t *testing.T) {
	svc := newCSRFAuthService()

	token, err := svc.GenerateCSRFToken()
	require.NoError(t, err)

	// Decode, tamper with the message, re-encode
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	require.NoError(t, err)

	// Flip a character in the decoded content to tamper
	tampered := make([]byte, len(decoded))
	copy(tampered, decoded)
	// Change first byte of the nonce
	if tampered[0] == 'a' {
		tampered[0] = 'b'
	} else {
		tampered[0] = 'a'
	}
	tamperedToken := base64.RawURLEncoding.EncodeToString(tampered)

	assert.False(t, svc.ValidateCSRFToken(tamperedToken), "tampered token should be rejected")
}

func TestCSRF_EmptyToken(t *testing.T) {
	svc := newCSRFAuthService()

	assert.False(t, svc.ValidateCSRFToken(""), "empty token should be rejected")
}

func TestCSRF_MalformedTokens(t *testing.T) {
	svc := newCSRFAuthService()

	tests := []struct {
		name  string
		token string
	}{
		{"random string", "not-a-valid-token"},
		{"valid base64 but bad format", base64.RawURLEncoding.EncodeToString([]byte("no-dot-separator"))},
		{"missing timestamp", base64.RawURLEncoding.EncodeToString([]byte("nonce.signature"))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.False(t, svc.ValidateCSRFToken(tt.token))
		})
	}
}

func TestCSRF_DifferentSecretsReject(t *testing.T) {
	svc1 := newCSRFAuthService()
	svc2 := &authService{
		jwtConfig: config.JWTConfig{
			Secret:      "a-completely-different-secret-key-here!!",
			ExpiryHours: 24,
		},
	}

	token, err := svc1.GenerateCSRFToken()
	require.NoError(t, err)

	assert.True(t, svc1.ValidateCSRFToken(token), "original service should validate its own token")
	assert.False(t, svc2.ValidateCSRFToken(token), "different secret should reject the token")
}
