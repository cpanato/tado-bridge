# M2 local service

`tado-bridge serve` runs one collector and an HTTP server. The collector owns the tado client, counts requests and refreshes credentials when needed. Provider DTOs are normalized before entering the store. HTTP handlers have access only to the store; consumers cannot trigger upstream reads.

The in-memory store holds one serialized snapshot, limited to 4 MiB. A successful collection is serialized before atomic replacement. Encoding/size failures and upstream errors preserve the previous snapshot. Input objects and returned byte slices cannot mutate the cached copy. Nothing from the snapshot is written to disk.

## Scheduling and quota

Collection starts immediately. Every attempted upstream HTTP request passes through a process-local rolling 24-hour budget, including OAuth refresh and failed network attempts. The default cap is 90, leaving some headroom below tado's documented base allowance of 100/day. Other integrations, other processes and requests from `read`/`discover` are outside this process's accounting. Restarting the process resets its counter; it does not reset the provider's quota. Do not rely on restarts to bypass limits.

The collector learns a conservative cycle cost: actual REST calls plus at least one OAuth request reserved per cycle. The estimate only increases during the process lifetime, allowing room for refresh even when the current token was reused. The next delay is at least:

```text
max(POLL_INTERVAL, 24h * estimated_cycle_cost / TADO_DAILY_REQUEST_BUDGET)
```

One X home needs three REST calls; reserving a refresh gives four calls and a default 64-minute delay. One V3 home with eight zones needs eleven REST calls plus one refresh reserve: at least 192 minutes with the default budget. For larger accounts, raise `MAX_SNAPSHOT_AGE` to match the intended cadence, or choose an appropriate budget supported by your account. Discovery reuse and further request optimization can follow after measuring usage.

Before starting later cycles, the collector checks that the full estimated cycle fits the remaining rolling budget. The per-request guard enforces the cap even if discovery changes the cost mid-cycle. A budget smaller than the learned cycle estimate stops the service with a configuration error rather than looping indefinitely. No quota state is persisted. A single credentials lock is held for the service lifetime, preventing another command using the same canonical file path from rotating its refresh token concurrently.

Transient network/5xx errors use bounded exponential backoff with jitter: 30 seconds, 60 seconds, 120 seconds, each with up to 25% extra jitter. After three retries, collection returns to the effective polling cadence. Requests made by retries count against the same budget. Nontransient failures use the normal cadence. No error removes a good cached snapshot or silently starts interactive login.

On 429, the client enforces a cooldown before further requests. It honors the later of HTTP `Retry-After` (seconds or HTTP date) and an exhausted tado `RateLimit` reset (`r=0;t=...`). If no usable deadline is provided, it waits at least one hour. The collector also waits for request-budget availability; a server-requested delay is never shortened by backoff caps.

## HTTP and readiness

The listener defaults to `127.0.0.1:8080`. HTTP has bounded header size and header/read/write/idle timeouts. No public listener is opened automatically. Health endpoints reveal only generic process/readiness status, without home data or request diagnostics.

| GET route | Behavior |
|---|---|
| `/api/v1/snapshot` | Latest cached snapshot; 503 with a structured JSON error before the first successful collection |
| `/healthz` | 200 while the HTTP process is running, including upstream outages |
| `/readyz` | 200 if the cache has at least one climate value and its collection age is within `MAX_SNAPSHOT_AGE`; otherwise 503 |

Data remain available through the snapshot endpoint with status 200 when stale. Consumers check `collected_at`; readiness independently reports staleness. A snapshot with no homes or no available climate fields can be cached but is not ready. Collection age is not sensor age: existing `temperature_measured_at` remains null if upstream does not supply a timestamp.

Only GET is accepted. JSON responses use `application/json` and `Cache-Control: no-store`. When `API_TOKEN` is set, the snapshot route requires `Authorization: Bearer <token>`. Authentication uses a constant-time comparison; errors never reflect token values. Health/readiness remain unauthenticated for internal monitoring. Metrics are deferred to M3 and `/metrics` is not exposed.

## Lifecycle and boundaries

SIGINT/SIGTERM cancels waits and upstream HTTP requests, stops accepting HTTP traffic and waits for both the collector and server to exit before releasing the credentials lock. HTTP shutdown has a five-second deadline and closes remaining connections if necessary. A force-killed process can leave the existing lock directory; verify it is not running before removing that lock.

M2 has no outbound integrations. TRMNL, container packaging, releases and deployments are separate milestones. Future adapters consume cached snapshots and never obtain a provider client.
