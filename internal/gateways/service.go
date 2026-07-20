package gateways

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/znicelya/ajiasu-proxy/internal/platform/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Service struct {
	pools  *database.Pools
	now    func() time.Time
	random io.Reader
	newID  func() (uuid.UUID, error)
}

func NewService(pools *database.Pools) (*Service, error) {
	if pools == nil || pools.Platform == nil {
		return nil, ErrInvalidArgument
	}
	return &Service{pools: pools, now: func() time.Time { return time.Now().UTC() }, random: rand.Reader, newID: uuid.NewV7}, nil
}

func (s *Service) CreateEnrollment(ctx context.Context, gatewayName, certificateFingerprint string, createdBy uuid.UUID, validFor time.Duration) (Enrollment, string, error) {
	if strings.TrimSpace(gatewayName) == "" || len(certificateFingerprint) < 32 || createdBy == uuid.Nil || validFor <= 0 || validFor > time.Hour {
		return Enrollment{}, "", ErrInvalidArgument
	}
	token, err := randomToken(s.random)
	if err != nil {
		return Enrollment{}, "", ErrInvalidArgument
	}
	id, err := s.newID()
	if err != nil {
		return Enrollment{}, "", ErrInvalidArgument
	}
	now := s.now().UTC()
	expiry := now.Add(validFor)
	digest := sha256.Sum256([]byte(token))
	enrollment := Enrollment{ID: id, ExpectedGatewayName: strings.TrimSpace(gatewayName), TokenPrefix: token[:12], TokenVerifier: hexDigest(digest[:]), CertificateFingerprint: strings.ToLower(strings.TrimSpace(certificateFingerprint)), CreatedBy: createdBy, ExpiresAt: expiry, CreatedAt: now}
	_, err = s.pools.Platform.Exec(ctx, `INSERT INTO gateways.enrollments (id,expected_gateway_name,token_prefix,token_verifier,certificate_fingerprint,created_by,expires_at,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, enrollment.ID, enrollment.ExpectedGatewayName, enrollment.TokenPrefix, enrollment.TokenVerifier, enrollment.CertificateFingerprint, enrollment.CreatedBy, enrollment.ExpiresAt, enrollment.CreatedAt)
	if err != nil {
		return Enrollment{}, "", ErrInvalidArgument
	}
	return enrollment, token, nil
}

func (s *Service) ConsumeEnrollment(ctx context.Context, token, gatewayName, certificateFingerprint string, instanceID uuid.UUID, revision uint32) (Gateway, Session, error) {
	if len(token) < 12 || instanceID == uuid.Nil || revision != ProtocolRevision {
		return Gateway{}, Session{}, ErrInvalidArgument
	}
	digest := sha256.Sum256([]byte(token))
	verifier := hexDigest(digest[:])
	now := s.now().UTC()
	tx, err := s.pools.Platform.Begin(ctx)
	if err != nil {
		return Gateway{}, Session{}, ErrInvalidArgument
	}
	defer tx.Rollback(ctx)
	var enrollment Enrollment
	err = tx.QueryRow(ctx, `SELECT id,expected_gateway_name,token_prefix,token_verifier,certificate_fingerprint,created_by,expires_at,consumed_at,consumed_gateway_id,revoked_at,created_at FROM gateways.enrollments WHERE token_prefix=$1 FOR UPDATE`, token[:12]).Scan(&enrollment.ID, &enrollment.ExpectedGatewayName, &enrollment.TokenPrefix, &enrollment.TokenVerifier, &enrollment.CertificateFingerprint, &enrollment.CreatedBy, &enrollment.ExpiresAt, &enrollment.ConsumedAt, &enrollment.ConsumedGatewayID, &enrollment.RevokedAt, &enrollment.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Gateway{}, Session{}, ErrEnrollmentConsumed
	}
	if err != nil {
		return Gateway{}, Session{}, ErrInvalidArgument
	}
	if subtle.ConstantTimeCompare([]byte(verifier), []byte(enrollment.TokenVerifier)) != 1 {
		return Gateway{}, Session{}, ErrEnrollmentConsumed
	}
	if enrollment.RevokedAt != nil {
		return Gateway{}, Session{}, ErrEnrollmentRevoked
	}
	if !enrollment.ExpiresAt.After(now) {
		return Gateway{}, Session{}, ErrEnrollmentExpired
	}
	if enrollment.ConsumedAt != nil {
		return Gateway{}, Session{}, ErrEnrollmentConsumed
	}
	if !strings.EqualFold(enrollment.ExpectedGatewayName, gatewayName) || !strings.EqualFold(enrollment.CertificateFingerprint, certificateFingerprint) {
		return Gateway{}, Session{}, ErrCertificateMismatch
	}
	gatewayID, _ := s.newID()
	sessionID, _ := s.newID()
	sessionToken, _ := randomToken(s.random)
	sessionDigest := sha256.Sum256([]byte(sessionToken))
	sessionExpiry := now.Add(15 * time.Minute)
	gateway := Gateway{ID: gatewayID, Name: enrollment.ExpectedGatewayName, CertificateFingerprint: enrollment.CertificateFingerprint, State: "active", ConnectivityState: "online", SessionGeneration: 1, Version: 1, CreatedAt: now, UpdatedAt: now}
	session := Session{ID: sessionID, GatewayID: gatewayID, GatewayInstanceID: instanceID, TokenPrefix: sessionToken[:12], TokenVerifier: hexDigest(sessionDigest[:]), ProtocolRevision: revision, SessionGeneration: 1, ExpiresAt: sessionExpiry, CreatedAt: now}
	if _, err = tx.Exec(ctx, `INSERT INTO gateways.gateways (id,name,normalized_name,certificate_fingerprint,state,connectivity_state,session_generation,version,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)`, gateway.ID, gateway.Name, strings.ToLower(gateway.Name), gateway.CertificateFingerprint, gateway.State, gateway.ConnectivityState, gateway.SessionGeneration, gateway.Version, now); err != nil {
		return Gateway{}, Session{}, ErrInvalidArgument
	}
	if _, err = tx.Exec(ctx, `INSERT INTO gateways.sessions (id,gateway_id,gateway_instance_id,token_prefix,token_verifier,protocol_revision,session_generation,expires_at,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, session.ID, session.GatewayID, session.GatewayInstanceID, session.TokenPrefix, session.TokenVerifier, session.ProtocolRevision, session.SessionGeneration, session.ExpiresAt, now); err != nil {
		return Gateway{}, Session{}, ErrInvalidArgument
	}
	if _, err = tx.Exec(ctx, `UPDATE gateways.enrollments SET consumed_at=$2,consumed_gateway_id=$3 WHERE id=$1`, enrollment.ID, now, gateway.ID); err != nil {
		return Gateway{}, Session{}, ErrInvalidArgument
	}
	if err = tx.Commit(ctx); err != nil {
		return Gateway{}, Session{}, ErrInvalidArgument
	}
	session.Token = sessionToken
	return gateway, session, nil
}

func (s *Service) AuthenticateSession(ctx context.Context, token string, gatewayID, instanceID uuid.UUID, revision uint32) (Gateway, error) {
	if len(token) < 12 || gatewayID == uuid.Nil || instanceID == uuid.Nil || revision != ProtocolRevision {
		return Gateway{}, ErrSessionExpired
	}
	digest := sha256.Sum256([]byte(token))
	verifier := hexDigest(digest[:])
	now := s.now().UTC()
	tx, err := s.pools.Platform.Begin(ctx)
	if err != nil {
		return Gateway{}, ErrSessionExpired
	}
	defer tx.Rollback(ctx)
	var sessionGateway, sessionInstance uuid.UUID
	var storedVerifier string
	var storedRevision uint32
	var generation int64
	var expires time.Time
	if err := tx.QueryRow(ctx, `SELECT gateway_id,gateway_instance_id,token_verifier,protocol_revision,session_generation,expires_at FROM gateways.sessions WHERE token_prefix=$1 AND revoked_at IS NULL FOR UPDATE`, token[:12]).Scan(&sessionGateway, &sessionInstance, &storedVerifier, &storedRevision, &generation, &expires); err != nil {
		return Gateway{}, ErrSessionExpired
	}
	if subtle.ConstantTimeCompare([]byte(verifier), []byte(storedVerifier)) != 1 || sessionGateway != gatewayID || sessionInstance != instanceID || storedRevision != revision || !expires.After(now) {
		return Gateway{}, ErrSessionExpired
	}
	var gateway Gateway
	if err := tx.QueryRow(ctx, `SELECT id,name,certificate_fingerprint,state,connectivity_state,session_generation,version,created_at,updated_at FROM gateways.gateways WHERE id=$1 FOR UPDATE`, gatewayID).Scan(&gateway.ID, &gateway.Name, &gateway.CertificateFingerprint, &gateway.State, &gateway.ConnectivityState, &gateway.SessionGeneration, &gateway.Version, &gateway.CreatedAt, &gateway.UpdatedAt); err != nil {
		return Gateway{}, ErrSessionExpired
	}
	if gateway.State != "active" || gateway.SessionGeneration != generation {
		return Gateway{}, ErrSessionRevoked
	}
	if _, err := tx.Exec(ctx, `UPDATE gateways.sessions SET last_used_at=$1 WHERE gateway_id=$2 AND gateway_instance_id=$3`, now, gatewayID, instanceID); err != nil {
		return Gateway{}, ErrSessionExpired
	}
	if _, err := tx.Exec(ctx, `UPDATE gateways.gateways SET connectivity_state='online',updated_at=$1 WHERE id=$2`, now, gatewayID); err != nil {
		return Gateway{}, ErrSessionExpired
	}
	if err := tx.Commit(ctx); err != nil {
		return Gateway{}, ErrSessionExpired
	}
	gateway.ConnectivityState = "online"
	gateway.UpdatedAt = now
	return gateway, nil
}

func (s *Service) RecordHeartbeat(ctx context.Context, gatewayID uuid.UUID, observedAt time.Time) error {
	if gatewayID == uuid.Nil || observedAt.IsZero() || observedAt.After(s.now().UTC().Add(time.Minute)) {
		return ErrInvalidArgument
	}
	now := s.now().UTC()
	tx, err := s.pools.Platform.Begin(ctx)
	if err != nil {
		return ErrSessionExpired
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `UPDATE gateways.gateways SET connectivity_state='online',last_heartbeat_at=$1,updated_at=$2 WHERE id=$3 AND state='active'`, observedAt.UTC(), now, gatewayID)
	if err != nil {
		return ErrSessionExpired
	}
	if rows := result.RowsAffected(); rows != 1 {
		return ErrSessionRevoked
	}
	result, err = tx.Exec(ctx, `UPDATE gateways.sessions SET expires_at=$1,last_used_at=$2 WHERE gateway_id=$3 AND revoked_at IS NULL AND session_generation=(SELECT session_generation FROM gateways.gateways WHERE id=$3)`, now.Add(15*time.Minute), now, gatewayID)
	if err != nil || result.RowsAffected() != 1 {
		return ErrSessionExpired
	}
	if err := tx.Commit(ctx); err != nil {
		return ErrSessionExpired
	}
	return nil
}

func randomToken(reader io.Reader) (string, error) {
	data := make([]byte, 32)
	if _, err := io.ReadFull(reader, data); err != nil {
		return "", err
	}
	return "gwe_" + base64.RawURLEncoding.EncodeToString(data), nil
}
func hexDigest(value []byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, len(value)*2)
	for i, b := range value {
		out[2*i], out[2*i+1] = hex[b>>4], hex[b&15]
	}
	return string(out)
}
