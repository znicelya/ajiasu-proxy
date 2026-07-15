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

	"github.com/dnomd343/ajiasu-proxy/internal/platform/database"
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
