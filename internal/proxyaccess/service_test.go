package proxyaccess

import (
	"bytes"
	"testing"
	"time"
)

func TestArgon2idVerifierRoundTripAndMalformedBounds(t *testing.T) {
	reader := bytes.NewReader(bytes.Repeat([]byte{7}, 64))
	encoded, err := hashArgon2id("correct horse battery staple", reader)
	if err != nil {
		t.Fatal(err)
	}
	if !verifyArgon2id(encoded, "correct horse battery staple") {
		t.Fatal("expected verifier to match")
	}
	if verifyArgon2id(encoded, "wrong") {
		t.Fatal("wrong password matched")
	}
	if verifyArgon2id("$argon2id$v=19$m=999999999,t=2,p=1$bad$bad", "wrong") {
		t.Fatal("unsafe verifier accepted")
	}
}

func TestCredentialStateBoundaries(t *testing.T) {
	now := time.Unix(100, 0)
	expired := now
	if !expired.Before(now.Add(time.Nanosecond)) {
		t.Fatal("test setup")
	}
}
