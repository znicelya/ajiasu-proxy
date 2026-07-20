package identity

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/znicelya/ajiasu-proxy/internal/audit"
	"github.com/znicelya/ajiasu-proxy/internal/identity/dbgen"
	"github.com/znicelya/ajiasu-proxy/internal/platform/database"
	"github.com/znicelya/ajiasu-proxy/internal/platform/keyring"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrSessionNotFound    = errors.New("session not found")
	ErrCSRFRejected       = errors.New("csrf validation failed")
	ErrReturnPathRejected = errors.New("return path rejected")
)

type TenantGrant struct {
	TenantID uuid.UUID
	Roles    []string
}
type AuthenticatedSession struct {
	Session    Session
	Grants     []TenantGrant
	LocalAdmin bool
}

type SessionService struct {
	pools                        *database.Pools
	audit                        audit.Service
	now                          func() time.Time
	idleTimeout, absoluteTimeout time.Duration
	cookie                       SessionCookieConfig
}

func NewSessionService(pools *database.Pools, auditService audit.Service, cookie SessionCookieConfig) (*SessionService, error) {
	if pools == nil || pools.Platform == nil {
		return nil, ErrLocalAuthNotConfigured
	}
	if auditService == nil {
		auditService = audit.NewService()
	}
	return &SessionService{pools: pools, audit: auditService, now: func() time.Time { return time.Now().UTC() }, idleTimeout: 30 * time.Minute, absoluteTimeout: 12 * time.Hour, cookie: cookie}, nil
}

func (s *SessionService) ConfigureTimeouts(idle, absolute time.Duration) error {
	if s == nil || idle <= 0 || absolute <= 0 || idle > absolute {
		return ErrInvalidArgument
	}
	s.idleTimeout = idle
	s.absoluteTimeout = absolute
	return nil
}

func (s *SessionService) CreateSession(ctx context.Context, identityID uuid.UUID) (Session, SessionToken, *http.Cookie, error) {
	if identityID == uuid.Nil {
		return Session{}, SessionToken{}, nil, ErrInvalidArgument
	}
	token, err := NewSessionToken()
	if err != nil {
		return Session{}, SessionToken{}, nil, err
	}
	csrf, err := NewSynchronizerToken()
	if err != nil {
		return Session{}, SessionToken{}, nil, err
	}
	token.CSRFToken = csrf
	cookie, err := NewSessionCookie(s.cookie, token.Plaintext)
	if err != nil {
		return Session{}, SessionToken{}, nil, err
	}
	now := s.now().UTC()
	row, err := database.InPlatformTx(ctx, s.pools.Platform, identityID, func(ctx context.Context, tx pgx.Tx) (dbgen.IdentityAuthSession, error) {
		return s.createSessionRow(ctx, tx, identityID, token, now)
	})
	if err != nil {
		return Session{}, SessionToken{}, nil, err
	}
	return sessionFromRow(row), token, cookie, nil
}

func (s *SessionService) createSessionRow(ctx context.Context, tx pgx.Tx, identityID uuid.UUID, token SessionToken, now time.Time) (dbgen.IdentityAuthSession, error) {
	var disabledAt *time.Time
	if err := tx.QueryRow(ctx, "SELECT disabled_at FROM identity.user_identities WHERE id=$1 FOR UPDATE", identityID).Scan(&disabledAt); err != nil {
		return dbgen.IdentityAuthSession{}, err
	}
	if disabledAt != nil {
		return dbgen.IdentityAuthSession{}, ErrSessionRevoked
	}
	return dbgen.New(tx).CreateAuthSession(ctx, dbgen.CreateAuthSessionParams{ID: uuid.New(), IdentityID: identityID, TokenDigest: token.Digest[:], CsrfDigest: DigestSynchronizerToken(token.CSRFToken), IssuedAt: now, IdleExpiresAt: now.Add(s.idleTimeout), AbsoluteExpiresAt: now.Add(s.absoluteTimeout)})
}

func (s *SessionService) AuthenticateSession(ctx context.Context, plaintext string) (AuthenticatedSession, error) {
	if strings.TrimSpace(plaintext) == "" {
		return AuthenticatedSession{}, ErrSessionNotFound
	}
	digest := DigestSessionToken(plaintext)
	now := s.now().UTC()
	result, err := database.InPlatformTx(ctx, s.pools.Platform, uuid.New(), func(ctx context.Context, tx pgx.Tx) (AuthenticatedSession, error) {
		q := dbgen.New(tx)
		row, err := q.GetAuthSessionByDigestForUpdate(ctx, digest[:])
		if errors.Is(err, pgx.ErrNoRows) {
			return AuthenticatedSession{}, ErrSessionNotFound
		}
		if err != nil {
			return AuthenticatedSession{}, err
		}
		sess := sessionFromSessionRow(row)
		if row.DisabledAt != nil {
			return AuthenticatedSession{}, ErrSessionRevoked
		}
		if err := sess.ValidateAt(now); err != nil {
			return AuthenticatedSession{}, err
		}
		idle := now.Add(s.idleTimeout)
		if idle.After(sess.AbsoluteExpiresAt) {
			idle = sess.AbsoluteExpiresAt
		}
		updated, err := q.TouchAuthSession(ctx, dbgen.TouchAuthSessionParams{LastUsedAt: now, IdleExpiresAt: idle, ID: sess.ID, ExpectedVersion: row.Version})
		if err != nil {
			return AuthenticatedSession{}, err
		}
		grants, err := q.LoadSessionTenantGrants(ctx, sess.IdentityID)
		if err != nil {
			return AuthenticatedSession{}, err
		}
		local, err := q.IsLocalAdmin(ctx, sess.IdentityID)
		if err != nil {
			return AuthenticatedSession{}, err
		}
		return AuthenticatedSession{Session: sessionFromRow(updated), Grants: groupGrants(grants), LocalAdmin: local}, nil
	})
	if err != nil {
		return AuthenticatedSession{}, err
	}
	return result, nil
}

func (s *SessionService) RotateSession(ctx context.Context, plaintext string) (Session, SessionToken, *http.Cookie, error) {
	if plaintext == "" {
		return Session{}, SessionToken{}, nil, ErrSessionNotFound
	}
	old := DigestSessionToken(plaintext)
	token, err := NewSessionToken()
	if err != nil {
		return Session{}, SessionToken{}, nil, err
	}
	csrf, err := NewSynchronizerToken()
	if err != nil {
		return Session{}, SessionToken{}, nil, err
	}
	token.CSRFToken = csrf
	cookie, err := NewSessionCookie(s.cookie, token.Plaintext)
	if err != nil {
		return Session{}, SessionToken{}, nil, err
	}
	now := s.now().UTC()
	row, err := database.InPlatformTx(ctx, s.pools.Platform, uuid.New(), func(ctx context.Context, tx pgx.Tx) (dbgen.IdentityAuthSession, error) {
		q := dbgen.New(tx)
		current, err := q.GetAuthSessionByDigestForUpdate(ctx, old[:])
		if err != nil {
			return dbgen.IdentityAuthSession{}, ErrSessionNotFound
		}
		sess := sessionFromSessionRow(current)
		if current.DisabledAt != nil {
			return dbgen.IdentityAuthSession{}, ErrSessionRevoked
		}
		if err := sess.ValidateAt(now); err != nil {
			return dbgen.IdentityAuthSession{}, err
		}
		idle := now.Add(s.idleTimeout)
		return q.RotateAuthSession(ctx, dbgen.RotateAuthSessionParams{TokenDigest: token.Digest[:], CsrfDigest: DigestSynchronizerToken(csrf), RotatedAt: now, IdleExpiresAt: idle, ID: sess.ID, ExpectedVersion: current.Version})
	})
	if err != nil {
		return Session{}, SessionToken{}, nil, err
	}
	return sessionFromRow(row), token, cookie, nil
}

func (s *SessionService) RevokeSession(ctx context.Context, plaintext string) error {
	return s.RevokeSessionAs(ctx, plaintext, uuid.New())
}

func (s *SessionService) RevokeSessionAs(ctx context.Context, plaintext string, actorID uuid.UUID) error {
	if plaintext == "" {
		return ErrSessionNotFound
	}
	if actorID == uuid.Nil {
		return ErrInvalidArgument
	}
	d := DigestSessionToken(plaintext)
	now := s.now().UTC()
	_, err := database.InPlatformTx(ctx, s.pools.Platform, actorID, func(ctx context.Context, tx pgx.Tx) (struct{}, error) {
		q := dbgen.New(tx)
		row, err := q.GetAuthSessionByDigestForUpdate(ctx, d[:])
		if errors.Is(err, pgx.ErrNoRows) {
			return struct{}{}, ErrSessionNotFound
		}
		if err != nil {
			return struct{}{}, err
		}
		if _, err := q.RevokeAuthSession(ctx, dbgen.RevokeAuthSessionParams{RevokedAt: &now, ID: row.ID}); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, nil
	})
	return err
}

func (s *SessionService) ValidateCSRF(ctx context.Context, plaintext, token, origin string, trustedOrigins []string) error {
	if err := ValidateOrigin(origin, trustedOrigins); err != nil {
		return ErrCSRFRejected
	}
	if plaintext == "" || token == "" {
		return ErrCSRFRejected
	}
	d := DigestSessionToken(plaintext)
	now := s.now().UTC()
	returnValue, err := database.InPlatformTx(ctx, s.pools.Platform, uuid.New(), func(ctx context.Context, tx pgx.Tx) (error, error) {
		row, err := dbgen.New(tx).GetAuthSessionByDigestForUpdate(ctx, d[:])
		if err != nil {
			return ErrCSRFRejected, nil
		}
		if row.DisabledAt != nil || sessionFromSessionRow(row).ValidateAt(now) != nil {
			return ErrCSRFRejected, nil
		}
		if subtle.ConstantTimeCompare(row.CsrfDigest, DigestSynchronizerToken(token)) != 1 {
			return ErrCSRFRejected, nil
		}
		return nil, nil
	})
	if err != nil {
		return err
	}
	return returnValue
}

type BeginOIDCRequest struct {
	ReturnPath string
	Metadata   AuthenticationMetadata
}
type BeginOIDCResult struct {
	AuthorizationURL string
	State            string
	Nonce            string
	BindingCookie    *http.Cookie
}
type CompleteOIDCRequest struct {
	State, Code, BindingToken string
	Metadata                  AuthenticationMetadata
}
type CompleteOIDCResult struct {
	IdentityID uuid.UUID
	Session    Session
	Token      SessionToken
	Cookie     *http.Cookie
	ReturnPath string
	Grants     []TenantGrant
}
type OIDCService struct {
	pools    *database.Pools
	ring     keyring.Keyring
	audit    audit.Service
	provider OIDCProvider
	sessions *SessionService
	now      func() time.Time
}

func NewOIDCService(pools *database.Pools, ring keyring.Keyring, provider OIDCProvider, auditService audit.Service, cookie SessionCookieConfig) (*OIDCService, error) {
	if pools == nil || pools.Platform == nil || ring == nil || provider == nil {
		return nil, ErrOIDCProvider
	}
	ss, err := NewSessionService(pools, auditService, cookie)
	if err != nil {
		return nil, err
	}
	if auditService == nil {
		auditService = audit.NewService()
	}
	return &OIDCService{pools: pools, ring: ring, provider: provider, audit: auditService, sessions: ss, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (s *OIDCService) ConfigureSessionTimeouts(idle, absolute time.Duration) error {
	if s == nil || s.sessions == nil {
		return ErrInvalidArgument
	}
	return s.sessions.ConfigureTimeouts(idle, absolute)
}

func (s *OIDCService) BeginOIDC(ctx context.Context, req BeginOIDCRequest) (BeginOIDCResult, error) {
	if err := req.Metadata.validate(); err != nil {
		return BeginOIDCResult{}, ErrInvalidArgument
	}
	path := strings.TrimSpace(req.ReturnPath)
	if !safeReturnPath(path) {
		return BeginOIDCResult{}, ErrReturnPathRejected
	}
	state, err := randomURLToken(32)
	if err != nil {
		return BeginOIDCResult{}, err
	}
	nonce, err := randomURLToken(32)
	if err != nil {
		return BeginOIDCResult{}, err
	}
	verifier, err := randomURLToken(32)
	if err != nil {
		return BeginOIDCResult{}, err
	}
	binding, err := randomURLToken(32)
	if err != nil {
		return BeginOIDCResult{}, err
	}
	bindingCookie, err := NewOIDCBindingCookie(s.sessions.cookie, binding)
	if err != nil {
		return BeginOIDCResult{}, err
	}
	cipher, err := s.ring.Encrypt([]byte(verifier), []byte("oidc-pkce:v1"))
	if err != nil {
		return BeginOIDCResult{}, err
	}
	now := s.now().UTC()
	_, err = database.InPlatformTx(ctx, s.pools.Platform, uuid.New(), func(ctx context.Context, tx pgx.Tx) (dbgen.IdentityOidcAuthTransaction, error) {
		return dbgen.New(tx).CreateOIDCAuthTransaction(ctx, dbgen.CreateOIDCAuthTransactionParams{ID: uuid.New(), StateDigest: digestString(state), BindingDigest: digestString(binding), NonceDigest: digestString(nonce), PkceVerifierCiphertext: cipher, ReturnPath: path, ExpiresAt: now.Add(10 * time.Minute), CreatedAt: now})
	})
	if err != nil {
		return BeginOIDCResult{}, err
	}
	return BeginOIDCResult{AuthorizationURL: s.provider.AuthorizationURL(state, nonce, pkceChallenge(verifier), ""), State: state, Nonce: nonce, BindingCookie: bindingCookie}, nil
}

func (s *OIDCService) CompleteOIDC(ctx context.Context, req CompleteOIDCRequest) (CompleteOIDCResult, error) {
	if req.State == "" || req.Code == "" || req.BindingToken == "" {
		return CompleteOIDCResult{}, ErrOIDCInvalidState
	}
	if err := req.Metadata.validate(); err != nil {
		return CompleteOIDCResult{}, ErrInvalidArgument
	}
	now := s.now().UTC()
	txrow, err := database.InPlatformTx(ctx, s.pools.Platform, uuid.New(), func(ctx context.Context, tx pgx.Tx) (dbgen.IdentityOidcAuthTransaction, error) {
		return dbgen.New(tx).ConsumeOIDCAuthTransaction(ctx, dbgen.ConsumeOIDCAuthTransactionParams{ConsumedAt: &now, StateDigest: digestString(req.State), BindingDigest: digestString(req.BindingToken)})
	})
	if err != nil {
		return CompleteOIDCResult{}, ErrOIDCInvalidState
	}
	if subtle.ConstantTimeCompare(txrow.BindingDigest, digestString(req.BindingToken)) != 1 {
		return CompleteOIDCResult{}, ErrOIDCInvalidState
	}
	verifier, err := s.ring.Decrypt(txrow.PkceVerifierCiphertext, []byte("oidc-pkce:v1"))
	if err != nil {
		return CompleteOIDCResult{}, ErrOIDCInvalidState
	}
	claims, err := s.provider.ExchangeAndVerify(ctx, req.Code, string(verifier))
	if err != nil {
		return CompleteOIDCResult{}, err
	}
	if !equalDigest(txrow.NonceDigest, claims.Nonce) {
		return CompleteOIDCResult{}, ErrOIDCInvalidClaims
	}
	token, err := NewSessionToken()
	if err != nil {
		return CompleteOIDCResult{}, err
	}
	csrf, err := NewSynchronizerToken()
	if err != nil {
		return CompleteOIDCResult{}, err
	}
	token.CSRFToken = csrf
	cookie, err := NewSessionCookie(s.sessions.cookie, token.Plaintext)
	if err != nil {
		return CompleteOIDCResult{}, err
	}
	type completion struct {
		identityID uuid.UUID
		session    dbgen.IdentityAuthSession
		grants     []dbgen.LoadSessionTenantGrantsRow
	}
	completed, err := database.InPlatformTx(ctx, s.pools.Platform, req.Metadata.RequestID, func(ctx context.Context, tx pgx.Tx) (completion, error) {
		identityID, created, err := s.jitIdentityTx(ctx, tx, claims, now)
		if err != nil {
			return completion{}, err
		}
		row, err := s.sessions.createSessionRow(ctx, tx, identityID, token, now)
		if err != nil {
			return completion{}, err
		}
		if created {
			if err := s.appendOIDCAudit(ctx, tx, req.Metadata, identityID, "identity.oidc_identity.created", "jit", now); err != nil {
				return completion{}, err
			}
		}
		if err := s.appendOIDCAudit(ctx, tx, req.Metadata, identityID, "identity.oidc_login.succeeded", "login", now); err != nil {
			return completion{}, err
		}
		grants, err := dbgen.New(tx).LoadSessionTenantGrants(ctx, identityID)
		if err != nil {
			return completion{}, err
		}
		return completion{identityID: identityID, session: row, grants: grants}, nil
	})
	if err != nil {
		return CompleteOIDCResult{}, err
	}
	return CompleteOIDCResult{IdentityID: completed.identityID, Session: sessionFromRow(completed.session), Token: token, Cookie: cookie, ReturnPath: txrow.ReturnPath, Grants: groupGrants(completed.grants)}, nil
}

func (s *OIDCService) jitIdentityTx(ctx context.Context, tx pgx.Tx, claims Claims, now time.Time) (uuid.UUID, bool, error) {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", claims.Issuer+"\n"+claims.Subject); err != nil {
		return uuid.Nil, false, err
	}
	var existing uuid.UUID
	var disabledAt *time.Time
	err := tx.QueryRow(ctx, "SELECT oidc.identity_id, principal.disabled_at FROM identity.oidc_identities oidc JOIN identity.user_identities principal ON principal.id=oidc.identity_id WHERE oidc.issuer=$1 AND oidc.subject=$2 FOR UPDATE OF oidc, principal", claims.Issuer, claims.Subject).Scan(&existing, &disabledAt)
	if err == nil {
		if disabledAt != nil {
			return uuid.Nil, false, ErrSessionRevoked
		}
		_, err = tx.Exec(ctx, "UPDATE identity.oidc_identities SET email=$1, display_name=$2, updated_at=$3 WHERE issuer=$4 AND subject=$5", claims.Email, claims.Name, now, claims.Issuer, claims.Subject)
		return existing, false, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, err
	}
	id := uuid.New()
	if _, err = tx.Exec(ctx, "INSERT INTO identity.user_identities (id,tenant_eligible,disabled_at,version,created_at,updated_at) VALUES ($1,true,NULL,1,$2,$2)", id, now); err != nil {
		return uuid.Nil, false, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO identity.oidc_identities (id,identity_id,issuer,subject,email,display_name,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$7)", uuid.New(), id, claims.Issuer, claims.Subject, claims.Email, claims.Name, now); err != nil {
		return uuid.Nil, false, err
	}
	return id, true, nil
}

func (s *OIDCService) appendOIDCAudit(ctx context.Context, tx pgx.Tx, metadata AuthenticationMetadata, identityID uuid.UUID, action, category string, now time.Time) error {
	resourceID := identityID
	return s.audit.Append(ctx, tx, audit.Event{ActorType: "oidc_user", ActorID: &identityID, Action: action, ResourceType: "oidc_identity", ResourceID: &resourceID, Result: "success", SourceIP: metadata.SourceIP.Unmap(), UserAgent: strings.TrimSpace(metadata.UserAgent), RequestID: metadata.RequestID, Details: map[string]any{"category": category}}, audit.OutboxEvent{EventType: action, AggregateType: "oidc_identity", AggregateID: identityID, PayloadVersion: 1, Payload: map[string]any{"category": category}, AvailableAt: now})
}

func sessionFromRow(r dbgen.IdentityAuthSession) Session {
	return Session{ID: r.ID, IdentityID: r.IdentityID, TokenDigest: toDigest(r.TokenDigest), CreatedAt: r.IssuedAt, LastSeenAt: r.LastUsedAt, IdleExpiresAt: r.IdleExpiresAt, AbsoluteExpiresAt: r.AbsoluteExpiresAt, RevokedAt: r.RevokedAt}
}
func sessionFromSessionRow(r dbgen.GetAuthSessionByDigestForUpdateRow) Session {
	return Session{ID: r.ID, IdentityID: r.IdentityID, TokenDigest: toDigest(r.TokenDigest), CreatedAt: r.IssuedAt, LastSeenAt: r.LastUsedAt, IdleExpiresAt: r.IdleExpiresAt, AbsoluteExpiresAt: r.AbsoluteExpiresAt, RevokedAt: r.RevokedAt}
}
func toDigest(b []byte) [32]byte { var d [32]byte; copy(d[:], b); return d }
func groupGrants(rows []dbgen.LoadSessionTenantGrantsRow) []TenantGrant {
	out := make([]TenantGrant, 0)
	for _, r := range rows {
		if len(out) == 0 || out[len(out)-1].TenantID != r.TenantID {
			out = append(out, TenantGrant{TenantID: r.TenantID})
		}
		out[len(out)-1].Roles = append(out[len(out)-1].Roles, r.Role)
	}
	return out
}

func NewOIDCBindingCookie(config SessionCookieConfig, token string) (*http.Cookie, error) {
	config.Name = strings.TrimSpace(config.Name) + "_oidc"
	config.Path = "/"
	cookie, err := NewSessionCookie(config, token)
	if err != nil {
		return nil, err
	}
	cookie.MaxAge = 600
	return cookie, nil
}
func safeReturnPath(path string) bool {
	if path == "" || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "\r\n") || strings.Contains(path, "\\") {
		return false
	}
	u, err := url.Parse(path)
	if err != nil || u.IsAbs() || u.Host != "" || u.Opaque != "" || strings.HasPrefix(u.Path, "//") || strings.ContainsAny(u.Path, "\\\r\n") {
		return false
	}
	for _, r := range u.Path {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
