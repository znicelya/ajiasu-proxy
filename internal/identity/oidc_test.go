package identity

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/dnomd343/ajiasu-proxy/internal/audit"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/keyring"
	"github.com/dnomd343/ajiasu-proxy/internal/testkit"
	"github.com/google/uuid"
)

type fakeOIDCProvider struct {
	mu              sync.Mutex
	nonce, verifier string
}

func (p *fakeOIDCProvider) AuthorizationURL(state, nonce, challenge, redirect string) string {
	p.mu.Lock()
	p.nonce = nonce
	p.mu.Unlock()
	return "https://issuer.example.test/authorize?state=" + url.QueryEscape(state) + "&challenge=" + url.QueryEscape(challenge)
}
func (p *fakeOIDCProvider) ExchangeAndVerify(_ context.Context, code, verifier string) (Claims, error) {
	if code != "valid-code" {
		return Claims{}, ErrOIDCInvalidCode
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.verifier = verifier
	return Claims{Issuer: "https://issuer.example.test", Subject: "subject-1", Email: "user@example.test", Name: "OIDC User", Nonce: p.nonce, IssuedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(time.Minute)}, nil
}

func TestOIDCCompleteCreatesJITSessionAuditWithoutMembership(t *testing.T) {
	db := startIdentityDatabase(t)
	provider := &fakeOIDCProvider{}
	ring, err := keyring.NewAESGCM(bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewOIDCService(db.pools, ring, provider, audit.NewService(), SessionCookieConfig{Name: "ajiasu_session", Path: "/", Secure: false, Development: true})
	if err != nil {
		t.Fatal(err)
	}
	begin, err := service.BeginOIDC(t.Context(), BeginOIDCRequest{ReturnPath: "/tenants?view=all", Metadata: oidcMetadata()})
	if err != nil {
		t.Fatalf("BeginOIDC: %v", err)
	}
	if begin.State == "" || begin.Nonce == "" || begin.AuthorizationURL == "" {
		t.Fatalf("begin=%#v", begin)
	}
	if _, err := service.CompleteOIDC(t.Context(), CompleteOIDCRequest{State: begin.State, Code: "valid-code", BindingToken: "wrong-binding", Metadata: oidcMetadata()}); !errors.Is(err, ErrOIDCInvalidState) {
		t.Fatalf("wrong binding error=%v", err)
	}
	result, err := service.CompleteOIDC(t.Context(), CompleteOIDCRequest{State: begin.State, Code: "valid-code", BindingToken: begin.BindingCookie.Value, Metadata: oidcMetadata()})
	if err != nil {
		t.Fatalf("CompleteOIDC: %v", err)
	}
	if result.IdentityID == uuid.Nil || result.Token.Plaintext == "" || result.Token.CSRFToken == "" || result.Cookie == nil || result.ReturnPath != "/tenants?view=all" {
		t.Fatalf("completion=%#v", result)
	}
	provider.mu.Lock()
	verifier := provider.verifier
	provider.mu.Unlock()
	if len(verifier) < 43 {
		t.Fatalf("PKCE verifier length=%d", len(verifier))
	}
	var identities, memberships, sessions, audits, outbox int
	if err := db.admin.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM identity.oidc_identities WHERE identity_id=$1),(SELECT count(*) FROM tenancy.memberships WHERE identity_id=$1),(SELECT count(*) FROM identity.auth_sessions WHERE identity_id=$1),(SELECT count(*) FROM audit.audit_events WHERE actor_id=$1),(SELECT count(*) FROM platform.outbox_events WHERE aggregate_id=$1)`, result.IdentityID).Scan(&identities, &memberships, &sessions, &audits, &outbox); err != nil {
		t.Fatal(err)
	}
	if identities != 1 || memberships != 0 || sessions != 1 || audits != 2 || outbox != 2 {
		t.Fatalf("rows identities/memberships/sessions/audits/outbox=%d/%d/%d/%d/%d", identities, memberships, sessions, audits, outbox)
	}
	if _, err := service.CompleteOIDC(t.Context(), CompleteOIDCRequest{State: begin.State, Code: "valid-code", BindingToken: begin.BindingCookie.Value, Metadata: oidcMetadata()}); !errors.Is(err, ErrOIDCInvalidState) {
		t.Fatalf("replayed state error=%v", err)
	}
}

func TestOIDCAuditFailureRollsBackJITAndSession(t *testing.T) {
	db := startIdentityDatabase(t)
	provider := &fakeOIDCProvider{}
	ring, err := keyring.NewAESGCM(bytes.Repeat([]byte{0x43}, 32))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewOIDCService(db.pools, ring, provider, failingAudit{}, SessionCookieConfig{Name: "sid", Path: "/", Development: true})
	if err != nil {
		t.Fatal(err)
	}
	begin, err := service.BeginOIDC(t.Context(), BeginOIDCRequest{ReturnPath: "/", Metadata: oidcMetadata()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CompleteOIDC(t.Context(), CompleteOIDCRequest{State: begin.State, Code: "valid-code", BindingToken: begin.BindingCookie.Value, Metadata: oidcMetadata()}); err == nil {
		t.Fatal("CompleteOIDC succeeded despite audit failure")
	}
	var identities, sessions, audits, outbox int
	if err := db.admin.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM identity.oidc_identities),(SELECT count(*) FROM identity.auth_sessions),(SELECT count(*) FROM audit.audit_events),(SELECT count(*) FROM platform.outbox_events)").Scan(&identities, &sessions, &audits, &outbox); err != nil {
		t.Fatal(err)
	}
	if identities != 0 || sessions != 0 || audits != 0 || outbox != 0 {
		t.Fatalf("rows after audit rollback=%d/%d/%d/%d", identities, sessions, audits, outbox)
	}
}

func TestSessionAuthorizationAndDisableAreImmediate(t *testing.T) {
	db := startIdentityDatabase(t)
	identityID := uuid.New()
	now := time.Now().UTC()
	if _, err := db.admin.Exec(t.Context(), "INSERT INTO identity.user_identities(id,tenant_eligible,disabled_at,version,created_at,updated_at) VALUES($1,true,NULL,1,$2,$2)", identityID, now); err != nil {
		t.Fatal(err)
	}
	service, err := NewSessionService(db.pools, audit.NewService(), SessionCookieConfig{Name: "sid", Path: "/", Development: true})
	if err != nil {
		t.Fatal(err)
	}
	_, token, _, err := service.CreateSession(t.Context(), identityID)
	if err != nil {
		t.Fatal(err)
	}
	tenantID, membershipID, bindingID := uuid.New(), uuid.New(), uuid.New()
	if _, err := db.admin.Exec(t.Context(), `INSERT INTO tenancy.tenants(id,slug,name,state,version,created_at,updated_at) VALUES($1,'oidc-session','OIDC Session','active',1,$2,$2)`, tenantID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.admin.Exec(t.Context(), `INSERT INTO tenancy.memberships(id,tenant_id,identity_id,version,created_at,updated_at,identity_tenant_eligible) VALUES($1,$2,$3,1,$4,$4,true)`, membershipID, tenantID, identityID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.admin.Exec(t.Context(), `INSERT INTO tenancy.role_bindings(id,tenant_id,membership_id,role,version,created_at,updated_at) VALUES($1,$2,$3,'auditor',1,$4,$4)`, bindingID, tenantID, membershipID, now); err != nil {
		t.Fatal(err)
	}
	authenticated, err := service.AuthenticateSession(t.Context(), token.Plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if len(authenticated.Grants) != 1 || len(authenticated.Grants[0].Roles) != 1 {
		t.Fatalf("grants=%#v", authenticated.Grants)
	}
	if _, err := db.admin.Exec(t.Context(), "DELETE FROM tenancy.role_bindings WHERE id=$1", bindingID); err != nil {
		t.Fatal(err)
	}
	authenticated, err = service.AuthenticateSession(t.Context(), token.Plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if len(authenticated.Grants) != 0 {
		t.Fatalf("stale grants=%#v", authenticated.Grants)
	}
	if _, err := db.admin.Exec(t.Context(), "UPDATE identity.user_identities SET disabled_at=$2 WHERE id=$1", identityID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AuthenticateSession(t.Context(), token.Plaintext); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("disabled identity error=%v", err)
	}
	if _, _, _, err := service.RotateSession(t.Context(), token.Plaintext); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("disabled rotate error=%v", err)
	}
	if _, _, _, err := service.CreateSession(t.Context(), identityID); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("disabled create error=%v", err)
	}
	if err := service.ValidateCSRF(t.Context(), token.Plaintext, token.CSRFToken, "https://console.example.test", []string{"https://console.example.test"}); !errors.Is(err, ErrCSRFRejected) {
		t.Fatalf("disabled csrf error=%v", err)
	}
}

func TestSessionRotateRevokeAndCSRF(t *testing.T) {
	db := startIdentityDatabase(t)
	id := uuid.New()
	now := time.Now().UTC()
	if _, err := db.admin.Exec(t.Context(), "INSERT INTO identity.user_identities(id,tenant_eligible,disabled_at,version,created_at,updated_at) VALUES($1,true,NULL,1,$2,$2)", id, now); err != nil {
		t.Fatal(err)
	}
	s, err := NewSessionService(db.pools, nil, SessionCookieConfig{Name: "sid", Path: "/auth", Development: true})
	if err != nil {
		t.Fatal(err)
	}
	_, token, _, err := s.CreateSession(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateCSRF(t.Context(), token.Plaintext, token.CSRFToken, "https://console.example.test", []string{"https://console.example.test"}); err != nil {
		t.Fatalf("valid csrf: %v", err)
	}
	if err := s.ValidateCSRF(t.Context(), token.Plaintext, token.CSRFToken, "https://evil.example.test", []string{"https://console.example.test"}); !errors.Is(err, ErrCSRFRejected) {
		t.Fatalf("evil origin error=%v", err)
	}
	_, rotated, _, err := s.RotateSession(t.Context(), token.Plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateSession(t.Context(), token.Plaintext); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("old token error=%v", err)
	}
	if _, err := s.AuthenticateSession(t.Context(), rotated.Plaintext); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeSession(t.Context(), rotated.Plaintext); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateSession(t.Context(), rotated.Plaintext); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("revoked token error=%v", err)
	}
}

func TestOIDCMigrationDownToFourAndUp(t *testing.T) {
	postgres := testkit.StartPostgres(t)
	testkit.MigrationsUp(t, postgres.AdminDSN)
	testkit.MigrationsDownTo(t, postgres.AdminDSN, 4)
	pool := openIdentityPool(t, postgres.AdminDSN)
	var oidc, session, local bool
	if err := pool.QueryRow(t.Context(), "SELECT to_regclass('identity.oidc_identities') IS NOT NULL,to_regclass('identity.auth_sessions') IS NOT NULL,to_regclass('identity.local_admins') IS NOT NULL").Scan(&oidc, &session, &local); err != nil {
		t.Fatal(err)
	}
	if oidc || session || !local {
		t.Fatalf("down state oidc=%t session=%t local=%t", oidc, session, local)
	}
	testkit.MigrationsUp(t, postgres.AdminDSN)
	if err := pool.QueryRow(t.Context(), "SELECT to_regclass('identity.oidc_identities') IS NOT NULL,to_regclass('identity.auth_sessions') IS NOT NULL").Scan(&oidc, &session); err != nil {
		t.Fatal(err)
	}
	if !oidc || !session {
		t.Fatalf("up state oidc=%t session=%t", oidc, session)
	}
}

func oidcMetadata() AuthenticationMetadata {
	return AuthenticationMetadata{SourceIP: netip.MustParseAddr("203.0.113.77"), UserAgent: "oidc-test/1.0", RequestID: uuid.New()}
}
