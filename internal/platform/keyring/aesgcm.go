package keyring

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
)

const (
	aes256KeySize     = 32
	ciphertextVersion = byte(1)
)

// Keyring encrypts and decrypts secrets bound to authenticated context.
type Keyring interface {
	Encrypt(plaintext, additionalData []byte) ([]byte, error)
	Decrypt(ciphertext, additionalData []byte) ([]byte, error)
}

// AESGCM implements Keyring with AES-256-GCM.
type AESGCM struct {
	aead cipher.AEAD
}

// NewAESGCM constructs an AES-256-GCM keyring from an exact 32-byte key.
func NewAESGCM(key []byte) (*AESGCM, error) {
	if len(key) != aes256KeySize {
		return nil, fmt.Errorf("aes-256-gcm key must be exactly %d bytes", aes256KeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create AES-GCM cipher: %w", err)
	}
	return &AESGCM{aead: aead}, nil
}

// Encrypt returns version byte || nonce || sealed ciphertext.
func (k *AESGCM) Encrypt(plaintext, additionalData []byte) ([]byte, error) {
	if k == nil || k.aead == nil {
		return nil, fmt.Errorf("aes-gcm keyring is not initialized")
	}
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate AES-GCM nonce: %w", err)
	}

	prefix := make([]byte, 1+len(nonce))
	prefix[0] = ciphertextVersion
	copy(prefix[1:], nonce)
	return k.aead.Seal(prefix, nonce, plaintext, additionalData), nil
}

// Decrypt authenticates the versioned ciphertext and its additional data.
func (k *AESGCM) Decrypt(ciphertext, additionalData []byte) ([]byte, error) {
	if k == nil || k.aead == nil {
		return nil, fmt.Errorf("aes-gcm keyring is not initialized")
	}
	minimumLength := 1 + k.aead.NonceSize() + k.aead.Overhead()
	if len(ciphertext) < minimumLength {
		return nil, fmt.Errorf("aes-gcm ciphertext is truncated")
	}
	if ciphertext[0] != ciphertextVersion {
		return nil, fmt.Errorf("unsupported aes-gcm ciphertext version %d", ciphertext[0])
	}

	nonceEnd := 1 + k.aead.NonceSize()
	nonce := ciphertext[1:nonceEnd]
	plaintext, err := k.aead.Open(nil, nonce, ciphertext[nonceEnd:], additionalData)
	if err != nil {
		return nil, fmt.Errorf("authenticate aes-gcm ciphertext: %w", err)
	}
	return plaintext, nil
}

var _ Keyring = (*AESGCM)(nil)
