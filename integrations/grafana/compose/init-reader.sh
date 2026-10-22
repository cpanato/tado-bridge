#!/usr/bin/env bash
# Dedicated local history database only. Runs on first init; safe to rerun when
# enabling Grafana on an existing volume or deliberately rotating its password.
set -eu

: "${GRAFANA_DB_PASSWORD:?Set GRAFANA_DB_PASSWORD}"
: "${POSTGRES_USER:?Set POSTGRES_USER}"
: "${POSTGRES_DB:?Set POSTGRES_DB}"

psql --no-psqlrc --set=ON_ERROR_STOP=1 \
  --username "${POSTGRES_USER}" --dbname "${POSTGRES_DB}" \
  --set=reader_password="${GRAFANA_DB_PASSWORD}" \
  --set=bridge_user="${POSTGRES_USER}" --set=bridge_db="${POSTGRES_DB}" <<'SQL'
BEGIN;
SELECT 'CREATE ROLE grafana_reader LOGIN'
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'grafana_reader') \gexec
ALTER ROLE grafana_reader NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD :'reader_password';
GRANT CONNECT ON DATABASE :"bridge_db" TO grafana_reader;
GRANT USAGE ON SCHEMA public TO grafana_reader;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO grafana_reader;
ALTER DEFAULT PRIVILEGES FOR ROLE :"bridge_user" IN SCHEMA public GRANT SELECT ON TABLES TO grafana_reader;
COMMIT;
SQL
