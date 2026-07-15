package identity

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" // RFC 6238 interoperability requires HMAC-SHA-1 by default.
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	totpSecretBytes = 20
	totpPeriod      = 30 * time.Second
	totpDigits      = 6
)

var ErrInvalidTOTP = errors.New("invalid TOTP input")

func GenerateTOTPSecret() (string, error) {
	secret := make([]byte, totpSecretBytes)
	if _, err := rand.Read(secret); err != nil {
		return "", fmt.Errorf("generate TOTP secret: %w", err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret), nil
}

func GenerateTOTPCode(secret string, at time.Time) (string, error) {
	key, err := decodeTOTPSecret(secret)
	if err != nil {
		return "", err
	}
	defer clear(key)
	return generateTOTPCode(key, at), nil
}

func generateTOTPCode(key []byte, at time.Time) string {
	counter := uint64(at.UTC().Unix() / int64(totpPeriod/time.Second))
	var message [8]byte
	binary.BigEndian.PutUint64(message[:], counter)
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(message[:])
	digest := mac.Sum(nil)
	offset := digest[len(digest)-1] & 0x0f
	value := (uint32(digest[offset])&0x7f)<<24 |
		uint32(digest[offset+1])<<16 |
		uint32(digest[offset+2])<<8 |
		uint32(digest[offset+3])
	return fmt.Sprintf("%0*d", totpDigits, value%1_000_000)
}

func VerifyTOTP(secret, code string, at time.Time) (bool, error) {
	key, err := decodeTOTPSecret(secret)
	if err != nil {
		return false, err
	}
	defer clear(key)
	return verifyTOTPKey(key, code, at), nil
}

func verifyTOTPSecretBytes(secret []byte, code string, at time.Time) (bool, error) {
	key, err := decodeTOTPSecretBytes(secret)
	if err != nil {
		return false, err
	}
	defer clear(key)
	return verifyTOTPKey(key, code, at), nil
}

func verifyTOTPKey(key []byte, code string, at time.Time) bool {
	if len(code) != totpDigits {
		return false
	}
	if _, err := strconv.Atoi(code); err != nil {
		return false
	}
	for offset := -1; offset <= 1; offset++ {
		candidate := generateTOTPCode(key, at.Add(time.Duration(offset)*totpPeriod))
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

func decodeTOTPSecret(secret string) ([]byte, error) {
	return decodeTOTPSecretBytes([]byte(strings.TrimSpace(secret)))
}

func decodeTOTPSecretBytes(secret []byte) ([]byte, error) {
	normalized := bytes.ToUpper(bytes.TrimSpace(secret))
	defer clear(normalized)
	encoding := base32.StdEncoding.WithPadding(base32.NoPadding)
	decoded := make([]byte, encoding.DecodedLen(len(normalized)))
	n, err := encoding.Decode(decoded, normalized)
	decoded = decoded[:n]
	if err != nil || len(decoded) < 16 {
		clear(decoded)
		return nil, ErrInvalidTOTP
	}
	return decoded, nil
}
