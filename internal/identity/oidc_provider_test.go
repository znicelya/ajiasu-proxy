package identity

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type oidcFixture struct {
	mu                                          sync.Mutex
	issuer, kid, audience, claimedIssuer, nonce string
	key                                         *rsa.PrivateKey
}

func TestOIDCProviderDiscoveryExchangeValidationAndKeyRotation(t *testing.T) {
	key1, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	key2, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &oidcFixture{kid: "kid-1", audience: "control-plane", nonce: "nonce-1", key: key1}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f.serve(w, r) }))
	defer server.Close()
	f.issuer = server.URL
	f.claimedIssuer = server.URL
	provider, err := NewOIDCProvider(OIDCConfig{Issuer: server.URL, ClientID: "control-plane", ClientSecret: "test-secret", RedirectURL: server.URL + "/callback", HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("NewOIDCProvider: %v", err)
	}
	auth := provider.AuthorizationURL("state", "nonce-1", "challenge", "")
	if auth == "" {
		t.Fatal("empty authorization URL")
	}
	claims, err := provider.ExchangeAndVerify(context.Background(), "code", string(make([]byte, 43)))
	if err != nil {
		t.Fatalf("ExchangeAndVerify: %v", err)
	}
	if claims.Subject != "subject-1" || claims.Nonce != "nonce-1" {
		t.Fatalf("claims=%#v", claims)
	}
	f.mu.Lock()
	f.key = key2
	f.kid = "kid-2"
	f.mu.Unlock()
	if _, err := provider.ExchangeAndVerify(context.Background(), "code", string(make([]byte, 43))); err != nil {
		t.Fatalf("unknown kid rotation: %v", err)
	}
	f.mu.Lock()
	f.audience = "wrong-client"
	f.mu.Unlock()
	if _, err := provider.ExchangeAndVerify(context.Background(), "code", string(make([]byte, 43))); !errors.Is(err, ErrOIDCInvalidClaims) {
		t.Fatalf("wrong audience error=%v", err)
	}
	f.mu.Lock()
	f.audience = "control-plane"
	f.claimedIssuer = server.URL + "/wrong"
	f.mu.Unlock()
	if _, err := provider.ExchangeAndVerify(context.Background(), "code", string(make([]byte, 43))); !errors.Is(err, ErrOIDCInvalidClaims) {
		t.Fatalf("wrong issuer error=%v", err)
	}
}

func (f *oidcFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		json.NewEncoder(w).Encode(map[string]any{"issuer": f.issuer, "authorization_endpoint": f.issuer + "/authorize", "token_endpoint": f.issuer + "/token", "jwks_uri": f.issuer + "/jwks", "code_challenge_methods_supported": []string{"S256"}})
	case "/jwks":
		e := big.NewInt(int64(f.key.PublicKey.E)).Bytes()
		json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{"kid": f.kid, "kty": "RSA", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(f.key.PublicKey.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(e)}}})
	case "/token":
		_ = r.ParseForm()
		if r.Form.Get("client_secret") != "test-secret" || r.Form.Get("code_verifier") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id_token": fixtureJWT(f.key, f.kid, map[string]any{"iss": f.claimedIssuer, "sub": "subject-1", "aud": f.audience, "nonce": f.nonce, "email": "user@example.test", "name": "OIDC User", "iat": time.Now().Add(-time.Minute).Unix(), "exp": time.Now().Add(time.Minute).Unix()})})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func fixtureJWT(key *rsa.PrivateKey, kid string, claims map[string]any) string {
	header, _ := json.Marshal(map[string]any{"alg": "RS256", "kid": kid, "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(unsigned))
	signature, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
}
