# Optional Prometheus metrics

Set `METRICS_ENABLED=true` when starting `serve`. Without it, `/metrics` returns 404 and no exporter is registered. This feature uses a private registry from the [official Prometheus Go client](https://github.com/prometheus/client_golang); its dependency is declared in [go.mod](../go.mod). It adds no outbound connections or upstream polling.

Metrics use the same listener and optional API_TOKEN bearer authentication as the snapshot endpoint. Health/readiness remain unauthenticated. The default bind is loopback; enabling metrics does not change networking or authentication. Use Prometheus's `authorization.credentials_file` option to keep the bearer token out of checked-in scrape configuration.

| Metric | Meaning |
|---|---|
| `tado_bridge_last_successful_collection_timestamp_seconds` | Unix timestamp of the cached successful collection |
| `tado_bridge_snapshot_age_seconds` | Age of the cached collection, calculated at scrape time |
| `tado_bridge_collection_errors_total` | Failed collections, including cache replacement errors |
| `tado_bridge_collections_total{result="success\|error"}` | Attempt counts with two fixed result labels |
| `tado_bridge_upstream_requests_total{kind="api\|oauth"}` | Actual HTTP attempts with two fixed request classes |
| `tado_bridge_temperature_celsius{statistic="min\|max\|mean"}` | Available measured temperatures across all rooms |
| `tado_bridge_target_temperature_celsius{statistic="min\|max\|mean"}` | Available active target temperatures across all rooms |
| `tado_bridge_humidity_percent{statistic="min\|max\|mean"}` | Available humidity across all rooms |

Before the first successful collection, timestamp/age/climate series are absent rather than fabricated. Aggregates omit null fields and preserve legitimate zero values; a family with no available values is absent. OFF rooms have no active target. A failed collection retains old values and increments the error counter; snapshot age continues increasing. Collection age is not sensor age, particularly when the provider supplies no measurement timestamp.

Labels are fixed enums, independent of home or room count. No account/home/zone IDs, room names, provider-supplied labels, addresses, device serials, errors, tokens or credential paths are exported. Even aggregate climate values can be sensitive; protect access and choose Prometheus retention deliberately. Per-room metrics and display-specific labels are not implemented.
