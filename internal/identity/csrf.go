package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
)

var ErrUntrustedOrigin = errors.New("request origin is not trusted")

func NewCSRFToken() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(value[:])
	clear(value[:])
	return token, nil
}

func NewSynchronizerToken() (string, error) { return NewCSRFToken() }

func DigestSynchronizerToken(token string) []byte {
	d := sha256.Sum256([]byte(token))
	return d[:]
}

func VerifyCSRFToken(expected, provided string) bool {
	if expected == "" || provided == "" {
		return false
	}
	expectedDigest := sha256.Sum256([]byte(expected))
	providedDigest := sha256.Sum256([]byte(provided))
	return subtle.ConstantTimeCompare(expectedDigest[:], providedDigest[:]) == 1
}

func ValidateOrigin(origin string, trusted []string) error {
	candidate, ok := canonicalOrigin(origin)
	if !ok {
		return ErrUntrustedOrigin
	}
	for _, allowed := range trusted {
		trustedOrigin, valid := canonicalOrigin(allowed)
		if valid && candidate == trustedOrigin {
			return nil
		}
	}
	return ErrUntrustedOrigin
}

func canonicalOrigin(value string) (string, bool) {
	if strings.TrimSpace(value) != value || value == "" || value == "null" {
		return "", false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", false
	}
	return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host), true
}
