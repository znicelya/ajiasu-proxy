package secrets

import (
	"context"
	"strings"
)

const (
	VaultProviderName = "vault"
	KMSProviderName   = "kms"
)

type ExternalClient interface {
	Store(context.Context, []byte, []byte) (reference, keyID string, err error)
	Load(context.Context, string, string, []byte) ([]byte, error)
	Delete(context.Context, string, string, []byte) error
}

type RemoteProvider struct {
	name   string
	client ExternalClient
}

func NewVaultProvider(client ExternalClient) (*RemoteProvider, error) {
	return newRemoteProvider(VaultProviderName, client)
}
func NewKMSProvider(client ExternalClient) (*RemoteProvider, error) {
	return newRemoteProvider(KMSProviderName, client)
}

func newRemoteProvider(name string, client ExternalClient) (*RemoteProvider, error) {
	if client == nil || name != VaultProviderName && name != KMSProviderName {
		return nil, ErrUnavailable
	}
	return &RemoteProvider{name: name, client: client}, nil
}

func (p *RemoteProvider) Seal(ctx context.Context, secretContext Context, plaintext []byte) (SealedSecret, error) {
	if p == nil || p.client == nil || !secretContext.Valid() || len(plaintext) == 0 || len(plaintext) > 64*1024 {
		return SealedSecret{}, ErrInvalidContext
	}
	aad, err := authenticatedContext(secretContext, "external")
	if err != nil {
		return SealedSecret{}, err
	}
	copy := append([]byte(nil), plaintext...)
	defer clear(copy)
	reference, keyID, err := p.client.Store(ctx, aad, copy)
	if err != nil || strings.TrimSpace(reference) == "" || strings.TrimSpace(keyID) == "" || len(reference) > 2048 || len(keyID) > 256 {
		return SealedSecret{}, ErrUnavailable
	}
	return SealedSecret{Provider: p.name, KeyID: keyID, ExternalRef: reference}, nil
}

func (p *RemoteProvider) Open(ctx context.Context, secretContext Context, sealed SealedSecret) ([]byte, error) {
	if p == nil || p.client == nil {
		return nil, ErrUnavailable
	}
	if !secretContext.Valid() || sealed.Provider != p.name || sealed.ExternalRef == "" || sealed.KeyID == "" || len(sealed.Ciphertext) != 0 || len(sealed.WrappedDEK) != 0 {
		return nil, ErrInvalidSecret
	}
	aad, err := authenticatedContext(secretContext, "external")
	if err != nil {
		return nil, err
	}
	plaintext, err := p.client.Load(ctx, sealed.ExternalRef, sealed.KeyID, aad)
	if err != nil || len(plaintext) == 0 {
		clear(plaintext)
		return nil, ErrUnavailable
	}
	return plaintext, nil
}

func (p *RemoteProvider) Destroy(ctx context.Context, secretContext Context, sealed SealedSecret) error {
	if p == nil || p.client == nil || !secretContext.Valid() || sealed.Provider != p.name || sealed.ExternalRef == "" || sealed.KeyID == "" {
		return ErrInvalidSecret
	}
	aad, err := authenticatedContext(secretContext, "external")
	if err != nil {
		return err
	}
	if err := p.client.Delete(ctx, sealed.ExternalRef, sealed.KeyID, aad); err != nil {
		return ErrUnavailable
	}
	return nil
}

var _ Provider = (*RemoteProvider)(nil)
