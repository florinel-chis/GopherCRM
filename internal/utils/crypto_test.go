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

// The expected values below were computed outside Go:
//
//	printf '%s' test-api-key-value | openssl dgst -sha256 -hmac test-secret-for-hmac
//	printf '%s' test-api-key-value | openssl dgst -sha256 -hmac wrong-secret
//	printf '%s' test-api-key-value | shasum -a 256
//
// Stored API key hashes depend on these exact outputs, so any change to the
// hashing breaks every issued key.
const (
	vectorKey             = "test-api-key-value"
	vectorSecret          = "test-secret-for-hmac"
	vectorHMAC            = "hmac$a6f6563d9982bc355812f93f148588e85cc4f92c0fd7bed8e93e7e7164c6323c"
	vectorHMACOtherSecret = "hmac$97d323897ac8141e0b33eeb88c2623df1f6098e3c41be11a4e7d07279ce98830"
	vectorLegacySHA256    = "5b918bc91303c604e6bdfdad525f6a7b41bd905703e47c377b6998f61f9fc9d9"
)

func TestHashAPIKeyHMAC_FixedVector(t *testing.T) {
	t.Run("matches the reference HMAC-SHA256 with the hmac$ prefix", func(t *testing.T) {
		assert.Equal(t, vectorHMAC, HashAPIKeyHMAC(vectorKey, vectorSecret))
	})

	t.Run("strips the gcrm_ prefix before hashing", func(t *testing.T) {
		assert.Equal(t, vectorHMAC, HashAPIKeyHMAC("gcrm_"+vectorKey, vectorSecret))
	})

	t.Run("is keyed by the secret", func(t *testing.T) {
		assert.Equal(t, vectorHMACOtherSecret, HashAPIKeyHMAC(vectorKey, "wrong-secret"))
	})
}

func TestHashAPIKey_FixedVector(t *testing.T) {
	t.Run("matches the reference SHA-256 without a prefix", func(t *testing.T) {
		assert.Equal(t, vectorLegacySHA256, HashAPIKey(vectorKey))
	})

	t.Run("strips the gcrm_ prefix before hashing", func(t *testing.T) {
		assert.Equal(t, vectorLegacySHA256, HashAPIKey("gcrm_"+vectorKey))
	})
}
