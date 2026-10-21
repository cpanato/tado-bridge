# tado-bridge — Technical design

Status: implemented architecture and current scope. Version: 0.9. Updated: 2026-10-21.

This document describes the code that exists today and the boundaries for future changes. Setup instructions are in the [README](../../README.md); operational details are in [service architecture](../architecture.md), [authentication](../authentication.md), [containers](../containers.md) and [releases](../releases.md).

## Scope

`tado-bridge` is a read-only CLI and service for tado° V3/V3+ and X cloud APIs. It discovers homes/rooms, normalizes climate measurements, caches successful collections, and exposes a versioned JSON API. Prometheus metrics and TRMNL publishing are optional consumers of that cache.

The collector and domain model are independent of displays and dashboards. Multiple homes and mixed generations share the same normalized schema. No consumer can initiate an upstream read.

The project does not implement thermostat controls, local Matter access, historical storage, a built-in dashboard, multi-user hosting, MQTT or generic outbound webhooks. New adapters should be added when there is a concrete need, without a generic plugin framework or a mandatory display dependency.

## Architecture

```text
 tado° cloud APIs
       |
       v
 [Client + OAuth] --> [Collector / normalizer] --> [Snapshot store]
                                                       |
                         +-----------------------------+------------------+
                         |                             |                  |
                         v                             v                  v
                   Cached JSON API             Optional metrics     Optional TRMNL
                         |                             |                  |
                    Any client                    Prometheus        Private plugin
```

| Package | Responsibility |
|---|---|
| `internal/cli` | Cobra command tree, configuration and component wiring |
| `internal/tado` | OAuth, generation detection, provider responses and normalization |
| `internal/credentials` | Private token storage, atomic rotation and locking |
| `internal/domain` | Provider-neutral discovery and measurement types |
| `internal/collector` | Collection scheduling, rolling request budget and retries |
| `internal/store` | Immutable serialized snapshot replacement and reads |
| `internal/httpapi` | Cached JSON, health, readiness and API authentication |
| `internal/metrics` | Optional bounded aggregates and process-local counters |
| `internal/integrations/trmnl` | Optional selected-room payloads and webhook publishing |
| `internal/service` | Runtime validation and coordinated shutdown |
| `internal/testutil` | Reusable synthetic upstream APIs and deployment fixture tooling |
| `internal/packaging` | Chart and release configuration checks |

A single collector owns the tado client and its token state. A complete successful collection is serialized before atomic replacement in the store. Upstream or encoding failures preserve the previous snapshot. The cache is memory-only and limited to 4 MiB; callers cannot mutate its stored bytes.

## Data contract

The implemented types are defined in [internal/domain](../../internal/domain). A snapshot contains `provider`, `collected_at`, homes and normalized zones. X rooms use the same `zones` representation as V3 zones.

Each zone includes measured/target temperature, humidity, heating activity and an optional temperature measurement timestamp. Units are Celsius and percent. Missing values are JSON `null`; valid zero and false values remain distinguishable from missing data.

An OFF room has no active target, even when upstream retains a setpoint. Heating activity reflects explicit upstream heating power above zero, without inferring it from temperature differences or claiming that a boiler is firing. `collected_at` is collection completion time; a sensor timestamp is retained only when upstream supplies it. Neither HTTP success nor collection time proves sensor freshness.

Home and room identifiers are strings. Names and IDs remain private data even though they are not credentials. Metrics omit them; TRMNL uses IDs only for local selection and sends configured display aliases.

## CLI and authentication

The command tree provides `auth login`, `auth status`, `auth logout`, `discover`, `read`, `serve` and `healthcheck`. Help and argument validation occur before credential access or upstream calls. JSON goes to stdout; login instructions, logs and request counts go to stderr.

Authorization uses the tado° OAuth device-code flow with `offline_access`, a published public client ID and no client secret or password grant. The client preserves the complete browser verification URL, polls at the provider's interval and handles pending/slow-down responses. Refreshes reuse access tokens until near expiry and persist rotated refresh tokens before collection proceeds.

Credentials use an explicitly configured path, mode-0600 regular files and private directories. Updates use same-directory temporary files, syncing and atomic rename. A lock serializes commands sharing that canonical path; `serve` owns it for its entire lifetime. Independent credential copies cannot coordinate refresh rotation.

`auth status` checks local state only. `auth logout` deletes the local file without upstream revocation. `healthcheck` checks local liveness without opening credentials or calling tado°. Endpoints, source evidence and live-validation limits are documented in [authentication](../authentication.md).

## Scheduling and resilience

Collection starts immediately. `POLL_INTERVAL` defaults to 30 minutes with a five-minute minimum, but the rolling request budget can increase the delay. The default budget is 90 REST/OAuth attempts in a process-local 24-hour window. Failed transport attempts and retries count too.

The scheduler learns the cycle cost and reserves at least one OAuth request per cycle:

```text
max(POLL_INTERVAL, 24h * estimated_cycle_cost / TADO_DAILY_REQUEST_BUDGET)
```

One X home reserves four requests and therefore waits at least 64 minutes with defaults. V3 collection adds a request for each zone; its freshness limit may need to be raised to match the effective cadence. A per-request guard enforces the budget even if discovery changes the estimated cost mid-cycle.

HTTP 429 honors the later of `Retry-After` and an exhausted upstream `RateLimit` reset, with a conservative fallback when neither provides a deadline. Transient failures use bounded exponential backoff with jitter. Failures never delete a good snapshot or silently begin interactive login.

Budget state resets with the process and excludes other clients. Restarting does not reset tado°'s account quota. See [scheduling details](../architecture.md#scheduling-and-quota).

## HTTP and metrics

| GET endpoint | Contract |
|---|---|
| `/api/v1/snapshot` | 503 before the first successful collection; then 200 with the cached snapshot, even when stale |
| `/healthz` | Process liveness, independent of upstream availability |
| `/readyz` | 200 only when climate values exist and collection age is within `MAX_SNAPSHOT_AGE` |
| `/metrics` | Optional cached Prometheus aggregates and request/collection counters |

The listener defaults to loopback. A non-loopback bind requires `ALLOW_NETWORK_EXPOSURE=true` and an API token. When configured, bearer authentication protects snapshots and metrics; health/readiness expose generic status without authentication. HTTP headers and connection lifetimes are bounded; upstream response reads have size limits.

Snapshots use RFC3339 UTC timestamps, explicit nulls and `Cache-Control: no-store`. Stale snapshots remain accessible with their original timestamp; readiness reports that they are too old. Metrics contain fixed labels only, omit unavailable aggregates and never trigger collection. See [metrics](../metrics.md).

## TRMNL adapter

TRMNL publishing is disabled by default and requires a private-plugin webhook plus explicit room selection through environment variables. Configured aliases are sent instead of home/room IDs. The destination must be an official HTTPS private-plugin endpoint; webhook secrets are excluded from logs and errors.

The publisher reads the cache in its own background task. It sends a successful snapshot once, then sends again if that snapshot transitions to stale. Payloads include `source`, `collected_at`, `published_at`, `max_age_seconds` and `stale`, plus only the selected room readings. It never fabricates data before the first collection.

Webhook calls have bounded timeouts and retries, respect `Retry-After`, disable redirects/proxy environment variables and do not stop collection or the JSON API on failure. Payloads are limited to the base webhook size allowance. The four Liquid layouts handle unavailable/zero values, room limits and stale warnings.

Setup, payloads and device checks are documented in [integrations/trmnl](../../integrations/trmnl/README.md). The integration sends selected climate data to TRMNL cloud only after explicit opt-in.

## Runtime and packaging

SIGINT/SIGTERM cancels requests and waits, stops accepting HTTP traffic and joins the collector, server and optional publisher before releasing credentials. Forced termination may leave a stale lock requiring operator recovery after confirming no process still uses the credentials.

Containers use Chainguard static `latest` and ko, run nonroot and support Linux amd64/arm64. Compose uses a read-only root and a writable credentials directory, publishes only to host loopback and builds the local image before starting.

The Helm chart uses one replica with Recreate updates, existing Secrets, an existing writable credentials PVC and an optional ClusterIP Service. It mounts the whole credential directory to support atomic rotation. Liveness remains independent of upstream freshness; readiness checks the cache. No public ingress or automatic external deployment is configured.

GoReleaser builds Linux/macOS binaries and multi-platform images. Snapshot CI does not publish or sign. Operator-pushed version tags trigger the separate GitHub/GHCR release workflow, with native GitHub notes and keyless cosign signatures. Actions are pinned by SHA; Dependabot checks Go modules and Actions with a seven-day version-update cooldown. Tool selection belongs in `go.mod` and the workflows rather than a duplicated version table. See [release operation](../releases.md) and [security policy](../../SECURITY.md).

## Validation and future work

| Area | Current evidence or remaining work |
|---|---|
| OAuth and V3/X normalization | Synthetic fixture tests, cancellation/retry/429 coverage and live X validation |
| Cache, API and metrics | Unit/race tests for immutability, null/zero values, freshness, authentication and bounded labels |
| Containers and chart | Local smoke tests and disposable Kind deployment checks against synthetic HTTPS tado APIs |
| Credential persistence | Fixture tests cover rotation into the PVC and valid-token reuse after pod restart |
| TRMNL | Synthetic payload/layout tests, virtual-device checks and successful real-data webhook refreshes |
| Continuous operation | Initial native day test accepted; uninterrupted 24-hour testing remains deferred |
| Live V3/V3+ and physical TRMNL | Still require acceptance on suitable hardware/accounts |
| Release signing | Configuration and synthetic pipeline checks; real OIDC/registry signing is exercised by a tagged release |

Account-wide quota sharing, provider availability and sensor timestamps remain upstream constraints. MQTT and other output adapters are possible extensions when needed; consumers should continue using immutable cached snapshots without taking ownership of the upstream client. Local Matter, controls and persistent history are not implemented.
