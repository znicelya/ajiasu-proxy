package testkit

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	keycloakPort = "8080/tcp"

	KeycloakTestRealm             = "ajiasu-test"
	KeycloakTestClientID          = "control-plane"
	KeycloakTestClientSecret      = "control-plane-test-secret"
	KeycloakWrongAudienceClientID = "wrong-audience-client"
	KeycloakWrongAudienceSecret   = "wrong-audience-test-secret"
	KeycloakTestUsername          = "oidc-user"
	KeycloakTestPassword          = "oidc-user-test-password"
	KeycloakTestSubject           = "01900000-0000-7000-8000-000000000601"
	KeycloakTestRedirectURL       = "http://127.0.0.1/callback"
)

//go:embed testdata/keycloak/ajiasu-test-realm.json
var keycloakRealmJSON []byte

// Keycloak describes a locked, realm-imported integration-test instance.
type Keycloak struct {
	Container testcontainers.Container

	BaseURL               string
	Issuer                string
	DiscoveryURL          string
	AuthorizationEndpoint string
	TokenEndpoint         string
	JWKSURL               string
	Realm                 string
	ClientID              string
	ClientSecret          string
	WrongAudienceClientID string
	WrongAudienceSecret   string
	RedirectURL           string
	Username              string
	Password              string
	Subject               string
	AdminUsername         string
	AdminPassword         string
}

// StartKeycloak starts the digest-locked Keycloak image and imports the test realm.
func StartKeycloak(t *testing.T) *Keycloak {
	t.Helper()
	ctx := t.Context()
	ensureKeycloakDocker(t, ctx)
	if !json.Valid(keycloakRealmJSON) {
		t.Fatal("embedded Keycloak realm fixture is not valid JSON")
	}

	adminUsername := "admin"
	adminPassword := randomPassword(t)
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        lockedKeycloakImage(t),
			ExposedPorts: []string{keycloakPort},
			Env: map[string]string{
				"KC_BOOTSTRAP_ADMIN_USERNAME": adminUsername,
				"KC_BOOTSTRAP_ADMIN_PASSWORD": adminPassword,
				"KC_HEALTH_ENABLED":           "true",
			},
			Cmd: []string{"start-dev", "--import-realm", "--http-enabled=true", "--hostname-strict=false"},
			Files: []testcontainers.ContainerFile{
				{
					Reader:            bytes.NewReader(keycloakRealmJSON),
					ContainerFilePath: "/opt/keycloak/data/import/ajiasu-test-realm.json",
					FileMode:          0o444,
				},
			},
			WaitingFor: wait.ForHTTP("/realms/" + KeycloakTestRealm + "/.well-known/openid-configuration").
				WithPort(keycloakPort).
				WithStartupTimeout(4 * time.Minute),
		},
		Started: true,
	})
	testcontainers.CleanupContainer(t, container)
	if err != nil {
		t.Fatalf("start locked Keycloak container: %v", err)
	}

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("resolve Keycloak host: %v", err)
	}
	port, err := container.MappedPort(ctx, keycloakPort)
	if err != nil {
		t.Fatalf("resolve Keycloak port: %v", err)
	}
	baseURL := (&url.URL{Scheme: "http", Host: net.JoinHostPort(host, port.Port())}).String()
	issuer := baseURL + "/realms/" + KeycloakTestRealm
	keycloak := &Keycloak{
		Container:             container,
		BaseURL:               baseURL,
		Issuer:                issuer,
		DiscoveryURL:          issuer + "/.well-known/openid-configuration",
		AuthorizationEndpoint: issuer + "/protocol/openid-connect/auth",
		TokenEndpoint:         issuer + "/protocol/openid-connect/token",
		JWKSURL:               issuer + "/protocol/openid-connect/certs",
		Realm:                 KeycloakTestRealm,
		ClientID:              KeycloakTestClientID,
		ClientSecret:          KeycloakTestClientSecret,
		WrongAudienceClientID: KeycloakWrongAudienceClientID,
		WrongAudienceSecret:   KeycloakWrongAudienceSecret,
		RedirectURL:           KeycloakTestRedirectURL,
		Username:              KeycloakTestUsername,
		Password:              KeycloakTestPassword,
		Subject:               KeycloakTestSubject,
		AdminUsername:         adminUsername,
		AdminPassword:         adminPassword,
	}
	verifyKeycloakDiscovery(t, ctx, keycloak)
	verifyKeycloakFixture(t, ctx, keycloak)
	return keycloak
}

func ensureKeycloakDocker(t *testing.T, ctx context.Context) {
	t.Helper()
	provider, err := testcontainers.ProviderDocker.GetProvider()
	if err != nil {
		if dockerRequired() {
			t.Fatalf("Docker provider is required for Keycloak integration tests: %v", err)
		}
		t.Skip("BLOCKED: Docker provider is unavailable; Keycloak integration behavior is not mocked")
	}
	defer provider.Close()
	if err := provider.Health(ctx); err != nil {
		if dockerRequired() {
			t.Fatalf("Docker daemon is required for Keycloak integration tests: %v", err)
		}
		t.Skip("BLOCKED: Docker daemon is unavailable; Keycloak integration behavior is not mocked")
	}
}

func lockedKeycloakImage(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate Keycloak testkit source")
	}
	lockPath := filepath.Join(filepath.Dir(source), "..", "..", "build", "control-plane-images.lock")
	content, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read control-plane image lock: %v", err)
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n") {
		image, found := strings.CutPrefix(line, "KEYCLOAK_IMAGE=")
		if !found {
			continue
		}
		tag, digest, ok := strings.Cut(image, "@sha256:")
		if !ok || tag == "" || len(digest) != 64 {
			t.Fatal("invalid KEYCLOAK_IMAGE lock")
		}
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != 32 || digest != strings.ToLower(digest) {
			t.Fatal("invalid KEYCLOAK_IMAGE digest")
		}
		return image
	}
	t.Fatal("KEYCLOAK_IMAGE is missing from control-plane image lock")
	return ""
}

func verifyKeycloakDiscovery(t *testing.T, ctx context.Context, keycloak *Keycloak) {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, keycloak.DiscoveryURL, nil)
	if err != nil {
		t.Fatalf("create Keycloak Discovery request: %v", err)
	}
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("query Keycloak Discovery: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
		t.Fatalf("Keycloak Discovery status = %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	var document struct {
		Issuer                        string   `json:"issuer"`
		AuthorizationEndpoint         string   `json:"authorization_endpoint"`
		TokenEndpoint                 string   `json:"token_endpoint"`
		JWKSURL                       string   `json:"jwks_uri"`
		CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&document); err != nil {
		t.Fatalf("decode Keycloak Discovery: %v", err)
	}
	if document.Issuer != keycloak.Issuer ||
		document.AuthorizationEndpoint != keycloak.AuthorizationEndpoint ||
		document.TokenEndpoint != keycloak.TokenEndpoint ||
		document.JWKSURL != keycloak.JWKSURL {
		t.Fatalf("unexpected Keycloak Discovery document: issuer=%q authorization=%q token=%q jwks=%q",
			document.Issuer, document.AuthorizationEndpoint, document.TokenEndpoint, document.JWKSURL)
	}
	if !containsString(document.CodeChallengeMethodsSupported, "S256") {
		t.Fatalf("Keycloak Discovery code_challenge_methods_supported = %q, want S256", document.CodeChallengeMethodsSupported)
	}
}

func verifyKeycloakFixture(t *testing.T, ctx context.Context, keycloak *Keycloak) {
	t.Helper()

	primaryIDToken := requestPasswordGrant(t, ctx, keycloak.TokenEndpoint, url.Values{
		"grant_type":    {"password"},
		"client_id":     {keycloak.ClientID},
		"client_secret": {keycloak.ClientSecret},
		"username":      {keycloak.Username},
		"password":      {keycloak.Password},
		"scope":         {"openid"},
	}, http.StatusOK, "")
	verifyFixtureIDToken(t, primaryIDToken, keycloak.Subject, keycloak.ClientID)

	requestPasswordGrant(t, ctx, keycloak.TokenEndpoint, url.Values{
		"grant_type":    {"password"},
		"client_id":     {keycloak.ClientID},
		"client_secret": {keycloak.ClientSecret + "-invalid"},
		"username":      {keycloak.Username},
		"password":      {keycloak.Password},
		"scope":         {"openid"},
	}, http.StatusUnauthorized, "unauthorized_client")
	requestPasswordGrant(t, ctx, keycloak.TokenEndpoint, url.Values{
		"grant_type":    {"password"},
		"client_id":     {keycloak.ClientID},
		"client_secret": {keycloak.ClientSecret},
		"username":      {keycloak.Username},
		"password":      {keycloak.Password + "-invalid"},
		"scope":         {"openid"},
	}, http.StatusUnauthorized, "invalid_grant")

	wrongAudienceIDToken := requestPasswordGrant(t, ctx, keycloak.TokenEndpoint, url.Values{
		"grant_type":    {"password"},
		"client_id":     {keycloak.WrongAudienceClientID},
		"client_secret": {keycloak.WrongAudienceSecret},
		"username":      {keycloak.Username},
		"password":      {keycloak.Password},
		"scope":         {"openid"},
	}, http.StatusOK, "")
	verifyFixtureIDToken(t, wrongAudienceIDToken, keycloak.Subject, keycloak.WrongAudienceClientID)
	verifyKeycloakPKCEFixture(t, ctx, keycloak)
}

func requestPasswordGrant(
	t *testing.T,
	ctx context.Context,
	tokenEndpoint string,
	form url.Values,
	wantStatus int,
	wantOAuthError string,
) string {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("create Keycloak token request: %v", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("query Keycloak token endpoint: %v", err)
	}
	defer response.Body.Close()

	var result struct {
		IDToken          string `json:"id_token"`
		OAuthError       string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		t.Fatalf("decode Keycloak token response with status %d: %v", response.StatusCode, err)
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("Keycloak token status = %d, want %d (oauth_error=%q)", response.StatusCode, wantStatus, result.OAuthError)
	}
	if result.OAuthError != wantOAuthError {
		t.Fatalf("Keycloak token oauth error = %q, want %q", result.OAuthError, wantOAuthError)
	}
	if wantStatus == http.StatusOK && result.IDToken == "" {
		t.Fatal("Keycloak successful password grant omitted id_token")
	}
	return result.IDToken
}

func verifyFixtureIDToken(t *testing.T, rawToken, wantSubject, wantAudience string) {
	t.Helper()
	claims, err := decodeFixtureIDToken(rawToken)
	if err != nil {
		t.Fatalf("decode Keycloak fixture ID token: %v", err)
	}
	if claims.Subject != wantSubject {
		t.Fatalf("Keycloak fixture ID token subject = %q, want %q", claims.Subject, wantSubject)
	}
	if len(claims.Audience) != 1 || claims.Audience[0] != wantAudience {
		t.Fatalf("Keycloak fixture ID token audience = %q, want only %q", claims.Audience, wantAudience)
	}
}

type fixtureIDTokenClaims struct {
	Subject  string
	Audience []string
}

func decodeFixtureIDToken(rawToken string) (fixtureIDTokenClaims, error) {
	parts := strings.Split(rawToken, ".")
	if len(parts) != 3 {
		return fixtureIDTokenClaims{}, errors.New("token does not have three JWT segments")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fixtureIDTokenClaims{}, errors.New("decode JWT payload")
	}
	var document struct {
		Subject  string          `json:"sub"`
		Audience json.RawMessage `json:"aud"`
	}
	if err := json.Unmarshal(payload, &document); err != nil {
		return fixtureIDTokenClaims{}, errors.New("decode JWT claims")
	}
	audience, err := decodeFixtureAudience(document.Audience)
	if err != nil {
		return fixtureIDTokenClaims{}, err
	}
	return fixtureIDTokenClaims{Subject: document.Subject, Audience: audience}, nil
}

func decodeFixtureAudience(raw json.RawMessage) ([]string, error) {
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return []string{single}, nil
	}
	var multiple []string
	if err := json.Unmarshal(raw, &multiple); err != nil {
		return nil, errors.New("JWT aud claim is not a string or string array")
	}
	return multiple, nil
}

func verifyKeycloakPKCEFixture(t *testing.T, ctx context.Context, keycloak *Keycloak) {
	t.Helper()
	verifier := "ajiasu-keycloak-fixture-pkce-verifier-00000000001"
	digest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])

	validQuery := authorizationFixtureQuery(keycloak)
	validQuery.Set("code_challenge", challenge)
	validQuery.Set("code_challenge_method", "S256")
	validResponse := requestAuthorizationFixture(t, ctx, keycloak.AuthorizationEndpoint, validQuery)
	defer validResponse.Body.Close()
	if validResponse.StatusCode != http.StatusOK {
		t.Fatalf("Keycloak S256 authorization fixture status = %d, want %d", validResponse.StatusCode, http.StatusOK)
	}

	missingResponse := requestAuthorizationFixture(t, ctx, keycloak.AuthorizationEndpoint, authorizationFixtureQuery(keycloak))
	defer missingResponse.Body.Close()
	if missingResponse.StatusCode == http.StatusOK {
		t.Fatal("Keycloak authorization fixture accepted a request without PKCE")
	}
	location := missingResponse.Header.Get("Location")
	if location != "" {
		redirect, err := url.Parse(location)
		if err != nil {
			t.Fatalf("parse Keycloak missing-PKCE redirect: %v", err)
		}
		query := redirect.Query()
		if query.Get("error") == "" || !strings.Contains(strings.ToLower(query.Get("error_description")), "code_challenge") {
			t.Fatalf("unexpected Keycloak missing-PKCE redirect error=%q description=%q", query.Get("error"), query.Get("error_description"))
		}
		return
	}
	body, err := io.ReadAll(io.LimitReader(missingResponse.Body, 64<<10))
	if err != nil {
		t.Fatalf("read Keycloak missing-PKCE response: %v", err)
	}
	if !strings.Contains(strings.ToLower(string(body)), "code_challenge") {
		t.Fatalf("Keycloak missing-PKCE status = %d without a code_challenge error", missingResponse.StatusCode)
	}
}

func authorizationFixtureQuery(keycloak *Keycloak) url.Values {
	return url.Values{
		"response_type": {"code"},
		"client_id":     {keycloak.ClientID},
		"redirect_uri":  {keycloak.RedirectURL},
		"scope":         {"openid"},
		"state":         {"keycloak-fixture-state"},
		"nonce":         {"keycloak-fixture-nonce"},
	}
}

func requestAuthorizationFixture(t *testing.T, ctx context.Context, endpoint string, query url.Values) *http.Response {
	t.Helper()
	requestURL, err := url.Parse(endpoint)
	if err != nil {
		t.Fatalf("parse Keycloak authorization endpoint: %v", err)
	}
	requestURL.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		t.Fatalf("create Keycloak authorization request: %v", err)
	}
	client := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("query Keycloak authorization endpoint: %v", err)
	}
	return response
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
