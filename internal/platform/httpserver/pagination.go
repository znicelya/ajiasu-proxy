package httpserver

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
)

const (
	cursorVersion   = byte(1)
	cursorByteSize  = 1 + 8 + 16
	DefaultPageSize = 50
	MaximumPageSize = 200
)

var ErrInvalidCursor = errors.New("invalid pagination cursor")

func EncodeCursor(createdAt time.Time, id uuid.UUID) string {
	payload := make([]byte, cursorByteSize)
	payload[0] = cursorVersion
	binary.BigEndian.PutUint64(payload[1:9], uint64(createdAt.UTC().UnixNano()))
	copy(payload[9:], id[:])
	return base64.RawURLEncoding.EncodeToString(payload)
}

func DecodeCursor(value string) (time.Time, uuid.UUID, error) {
	if value == "" {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(payload) != cursorByteSize || payload[0] != cursorVersion {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	id, err := uuid.FromBytes(payload[9:])
	if err != nil || id == uuid.Nil {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	nanos := int64(binary.BigEndian.Uint64(payload[1:9]))
	createdAt := time.Unix(0, nanos).UTC()
	if createdAt.UnixNano() != nanos {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	return createdAt, id, nil
}

func ParsePage(values url.Values) (int32, time.Time, uuid.UUID, error) {
	pageSize := DefaultPageSize
	if raw := values.Get("page_size"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > MaximumPageSize {
			return 0, time.Time{}, uuid.Nil, ErrInvalidCursor
		}
		pageSize = parsed
	}
	if raw := values.Get("cursor"); raw != "" {
		createdAt, id, err := DecodeCursor(raw)
		return int32(pageSize), createdAt, id, err
	}
	return int32(pageSize), time.Unix(0, 0).UTC(), uuid.Nil, nil
}
