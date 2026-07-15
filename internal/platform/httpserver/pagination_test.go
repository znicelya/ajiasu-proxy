package httpserver

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCursorRoundTripAndValidation(t *testing.T) {
	createdAt := time.Date(2026, 7, 15, 1, 2, 3, 456789, time.FixedZone("offset", 8*60*60))
	id := uuid.MustParse("019810d7-cc70-7f04-9b5a-d12cb85c7a11")
	cursor := EncodeCursor(createdAt, id)
	decodedAt, decodedID, err := DecodeCursor(cursor)
	if err != nil {
		t.Fatal(err)
	}
	if !decodedAt.Equal(createdAt) || decodedAt.Location() != time.UTC || decodedID != id {
		t.Fatalf("decoded cursor = %v / %s", decodedAt, decodedID)
	}
	for _, invalid := range []string{"", cursor + "=", cursor[:len(cursor)-1], "!" + cursor[1:], strings.Repeat("A", 34)} {
		if _, _, err := DecodeCursor(invalid); !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("DecodeCursor(%q) error = %v", invalid, err)
		}
	}
}

func TestParsePageDefaultsLimitsAndCursor(t *testing.T) {
	size, after, id, err := ParsePage(url.Values{})
	if err != nil || size != DefaultPageSize || !after.Equal(time.Unix(0, 0).UTC()) || id != uuid.Nil {
		t.Fatalf("default page = %d %v %s %v", size, after, id, err)
	}
	wantTime := time.Now().UTC().Round(0)
	wantID := uuid.New()
	size, after, id, err = ParsePage(url.Values{"page_size": {"200"}, "cursor": {EncodeCursor(wantTime, wantID)}})
	if err != nil || size != MaximumPageSize || !after.Equal(wantTime) || id != wantID {
		t.Fatalf("parsed page = %d %v %s %v", size, after, id, err)
	}
	for _, value := range []string{"0", "201", "x", "1.5"} {
		if _, _, _, err := ParsePage(url.Values{"page_size": {value}}); !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("page_size %q error = %v", value, err)
		}
	}
}
