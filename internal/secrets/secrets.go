// Package secrets encrypts small values (HA access tokens) at rest using AES-256-GCM.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
)

type Box struct {
	aead cipher.AEAD
}

func New(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, errors.New("secrets: key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts plaintext, binding it to aad (e.g. the instance ID) so a
// ciphertext cannot be swapped between rows.
func (b *Box) Seal(plaintext, aad []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, plaintext, aad), nil
}

func (b *Box) Open(ciphertext, aad []byte) ([]byte, error) {
	ns := b.aead.NonceSize()
	if len(ciphertext) < ns {
		return nil, errors.New("secrets: ciphertext too short")
	}
	return b.aead.Open(nil, ciphertext[:ns], ciphertext[ns:], aad)
}
