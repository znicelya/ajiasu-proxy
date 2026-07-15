package config

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type Environment string

const (
	EnvironmentDevelopment Environment = "development"
	EnvironmentProduction  Environment = "production"
)

type Config struct {
	Environment Environment
	HTTP        HTTP
	AgentGRPC   AgentGRPC
	Database    Database
	Redis       Redis
	OIDC        OIDC
	Session     Session
	KeyringFile string
	LocalAuth   LocalAuth
}

type Redis struct {
	Address            string
	Username           string
	PasswordFile       string
	Database           int
	TLS                bool
	LeaseNamespace     string
	LeaseTTL           time.Duration
	LeaseRenewInterval time.Duration
	OperationTimeout   time.Duration
}

type HTTP struct {
	Bind              string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
}

type AgentGRPC struct {
	Bind         string
	Insecure     bool
	CertFile     string
	KeyFile      string
	MaxRecvBytes int
	MaxSendBytes int
}

type Database struct {
	Normal   DatabasePool
	Platform DatabasePool
}

type DatabasePool struct {
	DSN                string
	MaxOpenConnections int
	MinIdleConnections int
	// MaxIdleConnections is the Task 1 compatibility alias for MinIdleConnections.
	// New callers should use MinIdleConnections because pgx configures a minimum
	// warm-idle target rather than a maximum idle connection count.
	MaxIdleConnections int
}

type OIDC struct {
	Issuer           string
	ClientID         string
	ClientSecretFile string
	RedirectURL      string
}

type Session struct {
	CookieName      string
	CookieSecure    bool
	IdleTimeout     time.Duration
	AbsoluteTimeout time.Duration
}

type LocalAuth struct {
	Enabled      bool
	AllowedCIDRs []netip.Prefix
}

type loader struct {
	lookup func(string) (string, bool)
}

func Load(lookup func(string) (string, bool)) (Config, error) {
	if lookup == nil {
		return Config{}, fmt.Errorf("configuration lookup is required")
	}
	l := loader{lookup: lookup}

	environmentValue, err := l.required("AJIASU_ENVIRONMENT")
	if err != nil {
		return Config{}, err
	}
	environment := Environment(environmentValue)
	if environment != EnvironmentDevelopment && environment != EnvironmentProduction {
		return Config{}, fieldError("AJIASU_ENVIRONMENT", "must be development or production")
	}

	httpConfig, err := l.loadHTTP()
	if err != nil {
		return Config{}, err
	}
	database, err := l.loadDatabase()
	if err != nil {
		return Config{}, err
	}
	redisConfig, err := l.loadRedis()
	if err != nil {
		return Config{}, err
	}
	oidc, err := l.loadOIDC(environment)
	if err != nil {
		return Config{}, err
	}
	session, err := l.loadSession()
	if err != nil {
		return Config{}, err
	}
	if environment == EnvironmentProduction && !session.CookieSecure {
		return Config{}, fieldError("AJIASU_SESSION_COOKIE_SECURE", "must be true in production")
	}
	keyringFile, err := l.required("AJIASU_KEYRING_FILE")
	if err != nil {
		return Config{}, err
	}
	if err := validateKeyringFile(keyringFile); err != nil {
		return Config{}, fieldError("AJIASU_KEYRING_FILE", err.Error())
	}
	localAuth, err := l.loadLocalAuth()
	if err != nil {
		return Config{}, err
	}
	agentGRPC, err := l.loadAgentGRPC(environment)
	if err != nil {
		return Config{}, err
	}

	return Config{
		Environment: environment,
		HTTP:        httpConfig,
		AgentGRPC:   agentGRPC,
		Database:    database,
		Redis:       redisConfig,
		OIDC:        oidc,
		Session:     session,
		KeyringFile: keyringFile,
		LocalAuth:   localAuth,
	}, nil
}

func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("environment", string(c.Environment)),
		slog.Group("http",
			slog.String("bind", c.HTTP.Bind),
			slog.Duration("read_header_timeout", c.HTTP.ReadHeaderTimeout),
			slog.Duration("read_timeout", c.HTTP.ReadTimeout),
			slog.Duration("write_timeout", c.HTTP.WriteTimeout),
			slog.Duration("idle_timeout", c.HTTP.IdleTimeout),
			slog.Duration("shutdown_timeout", c.HTTP.ShutdownTimeout),
		),
		slog.Group("agent_grpc", slog.String("bind", c.AgentGRPC.Bind), slog.Bool("insecure", c.AgentGRPC.Insecure), slog.Int("max_recv_bytes", c.AgentGRPC.MaxRecvBytes), slog.Int("max_send_bytes", c.AgentGRPC.MaxSendBytes)),
		slog.Group("database",
			slog.String("normal_dsn", "[redacted]"),
			slog.Int("normal_max_open", c.Database.Normal.MaxOpenConnections),
			slog.Int("normal_min_idle", c.Database.Normal.MinIdleConnections),
			slog.String("platform_dsn", "[redacted]"),
			slog.Int("platform_max_open", c.Database.Platform.MaxOpenConnections),
			slog.Int("platform_min_idle", c.Database.Platform.MinIdleConnections),
		),
		slog.Group("redis",
			slog.String("address", c.Redis.Address),
			slog.Int("database", c.Redis.Database),
			slog.Bool("tls", c.Redis.TLS),
			slog.Duration("lease_ttl", c.Redis.LeaseTTL),
			slog.Duration("lease_renew_interval", c.Redis.LeaseRenewInterval),
			slog.Duration("operation_timeout", c.Redis.OperationTimeout),
		),
		slog.Group("oidc",
			slog.String("issuer", c.OIDC.Issuer),
			slog.String("client_id", c.OIDC.ClientID),
			slog.String("AJIASU_OIDC_CLIENT_SECRET_FILE", "[redacted]"),
			slog.String("redirect_url", c.OIDC.RedirectURL),
		),
		slog.String("session", "[redacted]"),
		slog.String("keyring_file", "[redacted]"),
		slog.Group("local_auth",
			slog.Bool("enabled", c.LocalAuth.Enabled),
			slog.Int("allowed_cidr_count", len(c.LocalAuth.AllowedCIDRs)),
		),
	)
}

func (l loader) loadRedis() (Redis, error) {
	address, err := l.required("AJIASU_REDIS_ADDRESS")
	if err != nil {
		return Redis{}, err
	}
	if _, _, err := net.SplitHostPort(address); err != nil {
		return Redis{}, fieldError("AJIASU_REDIS_ADDRESS", "must be host:port")
	}
	username, _ := l.lookup("AJIASU_REDIS_USERNAME")
	passwordFile, err := l.required("AJIASU_REDIS_PASSWORD_FILE")
	if err != nil {
		return Redis{}, err
	}
	password, _, err := readRegularFile(passwordFile, 64*1024+1)
	if err != nil || len(bytes.TrimSpace(password)) == 0 || len(password) > 64*1024 {
		clear(password)
		return Redis{}, fieldError("AJIASU_REDIS_PASSWORD_FILE", "must identify a nonempty accessible regular file")
	}
	clear(password)
	database, err := l.integer("AJIASU_REDIS_DATABASE")
	if err != nil || database < 0 {
		return Redis{}, fieldError("AJIASU_REDIS_DATABASE", "must be a nonnegative integer")
	}
	tlsEnabled, err := l.boolean("AJIASU_REDIS_TLS")
	if err != nil {
		return Redis{}, err
	}
	namespace, err := l.required("AJIASU_SCHEDULER_LEASE_NAMESPACE")
	if err != nil {
		return Redis{}, err
	}
	ttl, err := l.duration("AJIASU_SCHEDULER_LEASE_TTL")
	if err != nil {
		return Redis{}, err
	}
	renew, err := l.duration("AJIASU_SCHEDULER_LEASE_RENEW_INTERVAL")
	if err != nil {
		return Redis{}, err
	}
	timeout, err := l.duration("AJIASU_REDIS_OPERATION_TIMEOUT")
	if err != nil {
		return Redis{}, err
	}
	if ttl < 3*time.Second || ttl > 5*time.Minute || renew >= ttl/2 || timeout > renew {
		return Redis{}, fieldError("AJIASU_SCHEDULER_LEASE_TTL", "is incompatible with renewal and timeout settings")
	}
	return Redis{Address: address, Username: strings.TrimSpace(username), PasswordFile: passwordFile, Database: database, TLS: tlsEnabled, LeaseNamespace: namespace, LeaseTTL: ttl, LeaseRenewInterval: renew, OperationTimeout: timeout}, nil
}

func (c Config) String() string {
	return fmt.Sprintf(
		"Config{environment=%q,http_bind=%q,agent_grpc_bind=%q,database=[redacted],oidc=[redacted],session=[redacted],keyring_file=[redacted],local_auth_enabled=%t}",
		c.Environment,
		c.HTTP.Bind,
		c.AgentGRPC.Bind,
		c.LocalAuth.Enabled,
	)
}

func (l loader) loadAgentGRPC(environment Environment) (AgentGRPC, error) {
	bind, err := l.required("AJIASU_AGENT_GRPC_BIND")
	if err != nil {
		return AgentGRPC{}, err
	}
	if _, _, err := net.SplitHostPort(bind); err != nil {
		return AgentGRPC{}, fieldError("AJIASU_AGENT_GRPC_BIND", "must be host:port")
	}
	insecure, err := l.boolean("AJIASU_AGENT_GRPC_INSECURE")
	if err != nil {
		return AgentGRPC{}, err
	}
	if insecure {
		if environment != EnvironmentDevelopment {
			return AgentGRPC{}, fieldError("AJIASU_AGENT_GRPC_INSECURE", "is allowed only in development")
		}
		host, _, _ := net.SplitHostPort(bind)
		if host != "localhost" {
			address, parseErr := netip.ParseAddr(host)
			if parseErr != nil || !address.IsLoopback() {
				return AgentGRPC{}, fieldError("AJIASU_AGENT_GRPC_BIND", "must be loopback when insecure")
			}
		}
		return AgentGRPC{Bind: bind, Insecure: true, MaxRecvBytes: 4 << 20, MaxSendBytes: 4 << 20}, nil
	}
	certFile, err := l.required("AJIASU_AGENT_GRPC_CERT_FILE")
	if err != nil {
		return AgentGRPC{}, err
	}
	keyFile, err := l.required("AJIASU_AGENT_GRPC_KEY_FILE")
	if err != nil {
		return AgentGRPC{}, err
	}
	if _, _, err := readRegularFile(certFile, 4<<20); err != nil {
		return AgentGRPC{}, fieldError("AJIASU_AGENT_GRPC_CERT_FILE", "must identify an accessible regular file")
	}
	if _, info, err := readRegularFile(keyFile, 4<<20); err != nil {
		return AgentGRPC{}, fieldError("AJIASU_AGENT_GRPC_KEY_FILE", "must identify an accessible regular file")
	} else if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return AgentGRPC{}, fieldError("AJIASU_AGENT_GRPC_KEY_FILE", "must not be accessible by group or other users")
	}
	return AgentGRPC{Bind: bind, CertFile: certFile, KeyFile: keyFile, MaxRecvBytes: 4 << 20, MaxSendBytes: 4 << 20}, nil
}

func (l loader) loadHTTP() (HTTP, error) {
	bind, err := l.required("AJIASU_HTTP_BIND")
	if err != nil {
		return HTTP{}, err
	}
	readHeaderTimeout, err := l.duration("AJIASU_HTTP_READ_HEADER_TIMEOUT")
	if err != nil {
		return HTTP{}, err
	}
	readTimeout, err := l.duration("AJIASU_HTTP_READ_TIMEOUT")
	if err != nil {
		return HTTP{}, err
	}
	writeTimeout, err := l.duration("AJIASU_HTTP_WRITE_TIMEOUT")
	if err != nil {
		return HTTP{}, err
	}
	idleTimeout, err := l.duration("AJIASU_HTTP_IDLE_TIMEOUT")
	if err != nil {
		return HTTP{}, err
	}
	shutdownTimeout, err := l.duration("AJIASU_HTTP_SHUTDOWN_TIMEOUT")
	if err != nil {
		return HTTP{}, err
	}
	return HTTP{Bind: bind, ReadHeaderTimeout: readHeaderTimeout, ReadTimeout: readTimeout, WriteTimeout: writeTimeout, IdleTimeout: idleTimeout, ShutdownTimeout: shutdownTimeout}, nil
}

func (l loader) loadDatabase() (Database, error) {
	normal, err := l.databasePool("NORMAL")
	if err != nil {
		return Database{}, err
	}
	platform, err := l.databasePool("PLATFORM")
	if err != nil {
		return Database{}, err
	}
	return Database{Normal: normal, Platform: platform}, nil
}

func (l loader) databasePool(name string) (DatabasePool, error) {
	prefix := "AJIASU_DATABASE_" + name
	dsn, err := l.required(prefix + "_DSN")
	if err != nil {
		return DatabasePool{}, err
	}
	maxOpen, err := l.positiveInt(prefix + "_MAX_OPEN")
	if err != nil {
		return DatabasePool{}, err
	}
	minIdle, err := l.idleConnections(prefix)
	if err != nil {
		return DatabasePool{}, err
	}
	if minIdle > maxOpen {
		return DatabasePool{}, fieldError(prefix+"_MIN_IDLE", "must not exceed max open connections")
	}
	return DatabasePool{
		DSN:                dsn,
		MaxOpenConnections: maxOpen,
		MinIdleConnections: minIdle,
		MaxIdleConnections: minIdle,
	}, nil
}

func (l loader) idleConnections(prefix string) (int, error) {
	minName := prefix + "_MIN_IDLE"
	legacyName := prefix + "_MAX_IDLE"
	minValue, minSet := l.lookup(minName)
	legacyValue, legacySet := l.lookup(legacyName)
	minSet = minSet && strings.TrimSpace(minValue) != ""
	legacySet = legacySet && strings.TrimSpace(legacyValue) != ""
	if !minSet && !legacySet {
		return 0, fieldError(minName, "is required")
	}

	parse := func(name, value string) (int, error) {
		result, err := strconv.Atoi(value)
		if err != nil {
			return 0, fieldError(name, "must be an integer")
		}
		if result < 0 {
			return 0, fieldError(name, "must not be negative")
		}
		return result, nil
	}

	if minSet {
		minIdle, err := parse(minName, minValue)
		if err != nil {
			return 0, err
		}
		if legacySet {
			legacyIdle, err := parse(legacyName, legacyValue)
			if err != nil {
				return 0, err
			}
			if minIdle != legacyIdle {
				return 0, fieldError(minName, fmt.Sprintf("must match %s when both aliases are set", legacyName))
			}
		}
		return minIdle, nil
	}
	return parse(legacyName, legacyValue)
}

func (l loader) loadOIDC(environment Environment) (OIDC, error) {
	issuer, err := l.required("AJIASU_OIDC_ISSUER")
	if err != nil {
		return OIDC{}, err
	}
	if err := validateHTTPURL(issuer, environment); err != nil {
		return OIDC{}, fieldError("AJIASU_OIDC_ISSUER", err.Error())
	}
	clientID, err := l.required("AJIASU_OIDC_CLIENT_ID")
	if err != nil {
		return OIDC{}, err
	}
	clientSecretFile, err := l.required("AJIASU_OIDC_CLIENT_SECRET_FILE")
	if err != nil {
		return OIDC{}, err
	}
	if err := validateClientSecretFile(clientSecretFile); err != nil {
		return OIDC{}, fieldError("AJIASU_OIDC_CLIENT_SECRET_FILE", err.Error())
	}
	redirectURL, err := l.required("AJIASU_OIDC_REDIRECT_URL")
	if err != nil {
		return OIDC{}, err
	}
	if err := validateHTTPURL(redirectURL, environment); err != nil {
		return OIDC{}, fieldError("AJIASU_OIDC_REDIRECT_URL", err.Error())
	}
	return OIDC{Issuer: issuer, ClientID: clientID, ClientSecretFile: clientSecretFile, RedirectURL: redirectURL}, nil
}

func validateHTTPURL(value string, environment Environment) error {
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return fmt.Errorf("must be an absolute HTTP or HTTPS URL with a host")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("must be an absolute HTTP or HTTPS URL with a host")
	}
	if environment == EnvironmentProduction && scheme != "https" {
		return fmt.Errorf("must use HTTPS in production")
	}
	return nil
}

func (l loader) loadSession() (Session, error) {
	cookieName, err := l.required("AJIASU_SESSION_COOKIE_NAME")
	if err != nil {
		return Session{}, err
	}
	cookieSecure, err := l.boolean("AJIASU_SESSION_COOKIE_SECURE")
	if err != nil {
		return Session{}, err
	}
	idleTimeout, err := l.duration("AJIASU_SESSION_IDLE_TIMEOUT")
	if err != nil {
		return Session{}, err
	}
	absoluteTimeout, err := l.duration("AJIASU_SESSION_ABSOLUTE_TIMEOUT")
	if err != nil {
		return Session{}, err
	}
	if idleTimeout > absoluteTimeout {
		return Session{}, fieldError("AJIASU_SESSION_IDLE_TIMEOUT", "must not exceed absolute timeout")
	}
	return Session{CookieName: cookieName, CookieSecure: cookieSecure, IdleTimeout: idleTimeout, AbsoluteTimeout: absoluteTimeout}, nil
}

func (l loader) loadLocalAuth() (LocalAuth, error) {
	enabled, err := l.boolean("AJIASU_LOCAL_AUTH_ENABLED")
	if err != nil {
		return LocalAuth{}, err
	}
	value, err := l.required("AJIASU_LOCAL_AUTH_ALLOWED_CIDRS")
	if err != nil {
		return LocalAuth{}, err
	}
	parts := strings.Split(value, ",")
	prefixes := make([]netip.Prefix, 0, len(parts))
	for _, part := range parts {
		prefix, parseErr := netip.ParsePrefix(strings.TrimSpace(part))
		if parseErr != nil {
			return LocalAuth{}, fieldError("AJIASU_LOCAL_AUTH_ALLOWED_CIDRS", "contains an invalid CIDR")
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return LocalAuth{Enabled: enabled, AllowedCIDRs: prefixes}, nil
}

func (l loader) required(name string) (string, error) {
	value, ok := l.lookup(name)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fieldError(name, "is required")
	}
	return value, nil
}

func (l loader) duration(name string) (time.Duration, error) {
	value, err := l.required(name)
	if err != nil {
		return 0, err
	}
	duration, parseErr := time.ParseDuration(value)
	if parseErr != nil || duration <= 0 {
		return 0, fieldError(name, "must be a positive duration")
	}
	return duration, nil
}

func (l loader) boolean(name string) (bool, error) {
	value, err := l.required(name)
	if err != nil {
		return false, err
	}
	result, parseErr := strconv.ParseBool(value)
	if parseErr != nil {
		return false, fieldError(name, "must be true or false")
	}
	return result, nil
}

func (l loader) positiveInt(name string) (int, error) {
	value, err := l.integer(name)
	if err != nil {
		return 0, err
	}
	if value <= 0 {
		return 0, fieldError(name, "must be positive")
	}
	return value, nil
}

func (l loader) integer(name string) (int, error) {
	value, err := l.required(name)
	if err != nil {
		return 0, err
	}
	result, parseErr := strconv.Atoi(value)
	if parseErr != nil {
		return 0, fieldError(name, "must be an integer")
	}
	return result, nil
}

func validateClientSecretFile(path string) error {
	content, _, err := readRegularFile(path, 64*1024+1)
	if err != nil {
		return fmt.Errorf("must identify an accessible regular file")
	}
	defer clear(content)
	if len(content) > 64*1024 || len(bytes.TrimSpace(content)) == 0 {
		return fmt.Errorf("must contain a nonempty client secret")
	}
	return nil
}

func validateKeyringFile(path string) error {
	content, info, err := readRegularFile(path, 33)
	if err != nil {
		return fmt.Errorf("must identify an accessible regular file")
	}
	defer clear(content)
	if len(content) != 32 {
		return fmt.Errorf("must be exactly 32 bytes")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("must not be accessible by group or other users")
	}
	return nil
}

func readRegularFile(path string, limit int64) ([]byte, os.FileInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("not a readable regular file")
	}
	content, err := io.ReadAll(io.LimitReader(file, limit))
	if err != nil {
		return nil, nil, err
	}
	return content, info, nil
}

func fieldError(name, reason string) error {
	return fmt.Errorf("configuration field %s %s", name, reason)
}
