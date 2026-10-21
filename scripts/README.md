# Packaging integration checks

These scripts run short integration checks against an **already built local image**. They are used by the GoReleaser snapshot CI job and can also be run locally. They are not needed to run `serve` or Compose normally, and they do not build binaries/images or push anything. The two smoke scripts use Docker only; the chart script below installs into a dedicated ephemeral Kind cluster.

The two checks cover different boundaries:

| Script | What it verifies | Make target |
|---|---|---|
| `container-smoke.sh` | Image declares UID/GID 65532:65532; its entrypoint supports help and liveness; the service runs with a read-only root, dropped capabilities and no privilege escalation; shutdown exits zero and releases its credential lock | `make image-smoke` |
| `compose-smoke.sh` | The actual `compose.yaml` starts the prebuilt image with valid synthetic configuration; its configured healthcheck becomes healthy; runtime networking and published ports are absent; the root is read-only; shutdown exits zero and releases its lock | `make compose-smoke` |

The image check isolates the packaged application from Compose configuration. The Compose check covers service arguments, environment interpolation, the directory mount and the healthcheck together. Keeping them here lets local development and CI use the same checks without duplicating shell code in workflows.

## Prerequisites

Run from the repository root, as your regular **nonroot** user, with Bash, a running local Docker daemon and access to the chosen image. The Compose check also requires Docker Compose V2 with `--wait` and the `!reset` override tag. The Docker daemon must be able to bind-mount the host temporary directory; a remote daemon does not automatically have access to it.

For GoReleaser's prebuilt snapshot:

```sh
make release-snapshot
make image-smoke compose-smoke IMAGE=ko.local:snapshot
```

For a local standalone ko image:

```sh
make image
make image-smoke compose-smoke
```

The default `IMAGE` is `ko.local/tado-bridge:local`. Both scripts also accept an explicit image when called directly:

```sh
IMAGE=ko.local:snapshot ./scripts/container-smoke.sh
IMAGE=ko.local:snapshot ./scripts/compose-smoke.sh
```

Use one of these existing build paths before checking the image; the scripts intentionally never rebuild it. CI calls them after its single GoReleaser build.

## Isolation and credential handling

Each run creates its own temporary directory containing synthetic access/refresh tokens in a mode-0600 file. The far-future expiry avoids an OAuth refresh during startup, and runtime networking is disabled before the application starts. No real credentials, webhook, home data or upstream connectivity are needed.

The image declares UID/GID 65532:65532. For these bind-mount tests, the runtime UID/GID is overridden to the nonroot host user's IDs so the private temporary directory remains accessible without weakening its permissions. This is why running the scripts as root is rejected.

The Compose script explicitly supplies synthetic settings, disables TRMNL, uses a unique project name and creates a temporary override that removes all published ports and sets `network_mode: none`. It also shortens the healthcheck interval to one second. This allows it to run alongside a native day-long test without sharing credentials or competing for port 8080. The container script similarly uses a unique name, no published ports and `--network none`.

Because no successful upstream collection can occur, `/readyz` would remain unavailable. These checks deliberately exercise **liveness**, configuration and lifecycle; collection, cached data, retries and TRMNL contracts are covered by Go tests. A healthy result does not prove that real tado credentials or a TRMNL plugin work.

## Completion and failures

Both scripts exit zero after their assertions pass and print a success summary. Any failed command/assertion produces a nonzero exit status. The container script polls liveness up to 30 times with one-second pauses and prints application logs on failure. Compose waits up to 30 seconds for its configured healthcheck. Docker/Compose and individual probe startup overhead can add to those waits.

Successful shutdown gets ten seconds, must return application exit code zero and must remove `credentials.json.lock`. An `EXIT` trap then removes only the test's own container/project and temporary files. Cleanup is best-effort if the daemon becomes unavailable, and abrupt termination such as SIGKILL cannot run the trap.

For failures, first check that the daemon is running, the image is present, your host user is nonroot and Docker can mount your temporary directory. Check Compose version/support for `!reset` if override parsing fails. If interruption leaves resources behind, inspect their exact `tado-bridge-smoke-<pid>` container or `tado-bridge-compose-smoke-<pid>` project name before removing them; do not stop unrelated workloads or remove your real credential lock.

Normal operation and migration instructions are in [container setup](../docs/containers.md); build outputs are described in [build tooling](../docs/releases.md).

## Chart deployment integration

`make chart-integration` (or `./scripts/chart-integration.sh`) uses the same GoReleaser snapshots to install the chart through `ct install`. It requires Docker, Kind, kubectl, Helm and chart-testing. A dedicated Helm job downloads the prebuilt images from the GoReleaser job using a same-run artifact retained for one day. CI tool selection is defined in the [workflows](../.github/workflows); local prerequisites are described in [build tooling](../docs/releases.md).

Static test settings live in `charts/tado-bridge/ci/integration-values.yaml`, automatically discovered by chart-testing for lint and install. The script generates `.local/helm-ci-values.yaml` only for `hostAliases`, whose Service IP is allocated at runtime. Neither file contains real credentials. Additional `ci/*-values.yaml` files create additional scenarios; the current post-install checks and fixed release name assume one scenario, so extend the runner's per-scenario checks/cleanup before adding more.

Only run against a dedicated disposable cluster. For example:

```sh
make release-snapshot
export KUBECONFIG="$(mktemp)"
kind create cluster --name tado-bridge-ci --config .github/helm/kind.yaml
make chart-integration
kind delete cluster --name tado-bridge-ci
rm -f "$KUBECONFIG"
unset KUBECONFIG
```

The script requires the current context to match `kind-${KIND_CLUSTER_NAME:-tado-bridge-ci}` and refuses an existing `tado-bridge-ci` namespace. A matching name is a guard against mistakes, not proof that a cluster is disposable. CI creates and removes its own Kind cluster; local operators own cluster cleanup.

The script loads `ko.local:snapshot` and the snapshot-only `ko.local:fixture` image, generates a one-day test CA, and creates synthetic Secrets, a PVC and an HTTPS fake tado service. Pod host aliases route all three tado hosts to that service; only the public CA certificate is mounted into the application. Production HTTP-client behavior and `/readyz` probes remain enabled. There are no real credentials or published host ports.

It verifies successful V3/X collection (including eight X rooms and zero/null values), API authentication, cached responses, authenticated metrics and OAuth rotation into the PVC. The first collection must include an OAuth attempt; cold-start transport retries can increase this counter. It then restarts the application and requires exactly zero OAuth attempts while verifying readiness with the persisted token. The reusable fake is documented in [test utilities](../internal/testutil/tadofake/README.md).

On failure, the script prints fixture pod status and bounded application/fake logs before cleanup. An exit trap deletes the fixture namespace, temporary TLS material and ignored `.local/helm-ci-values.yaml`. Namespace deletion is asynchronous; interrupted runs can need manual cleanup of that exact namespace in the dedicated test cluster. The script never deletes the cluster itself and must never target a cluster containing real workloads.
