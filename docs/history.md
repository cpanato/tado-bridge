# Optional PostgreSQL history

History is disabled unless `HISTORY_DATABASE_URL` is set. The normal in-memory cache, API, metrics and TRMNL do not require PostgreSQL. Only `serve` records history; `read` and `discover` remain one-shot upstream operations.

Each successful cache replacement is followed by one bounded database write. The database stores the complete normalized snapshot as JSONB in `tado_bridge_snapshots`, keyed by collection time. Nulls, zero/false readings, names, IDs and upstream sensor timestamps are preserved. Collection time is not proof of sensor freshness. No historical backfill or extra tado requests occur.

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `HISTORY_DATABASE_URL` | unset | PostgreSQL connection URL; setting it enables persistence and the history endpoint |
| `HISTORY_RETENTION` | `2160h` | 90 days; Go duration between `24h` and `87600h` |

Use a dedicated database for one collector and provide a URL such as `postgres://bridge:password@database.internal/tado_bridge?sslmode=verify-full`. URL-encode credentials and explicitly choose `sslmode`. Prefer `verify-full` with your database's trusted CA and hostname; `require` encrypts without authenticating the server, and `disable` is intended for isolated local testing. pgx supports `sslrootcert` in the URL for a mounted CA certificate. Store the actual URL in a local environment or existing Kubernetes Secret, outside Git. The service never logs the URL or raw driver errors.

At startup, the service connects and creates the initial table if absent. This is transactional and serialized using a PostgreSQL advisory lock. The database role needs `CONNECT`, `USAGE`/`CREATE` on its schema and `SELECT`/`INSERT`/`DELETE` on the table. An initialization failure stops startup before the listener or collection begins. Use a dedicated schema/database and do not share this table between unrelated bridge instances.

At runtime, writes and queries each have a five-second deadline. A write failure leaves the current cache available, logs a generic error and increments `tado_bridge_history_writes_total{result="error"}` when metrics are enabled. It does not retry tado or change polling cadence. There is no durable retry queue: failed writes leave gaps. The next successful collection attempts persistence again. Database availability does not determine `/healthz` or `/readyz`.

Retention cleanup runs in the same transaction as each write, based on the current clock. History API reads also exclude expired rows even when collection has stopped. Direct database consumers, including Grafana, can see expired rows until cleanup. Physical deletion waits for the next successful write; PostgreSQL vacuuming and backups remain database operator responsibilities. Decreasing retention permanently removes older rows on that next write. History does not restore the current cache or request budget on restart.

## Query API

`GET /api/v1/history` is registered only when history is enabled (otherwise 404). It uses the same bearer authentication as snapshots and metrics, and returns `Cache-Control: no-store`.

Required query parameters are `from` and `to`, RFC3339 timestamps. The interval is `[from, to)`, with at most 31 days per query. Optional `limit` defaults to 100 and accepts 1–100. Unknown/repeated parameters are rejected. Results are newest first and pages are bounded by count and approximately 8 MiB of stored JSON.

```sh
curl --fail --get http://127.0.0.1:8080/api/v1/history \
  --header "Authorization: Bearer $API_TOKEN" \
  --data-urlencode 'from=2026-10-20T00:00:00Z' \
  --data-urlencode 'to=2026-10-21T00:00:00Z' \
  --data-urlencode 'limit=24'
```

The response is `{"snapshots":[...],"next_to":"..."}`. Each entry follows the existing snapshot schema. When `next_to` is present, use that value as the next request's `to`, keeping `from` unchanged. PostgreSQL cursor keys use microsecond precision; the snapshot's original sensor/collection timestamps remain intact. The final page omits `next_to`; an empty result is `{"snapshots":[]}`. Invalid queries return 400; database failures return a generic 503 without connection details. Larger time ranges must be split into windows.

## Local Compose

The default `compose.yaml` can connect to an already available database through `HISTORY_DATABASE_URL`. Alternatively, the optional overlay starts a private PostgreSQL container with a named volume and no published database port:

```sh
# First configure TADO_CREDENTIALS_DIR and API_TOKEN as in containers.md.
export HISTORY_POSTGRES_PASSWORD="$(openssl rand -hex 24)"
make image
docker compose -f compose.yaml -f compose.history.yaml up -d --wait
```

The generated hex password is URL-safe. Keep it securely for future restarts: changing this variable does not change an initialized PostgreSQL role's password. The overlay uses cleartext PostgreSQL only on a dedicated internal Compose network. The bridge also keeps its default network for tado/TRMNL access. Its HTTP port remains bound to host loopback. `down` preserves `history-data`; removing that volume deletes history. Back up the database before destructive changes. Do not run native/Compose instances against the same OAuth credentials concurrently.

## Kubernetes

The chart connects to an existing PostgreSQL service; it does not install a database, operator or additional history PVC. Provision PostgreSQL separately with persistent storage, backups and private networking. Keep the existing credentials PVC for OAuth rotation and the chart's single replica with Recreate updates.

Create an existing Secret containing a `database-url` key using your secret-management process. Then add these nonsecret values to your private values file:

```yaml
history:
  enabled: true
  existingSecret: tado-bridge-history
  databaseURLKey: database-url
  retention: 2160h
config:
  metricsEnabled: true
```

The connection URL is read with `secretKeyRef`; it is never placed in Helm values or a ConfigMap. If you use a private PostgreSQL CA, mount its certificate using the chart's `tls.existingCASecret` and reference `/etc/tado-bridge-ca/ca.crt` as `sslrootcert` in the Secret URL. That chart option also changes outbound HTTPS trust, so include the public trust roots needed by tado/TRMNL. Preview the chart as described in its [README](../charts/tado-bridge/README.md) before an authorized deployment.

## Tests and dashboards

Unit tests cover disabled defaults, URL redaction, authentication, query bounds, cancellation, collector failure isolation and fixed metric labels. Real PostgreSQL tests cover initialization, idempotency, null/zero values, retention, pagination/byte limits, reconnection, cache/history HTTP serving and the Grafana SQL queries. CI runs these against an isolated PostgreSQL service with synthetic data.

To run them locally, set `TEST_HISTORY_DATABASE_URL` to a **disposable test database**, then run `go test -race -count=1 ./...`. Tests create and drop uniquely named schemas. They skip PostgreSQL integration checks when the variable is absent; they never use `HISTORY_DATABASE_URL` or real tado credentials.

Import the [Grafana dashboard template](../integrations/grafana/README.md) to combine room history with the current Prometheus aggregates.
