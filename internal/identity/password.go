package identity

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

var ErrInvalidPasswordVerifier = errors.New("invalid password verifier")

type PasswordParams struct {
	Memory      uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

var DefaultPasswordParams = PasswordParams{
	Memory: 64 * 1024, Iterations: 3, Parallelism: 2, SaltLength: 16, KeyLength: 32,
}

func HashPassword(password []byte) (string, error) {
	return HashPasswordWithParams(password, DefaultPasswordParams)
}

func HashPasswordWithParams(password []byte, params PasswordParams) (string, error) {
	if len(password) == 0 || !params.valid() {
		return "", ErrInvalidPasswordVerifier
	}
	salt := make([]byte, params.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	hash := argon2.IDKey(password, salt, params.Iterations, params.Memory, params.Parallelism, params.KeyLength)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, params.Memory, params.Iterations, params.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}

func VerifyPassword(password []byte, verifier string) (bool, error) {
	if len(password) > maxPasswordBytes || len(verifier) > 1024 {
		return false, ErrInvalidPasswordVerifier
	}
	params, salt, expected, err := parsePasswordVerifier(verifier)
	if err != nil {
		return false, err
	}
	actual := argon2.IDKey(password, salt, params.Iterations, params.Memory, params.Parallelism, params.KeyLength)
	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}

func (p PasswordParams) valid() bool {
	return p.Memory >= 8*1024 && p.Memory <= 256*1024 &&
		p.Iterations > 0 && p.Iterations <= 10 &&
		p.Parallelism > 0 && p.Parallelism <= 16 &&
		p.SaltLength >= 16 && p.SaltLength <= 64 &&
		p.KeyLength >= 16 && p.KeyLength <= 64
}

func parsePasswordVerifier(verifier string) (PasswordParams, []byte, []byte, error) {
	parts := strings.Split(verifier, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return PasswordParams{}, nil, nil, ErrInvalidPasswordVerifier
	}
	var params PasswordParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &params.Memory, &params.Iterations, &params.Parallelism); err != nil {
		return PasswordParams{}, nil, nil, ErrInvalidPasswordVerifier
	}
	if parts[3] != fmt.Sprintf("m=%d,t=%d,p=%d", params.Memory, params.Iterations, params.Parallelism) {
		return PasswordParams{}, nil, nil, ErrInvalidPasswordVerifier
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return PasswordParams{}, nil, nil, ErrInvalidPasswordVerifier
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return PasswordParams{}, nil, nil, ErrInvalidPasswordVerifier
	}
	params.SaltLength = uint32(len(salt))
	params.KeyLength = uint32(len(expected))
	if !params.valid() {
		return PasswordParams{}, nil, nil, ErrInvalidPasswordVerifier
	}
	return params, salt, expected, nil
}
