package identity

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/dnomd343/ajiasu-proxy/internal/audit"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/database"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/keyring"
	"github.com/dnomd343/ajiasu-proxy/internal/testkit"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLocalIdentityMigrationBackfillsMembershipsAndRollsBackToTaskFour(t *testing.T) {
	postgres := testkit.StartPostgres(t)
	testkit.MigrationsUp(t, postgres.AdminDSN)
	testkit.MigrationsDownTo(t, postgres.AdminDSN, 3)
	admin := openIdentityPool(t, postgres.AdminDSN)
	tenantID, membershipID, identityID := uuid.New(), uuid.New(), uuid.New()
	now := time.Now().UTC()
	if _, err := admin.Exec(t.Context(), `
INSERT INTO tenancy.tenants (id, slug, name, state, version, created_at, updated_at)
VALUES ($1, 'identity-backfill', 'Identity Backfill', 'active', 1, $2, $2)
`, tenantID, now); err != nil {
		t.Fatalf("seed Task 4 tenant: %v", err)
	}
	if _, err := admin.Exec(t.Context(), `
INSERT INTO tenancy.memberships (id, tenant_id, identity_id, version, created_at, updated_at)
VALUES ($1, $2, $3, 1, $4, $4)
`, membershipID, tenantID, identityID, now); err != nil {
		t.Fatalf("seed Task 4 membership: %v", err)
	}
	testkit.MigrationsUp(t, postgres.AdminDSN)
	var principalRows int
	if err := admin.QueryRow(t.Context(), "SELECT count(*) FROM identity.user_identities WHERE id = $1", identityID).Scan(&principalRows); err != nil {
		t.Fatalf("count backfilled identity: %v", err)
	}
	if principalRows != 1 {
		t.Fatalf("backfilled identity rows = %d, want 1", principalRows)
	}
	if _, err := admin.Exec(t.Context(), `
INSERT INTO tenancy.memberships (id, tenant_id, identity_id, version, created_at, updated_at)
VALUES ($1, $2, $3, 1, $4, $4)
`, uuid.New(), tenantID, uuid.New(), now); err == nil {
		t.Fatal("membership accepted an identity absent from user_identities")
	}
	testkit.MigrationsDownTo(t, postgres.AdminDSN, 3)
	var membershipRows int
	if err := admin.QueryRow(t.Context(), "SELECT count(*) FROM tenancy.memberships WHERE id = $1", membershipID).Scan(&membershipRows); err != nil {
		t.Fatalf("count membership after down-to-3: %v", err)
	}
	if membershipRows != 1 {
		t.Fatalf("Task 4 membership rows after down-to-3 = %d, want 1", membershipRows)
	}
}

func TestBootstrapEncryptsTOTPGeneratesRecoveryCodesAndIsSingleton(t *testing.T) {
	db := startIdentityDatabase(t)
	service := newIdentityService(t, db.pools, audit.NewService(), true)
	fixed := time.Date(2026, 7, 14, 5, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return fixed }
	bootstrapped, err := service.Bootstrapped(t.Context())
	if err != nil || bootstrapped {
		t.Fatalf("Bootstrapped() before bootstrap = %t, %v", bootstrapped, err)
	}
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret() error = %v", err)
	}
	result, err := service.Bootstrap(t.Context(), bootstrapCommand(t, secret))
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	if result.IdentityID == uuid.Nil || result.Version != 1 || len(result.RecoveryCodes) != recoveryCodeCount {
		t.Fatalf("Bootstrap() result = %#v", result)
	}
	bootstrapped, err = service.Bootstrapped(t.Context())
	if err != nil || !bootstrapped {
		t.Fatalf("Bootstrapped() after bootstrap = %t, %v", bootstrapped, err)
	}
	var ciphertext []byte
	var admins, recoveryRows, auditRows, outboxRows int
	if err := db.admin.QueryRow(t.Context(), "SELECT totp_ciphertext FROM identity.local_admins WHERE identity_id = $1", result.IdentityID).Scan(&ciphertext); err != nil {
		t.Fatalf("read encrypted TOTP: %v", err)
	}
	if bytes.Contains(ciphertext, []byte(secret)) {
		t.Fatal("stored TOTP ciphertext contains plaintext secret")
	}
	if err := db.admin.QueryRow(t.Context(), "SELECT count(*) FROM identity.local_admins").Scan(&admins); err != nil {
		t.Fatalf("count local admins: %v", err)
	}
	if err := db.admin.QueryRow(t.Context(), "SELECT count(*) FROM identity.local_recovery_codes").Scan(&recoveryRows); err != nil {
		t.Fatalf("count recovery rows: %v", err)
	}
	if err := db.admin.QueryRow(t.Context(), "SELECT count(*) FROM audit.audit_events").Scan(&auditRows); err != nil {
		t.Fatalf("count bootstrap audit rows: %v", err)
	}
	if err := db.admin.QueryRow(t.Context(), "SELECT count(*) FROM platform.outbox_events").Scan(&outboxRows); err != nil {
		t.Fatalf("count bootstrap outbox rows: %v", err)
	}
	if admins != 1 || recoveryRows != recoveryCodeCount || auditRows != 1 || outboxRows != 1 {
		t.Fatalf("bootstrap rows = admins %d recovery %d audit %d outbox %d", admins, recoveryRows, auditRows, outboxRows)
	}
	tenantID := uuid.New()
	if _, err := db.admin.Exec(t.Context(), `
INSERT INTO tenancy.tenants (id, slug, name, state, version, created_at, updated_at)
VALUES ($1, 'local-admin-boundary', 'Local Admin Boundary', 'active', 1, $2, $2)
`, tenantID, fixed); err != nil {
		t.Fatalf("create tenant for local-admin boundary test: %v", err)
	}
	if _, err := db.admin.Exec(t.Context(), `
INSERT INTO tenancy.memberships (id, tenant_id, identity_id, version, created_at, updated_at)
VALUES ($1, $2, $3, 1, $4, $4)
`, uuid.New(), tenantID, result.IdentityID, fixed); err == nil {
		t.Fatal("local administrator identity was accepted as a tenant membership")
	}
	_, err = service.Bootstrap(t.Context(), bootstrapCommand(t, secret))
	if !errors.Is(err, ErrAlreadyBootstrapped) {
		t.Fatalf("second Bootstrap() error = %v, want ErrAlreadyBootstrapped", err)
	}
}

func TestConcurrentBootstrapAndRecoveryConsumptionHaveSingleWinners(t *testing.T) {
	db := startIdentityDatabase(t)
	service := newIdentityService(t, db.pools, audit.NewService(), true)
	fixed := time.Date(2026, 7, 14, 5, 15, 0, 0, time.UTC)
	service.now = func() time.Time { return fixed }
	secret, _ := GenerateTOTPSecret()
	type bootstrapOutcome struct {
		result BootstrapResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan bootstrapOutcome, 2)
	commands := []BootstrapLocalAdmin{bootstrapCommand(t, secret), bootstrapCommand(t, secret)}
	for _, command := range commands {
		go func(command BootstrapLocalAdmin) {
			<-start
			result, err := service.Bootstrap(t.Context(), command)
			outcomes <- bootstrapOutcome{result: result, err: err}
		}(command)
	}
	close(start)
	var winner BootstrapResult
	var successes, already int
	for range 2 {
		outcome := <-outcomes
		switch {
		case outcome.err == nil:
			successes++
			winner = outcome.result
		case errors.Is(outcome.err, ErrAlreadyBootstrapped):
			already++
		default:
			t.Fatalf("concurrent Bootstrap() error = %v", outcome.err)
		}
	}
	if successes != 1 || already != 1 {
		t.Fatalf("concurrent Bootstrap() outcomes = success %d already %d", successes, already)
	}

	recoveryStart := make(chan struct{})
	recoveryResults := make(chan error, 2)
	authCommands := []AuthenticateLocal{authCommand(t, winner.RecoveryCodes[0]), authCommand(t, winner.RecoveryCodes[0])}
	for _, command := range authCommands {
		go func(command AuthenticateLocal) {
			<-recoveryStart
			_, err := service.Authenticate(t.Context(), command)
			recoveryResults <- err
		}(command)
	}
	close(recoveryStart)
	successes, already = 0, 0
	for range 2 {
		err := <-recoveryResults
		if err == nil {
			successes++
		} else if errors.Is(err, ErrAuthenticationFailed) {
			already++
		} else {
			t.Fatalf("concurrent recovery Authenticate() error = %v", err)
		}
	}
	if successes != 1 || already != 1 {
		t.Fatalf("concurrent recovery outcomes = success %d rejected %d", successes, already)
	}
}

func TestLocalAuthenticationEnforcesCIDRLockoutTOTPAndOneTimeRecovery(t *testing.T) {
	db := startIdentityDatabase(t)
	service := newIdentityService(t, db.pools, audit.NewService(), true)
	current := time.Date(2026, 7, 14, 5, 30, 0, 0, time.UTC)
	service.now = func() time.Time { return current }
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret() error = %v", err)
	}
	bootstrap, err := service.Bootstrap(t.Context(), bootstrapCommand(t, secret))
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	code, err := GenerateTOTPCode(secret, current)
	if err != nil {
		t.Fatalf("GenerateTOTPCode() error = %v", err)
	}
	releaseSourceLock := holdIdentitySourceLock(t, db.admin, netip.MustParseAddr("203.0.113.10"))
	deadlineContext, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	_, err = service.Authenticate(deadlineContext, authCommand(t, code))
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		releaseSourceLock()
		t.Fatalf("Authenticate() while source lock held error = %v, want context deadline", err)
	}
	releaseSourceLock()
	wrongSource := authCommand(t, code)
	wrongSource.Metadata.SourceIP = netip.MustParseAddr("198.51.100.10")
	if _, err := service.Authenticate(t.Context(), wrongSource); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("Authenticate(wrong source) error = %v", err)
	}
	var failedAttempts int
	if err := db.admin.QueryRow(t.Context(), "SELECT failed_attempts FROM identity.local_admins WHERE identity_id = $1", bootstrap.IdentityID).Scan(&failedAttempts); err != nil {
		t.Fatalf("read failures after rejected source: %v", err)
	}
	if failedAttempts != 0 {
		t.Fatalf("failed attempts after rejected source = %d, want 0", failedAttempts)
	}
	principal, err := service.Authenticate(t.Context(), authCommand(t, code))
	if err != nil {
		t.Fatalf("Authenticate(TOTP) error = %v", err)
	}
	if principal.IdentityID != bootstrap.IdentityID {
		t.Fatalf("Authenticate(TOTP) identity = %s, want %s", principal.IdentityID, bootstrap.IdentityID)
	}
	recovery := authCommand(t, bootstrap.RecoveryCodes[0])
	if _, err := service.Authenticate(t.Context(), recovery); err != nil {
		t.Fatalf("Authenticate(recovery) error = %v", err)
	}
	if _, err := service.Authenticate(t.Context(), recovery); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("Authenticate(reused recovery) error = %v", err)
	}
	for attempt := 0; attempt < accountLockThreshold; attempt++ {
		invalid := authCommand(t, "000000")
		if _, err := service.Authenticate(t.Context(), invalid); !errors.Is(err, ErrAuthenticationFailed) {
			t.Fatalf("Authenticate(invalid factor %d) error = %v", attempt, err)
		}
	}
	if _, err := service.Authenticate(t.Context(), authCommand(t, code)); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("Authenticate(locked) error = %v", err)
	}
	current = current.Add(accountLockDuration + time.Minute)
	code, _ = GenerateTOTPCode(secret, current)
	if _, err := service.Authenticate(t.Context(), authCommand(t, code)); err != nil {
		t.Fatalf("Authenticate(after lock expiry) error = %v", err)
	}
	var attempts, successes, usedRecovery int
	if err := db.admin.QueryRow(t.Context(), "SELECT count(*), count(*) FILTER (WHERE success) FROM identity.local_login_attempts").Scan(&attempts, &successes); err != nil {
		t.Fatalf("count local login attempts: %v", err)
	}
	if err := db.admin.QueryRow(t.Context(), "SELECT count(*) FROM identity.local_recovery_codes WHERE used_at IS NOT NULL").Scan(&usedRecovery); err != nil {
		t.Fatalf("count consumed recovery codes: %v", err)
	}
	if attempts < 10 || successes != 3 || usedRecovery != 1 {
		t.Fatalf("authentication state = attempts %d successes %d used recovery %d", attempts, successes, usedRecovery)
	}
}

func TestBootstrapRollsBackWhenAuditFails(t *testing.T) {
	db := startIdentityDatabase(t)
	service := newIdentityService(t, db.pools, failingAudit{}, true)
	secret, _ := GenerateTOTPSecret()
	if _, err := service.Bootstrap(t.Context(), bootstrapCommand(t, secret)); err == nil {
		t.Fatal("Bootstrap() succeeded with failing audit")
	}
	var principals, admins, codes int
	if err := db.admin.QueryRow(t.Context(), "SELECT count(*) FROM identity.user_identities").Scan(&principals); err != nil {
		t.Fatalf("count principals after rollback: %v", err)
	}
	if err := db.admin.QueryRow(t.Context(), "SELECT count(*) FROM identity.local_admins").Scan(&admins); err != nil {
		t.Fatalf("count admins after rollback: %v", err)
	}
	if err := db.admin.QueryRow(t.Context(), "SELECT count(*) FROM identity.local_recovery_codes").Scan(&codes); err != nil {
		t.Fatalf("count codes after rollback: %v", err)
	}
	if principals != 0 || admins != 0 || codes != 0 {
		t.Fatalf("rows after audit rollback = principals %d admins %d codes %d", principals, admins, codes)
	}

	realService := newIdentityService(t, db.pools, audit.NewService(), true)
	bootstrap, err := realService.Bootstrap(t.Context(), bootstrapCommand(t, secret))
	if err != nil {
		t.Fatalf("Bootstrap(real audit) error = %v", err)
	}
	if _, err := service.Authenticate(t.Context(), authCommand(t, bootstrap.RecoveryCodes[0])); err == nil || errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("Authenticate(failing audit) error = %v, want dependency failure", err)
	}
	if err := db.admin.QueryRow(t.Context(), "SELECT count(*) FROM identity.local_recovery_codes WHERE used_at IS NOT NULL").Scan(&codes); err != nil {
		t.Fatalf("count codes after authentication audit rollback: %v", err)
	}
	if codes != 0 {
		t.Fatalf("used recovery codes after authentication audit rollback = %d, want 0", codes)
	}
	if _, err := realService.Authenticate(t.Context(), authCommand(t, bootstrap.RecoveryCodes[0])); err != nil {
		t.Fatalf("Authenticate(recovery after rollback) error = %v", err)
	}
}

type identityDatabase struct {
	admin *pgxpool.Pool
	pools *database.Pools
}

func startIdentityDatabase(t *testing.T) identityDatabase {
	t.Helper()
	postgres := testkit.StartPostgres(t)
	testkit.MigrationsUp(t, postgres.AdminDSN)
	postgres.GrantApplicationRoles(t)
	return identityDatabase{
		admin: openIdentityPool(t, postgres.AdminDSN),
		pools: &database.Pools{Platform: openIdentityPool(t, postgres.PlatformDSN), Tenant: openIdentityPool(t, postgres.TenantDSN)},
	}
}

func openIdentityPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse identity PostgreSQL config: %v", err)
	}
	config.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatalf("open identity PostgreSQL pool: %v", err)
	}
	if err := pool.Ping(t.Context()); err != nil {
		pool.Close()
		t.Fatalf("ping identity PostgreSQL pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func holdIdentitySourceLock(t *testing.T, pool *pgxpool.Pool, sourceIP netip.Addr) func() {
	t.Helper()
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin source-lock transaction: %v", err)
	}
	if _, err := tx.Exec(t.Context(), `
SELECT pg_advisory_xact_lock(hashtextextended('ajiasu-local-login-source:' || $1::inet::text, 0))
`, sourceIP); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("acquire local login source lock: %v", err)
	}
	return func() {
		if err := tx.Rollback(t.Context()); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Fatalf("release local login source lock: %v", err)
		}
	}
}

func newIdentityService(t *testing.T, pools *database.Pools, auditService audit.Service, enabled bool) *LocalService {
	t.Helper()
	ring, err := keyring.NewAESGCM(bytes.Repeat([]byte{0x5a}, 32))
	if err != nil {
		t.Fatalf("NewAESGCM() error = %v", err)
	}
	service, err := NewLocalService(pools, ring, auditService, enabled, []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")})
	if err != nil {
		t.Fatalf("NewLocalService() error = %v", err)
	}
	return service
}

func bootstrapCommand(t *testing.T, secret string) BootstrapLocalAdmin {
	t.Helper()
	return BootstrapLocalAdmin{
		Identifier: "Admin@Example.Test", DisplayName: "Break Glass Admin", Password: []byte("correct horse battery staple"), TOTPSecret: secret,
		Metadata: AuthenticationMetadata{SourceIP: netip.MustParseAddr("203.0.113.10"), UserAgent: "identity-test/1.0", RequestID: uuid.New()},
	}
}

func authCommand(t *testing.T, factor string) AuthenticateLocal {
	t.Helper()
	return AuthenticateLocal{
		Identifier: "admin@example.test", Password: []byte("correct horse battery staple"), SecondFactor: factor,
		Metadata: AuthenticationMetadata{SourceIP: netip.MustParseAddr("203.0.113.10"), UserAgent: "identity-test/1.0", RequestID: uuid.New()},
	}
}

type failingAudit struct{}

func (failingAudit) Append(context.Context, database.Executor, audit.Event, audit.OutboxEvent) error {
	return errors.New("injected audit failure")
}
