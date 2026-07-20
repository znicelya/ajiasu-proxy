package identity_test

import (
	"encoding/base32"
	"errors"
	"testing"
	"time"

	"github.com/znicelya/ajiasu-proxy/internal/identity"
)

func TestGenerateTOTPCodeMatchesRFC6238Vector(t *testing.T) {
	const secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	code, err := identity.GenerateTOTPCode(secret, time.Unix(59, 0))
	if err != nil {
		t.Fatalf("GenerateTOTPCode() error: %v", err)
	}
	if code != "287082" {
		t.Fatalf("GenerateTOTPCode() = %q, want RFC 6238 six-digit suffix 287082", code)
	}
}

func TestVerifyTOTPAcceptsOnlyTheBoundedAdjacentWindow(t *testing.T) {
	const secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	now := time.Unix(1_700_000_000, 0).UTC()
	for _, offset := range []time.Duration{-30 * time.Second, 0, 30 * time.Second} {
		code, err := identity.GenerateTOTPCode(secret, now.Add(offset))
		if err != nil {
			t.Fatalf("GenerateTOTPCode(%s) error: %v", offset, err)
		}
		valid, err := identity.VerifyTOTP(secret, code, now)
		if err != nil || !valid {
			t.Fatalf("VerifyTOTP(%s) = valid %t error %v, want accepted adjacent window", offset, valid, err)
		}
	}

	outsideCode, err := identity.GenerateTOTPCode(secret, now.Add(60*time.Second))
	if err != nil {
		t.Fatalf("GenerateTOTPCode(outside window) error: %v", err)
	}
	valid, err := identity.VerifyTOTP(secret, outsideCode, now)
	if err != nil {
		t.Fatalf("VerifyTOTP(outside window) error: %v", err)
	}
	if valid {
		t.Fatal("VerifyTOTP() accepted a code two time steps outside the window")
	}
}

func TestVerifyTOTPRejectsMalformedCodesAndSecrets(t *testing.T) {
	const secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	now := time.Unix(1_700_000_000, 0).UTC()
	for _, code := range []string{"", "12345", "1234567", "12a456"} {
		valid, err := identity.VerifyTOTP(secret, code, now)
		if err != nil {
			t.Fatalf("VerifyTOTP(%q) unexpected error: %v", code, err)
		}
		if valid {
			t.Fatalf("VerifyTOTP() accepted malformed code %q", code)
		}
	}
	if _, err := identity.GenerateTOTPCode("not-base32", now); !errors.Is(err, identity.ErrInvalidTOTP) {
		t.Fatalf("GenerateTOTPCode(invalid secret) error = %v, want ErrInvalidTOTP", err)
	}
}

func TestGenerateTOTPSecretIsRandomTwentyByteBase32(t *testing.T) {
	first, err := identity.GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("first GenerateTOTPSecret() error: %v", err)
	}
	second, err := identity.GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("second GenerateTOTPSecret() error: %v", err)
	}
	if first == second {
		t.Fatal("GenerateTOTPSecret() repeated a secret")
	}
	for _, secret := range []string{first, second} {
		decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
		if err != nil {
			t.Fatalf("generated secret is not unpadded base32: %v", err)
		}
		if len(decoded) != 20 {
			t.Fatalf("generated secret decoded length = %d, want 20", len(decoded))
		}
	}
}
