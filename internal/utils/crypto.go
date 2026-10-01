package utils

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// HashAPIKey hashes an API key using plain SHA256 (legacy, kept for migration).
// Deprecated: Use HashAPIKeyHMAC for new keys.
func HashAPIKey(key string) string {
	// Remove prefix if present
	if strings.HasPrefix(key, "gcrm_") {
		key = strings.TrimPrefix(key, "gcrm_")
	}

	// Create SHA256 hash
	hasher := sha256.New()
	hasher.Write([]byte(key))
	return hex.EncodeToString(hasher.Sum(nil))
}

// HashAPIKeyHMAC hashes an API key using HMAC-SHA256 with a secret.
// The result is prefixed with "hmac$" to distinguish from legacy SHA256 hashes.
func HashAPIKeyHMAC(key, secret string) string {
	// Remove prefix if present
	if strings.HasPrefix(key, "gcrm_") {
		key = strings.TrimPrefix(key, "gcrm_")
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(key))
	return "hmac$" + hex.EncodeToString(mac.Sum(nil))
}
