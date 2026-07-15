package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dnomd343/ajiasu-proxy/internal/accounts"
	"github.com/dnomd343/ajiasu-proxy/internal/audit"
	"github.com/dnomd343/ajiasu-proxy/internal/endpoints"
	"github.com/dnomd343/ajiasu-proxy/internal/identity"
	"github.com/dnomd343/ajiasu-proxy/internal/nodes"
	"github.com/dnomd343/ajiasu-proxy/internal/operations"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/config"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/database"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/httpserver"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/keyring"
	accountpools "github.com/dnomd343/ajiasu-proxy/internal/pools"
	"github.com/dnomd343/ajiasu-proxy/internal/proxyaccess"
	"github.com/dnomd343/ajiasu-proxy/internal/reconciler"
	"github.com/dnomd343/ajiasu-proxy/internal/scheduler"
	"github.com/dnomd343/ajiasu-proxy/internal/secrets"
	"github.com/dnomd343/ajiasu-proxy/internal/tenancy"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const supportedSchemaVersion int64 = 11

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
	mu                sync.Mutex
	cfg               config.Config
	logger            *slog.Logger
	install           func(http.Handler)
	build             applicationBuilder
	pools             *database.Pools
	handler           http.Handler
	agentGRPC         *grpc.Server
	agentListener     net.Listener
	agentWorkerCancel context.CancelFunc
}

type applicationBuilder func(config.Config, *slog.Logger, *database.Pools, httpserver.Readiness) (http.Handler, error)

func newApplicationRuntime(cfg config.Config, logger *slog.Logger, install func(http.Handler)) *applicationRuntime {
	return &applicationRuntime{cfg: cfg, logger: logger, install: install, build: buildApplicationHandler}
}

func (r *applicationRuntime) Check(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pools != nil {
		if r.agentWorkerCancel != nil {
			r.agentWorkerCancel()
			r.agentWorkerCancel = nil
		}
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
	if err := r.startAgentGRPC(pools); err != nil {
		if closer, ok := handler.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
		pools.Close()
		r.pools = nil
		return err
	}
	r.handler = handler
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
		if closer, ok := r.handler.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
		r.handler = nil
		if r.agentGRPC != nil {
			r.agentGRPC.GracefulStop()
			r.agentGRPC = nil
		}
		if r.agentListener != nil {
			_ = r.agentListener.Close()
			r.agentListener = nil
		}
		r.pools.Close()
		r.pools = nil
	}
}

type managedHandler struct {
	http.Handler
	close func() error
}

func (handler *managedHandler) Close() error {
	if handler == nil || handler.close == nil {
		return nil
	}
	return handler.close()
}

func (r *applicationRuntime) startAgentGRPC(pools *database.Pools) error {
	if r.cfg.AgentGRPC.Bind == "" {
		return nil
	}
	service, err := nodes.NewService(pools, audit.NewService())
	if err != nil {
		return err
	}
	control, err := nodes.NewGRPCServer(service)
	if err != nil {
		return err
	}
	reconcileService, err := reconciler.NewService(pools, audit.NewService())
	if err != nil {
		return err
	}
	control.SetAgentMessageHandler(reconcileService.ApplyAgentMessage)
	key, err := readSecretFile(r.cfg.KeyringFile, 32, 32)
	if err != nil {
		return fmt.Errorf("load reconciler keyring: %w", err)
	}
	ring, err := keyring.NewAESGCM(key)
	clear(key)
	if err != nil {
		return err
	}
	secretProvider, err := secrets.NewEnvelopeProvider(ring)
	if err != nil {
		return err
	}
	worker, err := reconciler.NewWorker(pools, control.Registry(), secretProvider)
	if err != nil {
		return err
	}
	options := []grpc.ServerOption{grpc.MaxRecvMsgSize(r.cfg.AgentGRPC.MaxRecvBytes), grpc.MaxSendMsgSize(r.cfg.AgentGRPC.MaxSendBytes)}
	if !r.cfg.AgentGRPC.Insecure {
		cert, err := tls.LoadX509KeyPair(r.cfg.AgentGRPC.CertFile, r.cfg.AgentGRPC.KeyFile)
		if err != nil {
			return fmt.Errorf("load agent grpc certificate: %w", err)
		}
		options = append(options, grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13})))
	}
	listener, err := net.Listen("tcp", r.cfg.AgentGRPC.Bind)
	if err != nil {
		return fmt.Errorf("listen agent grpc: %w", err)
	}
	server := grpc.NewServer(options...)
	nodes.RegisterAgentControlServer(server, control)
	r.agentListener, r.agentGRPC = listener, server
	workerContext, cancelWorker := context.WithCancel(context.Background())
	r.agentWorkerCancel = cancelWorker
	go worker.Run(workerContext)
	go func() {
		if err := server.Serve(listener); err != nil && r.agentGRPC == server {
			r.logger.Error("agent_grpc_failed", slog.String("error", err.Error()))
		}
	}()
	r.logger.Info("agent_grpc_started", slog.String("bind", r.cfg.AgentGRPC.Bind), slog.Bool("insecure", r.cfg.AgentGRPC.Insecure))
	return nil
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
	secretProvider, err := secrets.NewEnvelopeProvider(ring)
	if err != nil {
		return nil, err
	}
	accountService, err := accounts.NewService(pools, secretProvider, auditService)
	if err != nil {
		return nil, err
	}
	accountHTTP, err := accounts.NewHTTPHandler(accountService, idempotency)
	if err != nil {
		return nil, err
	}
	poolService, err := accountpools.NewService(pools, auditService)
	if err != nil {
		return nil, err
	}
	poolHTTP, err := accountpools.NewHTTPHandler(poolService, idempotency)
	if err != nil {
		return nil, err
	}
	nodeService, err := nodes.NewService(pools, auditService)
	if err != nil {
		return nil, err
	}
	nodeHTTP, err := nodes.NewHTTPHandler(nodeService, idempotency)
	if err != nil {
		return nil, err
	}
	endpointService, err := endpoints.NewService(pools, auditService)
	if err != nil {
		return nil, err
	}
	endpointHTTP, err := endpoints.NewHTTPHandler(endpointService, idempotency)
	if err != nil {
		return nil, err
	}
	operationService, err := operations.NewService(pools)
	if err != nil {
		return nil, err
	}
	operationHTTP, err := operations.NewHTTPHandler(operationService)
	if err != nil {
		return nil, err
	}
	reconcileService, err := reconciler.NewService(pools, auditService)
	if err != nil {
		return nil, err
	}
	reconcileHTTP, err := reconciler.NewHTTPHandler(reconcileService, idempotency)
	if err != nil {
		return nil, err
	}
	proxyAccessService, err := proxyaccess.NewService(pools, auditService)
	if err != nil {
		return nil, err
	}
	proxyAccessHTTP, err := proxyaccess.NewHTTPHandler(proxyAccessService, idempotency)
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
	redisPasswordBytes, err := readSecretFile(cfg.Redis.PasswordFile, 1, 64*1024)
	if err != nil {
		return nil, fmt.Errorf("load Redis password: %w", err)
	}
	redisPassword := string(bytes.TrimSpace(redisPasswordBytes))
	clear(redisPasswordBytes)
	redisClient, err := scheduler.NewRedisClient(scheduler.RedisOptions{Address: cfg.Redis.Address, Username: cfg.Redis.Username, Password: redisPassword, Database: cfg.Redis.Database, TLS: cfg.Redis.TLS})
	if err != nil {
		return nil, err
	}
	ownerID, err := uuid.NewV7()
	if err != nil {
		_ = redisClient.Close()
		return nil, err
	}
	leaseManager, err := scheduler.NewLeaseManager(redisClient, scheduler.LeaseConfig{Namespace: cfg.Redis.LeaseNamespace, TTL: cfg.Redis.LeaseTTL, RenewInterval: cfg.Redis.LeaseRenewInterval, Timeout: cfg.Redis.OperationTimeout}, ownerID)
	if err != nil {
		_ = redisClient.Close()
		return nil, err
	}
	assignmentService, err := scheduler.NewAssignmentService(pools, leaseManager)
	if err != nil {
		_ = redisClient.Close()
		return nil, err
	}
	schedulerHTTP, err := scheduler.NewHTTPHandler(assignmentService, idempotency)
	if err != nil {
		_ = redisClient.Close()
		return nil, err
	}
	router := httpserver.NewRouter(httpserver.Dependencies{
		Logger: logger, Readiness: readiness,
		Modules: []httpserver.ModuleRoutes{identityHTTP, tenancyHTTP, accountHTTP, poolHTTP, nodeHTTP, endpointHTTP, proxyAccessHTTP, operationHTTP, reconcileHTTP, schedulerHTTP, auditHTTP}, Authenticate: identityHTTP.Authenticate,
	})
	return &managedHandler{Handler: router, close: redisClient.Close}, nil
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
