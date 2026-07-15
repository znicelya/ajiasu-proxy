package secrets

import (
	"bytes"
	"errors"
	"testing"

	"github.com/dnomd343/ajiasu-proxy/internal/platform/keyring"
	"github.com/google/uuid"
)

func TestEnvelopeRoundTripAndContextBinding(t *testing.T) {
	master, _ := keyring.NewAESGCM(bytes.Repeat([]byte{0x11}, 32))
	provider, _ := NewEnvelopeProvider(master)
	secretContext := testContext()
	plaintext := []byte(`{"username":"fake-user","password":"fake-password"}`)
	sealed, err := provider.Seal(t.Context(), secretContext, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed.Ciphertext, plaintext) || bytes.Contains(sealed.WrappedDEK, plaintext) || len(sealed.Ciphertext) == 0 || len(sealed.WrappedDEK) == 0 {
		t.Fatal("sealed secret exposed plaintext or omitted envelope fields")
	}
	opened, err := provider.Open(t.Context(), secretContext, sealed)
	if err != nil || !bytes.Equal(opened, plaintext) {
		t.Fatalf("opened=%q err=%v", opened, err)
	}
	clear(opened)
	wrong := secretContext
	wrong.AccountID = uuid.New()
	if _, err := provider.Open(t.Context(), wrong, sealed); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("wrong context error=%v", err)
	}
	tampered := sealed.Clone()
	tampered.Ciphertext[len(tampered.Ciphertext)-1] ^= 0xff
	if _, err := provider.Open(t.Context(), secretContext, tampered); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("tampered ciphertext error=%v", err)
	}
}

func TestEnvelopeWrongMasterKeyFails(t *testing.T) {
	firstKey, _ := keyring.NewAESGCM(bytes.Repeat([]byte{0x21}, 32))
	secondKey, _ := keyring.NewAESGCM(bytes.Repeat([]byte{0x22}, 32))
	first, _ := NewEnvelopeProvider(firstKey)
	second, _ := NewEnvelopeProvider(secondKey)
	sealed, _ := first.Seal(t.Context(), testContext(), []byte("fake-secret"))
	if _, err := second.Open(t.Context(), testContext(), sealed); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("wrong master key error=%v", err)
	}
}

func testContext() Context {
	return Context{TenantID: uuid.New(), AccountID: uuid.New(), Version: 1, Purpose: AccountCredentialPurpose}
}
