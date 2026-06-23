// Package crypto provides AES-GCM encryption for secrets stored in the DB
// (API tokens, passwords, cached cookies). The key comes from the
// ENCRYPTION_KEY env var (32 bytes, base64-encoded).
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// Crypto encrypts/decrypts with a fixed AES-256-GCM key.
type Crypto struct {
	gcm cipher.AEAD
}

// New builds a Crypto from a base64-encoded 32-byte key.
func New(keyB64 string) (*Crypto, error) {
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		return nil, fmt.Errorf("decode ENCRYPTION_KEY (must be base64): %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("ENCRYPTION_KEY must decode to 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Crypto{gcm: gcm}, nil
}

// Encrypt returns nonce||ciphertext. Safe to store as a BLOB.
func (c *Crypto) Encrypt(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, c.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	// Seal appends the ciphertext to nonce, so the nonce becomes a prefix.
	return c.gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Decrypt reverses Encrypt.
func (c *Crypto) Decrypt(data []byte) ([]byte, error) {
	ns := c.gcm.NonceSize()
	if len(data) < ns {
		return nil, errors.New("ciphertext too short")
	}
	nonce, ct := data[:ns], data[ns:]
	return c.gcm.Open(nil, nonce, ct, nil)
}

// GenerateKey is a helper for producing a fresh base64 key (used by README/tools).
func GenerateKey() (string, error) {
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(key), nil
}
