package identity

import (
	"errors"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/znicelya/ajiasu-proxy/internal/audit"
	"github.com/znicelya/ajiasu-proxy/internal/tenancy"
	"github.com/google/uuid"
)

func TestServiceTokenFormatAndParser(t *testing.T) {
	plaintext, prefix, secret, err := NewOpaqueServiceToken()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(secret)
	if len(plaintext) != 60 || len(prefix) != 12 || !strings.HasPrefix(plaintext, "ajs_") {
		t.Fatalf("token format lengths=%d/%d value=%q", len(plaintext), len(prefix), plaintext[:4])
	}
	parsedPrefix, parsedSecret, err := ParseOpaqueServiceToken(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(parsedSecret)
	if parsedPrefix != prefix || len(parsedSecret) != 32 {
		t.Fatalf("parsed prefix/secret=%q/%d", parsedPrefix, len(parsedSecret))
	}
	for _, bad := range []string{"", plaintext + "x", "ajs_short_secret", strings.Replace(plaintext, "ajs_", "bad_", 1), strings.Replace(plaintext, "_", "+", 1)} {
		if _, _, err := ParseOpaqueServiceToken(bad); !errors.Is(err, ErrServiceAuthenticationFailed) {
			t.Fatalf("malformed %q error=%v", bad, err)
		}
	}
}

func TestServiceIdentityLifecycleRotationCIDRExpiryAndAudit(t *testing.T) {
	db := startIdentityDatabase(t)
	service, err := NewServiceIdentityService(db.pools, audit.NewService())
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 14, 4, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return base }
	actor := platformServiceActor()
	cidr := netip.MustParsePrefix("203.0.113.0/24")
	identity, first, err := service.Create(t.Context(), actor, CreateServiceIdentityCommand{Scope: ServiceScopePlatform, Name: "deploy-bot", Role: tenancy.PlatformAdmin, SourceCIDR: &cidr, ValidFor: time.Hour})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if first.Plaintext == "" || !first.ExpiresAt.Equal(base.Add(time.Hour)) || identity.Version != 1 {
		t.Fatalf("created identity/token=%#v %#v", identity, first.ServiceToken)
	}
	var verifier, storedPrefix string
	if err := db.admin.QueryRow(t.Context(), "SELECT verifier,prefix FROM identity.service_tokens WHERE id=$1", first.ID).Scan(&verifier, &storedPrefix); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(verifier, "$argon2id$") || strings.Contains(verifier, first.Plaintext) || storedPrefix != first.Prefix {
		t.Fatal("stored token material is invalid")
	}
	principal, err := service.Authenticate(t.Context(), serviceAuth(first.Plaintext, ServiceScopePlatform, nil, "203.0.113.44"))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if principal.IdentityID != identity.ID || principal.Role != tenancy.PlatformAdmin {
		t.Fatalf("principal=%#v", principal)
	}
	if _, err := service.Authenticate(t.Context(), serviceAuth(first.Plaintext, ServiceScopePlatform, nil, "198.51.100.1")); !errors.Is(err, ErrServiceAuthenticationFailed) {
		t.Fatalf("CIDR rejection=%v", err)
	}
	second, err := service.IssueToken(t.Context(), actor, IssueServiceTokenCommand{IdentityID: identity.ID, Role: tenancy.PlatformAdmin, ValidFor: 2 * time.Hour})
	if err != nil {
		t.Fatalf("Issue second: %v", err)
	}
	if _, err := service.IssueToken(t.Context(), actor, IssueServiceTokenCommand{IdentityID: identity.ID, Role: tenancy.PlatformAdmin}); !errors.Is(err, ErrServiceTokenLimit) {
		t.Fatalf("third active error=%v", err)
	}
	if err := service.RevokeToken(t.Context(), actor, identity.ID, first.ID); err != nil {
		t.Fatalf("Revoke first: %v", err)
	}
	if _, err := service.Authenticate(t.Context(), serviceAuth(first.Plaintext, ServiceScopePlatform, nil, "203.0.113.44")); !errors.Is(err, ErrServiceAuthenticationFailed) {
		t.Fatalf("revoked authenticate=%v", err)
	}
	third, err := service.IssueToken(t.Context(), actor, IssueServiceTokenCommand{IdentityID: identity.ID, Role: tenancy.PlatformAdmin, ValidFor: 30 * time.Minute})
	if err != nil {
		t.Fatalf("Issue after revoke: %v", err)
	}
	service.now = func() time.Time { return base.Add(time.Hour) }
	if _, err := service.Authenticate(t.Context(), serviceAuth(third.Plaintext, ServiceScopePlatform, nil, "203.0.113.44")); !errors.Is(err, ErrServiceAuthenticationFailed) {
		t.Fatalf("expired authenticate=%v", err)
	}
	if _, err := service.Authenticate(t.Context(), serviceAuth(second.Plaintext, ServiceScopePlatform, nil, "203.0.113.44")); err != nil {
		t.Fatalf("unexpired second: %v", err)
	}
	var auditRows, outboxRows int
	if err := db.admin.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM audit.audit_events),(SELECT count(*) FROM platform.outbox_events)").Scan(&auditRows, &outboxRows); err != nil {
		t.Fatal(err)
	}
	if auditRows < 9 || auditRows != outboxRows {
		t.Fatalf("audit/outbox=%d/%d", auditRows, outboxRows)
	}
}

func TestServiceIdentityDefaultMaximumAndScopeValidation(t *testing.T) {
	db := startIdentityDatabase(t)
	service, _ := NewServiceIdentityService(db.pools, nil)
	base := time.Now().UTC()
	service.now = func() time.Time { return base }
	actor := platformServiceActor()
	_, token, err := service.Create(t.Context(), actor, CreateServiceIdentityCommand{Scope: ServiceScopePlatform, Name: "default-validity", Role: tenancy.PlatformAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if delta := token.ExpiresAt.Sub(base.Add(24 * time.Hour)); delta < -time.Microsecond || delta > time.Microsecond {
		t.Fatalf("default expiry=%v", token.ExpiresAt)
	}
	invalid := []CreateServiceIdentityCommand{{Scope: ServiceScopePlatform, Name: "too-long", Role: tenancy.PlatformAdmin, ValidFor: 24*time.Hour + time.Second}, {Scope: ServiceScopePlatform, Name: "tenant-role", Role: tenancy.Operator}, {Scope: ServiceScopeTenant, Name: "missing-tenant", Role: tenancy.Operator}}
	for _, cmd := range invalid {
		if _, _, err := service.Create(t.Context(), actor, cmd); !errors.Is(err, ErrServiceInvalidArgument) {
			t.Fatalf("invalid cmd %#v error=%v", cmd, err)
		}
	}
}

func TestServiceConcurrentRotationNeverExceedsTwoActiveTokens(t *testing.T) {
	db := startIdentityDatabase(t)
	service, _ := NewServiceIdentityService(db.pools, nil)
	actor := platformServiceActor()
	identity, _, err := service.Create(t.Context(), actor, CreateServiceIdentityCommand{Scope: ServiceScopePlatform, Name: "concurrent", Role: tenancy.PlatformAdmin})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := service.IssueToken(t.Context(), actor, IssueServiceTokenCommand{IdentityID: identity.ID, Role: tenancy.PlatformAdmin})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	success, limited := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrServiceTokenLimit) {
			limited++
		} else {
			t.Fatalf("rotation error=%v", err)
		}
	}
	if success != 1 || limited != 1 {
		t.Fatalf("concurrent results success/limited=%d/%d", success, limited)
	}
	var active int
	if err := db.admin.QueryRow(t.Context(), "SELECT count(*) FROM identity.service_tokens WHERE service_identity_id=$1 AND revoked_at IS NULL AND expires_at>now()", identity.ID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 2 {
		t.Fatalf("active tokens=%d", active)
	}
}

func TestServiceConcurrentAuthenticateAndRevokeHaveStableOrdering(t *testing.T) {
	db := startIdentityDatabase(t)
	service, _ := NewServiceIdentityService(db.pools, nil)
	actor := platformServiceActor()
	identity, token, err := service.Create(t.Context(), actor, CreateServiceIdentityCommand{Scope: ServiceScopePlatform, Name: "auth-revoke", Role: tenancy.PlatformAdmin})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	authResult := make(chan error, 1)
	go func() {
		close(started)
		_, err := service.Authenticate(t.Context(), serviceAuth(token.Plaintext, ServiceScopePlatform, nil, "203.0.113.20"))
		authResult <- err
	}()
	<-started
	revokeResult := make(chan error, 1)
	go func() { revokeResult <- service.RevokeToken(t.Context(), actor, identity.ID, token.ID) }()
	select {
	case err := <-revokeResult:
		if err != nil {
			t.Fatalf("concurrent revoke=%v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("concurrent revoke timed out")
	}
	select {
	case err := <-authResult:
		if err != nil && !errors.Is(err, ErrServiceAuthenticationFailed) {
			t.Fatalf("concurrent auth=%v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("concurrent auth timed out")
	}
	if _, err := service.Authenticate(t.Context(), serviceAuth(token.Plaintext, ServiceScopePlatform, nil, "203.0.113.20")); !errors.Is(err, ErrServiceAuthenticationFailed) {
		t.Fatalf("post-revoke auth=%v", err)
	}
}

func TestServiceAuditFailureRollsBackCreation(t *testing.T) {
	db := startIdentityDatabase(t)
	service, _ := NewServiceIdentityService(db.pools, failingAudit{})
	if _, _, err := service.Create(t.Context(), platformServiceActor(), CreateServiceIdentityCommand{Scope: ServiceScopePlatform, Name: "rollback", Role: tenancy.PlatformAdmin}); err == nil {
		t.Fatal("creation succeeded despite audit failure")
	}
	var identities, tokens int
	if err := db.admin.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM identity.service_identities),(SELECT count(*) FROM identity.service_tokens)").Scan(&identities, &tokens); err != nil {
		t.Fatal(err)
	}
	if identities != 0 || tokens != 0 {
		t.Fatalf("rows after rollback=%d/%d", identities, tokens)
	}
}

func platformServiceActor() ServiceActor {
	return ServiceActor{ActorID: uuid.New(), Scope: ServiceScopePlatform, Role: tenancy.PlatformAdmin, Metadata: AuthenticationMetadata{SourceIP: netip.MustParseAddr("203.0.113.10"), UserAgent: "service-test/1.0", RequestID: uuid.New()}}
}
func serviceAuth(token string, scope ServiceScope, tenantID *uuid.UUID, source string) AuthenticateServiceTokenCommand {
	return AuthenticateServiceTokenCommand{Token: token, Scope: scope, TenantID: tenantID, Metadata: AuthenticationMetadata{SourceIP: netip.MustParseAddr(source), UserAgent: "service-auth-test/1.0", RequestID: uuid.New()}}
}
