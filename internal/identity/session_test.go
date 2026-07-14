package identity_test

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/dnomd343/ajiasu-proxy/internal/identity"
	"github.com/google/uuid"
)

func TestSessionTokenIsOpaqueAndOnlyItsDigestIsStable(t *testing.T) {
	first, err := identity.NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken() error: %v", err)
	}
	second, err := identity.NewSessionToken()
	if err != nil {
		t.Fatalf("second NewSessionToken() error: %v", err)
	}
	if first.Plaintext == "" || first.Plaintext == second.Plaintext {
		t.Fatal("session plaintext tokens are empty or repeated")
	}
	if first.Digest == ([32]byte{}) || first.Digest == second.Digest {
		t.Fatal("session token digests are empty or repeated")
	}
	if got := identity.DigestSessionToken(first.Plaintext); got != first.Digest {
		t.Fatal("DigestSessionToken() does not reproduce the stored digest")
	}
	if string(first.Digest[:]) == first.Plaintext {
		t.Fatal("stored session digest contains the plaintext token")
	}
}

func TestSessionValidationEnforcesIdleAbsoluteAndRevocation(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	session := validSession(base)
	if err := session.ValidateAt(base.Add(10 * time.Minute)); err != nil {
		t.Fatalf("active session rejected: %v", err)
	}
	if err := session.ValidateAt(session.IdleExpiresAt); !errors.Is(err, identity.ErrSessionIdleExpired) {
		t.Fatalf("idle expiry error = %v, want ErrSessionIdleExpired", err)
	}

	absoluteFirst := validSession(base)
	absoluteFirst.IdleExpiresAt = base.Add(24 * time.Hour)
	if err := absoluteFirst.ValidateAt(absoluteFirst.AbsoluteExpiresAt); !errors.Is(err, identity.ErrSessionAbsoluteExpired) {
		t.Fatalf("absolute expiry error = %v, want ErrSessionAbsoluteExpired", err)
	}

	revoked := validSession(base)
	revokedAt := base.Add(time.Minute)
	revoked.RevokedAt = &revokedAt
	if err := revoked.ValidateAt(base.Add(2 * time.Minute)); !errors.Is(err, identity.ErrSessionRevoked) {
		t.Fatalf("revoked session error = %v, want ErrSessionRevoked", err)
	}
}

func TestRotateAndRevokeSessionInvalidatePriorToken(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	session := validSession(base)
	oldDigest := session.TokenDigest
	rotated, token, err := identity.RotateSession(session, base.Add(5*time.Minute), 30*time.Minute)
	if err != nil {
		t.Fatalf("RotateSession() error: %v", err)
	}
	if token.Plaintext == "" || token.Digest == oldDigest || rotated.TokenDigest != token.Digest {
		t.Fatal("RotateSession() did not replace the prior opaque token")
	}
	if rotated.AbsoluteExpiresAt != session.AbsoluteExpiresAt {
		t.Fatal("RotateSession() extended the absolute lifetime")
	}
	if want := base.Add(35 * time.Minute); !rotated.IdleExpiresAt.Equal(want) {
		t.Fatalf("rotated idle expiry = %s, want %s", rotated.IdleExpiresAt, want)
	}

	revoked := identity.RevokeSession(rotated, base.Add(6*time.Minute))
	if revoked.RevokedAt == nil {
		t.Fatal("RevokeSession() did not record revocation")
	}
	if err := revoked.ValidateAt(base.Add(7 * time.Minute)); !errors.Is(err, identity.ErrSessionRevoked) {
		t.Fatalf("revoked rotated session error = %v, want ErrSessionRevoked", err)
	}
}

func TestSessionCookieUsesBrowserSecurityFlags(t *testing.T) {
	config := identity.SessionCookieConfig{Name: "ajiasu_session", Path: "/api/v1", Secure: true}
	cookie, err := identity.NewSessionCookie(config, "opaque-token")
	if err != nil {
		t.Fatalf("NewSessionCookie() error: %v", err)
	}
	if cookie.Name != config.Name || cookie.Value != "opaque-token" || cookie.Path != config.Path {
		t.Fatalf("session cookie identity = %#v", cookie)
	}
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie security flags = Secure %t HttpOnly %t SameSite %d", cookie.Secure, cookie.HttpOnly, cookie.SameSite)
	}

	_, err = identity.NewSessionCookie(identity.SessionCookieConfig{Name: "ajiasu_session", Path: "/api/v1"}, "opaque-token")
	if !errors.Is(err, identity.ErrInsecureSessionCookie) {
		t.Fatalf("production insecure cookie error = %v, want ErrInsecureSessionCookie", err)
	}
	development, err := identity.NewSessionCookie(identity.SessionCookieConfig{Name: "ajiasu_session", Path: "/api/v1", Development: true}, "opaque-token")
	if err != nil {
		t.Fatalf("explicit development cookie downgrade error: %v", err)
	}
	if development.Secure || !development.HttpOnly || development.SameSite != http.SameSiteLaxMode {
		t.Fatalf("development cookie flags = %#v", development)
	}
}

func validSession(base time.Time) identity.Session {
	token, err := identity.NewSessionToken()
	if err != nil {
		panic(err)
	}
	return identity.Session{
		ID:                uuid.New(),
		IdentityID:        uuid.New(),
		TokenDigest:       token.Digest,
		CreatedAt:         base,
		LastSeenAt:        base,
		IdleExpiresAt:     base.Add(30 * time.Minute),
		AbsoluteExpiresAt: base.Add(12 * time.Hour),
	}
}
