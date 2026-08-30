// Package secretbox does symmetric, at-rest encryption for the one value in
// this platform that must be both write-once-hashed (for fast, safe
// verification) AND recoverable later on demand: an app credential's
// secret. internal/apps.CredentialRepo stores both forms side by side —
// secret_hash (SHA-256, internal/apps.hashSecret) is what Verify checks on
// every request, unchanged by this package; secret_encrypted (this
// package's Seal output) exists solely so a dashboard user who closed the
// "shown once" modal can come back later and reveal it again
// (CredentialRepo.Reveal), the same "view your key anytime" UX most API
// platforms with server-held secrets provide, in place of the classic
// "we truly cannot show that to you again" — deliberately trading some risk
// (a compromised Box key or database now exposes live secrets, not just
// useless hashes) for that convenience. See cmd/api/main.go's wiring of
// APP_SECRET_ENCRYPTION_KEY for how the key itself is provisioned.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
)

// KeySize is the required length, in bytes, of the key passed to New —
// AES-256, matching the key size internal/platform/auth uses for HMAC
// signing keys in spirit (long, random, provisioned out of band).
const KeySize = 32

// Box seals and opens strings with AES-256-GCM. It holds no per-value
// state — every Seal call generates its own random nonce and prepends it
// to the ciphertext, so a single Box is safe to share across goroutines
// and across every credential this process ever touches.
type Box struct {
	aead cipher.AEAD
}

// New builds a Box from a raw 32-byte key (NOT a passphrase — this does no
// key derivation, matching auth.NewSigner's treatment of AuthSecret as an
// already-random key rather than something to stretch). Returns an error
// rather than panicking so config.Load can surface a clear startup failure
// instead of a crash deep in the first credential creation.
func New(key []byte) (*Box, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("secretbox: key must be %d bytes, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secretbox: build cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secretbox: build gcm: %w", err)
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts plaintext, returning nonce||ciphertext||tag as a single
// slice — self-contained, so the caller (CredentialRepo) stores exactly
// this and nothing else alongside it.
func (b *Box) Seal(plaintext string) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("secretbox: generate nonce: %w", err)
	}
	return b.aead.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

// Open reverses Seal. A non-nil error here (bad key, truncated/corrupted
// ciphertext, or a mismatched AEAD tag) always means "cannot recover this
// value" — there is no partial-success case to handle.
func (b *Box) Open(sealed []byte) (string, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return "", fmt.Errorf("secretbox: ciphertext shorter than nonce")
	}
	nonce, ciphertext := sealed[:n], sealed[n:]
	plaintext, err := b.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("secretbox: decrypt: %w", err)
	}
	return string(plaintext), nil
}
