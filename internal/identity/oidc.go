package identity

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrOIDCProvider          = errors.New("oidc provider failure")
	ErrOIDCInvalidState      = errors.New("invalid oidc state")
	ErrOIDCInvalidCode       = errors.New("invalid oidc authorization code")
	ErrOIDCInvalidClaims     = errors.New("invalid oidc claims")
	ErrOIDCUnsupportedIssuer = errors.New("unsupported oidc issuer")
)

type Claims struct {
	Issuer    string
	Subject   string
	Email     string
	Name      string
	Nonce     string
	IssuedAt  time.Time
	ExpiresAt time.Time
}

type OIDCProvider interface {
	AuthorizationURL(state, nonce, challenge, redirect string) string
	ExchangeAndVerify(ctx context.Context, code, verifier string) (Claims, error)
}

type OIDCConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	HTTPClient   *http.Client
	Clock        func() time.Time
}

type oidcDiscovery struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	JWKSURI               string   `json:"jwks_uri"`
	CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
}

type oidcProvider struct {
	cfg                OIDCConfig
	client             *http.Client
	discovery          oidcDiscovery
	mu                 sync.Mutex
	refreshMu          sync.Mutex
	keys               map[string]*rsa.PublicKey
	keysFetchedAt      time.Time
	lastUnknownRefresh time.Time
}

// NewOIDCProvider accepts either an OIDCConfig, or issuer/clientID/clientSecret/redirectURL.
// The variadic form keeps the small package usable by command and integration tests without
// exposing provider internals.
func NewOIDCProvider(args ...any) (*oidcProvider, error) {
	var cfg OIDCConfig
	if len(args) == 1 {
		var ok bool
		cfg, ok = args[0].(OIDCConfig)
		if !ok {
			return nil, fmt.Errorf("%w: expected OIDCConfig", ErrOIDCProvider)
		}
	} else if len(args) == 4 {
		var ok bool
		cfg.Issuer, ok = args[0].(string)
		if !ok {
			return nil, ErrOIDCProvider
		}
		cfg.ClientID, ok = args[1].(string)
		if !ok {
			return nil, ErrOIDCProvider
		}
		cfg.ClientSecret, ok = args[2].(string)
		if !ok {
			return nil, ErrOIDCProvider
		}
		cfg.RedirectURL, ok = args[3].(string)
		if !ok {
			return nil, ErrOIDCProvider
		}
	} else {
		return nil, fmt.Errorf("%w: invalid constructor arguments", ErrOIDCProvider)
	}
	issuer, err := validateAbsoluteURL(cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("%w: issuer: %v", ErrOIDCProvider, err)
	}
	redirect, err := validateAbsoluteURL(cfg.RedirectURL)
	if err != nil {
		return nil, fmt.Errorf("%w: redirect: %v", ErrOIDCProvider, err)
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, fmt.Errorf("%w: client credentials required", ErrOIDCProvider)
	}
	cfg.Issuer, cfg.RedirectURL = strings.TrimRight(issuer, "/"), redirect
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	client := *cfg.HTTPClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if cfg.Clock == nil {
		cfg.Clock = func() time.Time { return time.Now().UTC() }
	}
	p := &oidcProvider{cfg: cfg, client: &client, keys: make(map[string]*rsa.PublicKey)}
	if err := p.discover(context.Background()); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *oidcProvider) AuthorizationURL(state, nonce, challenge, redirect string) string {
	if p == nil {
		return ""
	}
	if redirect != "" && redirect != p.cfg.RedirectURL {
		return ""
	}
	redirect = p.cfg.RedirectURL
	u, err := url.Parse(p.discovery.AuthorizationEndpoint)
	if err != nil {
		return ""
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", p.cfg.ClientID)
	q.Set("redirect_uri", redirect)
	q.Set("scope", "openid profile email")
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	u.RawQuery = q.Encode()
	return u.String()
}

func (p *oidcProvider) ExchangeAndVerify(ctx context.Context, code, verifier string) (Claims, error) {
	if p == nil || strings.TrimSpace(code) == "" || len(verifier) < 43 {
		return Claims{}, ErrOIDCInvalidCode
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {p.cfg.ClientID}, "client_secret": {p.cfg.ClientSecret}, "redirect_uri": {p.cfg.RedirectURL}, "code_verifier": {verifier}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.discovery.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return Claims{}, fmt.Errorf("%w: token request: %v", ErrOIDCProvider, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := p.client.Do(req)
	if err != nil {
		return Claims{}, fmt.Errorf("%w: token exchange: %v", ErrOIDCProvider, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if res.StatusCode != http.StatusOK {
		return Claims{}, fmt.Errorf("%w: token endpoint status %d", ErrOIDCInvalidCode, res.StatusCode)
	}
	var token struct {
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(body, &token); err != nil || token.IDToken == "" {
		return Claims{}, ErrOIDCInvalidCode
	}
	return p.verifyJWT(ctx, token.IDToken)
}

func (p *oidcProvider) discover(ctx context.Context) error {
	endpoint := strings.TrimRight(p.cfg.Issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("%w: discovery request", ErrOIDCProvider)
	}
	res, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: discovery: %v", ErrOIDCProvider, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: discovery status %d", ErrOIDCProvider, res.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&p.discovery); err != nil {
		return fmt.Errorf("%w: decode discovery", ErrOIDCProvider)
	}
	if p.discovery.Issuer != p.cfg.Issuer || p.discovery.AuthorizationEndpoint == "" || p.discovery.TokenEndpoint == "" || p.discovery.JWKSURI == "" || !containsString(p.discovery.CodeChallengeMethods, "S256") {
		return ErrOIDCUnsupportedIssuer
	}
	issuerURL, _ := url.Parse(p.cfg.Issuer)
	for _, endpoint := range []string{p.discovery.AuthorizationEndpoint, p.discovery.TokenEndpoint, p.discovery.JWKSURI} {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Scheme != issuerURL.Scheme || !strings.EqualFold(parsed.Host, issuerURL.Host) || parsed.User != nil {
			return ErrOIDCUnsupportedIssuer
		}
	}
	return nil
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}

func (p *oidcProvider) verifyJWT(ctx context.Context, raw string) (Claims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return Claims{}, ErrOIDCInvalidClaims
	}
	decode := func(s string, v any) error {
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			return err
		}
		return json.Unmarshal(b, v)
	}
	var h jwtHeader
	var payload map[string]any
	if decode(parts[0], &h) != nil || decode(parts[1], &payload) != nil || h.Kid == "" || h.Alg != "RS256" {
		return Claims{}, ErrOIDCInvalidClaims
	}
	key, err := p.key(ctx, h.Kid)
	if err != nil {
		return Claims{}, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Claims{}, ErrOIDCInvalidClaims
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig); err != nil {
		return Claims{}, ErrOIDCInvalidClaims
	}
	str := func(name string) string { v, _ := payload[name].(string); return v }
	issuer, sub := str("iss"), str("sub")
	audiences := audienceValues(payload["aud"])
	azp := str("azp")
	if issuer != p.cfg.Issuer || sub == "" || !containsString(audiences, p.cfg.ClientID) || azp != "" && azp != p.cfg.ClientID || len(audiences) > 1 && azp != p.cfg.ClientID {
		return Claims{}, ErrOIDCInvalidClaims
	}
	now := p.cfg.Clock()
	exp, ok := numericTime(payload["exp"])
	if !ok || !now.Before(exp) {
		return Claims{}, ErrOIDCInvalidClaims
	}
	iat, ok := numericTime(payload["iat"])
	if !ok || iat.After(now.Add(60*time.Second)) {
		return Claims{}, ErrOIDCInvalidClaims
	}
	if nbf, ok := numericTime(payload["nbf"]); ok && nbf.After(now.Add(60*time.Second)) {
		return Claims{}, ErrOIDCInvalidClaims
	}
	return Claims{Issuer: issuer, Subject: sub, Email: str("email"), Name: firstNonEmpty(str("name"), strings.TrimSpace(str("preferred_username"))), Nonce: str("nonce"), IssuedAt: iat, ExpiresAt: exp}, nil
}

func (p *oidcProvider) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	p.mu.Lock()
	key := p.keys[kid]
	fetchedAt := p.keysFetchedAt
	p.mu.Unlock()
	if key != nil && p.cfg.Clock().Sub(fetchedAt) < 10*time.Minute {
		return key, nil
	}
	p.refreshMu.Lock()
	defer p.refreshMu.Unlock()
	p.mu.Lock()
	if key = p.keys[kid]; key != nil && p.cfg.Clock().Sub(p.keysFetchedAt) < 10*time.Minute {
		p.mu.Unlock()
		return key, nil
	}
	haveKeys := len(p.keys) != 0
	lastUnknown := p.lastUnknownRefresh
	p.mu.Unlock()
	if haveKeys && p.cfg.Clock().Sub(lastUnknown) < 5*time.Second {
		return nil, ErrOIDCInvalidClaims
	}
	if haveKeys {
		p.mu.Lock()
		p.lastUnknownRefresh = p.cfg.Clock()
		p.mu.Unlock()
	}
	if err := p.refreshKeys(ctx); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if key = p.keys[kid]; key == nil {
		return nil, ErrOIDCInvalidClaims
	}
	return key, nil
}

func (p *oidcProvider) refreshKeys(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.discovery.JWKSURI, nil)
	if err != nil {
		return fmt.Errorf("%w: jwks request", ErrOIDCProvider)
	}
	res, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: jwks: %v", ErrOIDCProvider, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: jwks status %d", ErrOIDCProvider, res.StatusCode)
	}
	var doc struct {
		Keys []struct{ Kid, Kty, Alg, N, E string } `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&doc); err != nil {
		return err
	}
	keys := make(map[string]*rsa.PublicKey)
	for _, k := range doc.Keys {
		if k.Kid == "" || k.Kty != "RSA" || k.Alg != "RS256" {
			continue
		}
		n, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			continue
		}
		e2, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil || len(n) == 0 || len(e2) == 0 {
			continue
		}
		exp := 0
		for _, b := range e2 {
			exp = exp<<8 | int(b)
		}
		bits := new(big.Int).SetBytes(n).BitLen()
		if exp < 3 || exp > 1<<30 || exp%2 == 0 || bits < 2048 || bits > 8192 {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: exp}
	}
	p.mu.Lock()
	if len(keys) == 0 {
		p.mu.Unlock()
		return ErrOIDCInvalidClaims
	}
	p.keys = keys
	p.keysFetchedAt = p.cfg.Clock()
	p.mu.Unlock()
	return nil
}

func audienceValues(v any) []string {
	if s, ok := v.(string); ok && s != "" {
		return []string{s}
	}
	a, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(a))
	for _, x := range a {
		s, ok := x.(string)
		if !ok || s == "" {
			return nil
		}
		out = append(out, s)
	}
	return out
}
func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
func numericTime(v any) (time.Time, bool) {
	switch n := v.(type) {
	case float64:
		return time.Unix(int64(n), 0), true
	case json.Number:
		i, e := strconv.ParseInt(string(n), 10, 64)
		return time.Unix(i, 0), e == nil
	}
	return time.Time{}, false
}
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
func validateAbsoluteURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" || u.User != nil || strings.ContainsAny(raw, "\r\n") {
		return "", errors.New("absolute http(s) URL required")
	}
	return u.String(), nil
}

func randomURLToken(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func pkceChallenge(verifier string) string {
	d := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(d[:])
}
func digestString(v string) []byte { d := sha256.Sum256([]byte(v)); return d[:] }
func equalDigest(a []byte, b string) bool {
	return len(a) == sha256.Size && subtle.ConstantTimeCompare(a, digestString(b)) == 1
}

var _ OIDCProvider = (*oidcProvider)(nil)
