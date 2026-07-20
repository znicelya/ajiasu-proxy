package identity_test

import (
	"errors"
	"regexp"
	"testing"

	"github.com/znicelya/ajiasu-proxy/internal/identity"
)

func TestHashPasswordEncodesArgon2idParametersAndVerifies(t *testing.T) {
	password := []byte("correct horse battery staple")
	verifier, err := identity.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword() error: %v", err)
	}
	pattern := regexp.MustCompile(`^\$argon2id\$v=19\$m=65536,t=3,p=2\$[A-Za-z0-9+/]{22}\$[A-Za-z0-9+/]{43}$`)
	if !pattern.MatchString(verifier) {
		t.Fatalf("password verifier does not encode the approved Argon2id parameters: %q", verifier)
	}
	matched, err := identity.VerifyPassword(password, verifier)
	if err != nil {
		t.Fatalf("VerifyPassword() error: %v", err)
	}
	if !matched {
		t.Fatal("VerifyPassword() rejected the correct password")
	}
}

func TestHashPasswordWithParamsPreservesUpgradeMetadata(t *testing.T) {
	params := identity.PasswordParams{Memory: 32 * 1024, Iterations: 4, Parallelism: 1, SaltLength: 24, KeyLength: 48}
	verifier, err := identity.HashPasswordWithParams([]byte("upgrade-test-password"), params)
	if err != nil {
		t.Fatalf("HashPasswordWithParams() error: %v", err)
	}
	if matched, err := regexp.MatchString(`^\$argon2id\$v=19\$m=32768,t=4,p=1\$[A-Za-z0-9+/]{32}\$[A-Za-z0-9+/]{64}$`, verifier); err != nil || !matched {
		t.Fatalf("custom verifier omitted parameters: %q", verifier)
	}
	matched, err := identity.VerifyPassword([]byte("upgrade-test-password"), verifier)
	if err != nil || !matched {
		t.Fatalf("VerifyPassword() custom verifier = matched %t error %v", matched, err)
	}
}

func TestVerifyPasswordRejectsWrongPasswordAndUsesIndependentSalts(t *testing.T) {
	password := []byte("same-password")
	first, err := identity.HashPassword(password)
	if err != nil {
		t.Fatalf("first HashPassword() error: %v", err)
	}
	second, err := identity.HashPassword(password)
	if err != nil {
		t.Fatalf("second HashPassword() error: %v", err)
	}
	if first == second {
		t.Fatal("two password verifiers reused the same salt")
	}
	matched, err := identity.VerifyPassword([]byte("wrong-password"), first)
	if err != nil {
		t.Fatalf("VerifyPassword() wrong password error: %v", err)
	}
	if matched {
		t.Fatal("VerifyPassword() accepted a wrong password")
	}
}

func TestPasswordVerifierRejectsMalformedOrWeakEncodings(t *testing.T) {
	invalid := []string{
		"",
		"not-a-verifier",
		"$argon2i$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA",
		"$argon2id$v=16$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0c2FsdA$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=1024,t=1,p=1$c2FsdHNhbHRzYWx0c2FsdA$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=65536,t=3,p=2$%%%$%%%",
	}
	for _, verifier := range invalid {
		t.Run(verifier, func(t *testing.T) {
			matched, err := identity.VerifyPassword([]byte("password"), verifier)
			if matched {
				t.Fatal("VerifyPassword() matched an invalid verifier")
			}
			if !errors.Is(err, identity.ErrInvalidPasswordVerifier) {
				t.Fatalf("VerifyPassword() error = %v, want ErrInvalidPasswordVerifier", err)
			}
		})
	}
	if _, err := identity.HashPassword(nil); !errors.Is(err, identity.ErrInvalidPasswordVerifier) {
		t.Fatalf("HashPassword(nil) error = %v, want ErrInvalidPasswordVerifier", err)
	}
}
