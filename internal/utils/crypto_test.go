package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHashAPIKeyHMAC(t *testing.T) {
	t.Run("produces hmac$ prefixed hash", func(t *testing.T) {
		hash := HashAPIKeyHMAC("my-api-key", "my-secret")
		assert.True(t, IsHMACHash(hash))
		assert.Contains(t, hash, "hmac$")
	})

	t.Run("strips gcrm_ prefix before hashing", func(t *testing.T) {
		hashWithPrefix := HashAPIKeyHMAC("gcrm_my-api-key", "my-secret")
		hashWithoutPrefix := HashAPIKeyHMAC("my-api-key", "my-secret")
		assert.Equal(t, hashWithPrefix, hashWithoutPrefix)
	})

	t.Run("different secrets produce different hashes", func(t *testing.T) {
		hash1 := HashAPIKeyHMAC("my-api-key", "secret-one")
		hash2 := HashAPIKeyHMAC("my-api-key", "secret-two")
		assert.NotEqual(t, hash1, hash2)
	})

	t.Run("different keys produce different hashes", func(t *testing.T) {
		hash1 := HashAPIKeyHMAC("key-one", "my-secret")
		hash2 := HashAPIKeyHMAC("key-two", "my-secret")
		assert.NotEqual(t, hash1, hash2)
	})

	t.Run("same inputs produce same hash (deterministic)", func(t *testing.T) {
		hash1 := HashAPIKeyHMAC("my-api-key", "my-secret")
		hash2 := HashAPIKeyHMAC("my-api-key", "my-secret")
		assert.Equal(t, hash1, hash2)
	})
}

func TestVerifyAPIKeyHMAC(t *testing.T) {
	secret := "test-secret-for-hmac"
	key := "test-api-key-value"

	t.Run("verifies valid key against its hash", func(t *testing.T) {
		hash := HashAPIKeyHMAC(key, secret)
		assert.True(t, VerifyAPIKeyHMAC(key, hash, secret))
	})

	t.Run("rejects wrong key", func(t *testing.T) {
		hash := HashAPIKeyHMAC(key, secret)
		assert.False(t, VerifyAPIKeyHMAC("wrong-key", hash, secret))
	})

	t.Run("rejects wrong secret", func(t *testing.T) {
		hash := HashAPIKeyHMAC(key, secret)
		assert.False(t, VerifyAPIKeyHMAC(key, hash, "wrong-secret"))
	})

	t.Run("rejects tampered hash", func(t *testing.T) {
		hash := HashAPIKeyHMAC(key, secret)
		tampered := hash[:len(hash)-1] + "X"
		assert.False(t, VerifyAPIKeyHMAC(key, tampered, secret))
	})

	t.Run("works with gcrm_ prefix", func(t *testing.T) {
		hash := HashAPIKeyHMAC("gcrm_"+key, secret)
		assert.True(t, VerifyAPIKeyHMAC("gcrm_"+key, hash, secret))
		assert.True(t, VerifyAPIKeyHMAC(key, hash, secret))
	})
}

func TestIsHMACHash(t *testing.T) {
	t.Run("identifies HMAC hash", func(t *testing.T) {
		hash := HashAPIKeyHMAC("key", "secret")
		assert.True(t, IsHMACHash(hash))
	})

	t.Run("rejects legacy SHA256 hash", func(t *testing.T) {
		hash := HashAPIKey("key")
		assert.False(t, IsHMACHash(hash))
	})

	t.Run("rejects empty string", func(t *testing.T) {
		assert.False(t, IsHMACHash(""))
	})
}

func TestHashAPIKey_Legacy(t *testing.T) {
	t.Run("legacy hash differs from HMAC hash", func(t *testing.T) {
		legacyHash := HashAPIKey("my-api-key")
		hmacHash := HashAPIKeyHMAC("my-api-key", "any-secret")
		assert.NotEqual(t, legacyHash, hmacHash)
	})

	t.Run("legacy hash is not prefixed with hmac$", func(t *testing.T) {
		hash := HashAPIKey("my-api-key")
		assert.False(t, IsHMACHash(hash))
	})

	t.Run("strips gcrm_ prefix", func(t *testing.T) {
		hash1 := HashAPIKey("gcrm_my-api-key")
		hash2 := HashAPIKey("my-api-key")
		assert.Equal(t, hash1, hash2)
	})
}

func TestMigrationPath(t *testing.T) {
	t.Run("can distinguish legacy from HMAC hashes", func(t *testing.T) {
		legacyHash := HashAPIKey("some-key")
		hmacHash := HashAPIKeyHMAC("some-key", "some-secret")

		// Legacy hashes are plain hex - no prefix
		assert.False(t, IsHMACHash(legacyHash))
		// HMAC hashes have the hmac$ prefix
		assert.True(t, IsHMACHash(hmacHash))
	})
}
