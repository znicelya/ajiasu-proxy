package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"

	"github.com/znicelya/ajiasu-proxy/internal/platform/keyring"
)

const (
	EnvelopeProviderName = "envelope"
	envelopeKeyID        = "local-envelope-v1"
	envelopeVersion      = byte(1)
	dekSize              = 32
)

type EnvelopeProvider struct{ master keyring.Keyring }

func NewEnvelopeProvider(master keyring.Keyring) (*EnvelopeProvider, error) {
	if master == nil {
		return nil, ErrUnavailable
	}
	return &EnvelopeProvider{master: master}, nil
}

func (p *EnvelopeProvider) Seal(_ context.Context, secretContext Context, plaintext []byte) (SealedSecret, error) {
	if p == nil || p.master == nil {
		return SealedSecret{}, ErrUnavailable
	}
	if !secretContext.Valid() || len(plaintext) == 0 || len(plaintext) > 64*1024 {
		return SealedSecret{}, ErrInvalidContext
	}
	dataContext, err := authenticatedContext(secretContext, "payload")
	if err != nil {
		return SealedSecret{}, err
	}
	wrapContext, err := authenticatedContext(secretContext, "wrapped-dek")
	if err != nil {
		return SealedSecret{}, err
	}
	dek := make([]byte, dekSize)
	if _, err := io.ReadFull(rand.Reader, dek); err != nil {
		return SealedSecret{}, ErrUnavailable
	}
	defer clear(dek)
	block, err := aes.NewCipher(dek)
	if err != nil {
		return SealedSecret{}, ErrUnavailable
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return SealedSecret{}, ErrUnavailable
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return SealedSecret{}, ErrUnavailable
	}
	payload := append([]byte(nil), plaintext...)
	defer clear(payload)
	ciphertext := make([]byte, 1+len(nonce))
	ciphertext[0] = envelopeVersion
	copy(ciphertext[1:], nonce)
	ciphertext = aead.Seal(ciphertext, nonce, payload, dataContext)
	wrapped, err := p.master.Encrypt(dek, wrapContext)
	if err != nil {
		return SealedSecret{}, ErrUnavailable
	}
	return SealedSecret{Provider: EnvelopeProviderName, KeyID: envelopeKeyID, Ciphertext: ciphertext, WrappedDEK: wrapped}, nil
}

func (p *EnvelopeProvider) Open(_ context.Context, secretContext Context, sealed SealedSecret) ([]byte, error) {
	if p == nil || p.master == nil {
		return nil, ErrUnavailable
	}
	if !secretContext.Valid() || sealed.Provider != EnvelopeProviderName || sealed.KeyID != envelopeKeyID || len(sealed.Ciphertext) == 0 || len(sealed.WrappedDEK) == 0 || sealed.ExternalRef != "" {
		return nil, ErrInvalidSecret
	}
	dataContext, err := authenticatedContext(secretContext, "payload")
	if err != nil {
		return nil, err
	}
	wrapContext, err := authenticatedContext(secretContext, "wrapped-dek")
	if err != nil {
		return nil, err
	}
	dek, err := p.master.Decrypt(sealed.WrappedDEK, wrapContext)
	if err != nil || len(dek) != dekSize {
		clear(dek)
		return nil, ErrIntegrity
	}
	defer clear(dek)
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, ErrIntegrity
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrIntegrity
	}
	minimum := 1 + aead.NonceSize() + aead.Overhead()
	if len(sealed.Ciphertext) < minimum || sealed.Ciphertext[0] != envelopeVersion {
		return nil, ErrInvalidSecret
	}
	nonceEnd := 1 + aead.NonceSize()
	plaintext, err := aead.Open(nil, sealed.Ciphertext[1:nonceEnd], sealed.Ciphertext[nonceEnd:], dataContext)
	if err != nil {
		return nil, ErrIntegrity
	}
	return plaintext, nil
}

func (p *EnvelopeProvider) Destroy(context.Context, Context, SealedSecret) error {
	if p == nil || p.master == nil {
		return ErrUnavailable
	}
	return nil
}

func (p *EnvelopeProvider) String() string {
	return fmt.Sprintf("SecretProvider{%s}", EnvelopeProviderName)
}

var _ Provider = (*EnvelopeProvider)(nil)
