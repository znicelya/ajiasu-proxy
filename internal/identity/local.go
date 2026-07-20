package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/znicelya/ajiasu-proxy/internal/audit"
	"github.com/znicelya/ajiasu-proxy/internal/identity/dbgen"
	"github.com/znicelya/ajiasu-proxy/internal/platform/database"
	"github.com/znicelya/ajiasu-proxy/internal/platform/keyring"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	recoveryCodeCount    = 10
	recoveryCodeBytes    = 16
	accountLockThreshold = 5
	accountLockDuration  = 15 * time.Minute
	sourceFailureLimit   = 20
	sourceFailureWindow  = 15 * time.Minute
	authenticationFloor  = 750 * time.Millisecond
	maxPasswordBytes     = 1024
)

var (
	ErrInvalidArgument        = errors.New("invalid local identity argument")
	ErrAlreadyBootstrapped    = errors.New("local administrator already exists")
	ErrAuthenticationFailed   = errors.New("local authentication failed")
	ErrLocalIdentityStorage   = errors.New("local identity storage failure")
	ErrLocalAuthNotConfigured = errors.New("local authentication is not configured")
)

type AuthenticationMetadata struct {
	SourceIP  netip.Addr
	UserAgent string
	RequestID uuid.UUID
}

func (m AuthenticationMetadata) validate() error {
	if !m.SourceIP.IsValid() || m.SourceIP.Zone() != "" || strings.TrimSpace(m.UserAgent) == "" || len(strings.TrimSpace(m.UserAgent)) > 1024 || m.RequestID == uuid.Nil {
		return ErrInvalidArgument
	}
	return nil
}

type BootstrapLocalAdmin struct {
	Identifier  string
	DisplayName string
	Password    []byte
	TOTPSecret  string
	Metadata    AuthenticationMetadata
}

type BootstrapResult struct {
	IdentityID    uuid.UUID
	RecoveryCodes []string
	Version       int64
}

type AuthenticateLocal struct {
	Identifier   string
	Password     []byte
	SecondFactor string
	Metadata     AuthenticationMetadata
}

type LocalPrincipal struct {
	IdentityID  uuid.UUID
	DisplayName string
	Version     int64
}

type LocalService struct {
	pools         *database.Pools
	keyring       keyring.Keyring
	audit         audit.Service
	enabled       bool
	allowedCIDRs  []netip.Prefix
	now           func() time.Time
	newID         func() (uuid.UUID, error)
	dummyVerifier string
}

func NewLocalService(pools *database.Pools, ring keyring.Keyring, auditService audit.Service, enabled bool, allowedCIDRs []netip.Prefix) (*LocalService, error) {
	if pools == nil || pools.Platform == nil || ring == nil {
		return nil, ErrLocalAuthNotConfigured
	}
	if auditService == nil {
		auditService = audit.NewService()
	}
	dummy, err := HashPassword([]byte("ajiasu-local-auth-dummy-password-never-used"))
	if err != nil {
		return nil, fmt.Errorf("create local authentication dummy verifier: %w", err)
	}
	prefixes := append([]netip.Prefix(nil), allowedCIDRs...)
	return &LocalService{
		pools: pools, keyring: ring, audit: auditService, enabled: enabled, allowedCIDRs: prefixes,
		now: func() time.Time { return time.Now().UTC() }, newID: uuid.NewV7, dummyVerifier: dummy,
	}, nil
}

func (s *LocalService) Bootstrapped(ctx context.Context) (bool, error) {
	count, err := dbgen.New(s.pools.Platform).CountLocalAdmins(ctx)
	if err != nil {
		return false, mapLocalStorageError(err)
	}
	return count != 0, nil
}

func (s *LocalService) Bootstrap(ctx context.Context, command BootstrapLocalAdmin) (BootstrapResult, error) {
	identifier := normalizeIdentifier(command.Identifier)
	if identifier == "" || len(identifier) > 254 || strings.TrimSpace(command.DisplayName) == "" || len(strings.TrimSpace(command.DisplayName)) > 200 || len(command.Password) < 12 || len(command.Password) > maxPasswordBytes {
		return BootstrapResult{}, ErrInvalidArgument
	}
	if err := command.Metadata.validate(); err != nil {
		return BootstrapResult{}, err
	}
	if _, err := GenerateTOTPCode(command.TOTPSecret, s.now()); err != nil {
		return BootstrapResult{}, ErrInvalidArgument
	}
	identityID, err := s.newID()
	if err != nil {
		return BootstrapResult{}, ErrLocalIdentityStorage
	}
	passwordVerifier, err := HashPassword(command.Password)
	if err != nil {
		return BootstrapResult{}, ErrLocalIdentityStorage
	}
	totpCiphertext, err := s.keyring.Encrypt([]byte(strings.ToUpper(strings.TrimSpace(command.TOTPSecret))), localAdminTOTPAdditionalData(identityID))
	if err != nil {
		return BootstrapResult{}, fmt.Errorf("%w: encrypt TOTP secret", ErrLocalIdentityStorage)
	}
	recoveryCodes, recoveryRows, err := s.prepareRecoveryCodes(identityID)
	if err != nil {
		return BootstrapResult{}, err
	}
	now := s.now().UTC()
	type outcome struct {
		adminVersion int64
		exists       bool
	}
	result, err := database.InPlatformTx(ctx, s.pools.Platform, identityID, func(ctx context.Context, tx pgx.Tx) (outcome, error) {
		queries := dbgen.New(tx)
		if err := queries.LockLocalAdminBootstrap(ctx); err != nil {
			return outcome{}, mapLocalStorageError(err)
		}
		count, err := queries.CountLocalAdmins(ctx)
		if err != nil {
			return outcome{}, mapLocalStorageError(err)
		}
		if count != 0 {
			if err := s.appendAuthAudit(ctx, tx, command.Metadata, nil, "identity.local_admin.bootstrap_rejected", "failure", "already_initialized", command.Metadata.RequestID, now); err != nil {
				return outcome{}, err
			}
			return outcome{exists: true}, nil
		}
		if _, err := queries.CreateUserIdentity(ctx, dbgen.CreateUserIdentityParams{ID: identityID, CreatedAt: now, UpdatedAt: now}); err != nil {
			return outcome{}, mapLocalStorageError(err)
		}
		admin, err := queries.CreateLocalAdmin(ctx, dbgen.CreateLocalAdminParams{
			IdentityID: identityID, Identifier: identifier, DisplayName: strings.TrimSpace(command.DisplayName),
			PasswordVerifier: passwordVerifier, TotpCiphertext: totpCiphertext, CreatedAt: now, UpdatedAt: now,
		})
		if err != nil {
			return outcome{}, mapLocalStorageError(err)
		}
		for _, row := range recoveryRows {
			if _, err := queries.CreateLocalRecoveryCode(ctx, row); err != nil {
				return outcome{}, mapLocalStorageError(err)
			}
		}
		if err := s.appendAuthAudit(ctx, tx, command.Metadata, &identityID, "identity.local_admin.bootstrapped", "success", "created", identityID, now); err != nil {
			return outcome{}, err
		}
		return outcome{adminVersion: admin.Version}, nil
	})
	if err != nil {
		return BootstrapResult{}, err
	}
	if result.exists {
		return BootstrapResult{}, ErrAlreadyBootstrapped
	}
	return BootstrapResult{IdentityID: identityID, RecoveryCodes: recoveryCodes, Version: result.adminVersion}, nil
}

func (s *LocalService) Authenticate(ctx context.Context, command AuthenticateLocal) (LocalPrincipal, error) {
	startedAt := time.Now()
	defer func() {
		if remaining := authenticationFloor - time.Since(startedAt); remaining > 0 {
			time.Sleep(remaining)
		}
	}()
	identifier := normalizeIdentifier(command.Identifier)
	if identifier == "" || len(command.Password) == 0 || len(command.Password) > maxPasswordBytes || strings.TrimSpace(command.SecondFactor) == "" || len(command.SecondFactor) > 128 {
		return LocalPrincipal{}, ErrAuthenticationFailed
	}
	if err := command.Metadata.validate(); err != nil {
		return LocalPrincipal{}, ErrAuthenticationFailed
	}
	now := s.now().UTC()
	type outcome struct {
		principal LocalPrincipal
		success   bool
	}
	transactionActor := command.Metadata.RequestID
	result, err := database.InPlatformTx(ctx, s.pools.Platform, transactionActor, func(ctx context.Context, tx pgx.Tx) (outcome, error) {
		queries := dbgen.New(tx)
		row, loadErr := queries.GetLocalAdminByIdentifierForUpdate(ctx, identifier)
		known := loadErr == nil
		if loadErr != nil && !errors.Is(loadErr, pgx.ErrNoRows) {
			return outcome{}, mapLocalStorageError(loadErr)
		}
		verifier := s.dummyVerifier
		if known {
			verifier = row.PasswordVerifier
		}
		passwordOK, verifyErr := VerifyPassword(command.Password, verifier)
		if verifyErr != nil {
			if known {
				return outcome{}, fmt.Errorf("%w: invalid stored password verifier", ErrLocalIdentityStorage)
			}
			passwordOK = false
		}
		reason := "invalid_credentials"
		method := "none"
		if err := queries.LockLocalLoginSource(ctx, command.Metadata.SourceIP.Unmap()); err != nil {
			return outcome{}, mapLocalStorageError(err)
		}
		sourceFailures, err := queries.CountRecentFailedLocalLoginsBySource(ctx, dbgen.CountRecentFailedLocalLoginsBySourceParams{
			SourceIp: command.Metadata.SourceIP.Unmap(), AttemptedSince: now.Add(-sourceFailureWindow),
		})
		if err != nil {
			return outcome{}, mapLocalStorageError(err)
		}
		allowed := s.enabled && s.sourceAllowed(command.Metadata.SourceIP) && sourceFailures < sourceFailureLimit
		if known && passwordOK && allowed && row.DisabledAt == nil && (row.LockedUntil == nil || !row.LockedUntil.After(now)) {
			secret, err := s.keyring.Decrypt(row.TotpCiphertext, localAdminTOTPAdditionalData(row.IdentityID))
			if err != nil {
				return outcome{}, fmt.Errorf("%w: decrypt TOTP secret", ErrLocalIdentityStorage)
			}
			totpOK, err := verifyTOTPSecretBytes(secret, strings.TrimSpace(command.SecondFactor), now)
			clear(secret)
			if err != nil {
				return outcome{}, fmt.Errorf("%w: invalid stored TOTP secret", ErrLocalIdentityStorage)
			}
			if totpOK {
				method = "totp"
				return s.completeLocalAuthentication(ctx, tx, queries, row, command.Metadata, identifier, method, now)
			}
			recoveryID, recoveryOK, err := verifyRecoveryCode(ctx, queries, row.IdentityID, command.SecondFactor)
			if err != nil {
				return outcome{}, err
			}
			if recoveryOK {
				if _, err := queries.ConsumeLocalRecoveryCode(ctx, dbgen.ConsumeLocalRecoveryCodeParams{UsedAt: &now, ID: recoveryID, IdentityID: row.IdentityID}); err != nil {
					return outcome{}, mapLocalStorageError(err)
				}
				method = "recovery_code"
				return s.completeLocalAuthentication(ctx, tx, queries, row, command.Metadata, identifier, method, now)
			}
		}
		if known {
			accountCanAccumulateFailure := allowed && row.DisabledAt == nil && (row.LockedUntil == nil || !row.LockedUntil.After(now))
			switch {
			case row.DisabledAt != nil:
				reason = "disabled"
			case row.LockedUntil != nil && row.LockedUntil.After(now):
				reason = "locked"
			case !allowed:
				reason = "source_rejected"
			}
			if accountCanAccumulateFailure {
				var lockedUntil *time.Time
				if row.FailedAttempts+1 >= accountLockThreshold {
					deadline := now.Add(accountLockDuration)
					lockedUntil = &deadline
					reason = "locked"
				}
				if _, err := queries.RecordLocalAdminFailure(ctx, dbgen.RecordLocalAdminFailureParams{LockedUntil: lockedUntil, UpdatedAt: now, IdentityID: row.IdentityID}); err != nil {
					return outcome{}, mapLocalStorageError(err)
				}
			}
		}
		if err := s.recordLoginAttempt(ctx, queries, knownIdentityID(known, row.IdentityID), identifier, command.Metadata.SourceIP, false, reason, now); err != nil {
			return outcome{}, err
		}
		if err := s.appendAuthAudit(ctx, tx, command.Metadata, knownIdentityID(known, row.IdentityID), "identity.local_login.failed", "failure", reason, command.Metadata.RequestID, now); err != nil {
			return outcome{}, err
		}
		return outcome{}, nil
	})
	if err != nil {
		return LocalPrincipal{}, err
	}
	if !result.success {
		return LocalPrincipal{}, ErrAuthenticationFailed
	}
	return result.principal, nil
}

func (s *LocalService) completeLocalAuthentication(ctx context.Context, tx pgx.Tx, queries *dbgen.Queries, row dbgen.GetLocalAdminByIdentifierForUpdateRow, metadata AuthenticationMetadata, identifier, method string, now time.Time) (struct {
	principal LocalPrincipal
	success   bool
}, error) {
	updated, err := queries.ResetLocalAdminFailures(ctx, dbgen.ResetLocalAdminFailuresParams{AuthenticatedAt: &now, IdentityID: row.IdentityID})
	if err != nil {
		return struct {
			principal LocalPrincipal
			success   bool
		}{}, mapLocalStorageError(err)
	}
	if err := s.recordLoginAttempt(ctx, queries, &row.IdentityID, identifier, metadata.SourceIP, true, "success", now); err != nil {
		return struct {
			principal LocalPrincipal
			success   bool
		}{}, err
	}
	if err := s.appendAuthAudit(ctx, tx, metadata, &row.IdentityID, "identity.local_login.succeeded", "success", method, row.IdentityID, now); err != nil {
		return struct {
			principal LocalPrincipal
			success   bool
		}{}, err
	}
	return struct {
		principal LocalPrincipal
		success   bool
	}{principal: LocalPrincipal{IdentityID: row.IdentityID, DisplayName: row.DisplayName, Version: updated.Version}, success: true}, nil
}

func (s *LocalService) prepareRecoveryCodes(identityID uuid.UUID) ([]string, []dbgen.CreateLocalRecoveryCodeParams, error) {
	codes := make([]string, 0, recoveryCodeCount)
	rows := make([]dbgen.CreateLocalRecoveryCodeParams, 0, recoveryCodeCount)
	now := s.now().UTC()
	for range recoveryCodeCount {
		code, err := generateRecoveryCode()
		if err != nil {
			return nil, nil, ErrLocalIdentityStorage
		}
		id, err := s.newID()
		if err != nil {
			return nil, nil, ErrLocalIdentityStorage
		}
		codes = append(codes, code)
		rows = append(rows, dbgen.CreateLocalRecoveryCodeParams{ID: id, IdentityID: identityID, Verifier: recoveryCodeVerifier(code), CreatedAt: now})
	}
	return codes, rows, nil
}

func (s *LocalService) recordLoginAttempt(ctx context.Context, queries *dbgen.Queries, identityID *uuid.UUID, identifier string, sourceIP netip.Addr, success bool, reason string, now time.Time) error {
	id, err := s.newID()
	if err != nil {
		return ErrLocalIdentityStorage
	}
	digest := sha256.Sum256([]byte(identifier))
	return mapLocalStorageError(queries.RecordLocalLoginAttempt(ctx, dbgen.RecordLocalLoginAttemptParams{
		ID: id, IdentityID: identityID, IdentifierDigest: digest[:], SourceIp: sourceIP.Unmap(), Success: success, Reason: reason, AttemptedAt: now,
	}))
}

func (s *LocalService) appendAuthAudit(ctx context.Context, tx pgx.Tx, metadata AuthenticationMetadata, actorID *uuid.UUID, action, result, category string, aggregateID uuid.UUID, now time.Time) error {
	resourceID := aggregateID
	return s.audit.Append(ctx, tx, audit.Event{
		ActorType: "local_admin", ActorID: actorID, Action: action, ResourceType: "local_admin", ResourceID: &resourceID,
		Result: result, SourceIP: metadata.SourceIP.Unmap(), UserAgent: strings.TrimSpace(metadata.UserAgent), RequestID: metadata.RequestID,
		Details: map[string]any{"category": category},
	}, audit.OutboxEvent{
		EventType: action, AggregateType: "local_admin", AggregateID: aggregateID, PayloadVersion: 1,
		Payload: map[string]any{"category": category}, AvailableAt: now,
	})
}

func (s *LocalService) sourceAllowed(address netip.Addr) bool {
	address = address.Unmap()
	for _, prefix := range s.allowedCIDRs {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func verifyRecoveryCode(ctx context.Context, queries *dbgen.Queries, identityID uuid.UUID, candidate string) (uuid.UUID, bool, error) {
	rows, err := queries.ListUnusedLocalRecoveryCodes(ctx, identityID)
	if err != nil {
		return uuid.Nil, false, mapLocalStorageError(err)
	}
	candidateVerifier := recoveryCodeVerifier(candidate)
	for _, row := range rows {
		if subtle.ConstantTimeCompare([]byte(row.Verifier), []byte(candidateVerifier)) == 1 {
			return row.ID, true, nil
		}
	}
	return uuid.Nil, false, nil
}

func generateRecoveryCode() (string, error) {
	buffer := make([]byte, recoveryCodeBytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buffer)
	return encoded[:5] + "-" + encoded[5:10] + "-" + encoded[10:15] + "-" + encoded[15:20] + "-" + encoded[20:], nil
}

func recoveryCodeVerifier(code string) string {
	normalized := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
	digest := sha256.Sum256([]byte(normalized))
	return "$sha256$" + hex.EncodeToString(digest[:])
}

func localAdminTOTPAdditionalData(identityID uuid.UUID) []byte {
	return []byte("identity.local_admin:" + identityID.String() + ":totp:v1")
}

func normalizeIdentifier(identifier string) string {
	return strings.ToLower(strings.TrimSpace(identifier))
}

func knownIdentityID(known bool, identityID uuid.UUID) *uuid.UUID {
	if !known {
		return nil
	}
	return &identityID
}

func mapLocalStorageError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrLocalIdentityStorage, err)
}
