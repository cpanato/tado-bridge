# tado-bridge — Technical Design Document

Status: M3 implemented; acceptance pending | Version: 0.6 | Implementation language: Go

M1 scope amendment: cloud discovery now includes tado° X, explicitly authorized after live verification identified the operator's home as `LINE_X`. Detect generation per home and normalize V3 zones and X rooms into the same provider-neutral discovery model. Local Matter support remains out of scope.

Post-M1 amendment: M1 is merged. A one-shot `tado-bridge read` command is authorized to validate normalized measurements before the M2 service: measured/target temperature, humidity, explicit heating-power activity and available sensor timestamps. It supports V3/V3+ and X, has no polling or HTTP listener, and returns no partial snapshot on failure. Periodic collection, cache and the local HTTP API remain M2 work.

M2 implementation note: `serve` now implements collection, cache and the local API. `POLL_INTERVAL=30m` is a requested minimum; the effective delay grows to fit the rolling request budget, default 90 attempted REST/OAuth calls per 24 hours. One X home reserves four calls per cycle and therefore waits at least 64 minutes with defaults. Budget accounting is process-local and resets on restart; the upstream account quota remains authoritative. See `docs/architecture.md` for scheduling and operational boundaries.

M3 implementation note: optional Prometheus metrics use cached aggregates without home/zone labels. Distroless nonroot containers support Linux/amd64 and Linux/arm64, with read-only root filesystems and a writable private credentials directory for token rotation. CI builds and tests images locally without publishing. See `docs/containers.md`, `docs/metrics.md`, `SECURITY.md` and `docs/releases.md`. TRMNL remains M4.

## 1. Summary

`tado-bridge` is a lightweight, read-only Go service that retrieves heating and indoor-climate measurements from tado° and exposes normalized, cached data through vendor-neutral interfaces. It is **not** a TRMNL project. TRMNL, Grafana, custom dashboards, automation services, and future consumers are external clients. TRMNL is an optional output adapter, never part of the core domain or repository identity.

## 2. Goals and non-goals

### Goals
- Support tado° V3/V3+ and tado° X cloud discovery in M1; validate generation-specific measurements for M2.
- Discover homes/zones and collect available measured temperature, target temperature, relative humidity, heating state and relevant timestamps; represent unavailable fields explicitly rather than fabricating values.
- Offer a stable versioned JSON API and optional Prometheus metrics.
- Cache the last successful snapshot; consumers must never cause upstream API calls.
- Minimize upstream requests and handle rate limits gracefully.
- Run as a small single binary or container on Linux/amd64 and Linux/arm64.
- Support extension through interfaces without prematurely building a plugin framework.

### Non-goals for MVP
- Setting temperatures, changing schedules, controlling devices.
- Local Matter support (future provider adapter).
- Multi-user SaaS, a frontend, a database, or a mandatory display-specific dependency.
- Public internet exposure by default.

## 3. Architecture

```text
 tado° V3/V3+ / X Cloud APIs
           |
           v
 [Tado Client + OAuth]
           |
           v
 [Collector / Normalizer] --periodic refresh--> [In-memory Snapshot Store]
                                                    |           |
                                                    v           v
                                              REST JSON     /metrics (optional)
                                                    |           |
                                               Any client   Prometheus
```

Separation of concerns:
- `provider`: upstream tado° authentication, home discovery, zone-state fetch and upstream DTOs.
- `collector`: polling, rate-limit handling, state normalization and atomic snapshot replacement.
- `domain`: provider-neutral models with typed optional fields and timestamps.
- `transport/http`: read-only JSON API, health and readiness checks.
- `observability`: logs and optional Prometheus exporter.

Optional output adapters (including TRMNL webhook push) consume snapshots through an internal interface and never call tado° directly. Implement after the core collector/API milestones.

## 4. Data model

Suggested domain types:

```go
type Snapshot struct {
    Provider    string    `json:"provider"`
    CollectedAt time.Time `json:"collected_at"`
    Homes       []Home    `json:"homes"`
}
type Home struct {
    ID    string `json:"id"`
    Name  string `json:"name"`
    Zones []Zone `json:"zones"`
}
type Zone struct {
    ID                string   `json:"id"`
    Name              string   `json:"name"`
    TemperatureC      *float64 `json:"temperature_c"`
    TargetTemperatureC *float64 `json:"target_temperature_c"`
    HumidityPercent   *float64 `json:"humidity_percent"`
    HeatingActive     *bool    `json:"heating_active"`
}
```

All example fields are provisional until validated against real V3/V3+ API responses. `null` means unknown/unavailable, not zero or false. Never infer `heating_active` from target vs measured temperature alone; omit/null if upstream state does not support it. Preserve stable IDs; avoid leaking personally identifying metadata in metrics.

## 5. API contract (MVP)

- `GET /api/v1/snapshot`: latest normalized snapshot, including `collected_at`, `provider`, homes and zones. Return `503` with a structured JSON error if no successful collection has occurred.
- `GET /healthz`: process liveness (`200` when running).
- `GET /readyz`: `200` only when a usable snapshot exists and is not beyond a configurable staleness threshold; otherwise `503`.
- `GET /metrics`: optional, configurable; Prometheus metrics for last successful collection time, collection errors, snapshot age, and available temperatures/humidity with bounded labels. Do not export access tokens, personal IDs or addresses.

Serve cached data only. Document timestamps in RFC3339 UTC, temperature in Celsius and humidity in percent. Set appropriate JSON content types and reasonable server timeouts. If auth is enabled, protect data endpoints and metrics appropriately; health endpoints can remain internal/unauthenticated.

Example response (illustrative, not real user data):

```json
{
  "provider": "tado",
  "collected_at": "2026-10-18T16:30:00Z",
  "homes": [{
    "id": "123", "name": "Home",
    "zones": [{"id":"1","name":"Living Room","temperature_c":21.5,"target_temperature_c":22.0,"humidity_percent":48.0,"heating_active":null}]
  }]
}
```

## 6. Authentication and credentials

Implement the OAuth device authorization flow required by the current tado° V3/V3+ API, including refresh-token handling. **Before implementation, verify current endpoints, client registration/client ID requirements, scopes and API terms against official documentation or the actual supported flow. Do not invent constants.**

- Interactive one-time setup command, e.g. `tado-bridge auth login`, displays verification URL/code and waits for authorization.
- Persist tokens in a user-specified credentials file with restrictive permissions (`0600`), never log or commit them.
- Refresh tokens safely; update credential file atomically, preventing partial writes. Handle refresh token rotation.
- No password-based login or collection of tado° account passwords.
- Support noninteractive server runtime after initial authorization.
- Provide `auth status` and `auth logout` subcommands if straightforward.

## 7. Polling, quota, resilience

- Requested default polling interval: 30 minutes; configurable with a 5-minute lower bound. Increase the effective interval according to observed cycle cost and configured budget; do not exceed the budget to satisfy the requested interval.
- A single polling cycle may involve multiple API calls (home discovery and per-zone state). **Do not assume one poll equals one request.**
- Discover and record actual upstream call counts, and allow a configurable daily request budget (`TADO_DAILY_REQUEST_BUDGET`, conservative default, subject to verification).
- On `429`, honor `Retry-After`; on transient errors, use bounded exponential backoff with jitter, without exceeding budget.
- Keep serving the last good snapshot on upstream failure; expose staleness via timestamp and readiness.
- Use HTTP client timeouts, context cancellation, graceful shutdown, and no concurrent duplicate refreshes.
- Log errors without tokens or full sensitive payloads.

## 8. Configuration

Use environment variables, optionally CLI flags; no config framework required initially.

| Variable | Purpose | Suggested default |
|---|---|---|
| `LISTEN_ADDR` | HTTP bind address | `127.0.0.1:8080` |
| `POLL_INTERVAL` | Collection cadence | `30m` |
| `MAX_SNAPSHOT_AGE` | Readiness freshness | `2h` |
| `TADO_CREDENTIALS_FILE` | OAuth token store | user-configured path |
| `TADO_DAILY_REQUEST_BUDGET` | Upstream request cap | documented conservative value |
| `METRICS_ENABLED` | Enable `/metrics` | `false` |
| `API_TOKEN` | Optional bearer auth for read API | unset (local-only) |
| `ALLOW_NETWORK_EXPOSURE` | Explicit acknowledgement for non-loopback binds; also requires `API_TOKEN` | `false` |

Fail fast if binding a non-loopback interface without explicitly acknowledging authentication/network exposure. No sensitive defaults in examples.

## 9. Proposed repository layout

```text
tado-bridge/
  cmd/tado-bridge/main.go
  internal/domain/
  internal/tado/
  internal/collector/
  internal/store/
  internal/httpapi/
  internal/metrics/              # optional
  docs/architecture.md
  docs/authentication.md
  examples/
  Dockerfile
  compose.yaml
  .gitignore
  .golangci.yml                 # only if useful
  go.mod
  README.md
  LICENSE                      # choose deliberately
```

Keep package boundaries pragmatic. Prefer Go standard library; Cobra is explicitly approved for CLI command routing and help, isolated in `internal/cli/`. A maintained OAuth2 dependency and Prometheus client are acceptable where they reduce security or maintenance risk. Avoid a generic plugin framework.

## 10. Security and privacy

- Read-only upstream access; never add thermostat-control operations to MVP.
- Restrict credential file permissions; redact authorization headers and tokens.
- Default to loopback bind; container deployment should document network boundaries.
- Protect APIs when accessed beyond trusted localhost/network; TLS via reverse proxy if exposed.
- Do not persist full home data to disk by default.
- Document that room names, temperatures and occupancy-adjacent signals can be sensitive.
- Never expose OAuth device codes or refresh tokens through the JSON API or metrics.

## 11. Testing and quality

- Table-driven unit tests for normalization, optional/null fields, snapshot replacement and freshness.
- Mock HTTP server tests for OAuth refresh, API parsing, timeouts, 429 and retry logic.
- Fake clock or injectable time source for deterministic quota and staleness tests.
- `go test ./...`, `go vet ./...`, `go test -race ./...` in CI where feasible.
- Integration tests against real tado° credentials are opt-in only, never CI-required.
- Multi-stage Docker build with nonroot runtime and amd64/arm64 build instructions.

## 12. Implementation milestones and acceptance criteria

### M1 — Upstream connectivity
- CLI OAuth device login and secure token storage.
- Discover home and zones and print sanitized JSON to stdout.
- Detect each home's generation; use V3 zones or X rooms as appropriate, preserving the same JSON schema.
- Unit tests using fixtures; no real credentials in repo.
- Document actual supported upstream API endpoints and observed request counts.

### M2 — Standalone service
- Periodic collector, normalized snapshot cache, `/api/v1/snapshot`, `/healthz`, `/readyz`.
- Rate-limit and error handling; clean shutdown.
- Configuration and local run instructions.

### M3 — Production hygiene
- Container build, Compose example, CI tests, optional metrics, security documentation.
- Verify operation on Linux/amd64 and Linux/arm64.

### M4 — Optional consumer adapters (TRMNL first)
- Document how generic clients poll the JSON endpoint.
- Consider webhook output or MQTT as independent optional adapters based on actual needs.
- Implement optional TRMNL push adapter and Liquid templates inside `integrations/trmnl/`, isolated from core packages; keep it disabled by default.

## 13. Open questions

1. Exact V3/V3+ API access rules, client credentials and quota at implementation time.
2. Which zone fields reliably expose active heating state versus inferred state?
3. Whether more than one home needs support in the first deployment.
4. Prometheus is implemented in M3 and remains disabled by default.
5. Deployment target: NUC systemd vs Docker vs k3s; the app should support all without coupling to one.

## 14. Codex implementation instructions

Create a new Git repository named `tado-bridge` following this design. Start with **M1 only** and make the smallest working, tested implementation. Before writing the tado° client, check current authoritative API/OAuth documentation and clearly note any uncertainties; do not hardcode guessed endpoints or silently use deprecated password authentication. Keep the service read-only, provider-neutral at the domain/API layer, and independent of TRMNL at the core/domain layer; TRMNL is an opt-in integration milestone. Use idiomatic Go and a small dependency footprint. Provide a README with prerequisites, device-code login steps, safe credentials handling, local development commands and tests. Stop after M1 and report actual API endpoints, quota assumptions and any unresolved blockers before proceeding to M2.

## 15. Shared integration contract (aligned with berlin-transit)

The two repositories are independent binaries and MUST NOT import one another. Adopt matching conventions, not a shared Go library prematurely. Each project has a provider collector, immutable normalized snapshot, bounded in-memory cache, versioned read-only REST API, and optional outbound integrations. `integrations/trmnl/` is a feature adapter, not the identity of the project.

**TRMNL push (first implementation):** operator creates a private TRMNL plugin and supplies its webhook URL via secret environment variable. A background publisher reads cached snapshots only and POSTs minimal JSON to that HTTPS URL. No inbound port, publicly exposed endpoint, or TRMNL account credential is needed. Do not send until `TRMNL_ENABLED=true`; startup should validate configured destination without logging its secret URL. Use configurable interval (default 30m), bounded payload, HTTP timeout, Retry-After and capped exponential backoff with jitter; avoid retry storms, duplicate updates, and overlapping sends. Never transmit stale data as fresh: include `collected_at`, `published_at`, `stale` and `source`. Do not send private home/zone identifiers unless required; user-selectable rooms, display names, and explicit privacy guidance. Do not publish automatically when there has never been a successful collection.

**Pull (optional, later):** generic read-only JSON endpoint is already available locally; do not create a TRMNL-specific public endpoint. Any remote pull requires operator-provided authenticated proxy/network access, never automatic public exposure.

**Recommended project files:** `internal/integrations/trmnl/{config,client,payload}.go` and tests; `integrations/trmnl/{README.md,settings.yml,full.liquid,half_horizontal.liquid,half_vertical.liquid,quadrant.liquid}`. Check current TRMNL template format and webhook contract before implementation. Templates must handle absent humidity, missing target temperatures, and stale status gracefully. Provide mock-server contract tests, disabled-by-default tests, secret redaction tests, and example sanitized payloads.

**Example configuration (illustrative; validate against implementation):**

```yaml
integrations:
  trmnl:
    enabled: false
    mode: push
    webhook_url_env: TRMNL_WEBHOOK_URL
    interval: 30m
    rooms: [] # empty means configured default selection, never infer private scope
```

**M4 acceptance:** optional TRMNL push works with a mock HTTPS server and a manually configured private plugin; normal REST behavior and collection remain fully functional with TRMNL disabled or unreachable. Document that TRMNL cloud push sends selected home data to an external provider, requiring explicit opt-in.

## 16. Repository and CI/security alignment

Use https://github.com/cpanato/pr-inbox as a **reference to inspect**, not a blindly copied template. Verify its current workflows, Go tooling and security controls before choosing patterns. Use the latest supported patched stable Go toolchain verified at implementation time; pin CI actions to full commit SHAs; minimum `GITHUB_TOKEN` permissions; avoid `pull_request_target` for untrusted code; use `go test ./...`, `go test -race ./...`, `go vet ./...`, lint and `govulncheck`; Dependabot for Go modules and GitHub Actions. Build containers in CI only without pushing. Docker runs nonroot with read-only filesystem where feasible; localhost binding and no automatic public ingress. Any tags, releases, registries, deploys, outbound telemetry or publish workflows require explicit operator authorization. Keep secrets out of logs, workflow outputs, examples and artifacts. Add `SECURITY.md`, Apache-2.0 LICENSE, Makefile, and clear release policy.

**Codex handoff update:** implement M1 first. Do not implement the TRMNL adapter until the core API is stable; then implement it as M4 without changing the domain model for display requirements.
