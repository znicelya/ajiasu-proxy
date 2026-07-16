#!/bin/sh
set -eu

normal_password=$(cat /run/secrets/ajiasu/database-normal-password)
platform_password=$(cat /run/secrets/ajiasu/database-platform-password)

psql --set=ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
  --set=normal_password="$normal_password" --set=platform_password="$platform_password" <<'SQL'
CREATE ROLE ajiasu_normal LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD :'normal_password';
CREATE ROLE ajiasu_control LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD :'platform_password';
SQL
