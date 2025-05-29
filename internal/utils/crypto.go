package utils

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// HashAPIKey hashes an API key for secure storage
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