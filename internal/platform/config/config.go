package config

import (
	"fmt"
	"log/slog"
	"net/netip"
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
	Database    Database
	OIDC        OIDC
	Session     Session
	KeyringFile string
	LocalAuth   LocalAuth
}

type HTTP struct {
	Bind              string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
}

type Database struct {
	Normal   DatabasePool
	Platform DatabasePool
}

type DatabasePool struct {
	DSN                string
	MaxOpenConnections int
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
	oidc, err := l.loadOIDC()
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

	return Config{
		Environment: environment,
		HTTP:        httpConfig,
		Database:    database,
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
		slog.Group("database",
			slog.String("normal_dsn", "[redacted]"),
			slog.Int("normal_max_open", c.Database.Normal.MaxOpenConnections),
			slog.Int("normal_max_idle", c.Database.Normal.MaxIdleConnections),
			slog.String("platform_dsn", "[redacted]"),
			slog.Int("platform_max_open", c.Database.Platform.MaxOpenConnections),
			slog.Int("platform_max_idle", c.Database.Platform.MaxIdleConnections),
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
	maxIdle, err := l.nonnegativeInt(prefix + "_MAX_IDLE")
	if err != nil {
		return DatabasePool{}, err
	}
	if maxIdle > maxOpen {
		return DatabasePool{}, fieldError(prefix+"_MAX_IDLE", "must not exceed max open connections")
	}
	return DatabasePool{DSN: dsn, MaxOpenConnections: maxOpen, MaxIdleConnections: maxIdle}, nil
}

func (l loader) loadOIDC() (OIDC, error) {
	issuer, err := l.required("AJIASU_OIDC_ISSUER")
	if err != nil {
		return OIDC{}, err
	}
	clientID, err := l.required("AJIASU_OIDC_CLIENT_ID")
	if err != nil {
		return OIDC{}, err
	}
	clientSecretFile, err := l.required("AJIASU_OIDC_CLIENT_SECRET_FILE")
	if err != nil {
		return OIDC{}, err
	}
	if err := validateRegularFile(clientSecretFile); err != nil {
		return OIDC{}, fieldError("AJIASU_OIDC_CLIENT_SECRET_FILE", err.Error())
	}
	redirectURL, err := l.required("AJIASU_OIDC_REDIRECT_URL")
	if err != nil {
		return OIDC{}, err
	}
	return OIDC{Issuer: issuer, ClientID: clientID, ClientSecretFile: clientSecretFile, RedirectURL: redirectURL}, nil
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

func (l loader) nonnegativeInt(name string) (int, error) {
	value, err := l.integer(name)
	if err != nil {
		return 0, err
	}
	if value < 0 {
		return 0, fieldError(name, "must not be negative")
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

func validateRegularFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("must identify an accessible regular file")
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("must identify a regular file")
	}
	return nil
}

func validateKeyringFile(path string) error {
	if err := validateRegularFile(path); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("must identify an accessible regular file")
	}
	if info.Size() != 32 {
		return fmt.Errorf("must be exactly 32 bytes")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("must not be accessible by group or other users")
	}
	return nil
}

func fieldError(name, reason string) error {
	return fmt.Errorf("configuration field %s %s", name, reason)
}
