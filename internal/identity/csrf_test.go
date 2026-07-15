package identity_test

import (
	"errors"
	"testing"

	"github.com/dnomd343/ajiasu-proxy/internal/identity"
)

func TestSynchronizerCSRFTokenIsRandomAndComparedExactly(t *testing.T) {
	first, err := identity.NewCSRFToken()
	if err != nil {
		t.Fatalf("NewCSRFToken() error: %v", err)
	}
	second, err := identity.NewCSRFToken()
	if err != nil {
		t.Fatalf("second NewCSRFToken() error: %v", err)
	}
	if first == "" || first == second {
		t.Fatal("CSRF synchronizer tokens are empty or repeated")
	}
	if !identity.VerifyCSRFToken(first, first) {
		t.Fatal("VerifyCSRFToken() rejected the matching synchronizer token")
	}
	if identity.VerifyCSRFToken(first, second) || identity.VerifyCSRFToken(first, "") {
		t.Fatal("VerifyCSRFToken() accepted a missing or different token")
	}
}

func TestValidateOriginRequiresExactTrustedOrigin(t *testing.T) {
	trusted := []string{"https://console.example.com", "https://console.example.com:8443"}
	for _, origin := range trusted {
		if err := identity.ValidateOrigin(origin, trusted); err != nil {
			t.Fatalf("ValidateOrigin(%q) error: %v", origin, err)
		}
	}

	rejected := []string{
		"",
		"null",
		"http://console.example.com",
		"https://evil.example.com",
		"https://console.example.com.evil.test",
		"https://console.example.com:9443",
		"https://user@console.example.com",
		"https://console.example.com/path",
		"not-a-url",
	}
	for _, origin := range rejected {
		t.Run(origin, func(t *testing.T) {
			if err := identity.ValidateOrigin(origin, trusted); !errors.Is(err, identity.ErrUntrustedOrigin) {
				t.Fatalf("ValidateOrigin(%q) error = %v, want ErrUntrustedOrigin", origin, err)
			}
		})
	}
}
