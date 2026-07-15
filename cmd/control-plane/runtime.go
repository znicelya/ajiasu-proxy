package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dnomd343/ajiasu-proxy/internal/audit"
	"github.com/dnomd343/ajiasu-proxy/internal/identity"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/config"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/database"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/httpserver"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/keyring"
	"github.com/dnomd343/ajiasu-proxy/internal/tenancy"
)

const supportedSchemaVersion int64 = 7

var errSchemaIncompatible = errors.New("schema version is incompatible")

type switchingHandler struct{ current atomic.Value }

func (s *switchingHandler) Store(handler http.Handler) {
	if handler == nil {
		panic("cannot install a nil HTTP handler")
	}
	s.current.Store(handler)
}

func (s *switchingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	handler, _ := s.current.Load().(http.Handler)
	if handler == nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	handler.ServeHTTP(w, r)
}

type applicationRuntime struct {
	mu      sync.Mutex
	cfg     config.Config
	logger  *slog.Logger
	install func(http.Handler)
	build   applicationBuilder
	pools   *database.Pools
}

type applicationBuilder func(config.Config, *slog.Logger, *database.Pools, httpserver.Readiness) (http.Handler, error)

func newApplicationRuntime(cfg config.Config, logger *slog.Logger, install func(http.Handler)) *applicationRuntime {
	return &applicationRuntime{cfg: cfg, logger: logger, install: install, build: buildApplicationHandler}
}

func (r *applicationRuntime) Check(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pools != nil {
		return checkRuntimeDatabase(ctx, r.pools)
	}
	pools, err := database.OpenPools(ctx, r.cfg.Database)
	if err != nil {
		return fmt.Errorf("open database pools: %w", err)
	}
	if err := checkRuntimeDatabase(ctx, pools); err != nil {
		pools.Close()
		return err
	}
	handler, err := r.build(r.cfg, r.logger, pools, r)
	if err != nil {
		pools.Close()
		return err
	}
	r.pools = pools
	r.install(handler)
	r.logger.Info("control_plane_dependencies_ready", slog.Int64("schema_version", supportedSchemaVersion))
	return nil
}

func (r *applicationRuntime) Warm(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		checkCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := r.Check(checkCtx)
		cancel()
		if err == nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *applicationRuntime) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pools != nil {
		r.pools.Close()
		r.pools = nil
	}
}

func checkRuntimeDatabase(ctx context.Context, pools *database.Pools) error {
	if pools == nil || pools.Platform == nil || pools.Tenant == nil {
		return errSchemaIncompatible
	}
	if err := pools.Platform.Ping(ctx); err != nil {
		return fmt.Errorf("ping platform database: %w", err)
	}
	if err := pools.Tenant.Ping(ctx); err != nil {
		return fmt.Errorf("ping tenant database: %w", err)
	}
	var version int64
	if err := pools.Platform.QueryRow(ctx, `
SELECT COALESCE(max(version_id) FILTER (WHERE is_applied), 0)
FROM public.goose_db_version
`).Scan(&version); err != nil {
		return fmt.Errorf("inspect schema version: %w", errSchemaIncompatible)
	}
	if version != supportedSchemaVersion {
		return fmt.Errorf("%w: supported=%d current=%d", errSchemaIncompatible, supportedSchemaVersion, version)
	}
	return nil
}

func buildApplicationHandler(cfg config.Config, logger *slog.Logger, pools *database.Pools, readiness httpserver.Readiness) (http.Handler, error) {
	auditService := audit.NewService()
	key, err := readSecretFile(cfg.KeyringFile, 32, 32)
	if err != nil {
		return nil, fmt.Errorf("load keyring: %w", err)
	}
	ring, err := keyring.NewAESGCM(key)
	clear(key)
	if err != nil {
		return nil, err
	}
	clientSecretBytes, err := readSecretFile(cfg.OIDC.ClientSecretFile, 1, 64*1024)
	if err != nil {
		return nil, fmt.Errorf("load OIDC client secret: %w", err)
	}
	clientSecret := string(bytes.TrimSpace(clientSecretBytes))
	clear(clientSecretBytes)
	provider, err := identity.NewOIDCProvider(identity.OIDCConfig{
		Issuer: cfg.OIDC.Issuer, ClientID: cfg.OIDC.ClientID, ClientSecret: clientSecret, RedirectURL: cfg.OIDC.RedirectURL,
	})
	if err != nil {
		return nil, err
	}
	cookie := identity.SessionCookieConfig{Name: cfg.Session.CookieName, Path: "/api/v1", Secure: cfg.Session.CookieSecure, Development: cfg.Environment == config.EnvironmentDevelopment}
	sessions, err := identity.NewSessionService(pools, auditService, cookie)
	if err != nil {
		return nil, err
	}
	if err := sessions.ConfigureTimeouts(cfg.Session.IdleTimeout, cfg.Session.AbsoluteTimeout); err != nil {
		return nil, err
	}
	oidcService, err := identity.NewOIDCService(pools, ring, provider, auditService, cookie)
	if err != nil {
		return nil, err
	}
	if err := oidcService.ConfigureSessionTimeouts(cfg.Session.IdleTimeout, cfg.Session.AbsoluteTimeout); err != nil {
		return nil, err
	}
	localService, err := identity.NewLocalService(pools, ring, auditService, cfg.LocalAuth.Enabled, cfg.LocalAuth.AllowedCIDRs)
	if err != nil {
		return nil, err
	}
	serviceIdentities, err := identity.NewServiceIdentityService(pools, auditService)
	if err != nil {
		return nil, err
	}
	idempotency, err := httpserver.NewIdempotencyStore(pools, ring)
	if err != nil {
		return nil, err
	}
	identityHTTP, err := identity.NewHTTPHandler(identity.HTTPOptions{
		Sessions: sessions, OIDC: oidcService, Local: localService, Services: serviceIdentities,
		Idempotency: idempotency, SessionCookie: cfg.Session.CookieName, TrustedOrigins: trustedOrigins(cfg.OIDC.RedirectURL),
	})
	if err != nil {
		return nil, err
	}
	tenancyHTTP, err := tenancy.NewHTTPHandler(tenancy.NewService(pools, auditService), idempotency)
	if err != nil {
		return nil, err
	}
	auditReader, err := audit.NewReader(pools)
	if err != nil {
		return nil, err
	}
	auditHTTP, err := audit.NewHTTPHandler(auditReader)
	if err != nil {
		return nil, err
	}
	return httpserver.NewRouter(httpserver.Dependencies{
		Logger: logger, Readiness: readiness,
		Modules: []httpserver.ModuleRoutes{identityHTTP, tenancyHTTP, auditHTTP}, Authenticate: identityHTTP.Authenticate,
	}), nil
}

func readSecretFile(path string, minimum, maximum int) ([]byte, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(content) < minimum || len(content) > maximum {
		clear(content)
		return nil, errors.New("secret file length is invalid")
	}
	return content, nil
}

func trustedOrigins(redirectURL string) []string {
	parsed, err := url.Parse(redirectURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil
	}
	return []string{strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host)}
}
