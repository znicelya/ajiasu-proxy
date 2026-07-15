package secrets

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

const AccountCredentialPurpose = "ajiasu-account-credential"

var (
	ErrInvalidContext = errors.New("invalid secret context")
	ErrInvalidSecret  = errors.New("invalid sealed secret")
	ErrIntegrity      = errors.New("secret integrity verification failed")
	ErrUnavailable    = errors.New("secret provider unavailable")
)

type Context struct {
	TenantID  uuid.UUID
	AccountID uuid.UUID
	Version   int64
	Purpose   string
}

func (c Context) Valid() bool {
	return c.TenantID != uuid.Nil && c.AccountID != uuid.Nil && c.Version > 0 && c.Purpose == AccountCredentialPurpose
}

type SealedSecret struct {
	Provider    string
	KeyID       string
	Ciphertext  []byte
	WrappedDEK  []byte
	ExternalRef string
}

func (s SealedSecret) Clone() SealedSecret {
	s.Ciphertext = append([]byte(nil), s.Ciphertext...)
	s.WrappedDEK = append([]byte(nil), s.WrappedDEK...)
	return s
}

type Provider interface {
	Seal(context.Context, Context, []byte) (SealedSecret, error)
	Open(context.Context, Context, SealedSecret) ([]byte, error)
	Destroy(context.Context, Context, SealedSecret) error
}
