package utils

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The master secrets below are obviously fake and exist only so the tests have
// two distinct keys to compare against each other.
const (
	testMasterSecret      = "unit-test-master-secret-0000000000"
	testOtherMasterSecret = "unit-test-other-master-secret-1111"
	testContext           = "configuration-secret"
)

func TestSecretBoxRoundTrip(t *testing.T) {
	box := NewSecretBox(testMasterSecret, testContext)

	sealed, err := box.Seal("not-a-real-api-key")
	require.NoError(t, err)
	assert.True(t, IsSealed(sealed), "sealed value must carry the version prefix: %s", sealed)
	assert.NotContains(t, sealed, "not-a-real-api-key")

	opened, err := box.Open(sealed)
	require.NoError(t, err)
	assert.Equal(t, "not-a-real-api-key", opened)
}

// Sealing the same plaintext twice must not produce the same ciphertext:
// a repeated nonce would leak that two entries hold the same secret.
func TestSecretBoxSealUsesAFreshNonce(t *testing.T) {
	box := NewSecretBox(testMasterSecret, testContext)

	first, err := box.Seal("not-a-real-api-key")
	require.NoError(t, err)
	second, err := box.Seal("not-a-real-api-key")
	require.NoError(t, err)

	assert.NotEqual(t, first, second)
}

func TestSecretBoxOpenRejectsTamperedCiphertext(t *testing.T) {
	box := NewSecretBox(testMasterSecret, testContext)

	sealed, err := box.Seal("not-a-real-api-key")
	require.NoError(t, err)

	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(sealed, sealedPrefix))
	require.NoError(t, err)
	raw[len(raw)-1] ^= 0x01
	tampered := sealedPrefix + base64.RawURLEncoding.EncodeToString(raw)

	opened, err := box.Open(tampered)
	assert.Empty(t, opened)
	assert.True(t, errors.Is(err, ErrSecretUndecryptable), "expected ErrSecretUndecryptable, got %v", err)
}

func TestSecretBoxOpenRejectsAnotherMasterSecret(t *testing.T) {
	sealed, err := NewSecretBox(testMasterSecret, testContext).Seal("not-a-real-api-key")
	require.NoError(t, err)

	opened, err := NewSecretBox(testOtherMasterSecret, testContext).Open(sealed)
	assert.Empty(t, opened)
	assert.True(t, errors.Is(err, ErrSecretUndecryptable), "expected ErrSecretUndecryptable, got %v", err)
}

// The context string separates keys derived from the same master secret, so a
// box built for another purpose must not open this one's values.
func TestSecretBoxOpenRejectsAnotherContext(t *testing.T) {
	sealed, err := NewSecretBox(testMasterSecret, testContext).Seal("not-a-real-api-key")
	require.NoError(t, err)

	opened, err := NewSecretBox(testMasterSecret, "some-other-context").Open(sealed)
	assert.Empty(t, opened)
	assert.True(t, errors.Is(err, ErrSecretUndecryptable), "expected ErrSecretUndecryptable, got %v", err)
}

func TestSecretBoxOpenRejectsUnsealedInput(t *testing.T) {
	box := NewSecretBox(testMasterSecret, testContext)

	cases := []struct {
		name   string
		stored string
	}{
		{name: "plain text", stored: "not-a-real-api-key"},
		{name: "prefix only", stored: sealedPrefix},
		{name: "prefix with invalid base64", stored: sealedPrefix + "not base64!"},
		{name: "prefix with too few bytes", stored: sealedPrefix + base64.RawURLEncoding.EncodeToString([]byte("short"))},
		{name: "older prefix", stored: "enc:v0:abcdef"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opened, err := box.Open(tc.stored)
			assert.Empty(t, opened)
			assert.True(t, errors.Is(err, ErrSecretUndecryptable), "expected ErrSecretUndecryptable, got %v", err)
		})
	}
}

// An empty string is the "cleared" state of a secret, not a value to encrypt:
// sealing it stays empty so a cleared entry is stored as an empty column.
func TestSecretBoxEmptyStringPassesThrough(t *testing.T) {
	box := NewSecretBox(testMasterSecret, testContext)

	sealed, err := box.Seal("")
	require.NoError(t, err)
	assert.Equal(t, "", sealed)

	opened, err := box.Open("")
	require.NoError(t, err)
	assert.Equal(t, "", opened)
}

func TestIsSealed(t *testing.T) {
	assert.False(t, IsSealed(""))
	assert.False(t, IsSealed("not-a-real-api-key"))
	assert.True(t, IsSealed(sealedPrefix+"whatever"))
}

// A nil box is what a caller gets when no master secret was configured. It must
// refuse to work rather than silently store plaintext.
func TestNilSecretBoxRefusesToSealOrOpen(t *testing.T) {
	var box *SecretBox

	sealed, err := box.Seal("not-a-real-api-key")
	assert.Error(t, err)
	assert.Empty(t, sealed)

	opened, err := box.Open(sealedPrefix + "whatever")
	assert.True(t, errors.Is(err, ErrSecretUndecryptable), "expected ErrSecretUndecryptable, got %v", err)
	assert.Empty(t, opened)

	// Clearing a value needs no key material, so the empty cases still work.
	sealed, err = box.Seal("")
	assert.NoError(t, err)
	assert.Empty(t, sealed)
}

func TestSecretBoxHandlesLongAndUnicodeValues(t *testing.T) {
	box := NewSecretBox(testMasterSecret, testContext)
	plaintext := strings.Repeat("ключ-ключ-", 200)

	sealed, err := box.Seal(plaintext)
	require.NoError(t, err)

	opened, err := box.Open(sealed)
	require.NoError(t, err)
	assert.Equal(t, plaintext, opened)
}
