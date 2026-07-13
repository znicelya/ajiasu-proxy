package keyring

import (
	"bytes"
	"strconv"
	"testing"
)

func TestNewAESGCMRequiresExactly32ByteKey(t *testing.T) {
	for _, length := range []int{0, 1, 16, 24, 31, 33, 64} {
		t.Run(strconv.Itoa(length), func(t *testing.T) {
			if _, err := NewAESGCM(make([]byte, length)); err == nil {
				t.Fatalf("NewAESGCM() accepted %d-byte key", length)
			}
		})
	}

	ring, err := NewAESGCM(make([]byte, 32))
	if err != nil {
		t.Fatalf("NewAESGCM() rejected 32-byte key: %v", err)
	}
	if ring == nil {
		t.Fatal("NewAESGCM() returned nil keyring")
	}
}

func TestAESGCMRoundTripAndVersionedFormat(t *testing.T) {
	ring := newTestKeyring(t)
	plaintext := []byte("totp-secret-JBSWY3DPEHPK3PXP")
	additionalData := []byte("local-admin:01900000-0000-7000-8000-000000000001")

	ciphertext, err := ring.Encrypt(plaintext, additionalData)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	const nonceSize = 12
	const authenticationTagSize = 16
	if want := 1 + nonceSize + len(plaintext) + authenticationTagSize; len(ciphertext) != want {
		t.Fatalf("ciphertext length = %d, want %d", len(ciphertext), want)
	}
	if ciphertext[0] != 1 {
		t.Fatalf("ciphertext version = %d, want 1", ciphertext[0])
	}

	decrypted, err := ring.Decrypt(ciphertext, additionalData)
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("Decrypt() = %q, want %q", decrypted, plaintext)
	}
}

func TestAESGCMUsesIndependentNonceForEveryEncryption(t *testing.T) {
	ring := newTestKeyring(t)
	plaintext := []byte("same plaintext")
	additionalData := []byte("same aad")

	first, err := ring.Encrypt(plaintext, additionalData)
	if err != nil {
		t.Fatalf("first Encrypt() error = %v", err)
	}
	second, err := ring.Encrypt(plaintext, additionalData)
	if err != nil {
		t.Fatalf("second Encrypt() error = %v", err)
	}
	if bytes.Equal(first[1:13], second[1:13]) {
		t.Fatalf("Encrypt() reused nonce %x", first[1:13])
	}
	if bytes.Equal(first, second) {
		t.Fatal("Encrypt() produced identical ciphertext for repeated input")
	}
}

func TestAESGCMRejectsAdditionalDataMismatch(t *testing.T) {
	ring := newTestKeyring(t)
	ciphertext, err := ring.Encrypt([]byte("protected"), []byte("identity:a"))
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	if _, err := ring.Decrypt(ciphertext, []byte("identity:b")); err == nil {
		t.Fatal("Decrypt() accepted mismatched additional data")
	}
}

func TestAESGCMRejectsTamperingAndUnsupportedVersion(t *testing.T) {
	ring := newTestKeyring(t)
	ciphertext, err := ring.Encrypt([]byte("protected"), []byte("context"))
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func([]byte)
	}{
		{name: "version", mutate: func(value []byte) { value[0]++ }},
		{name: "nonce", mutate: func(value []byte) { value[1] ^= 0x80 }},
		{name: "sealed_ciphertext", mutate: func(value []byte) { value[len(value)-1] ^= 0x80 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tampered := append([]byte(nil), ciphertext...)
			tt.mutate(tampered)
			if _, err := ring.Decrypt(tampered, []byte("context")); err == nil {
				t.Fatal("Decrypt() accepted tampered ciphertext")
			}
		})
	}

	for _, truncated := range [][]byte{nil, {}, {1}, ciphertext[:12], ciphertext[:28]} {
		if _, err := ring.Decrypt(truncated, []byte("context")); err == nil {
			t.Fatalf("Decrypt() accepted truncated ciphertext of length %d", len(truncated))
		}
	}
}

func TestAESGCMCiphertextDoesNotContainPlaintext(t *testing.T) {
	ring := newTestKeyring(t)
	plaintext := []byte("this plaintext marker must never appear in stored ciphertext")
	ciphertext, err := ring.Encrypt(plaintext, []byte("storage-record"))
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	if bytes.Contains(ciphertext, plaintext) {
		t.Fatalf("ciphertext contains plaintext marker: %x", ciphertext)
	}
}

func newTestKeyring(t *testing.T) *AESGCM {
	t.Helper()
	key := bytes.Repeat([]byte{0x5a}, 32)
	ring, err := NewAESGCM(key)
	if err != nil {
		t.Fatalf("NewAESGCM() error = %v", err)
	}
	return ring
}
