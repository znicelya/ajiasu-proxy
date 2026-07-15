package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/dnomd343/ajiasu-proxy/internal/platform/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Pools struct {
	Tenant   *pgxpool.Pool
	Platform *pgxpool.Pool
}

func OpenPools(ctx context.Context, cfg config.Database) (*Pools, error) {
	tenant, err := openPool(ctx, "tenant", "ajiasu_app", "ajiasu_platform", cfg.Normal)
	if err != nil {
		return nil, err
	}
	platform, err := openPool(ctx, "platform", "ajiasu_platform", "ajiasu_app", cfg.Platform)
	if err != nil {
		tenant.Close()
		return nil, err
	}
	return &Pools{Tenant: tenant, Platform: platform}, nil
}

func (p *Pools) Close() {
	if p == nil {
		return
	}
	if p.Platform != nil {
		p.Platform.Close()
	}
	if p.Tenant != nil {
		p.Tenant.Close()
	}
}

func openPool(ctx context.Context, name, requiredGroup, forbiddenGroup string, cfg config.DatabasePool) (*pgxpool.Pool, error) {
	if cfg.DSN == "" {
		return nil, fmt.Errorf("%s database DSN is required", name)
	}
	if cfg.MaxOpenConnections <= 0 {
		return nil, fmt.Errorf("%s database maximum open connections must be positive", name)
	}
	minIdleConnections, err := configuredMinIdleConnections(cfg)
	if err != nil || minIdleConnections > cfg.MaxOpenConnections {
		return nil, fmt.Errorf("%s database idle connection target is invalid", name)
	}
	poolConfig, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("parse %s database configuration", name)
	}
	poolConfig.MaxConns = int32(cfg.MaxOpenConnections)
	poolConfig.MinIdleConns = int32(minIdleConnections)

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("open %s database pool: %w", name, err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping %s database pool: %w", name, err)
	}
	if err := validatePoolRole(ctx, pool, requiredGroup, forbiddenGroup); err != nil {
		pool.Close()
		return nil, fmt.Errorf("validate %s database role: %w", name, err)
	}
	return pool, nil
}

func configuredMinIdleConnections(cfg config.DatabasePool) (int, error) {
	if cfg.MinIdleConnections < 0 || cfg.MaxIdleConnections < 0 {
		return 0, errors.New("idle connection target must not be negative")
	}
	if cfg.MinIdleConnections != 0 && cfg.MaxIdleConnections != 0 && cfg.MinIdleConnections != cfg.MaxIdleConnections {
		return 0, errors.New("idle connection aliases conflict")
	}
	if cfg.MinIdleConnections != 0 {
		return cfg.MinIdleConnections, nil
	}
	return cfg.MaxIdleConnections, nil
}

func validatePoolRole(ctx context.Context, pool *pgxpool.Pool, requiredGroup, forbiddenGroup string) error {
	var inherits, superuser, bypassRLS, member, forbiddenMember bool
	err := pool.QueryRow(ctx, `
SELECT role.rolinherit,
       role.rolsuper,
       role.rolbypassrls,
       pg_has_role(current_user, $1, 'MEMBER'),
       pg_has_role(current_user, $2, 'MEMBER')
FROM pg_roles AS role
WHERE role.rolname = current_user
`, requiredGroup, forbiddenGroup).Scan(&inherits, &superuser, &bypassRLS, &member, &forbiddenMember)
	if err != nil {
		return fmt.Errorf("inspect database role: %w", err)
	}
	if !member {
		return fmt.Errorf("database role is not a member of %s", requiredGroup)
	}
	if !inherits {
		return errors.New("database role must inherit group privileges")
	}
	if superuser || bypassRLS {
		return errors.New("database role has unsafe row-security bypass privileges")
	}
	if forbiddenMember {
		return fmt.Errorf("database role must not be a member of %s", forbiddenGroup)
	}
	return nil
}
