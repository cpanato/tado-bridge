# tado-bridge Helm chart

This chart packages a single collector and its private cached HTTP API. It creates a Deployment and an optional **ClusterIP-only** Service, with no Ingress, LoadBalancer, NodePort, RBAC, autoscaler or public port. History and TRMNL are disabled by default. Rendering/packaging the chart does not deploy it.

## Required runtime state

Provision the following in the chosen namespace before an explicitly authorized installation:

- A writable PVC, selected by `credentials.existingClaim` (default `tado-bridge-credentials`), containing the current `credentials.json` in its root. Mount the entire directory. The directory and file must be writable by UID/GID **65532:65532**; keep the file mode **0600** and the directory private. The chart does not create, initialize or delete the PVC. Prepare it with your storage platform's approved transfer/restore mechanism; the application image has no shell or tar utility.
- An existing Secret, selected by `secrets.existingSecret` (default `tado-bridge-secrets`), with a nonempty printable ASCII `api-token` key. The token authenticates snapshots and optional metrics. Keep its source outside Git and avoid inline secret values in Helm command lines.
- An application image available to your cluster's nodes. Defaults refer to a **locally built** `ko.local/tado-bridge:local`, not an assumed registry release. A Docker Desktop image is not automatically available in a remote Kubernetes cluster. Supply your explicitly authorized image repository/tag or digest, or load the local image into your chosen local cluster separately.

Stop any native/Compose instance using these credentials before transferring current tokens or starting the Kubernetes instance. Never run two instances with copied versions of the same refresh credentials. The chart fixes replicas to one and uses `Recreate` to avoid overlapping revisions. The file lock protects one shared path; it cannot coordinate independent copies on different volumes or hosts.

OAuth refresh saves a new token by atomic rename and creates a lock directory. A read-only Secret projection or a single-file `subPath` mount cannot replace the writable credentials PVC. Rotations update the PVC, **not** an original Kubernetes Secret or a host copy. Back up the current volume securely. The existing PVC survives chart removal. After an abrupt termination, a stale lock can require operator recovery after confirming no other process is using those credentials; the chart never removes a lock automatically.

## Local validation and packaging

Use a current stable Helm release. No cluster access is used by these commands:

```sh
make helm-check
make helm-package
helm template local charts/tado-bridge > /tmp/tado-bridge-manifests.yaml
```

The package is written under `dist/charts/`; this directory is ignored by Git. CI lints, renders, tests defaults/overrides/security boundaries and packages the chart. It also installs into a disposable Kind cluster using synthetic HTTPS tado APIs; see [integration checks](../../scripts/README.md#chart-deployment-integration). No chart is published.

chart-testing discovers `ci/*-values.yaml` automatically. The current `ci/integration-values.yaml` selects the local snapshot, authenticated metrics, PostgreSQL history and fixture CA Secret. The integration script supplies only the dynamic fake Service IP through an ignored overlay. `make helm-check` also validates the chart defaults, since chart-testing uses the CI values when present.

For a later authorized install, prepare a private values file outside the repository with the actual existing claim/Secret names and node-accessible image. Preview the manifests first:

```sh
helm template tado-bridge charts/tado-bridge \
  --namespace your-namespace --values /path/to/private-values.yaml

# Run only when the target cluster/namespace and installation are authorized.
helm upgrade --install tado-bridge charts/tado-bridge \
  --namespace your-namespace --values /path/to/private-values.yaml
```

An existing namespace is assumed; the chart does not create one. Use cluster context/namespace controls appropriate to your environment. Deployment validation uses only a dedicated ephemeral Kind cluster and synthetic credentials.

## Settings and operational behavior

| Values | Default | Purpose |
|---|---|---|
| `image.repository`, `image.tag` | `ko.local/tado-bridge`, `local` | Application image, not the Chainguard base image |
| `image.digest` | empty | `sha256:...` takes precedence over tag when provided |
| `tls.existingCASecret`, `tls.caKey` | empty, `ca.crt` | Optional custom outbound CA bundle; only this certificate key is projected read-only, never private keys |
| `hostAliases` | `[]` | Optional pod hostname mappings, used by isolated HTTPS fixtures |
| `image.pullPolicy` | `IfNotPresent` | Local images can use `Never`; registry images may need pull credentials |
| `credentials.existingClaim` | `tado-bridge-credentials` | Preinitialized writable credential volume |
| `secrets.existingSecret` | `tado-bridge-secrets` | Existing API/webhook Secret |
| `secrets.apiTokenKey` | `api-token` | Required API bearer token key |
| `service.enabled`, `service.port` | `true`, `8080` | ClusterIP Service; disable to omit it |
| `config.pollInterval` | `30m` | Requested minimum; request budget can increase cadence |
| `config.maxSnapshotAge` | `2h` | Cache freshness threshold for readiness and TRMNL |
| `config.dailyRequestBudget` | `90` | Process-local rolling upstream-attempt budget |
| `config.metricsEnabled` | `false` | Optional authenticated cached metrics |
| `config.metricsRoomDetails` | `false` | Opt into automatically discovered private room labels for Grafana gauges |
| `history.enabled` | `false` | Optional external PostgreSQL history |
| `history.existingSecret`, `history.databaseURLKey` | `tado-bridge-history`, `database-url` | Existing Secret containing the PostgreSQL URL |
| `history.retention` | `2160h` | Retention in hours (90 days by default) |
| `trmnl.enabled`, `trmnl.interval` | `false`, `30m` | Optional private-plugin publishing |
| `trmnl.rooms` | `[]` | Explicit home/zone IDs and display aliases; required when enabled |
| `secrets.trmnlWebhookKey` | `trmnl-webhook-url` | Secret key required only with TRMNL enabled |

Room selection IDs and aliases can be private even though they are not OAuth credentials; keep real values out of Git and shared rendered manifests. Helm stores supplied values in release metadata. TRMNL configuration still enforces official HTTPS destinations and explicit opt-in before sending selected climate data to its cloud.

The pod runs as nonroot, drops capabilities, disables privilege escalation and service-account token mounting, uses RuntimeDefault seccomp and has a read-only root filesystem. Only the credentials volume is writable. Inside the pod, binding on `0.0.0.0:8080` is explicitly acknowledged and always uses the Secret-backed API token. ClusterIP is an internal cluster interface, not loopback; use trusted networking and your cluster's policy controls. This chart does not terminate inbound TLS or configure egress/network policies. Optional custom CA and hostname overrides require trusted configuration because they affect outbound TLS trust and routing.

Startup/liveness probe `/healthz`; readiness probes `/readyz`. An upstream outage can make data stale and the pod unready without triggering restart loops. Resource defaults are starting points rather than measured sizing guarantees. Shutdown has 15 seconds to cancel/join requests and release credentials. The in-memory snapshot and rolling request-budget counter reset on restart; restarting does not reset tado's account-wide quota. Frequent restarts can discard pacing history, so keep headroom and observe actual account usage.

## PostgreSQL history and Grafana

Provision an external private PostgreSQL database with persistent storage and backups. Set `history.enabled=true` and select an existing Secret with the connection URL; the chart does not provision a database or additional history volume. Database initialization must succeed before HTTP serving starts. Runtime write/query failures do not affect cache readiness, and failed writes leave gaps. The application still runs one collector to protect OAuth rotation and request pacing. See [history configuration](../../docs/history.md#kubernetes) and the [Grafana template](../../integrations/grafana/README.md).

Optional daily summaries are served by the history API whenever `history.enabled` is true. For the TRMNL projection, additionally set `trmnl.historySummary: true` and optionally `trmnl.summaryTimezone` (default `UTC`); the template requires both history and TRMNL to be enabled. This explicitly opts into sending selected historical temperature summaries to TRMNL cloud. Grafana dashboards and native alert rules remain external to this chart; see the [Grafana integration](../../integrations/grafana/README.md).
