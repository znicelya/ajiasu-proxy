package identity

import (
	"net/url"
	"testing"

	"github.com/znicelya/ajiasu-proxy/internal/testkit"
)

func TestKeycloakDiscoveryAndPKCEAuthorizationContract(t *testing.T) {
	keycloak := testkit.StartKeycloak(t)
	provider, err := NewOIDCProvider(OIDCConfig{Issuer: keycloak.Issuer, ClientID: keycloak.ClientID, ClientSecret: keycloak.ClientSecret, RedirectURL: keycloak.RedirectURL})
	if err != nil {
		t.Fatalf("NewOIDCProvider(Keycloak): %v", err)
	}
	raw := provider.AuthorizationURL("state-value", "nonce-value", "challenge-value", "")
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if parsed.String() == "" || query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") != "challenge-value" || query.Get("state") != "state-value" || query.Get("nonce") != "nonce-value" {
		t.Fatalf("authorization URL=%q", raw)
	}
}
