package testkit

import (
	"encoding/base64"
	"testing"
)

func TestKeycloakFixtureContract(t *testing.T) {
	keycloak := StartKeycloak(t)
	if keycloak.Container == nil {
		t.Fatal("StartKeycloak returned a nil container")
	}
}

func TestDecodeFixtureIDTokenAudience(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		payload  string
		want     []string
		wantFail bool
	}{
		{name: "string", payload: `{"sub":"subject","aud":"client"}`, want: []string{"client"}},
		{name: "array", payload: `{"sub":"subject","aud":["client","account"]}`, want: []string{"client", "account"}},
		{name: "invalid", payload: `{"sub":"subject","aud":7}`, wantFail: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			rawToken := "header." + base64.RawURLEncoding.EncodeToString([]byte(test.payload)) + ".signature"
			claims, err := decodeFixtureIDToken(rawToken)
			if test.wantFail {
				if err == nil {
					t.Fatal("decodeFixtureIDToken() error = nil, want failure")
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeFixtureIDToken() error = %v", err)
			}
			if claims.Subject != "subject" {
				t.Fatalf("subject = %q, want subject", claims.Subject)
			}
			if len(claims.Audience) != len(test.want) {
				t.Fatalf("audience = %q, want %q", claims.Audience, test.want)
			}
			for index := range test.want {
				if claims.Audience[index] != test.want[index] {
					t.Fatalf("audience = %q, want %q", claims.Audience, test.want)
				}
			}
		})
	}
}
