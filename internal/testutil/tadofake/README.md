# Reusable synthetic tado API

`Server` implements `http.Handler` for deterministic integration tests. Its zero value is ready to use; each instance owns independent, concurrency-safe counters and outage state. It makes no outbound requests and uses only the exported synthetic token constants.

```go
fake := &tadofake.Server{}
server := httptest.NewServer(fake)
defer server.Close()
// Point the test client's API/OAuth transports at server.URL.
fake.SetUnavailable(true)  // authenticated REST requests now return 503
fake.SetUnavailable(false)
stats := fake.Stats()     // API and OAuth request attempts
```

The [client integration test](../../tado/fake_integration_test.go) uses this handler for OAuth refresh, V3/X collection, failure and recovery. New tests can reuse it wherever this shared fixture contract fits; focused edge cases can retain their own small handlers.

## Fixture contract

| Endpoint | Behavior |
|---|---|
| `POST /oauth2/token` | Requires refresh-token grant, nonempty client ID and an initial/rotated synthetic refresh token; issues a one-hour access token and rotated refresh token |
| `GET /api/v2/me` | Two synthetic homes, V3 and X |
| `GET /api/v2/homes/{1,2}` | V3 / LINE_X generation metadata |
| `GET /api/v2/homes/1/zones` | One V3 zone |
| `GET /api/v2/homes/1/zones/1/state` | Zero measured temperature/humidity, heating off and a retained target to exercise normalization |
| `GET /homes/2/rooms`, `/homes/2/roomsAndDevices` | Eight X rooms with synthetic measurements |
| `GET /healthz` | Fixture liveness |
| `GET /__fixture/stats` | Attempt counters, excluding fixture controls and liveness |
| `POST /__fixture/failure?enabled=true` | Toggle authenticated REST failures; `enabled=false` restores responses |

REST endpoints require `Bearer fixture-access`. Unknown authenticated paths return 404; unsupported REST methods return 405. Device authorization, rate-limit simulation and TRMNL webhooks are outside this fixture's current contract. Existing focused tests cover those behaviors separately.

## Snapshot-only deployment tool

`internal/testutil/cmd/tadofake` wraps the same handler for [chart integration](../../../scripts/README.md#chart-deployment-integration):

- `prepare DIR` creates a synthetic self-signed CA/server certificate and private key (mode 0600), valid for one day, for the three tado hostnames used by the client.
- `serve` seeds expired synthetic credentials in `/state/credentials.json` if missing and serves HTTPS on 8443 using `/tls/tls.crt` and `/tls/tls.key`. It handles termination with bounded shutdown.
- `check` calls the bridge at `BRIDGE_URL` using synthetic `API_TOKEN`, checks readiness, authentication, V3/X snapshots, metrics and persisted token rotation on the shared volume.
- `check --reused-token` performs the same checks after application restart and requires zero OAuth requests in the new process.

GoReleaser builds its binary/image only for snapshots and excludes it from product archives. The fixture controls are unauthenticated test helpers: run only in disposable test environments, never with real tokens or as an exposed service. Production clients keep their official URLs; the isolated chart test uses pod host aliases and a test CA instead of adding production endpoint override flags.

## Compose dashboard preview

The snapshot-only `tadofake preview` command serves a synthetic cache, metrics and history API, with 24 hours of generated readings for eight generic rooms in a disposable PostgreSQL database. It uses the actual history writer, API handler and Prometheus exporter; it never constructs a tado client, reads OAuth credentials or publishes to TRMNL. `scripts/observability-smoke.py` supplies its private database URL, API token and bind configuration and owns cleanup. This preview path is for fixtures only and is not included in the production CLI or published image.
