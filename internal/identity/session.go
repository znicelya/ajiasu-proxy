package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrSessionIdleExpired     = errors.New("session idle lifetime expired")
	ErrSessionAbsoluteExpired = errors.New("session absolute lifetime expired")
	ErrSessionRevoked         = errors.New("session is revoked")
	ErrInsecureSessionCookie  = errors.New("insecure session cookie is forbidden")
)

type SessionToken struct {
	Plaintext string
	Digest    [32]byte
	CSRFToken string
}

func NewSessionToken() (SessionToken, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return SessionToken{}, err
	}
	plaintext := base64.RawURLEncoding.EncodeToString(value[:])
	clear(value[:])
	return SessionToken{Plaintext: plaintext, Digest: DigestSessionToken(plaintext)}, nil
}

func DigestSessionToken(token string) [32]byte { return sha256.Sum256([]byte(token)) }

type Session struct {
	ID                uuid.UUID
	IdentityID        uuid.UUID
	TokenDigest       [32]byte
	CreatedAt         time.Time
	LastSeenAt        time.Time
	IdleExpiresAt     time.Time
	AbsoluteExpiresAt time.Time
	RevokedAt         *time.Time
}

func (s Session) ValidateAt(now time.Time) error {
	if s.RevokedAt != nil {
		return ErrSessionRevoked
	}
	if !now.Before(s.AbsoluteExpiresAt) {
		return ErrSessionAbsoluteExpired
	}
	if !now.Before(s.IdleExpiresAt) {
		return ErrSessionIdleExpired
	}
	return nil
}

func RotateSession(session Session, now time.Time, idleTimeout time.Duration) (Session, SessionToken, error) {
	if err := session.ValidateAt(now); err != nil {
		return Session{}, SessionToken{}, err
	}
	token, err := NewSessionToken()
	if err != nil {
		return Session{}, SessionToken{}, err
	}
	session.TokenDigest = token.Digest
	session.LastSeenAt = now.UTC()
	session.IdleExpiresAt = now.Add(idleTimeout).UTC()
	if session.IdleExpiresAt.After(session.AbsoluteExpiresAt) {
		session.IdleExpiresAt = session.AbsoluteExpiresAt
	}
	return session, token, nil
}

func RevokeSession(session Session, now time.Time) Session {
	revokedAt := now.UTC()
	session.RevokedAt = &revokedAt
	return session
}

type SessionCookieConfig struct {
	Name        string
	Path        string
	Secure      bool
	Development bool
}

func NewSessionCookie(config SessionCookieConfig, token string) (*http.Cookie, error) {
	name := strings.TrimSpace(config.Name)
	path := strings.TrimSpace(config.Path)
	if name == "" || token == "" || path == "" || path[0] != '/' || strings.ContainsAny(path, "\r\n;") {
		return nil, ErrInvalidArgument
	}
	if !config.Secure && !config.Development {
		return nil, ErrInsecureSessionCookie
	}
	return &http.Cookie{
		Name: name, Value: token, Path: path, Secure: config.Secure,
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
	}, nil
}
