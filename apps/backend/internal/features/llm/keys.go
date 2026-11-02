package llm

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
)

// secretsKey derives the 32-byte AES key that encrypts stored provider API
// keys. It is derived from SECRETS_KEY; in dev, an empty SECRETS_KEY derives a
// stable key from JWT_SECRET so keys survive restarts without extra config.
// (Production refuses to start without SECRETS_KEY — enforced in the startup
// hook, not in config.)
func secretsKey(cfgKey, jwtSecret string, dev bool) ([]byte, error) {
	secret := cfgKey
	if secret == "" {
		if !dev {
			return nil, errors.New("llm: SECRETS_KEY must be set in prod")
		}
		secret = jwtSecret
		if secret == "" {
			secret = "insecure-dev-fallback-secrets-key"
		}
	}
	if len(secret) < 32 {
		return nil, errors.New("llm: SECRETS_KEY must be at least 32 bytes")
	}
	sum := sha256.Sum256([]byte("wondercritique/secrets/" + secret))
	return sum[:], nil
}

// sealKey encrypts plaintext with AES-256-GCM under key.
func sealKey(key, plaintext []byte) ([]byte, error) {
	gcm, err := cipherGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("llm: nonce: %w", err)
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// openKey decrypts data with AES-256-GCM under key.
func openKey(key, data []byte) ([]byte, error) {
	gcm, err := cipherGCM(key)
	if err != nil {
		return nil, err
	}
	if len(data) < gcm.NonceSize() {
		return nil, errors.New("llm: malformed encrypted key")
	}
	plain, err := gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], nil)
	if err != nil {
		return nil, fmt.Errorf("llm: decrypt: %w", err)
	}
	return plain, nil
}

func cipherGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("llm: cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("llm: gcm: %w", err)
	}
	return gcm, nil
}
