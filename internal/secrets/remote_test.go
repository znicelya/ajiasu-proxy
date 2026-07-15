package secrets

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
)

type fakeExternalClient struct {
	mu      sync.Mutex
	entries map[string]fakeExternalEntry
}
type fakeExternalEntry struct {
	aad     [32]byte
	value   []byte
	deleted bool
}

func (f *fakeExternalClient) Store(_ context.Context, aad, value []byte) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.entries == nil {
		f.entries = map[string]fakeExternalEntry{}
	}
	ref := "ref-1"
	f.entries[ref] = fakeExternalEntry{aad: sha256.Sum256(aad), value: append([]byte(nil), value...)}
	return ref, "key-1", nil
}
func (f *fakeExternalClient) Load(_ context.Context, ref, _ string, aad []byte) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	entry, ok := f.entries[ref]
	if !ok || entry.deleted || entry.aad != sha256.Sum256(aad) {
		return nil, errors.New("unavailable")
	}
	return append([]byte(nil), entry.value...), nil
}
func (f *fakeExternalClient) Delete(_ context.Context, ref, _ string, aad []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	entry, ok := f.entries[ref]
	if !ok || entry.aad != sha256.Sum256(aad) {
		return errors.New("unavailable")
	}
	entry.deleted = true
	f.entries[ref] = entry
	return nil
}

func TestVaultAndKMSAdaptersBindExternalReference(t *testing.T) {
	for _, constructor := range []func(ExternalClient) (*RemoteProvider, error){NewVaultProvider, NewKMSProvider} {
		client := &fakeExternalClient{}
		provider, err := constructor(client)
		if err != nil {
			t.Fatal(err)
		}
		secretContext := testContext()
		sealed, err := provider.Seal(t.Context(), secretContext, []byte("fake-remote-secret"))
		if err != nil || sealed.ExternalRef == "" || len(sealed.Ciphertext) != 0 {
			t.Fatalf("sealed=%#v err=%v", sealed, err)
		}
		opened, err := provider.Open(t.Context(), secretContext, sealed)
		if err != nil || !bytes.Equal(opened, []byte("fake-remote-secret")) {
			t.Fatalf("opened=%q err=%v", opened, err)
		}
		wrong := secretContext
		wrong.Version++
		if _, err := provider.Open(t.Context(), wrong, sealed); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("wrong context error=%v", err)
		}
		if err := provider.Destroy(t.Context(), secretContext, sealed); err != nil {
			t.Fatal(err)
		}
		if _, err := provider.Open(t.Context(), secretContext, sealed); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("open destroyed error=%v", err)
		}
	}
}
