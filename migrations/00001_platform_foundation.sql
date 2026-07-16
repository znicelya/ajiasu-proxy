-- +goose Up
-- Single-host restore needs group roles before pg_restore can apply object
-- ownership and ACLs. Fresh and external installs create missing groups here.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ajiasu_app') THEN
        CREATE ROLE ajiasu_app NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ajiasu_platform') THEN
        CREATE ROLE ajiasu_platform NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ajiasu_normal') THEN
        GRANT ajiasu_app TO ajiasu_normal;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ajiasu_control') THEN
        GRANT ajiasu_platform TO ajiasu_control;
    END IF;
END
$$;
-- +goose StatementEnd

CREATE SCHEMA platform;
CREATE SCHEMA identity;
CREATE SCHEMA tenancy;
CREATE SCHEMA audit;

REVOKE ALL ON SCHEMA platform, identity, tenancy, audit FROM PUBLIC;
GRANT USAGE ON SCHEMA platform, identity, tenancy, audit TO ajiasu_app, ajiasu_platform;

-- +goose StatementBegin
CREATE FUNCTION platform.current_tenant_id() RETURNS uuid
LANGUAGE sql STABLE AS $$
  SELECT NULLIF(current_setting('app.tenant_id', true), '')::uuid
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION platform.current_actor_id() RETURNS uuid
LANGUAGE sql STABLE AS $$
  SELECT NULLIF(current_setting('app.actor_id', true), '')::uuid
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION platform.current_tenant_id(), platform.current_actor_id() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION platform.current_tenant_id(), platform.current_actor_id() TO ajiasu_app, ajiasu_platform;
GRANT SELECT ON public.goose_db_version TO ajiasu_app, ajiasu_platform;

-- +goose Down
REVOKE ALL ON public.goose_db_version FROM ajiasu_app, ajiasu_platform;
DROP FUNCTION platform.current_actor_id();
DROP FUNCTION platform.current_tenant_id();

DROP SCHEMA audit;
DROP SCHEMA tenancy;
DROP SCHEMA identity;
DROP SCHEMA platform;

DROP ROLE ajiasu_platform;
DROP ROLE ajiasu_app;
