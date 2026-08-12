package utils

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// sealedPrefix marks a value produced by SecretBox.Seal and pins the format
// version. Anything without it is not our ciphertext and is never decrypted:
// that is what keeps a plaintext value that predates encryption from being
// handed back as if it had been sealed.
const sealedPrefix = "enc:v1:"

// ErrSecretUndecryptable is returned whenever a stored value cannot be turned
// back into its plaintext: it is not sealed, it was tampered with, or it was
// sealed with different key material (a rotated master secret). Callers treat
// it as "unset" — a secret that cannot be read is re-entered, not recovered.
var ErrSecretUndecryptable = errors.New("secret cannot be decrypted")

// SecretBox seals and opens short strings with AES-256-GCM. It is safe for
// concurrent use.
//
// The key is derived from a master secret and a context string, so two
// subsystems sharing the same master secret still get independent keys and a
// value sealed for one of them cannot be opened by the other.
type SecretBox struct {
	aead cipher.AEAD
}

// NewSecretBox derives an AES-256-GCM key as SHA-256(masterSecret || 0x00 ||
// context) and returns a box using it. Any master secret length is accepted;
// the hash is what fixes the key size.
func NewSecretBox(masterSecret, context string) *SecretBox {
	sum := sha256.Sum256([]byte(masterSecret + "\x00" + context))

	block, err := aes.NewCipher(sum[:])
	if err != nil {
		// Unreachable: the digest is always a valid AES-256 key length.
		return &SecretBox{}
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return &SecretBox{}
	}
	return &SecretBox{aead: aead}
}

// Seal encrypts plaintext and returns its stored form,
// "enc:v1:" + base64url(nonce || ciphertext).
//
// An empty plaintext is the cleared state of a secret rather than a value, so
// it round-trips as an empty string instead of becoming ciphertext — otherwise
// "never configured" and "cleared" would be indistinguishable from the outside
// and every boot would rewrite the column.
func (b *SecretBox) Seal(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	if b == nil || b.aead == nil {
		return "", fmt.Errorf("secret encryption is not configured")
	}

	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("could not generate a nonce: %w", err)
	}

	sealed := b.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return sealedPrefix + base64.RawURLEncoding.EncodeToString(sealed), nil
}

// Open reverses Seal. An empty stored value opens to an empty string; anything
// else that is not this box's ciphertext yields ErrSecretUndecryptable, with no
// detail about the value in the error.
func (b *SecretBox) Open(stored string) (string, error) {
	if stored == "" {
		return "", nil
	}
	if !IsSealed(stored) {
		return "", ErrSecretUndecryptable
	}
	if b == nil || b.aead == nil {
		return "", ErrSecretUndecryptable
	}

	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(stored, sealedPrefix))
	if err != nil {
		return "", ErrSecretUndecryptable
	}
	nonceSize := b.aead.NonceSize()
	if len(raw) <= nonceSize {
		return "", ErrSecretUndecryptable
	}

	plaintext, err := b.aead.Open(nil, raw[:nonceSize], raw[nonceSize:], nil)
	if err != nil {
		return "", ErrSecretUndecryptable
	}
	return string(plaintext), nil
}

// IsSealed reports whether a stored value is in the sealed form. It says
// nothing about whether this box can open it.
func IsSealed(stored string) bool {
	return strings.HasPrefix(stored, sealedPrefix)
}
