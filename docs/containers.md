# Local container operation

The default local build uses ko with Go 1.27+ and the public [Chainguard static image](https://images.chainguard.dev/directory/image/static/overview), referenced as `cgr.dev/chainguard/static:latest` and resolved at build time. Both Linux amd64/arm64 are supported. The image runs as UID/GID 65532:65532. GoReleaser snapshots build binaries and images through its native ko integration; see [build tooling and release policy](releases.md). Images are built from the Go binary with ko; no Docker build context is used.

## Compose

Run these commands as your regular nonroot user. The example expects `credentials.json` in an existing private directory. If you already logged in with this path, reuse it and stop the host service before starting the container.

```sh
export TADO_CREDENTIALS_FILE="$HOME/.config/tado-bridge/credentials.json"
# Only if you have not logged in yet, with the native service stopped:
# ./bin/tado-bridge auth login

export TADO_CREDENTIALS_DIR="$HOME/.config/tado-bridge"
export TADO_BRIDGE_UID="$(id -u)"
export TADO_BRIDGE_GID="$(id -g)"
export API_TOKEN="$(openssl rand -hex 32)"

make compose-up
```

`make compose-up` validates configuration, builds the image with ko and then starts Compose. `make compose-down` stops it. To run these steps separately, use `make image`, `docker compose config --quiet` and `docker compose up`, in that order.

Compose consumes the already built `ko.local/tado-bridge:local` image, passes `serve` explicitly and never pulls an image automatically. `TADO_BRIDGE_IMAGE` can select a different image already present locally. Stop any native service before using its credential directory in Compose. Packaging checks use separate synthetic credentials and can run alongside a real instance.

Use the same API_TOKEN in your clients; keep it private. It is passed only as runtime configuration, never as a build argument or image layer. Compose config output can contain environment values, so use `--quiet` when validating it and do not paste expanded configuration into issues.

The root filesystem is read-only. The whole credentials directory is mounted read/write, allowing atomic refresh-token rotation and locking. Do not mount only the file, copy the token into the image, or share it with another running process. Matching the host UID/GID preserves existing `0600` permissions and lets the container rotate tokens. The image itself defaults to nonroot UID/GID 65532 when no Compose override is supplied.

Inside the container, LISTEN_ADDR is `0.0.0.0:8080` and the network acknowledgement is explicit, but the host port is published only on `127.0.0.1:8080`. API_TOKEN is required, including for metrics when enabled. Compose publishes no public interface and never pushes to a registry or deploys to a cluster. Collection and quota settings are the same as the standalone binary. Set METRICS_ENABLED=true before startup to enable metrics.

The Compose healthcheck calls `/ko-app/tado-bridge healthcheck`, which checks only `/healthz` with a two-second timeout and does not load credentials or call tado. A healthy container may still have stale data or an upstream failure; use `/readyz` to check collection readiness. Stop with Ctrl-C or `docker compose down`; Compose allows ten seconds for graceful shutdown.

## Local image checks

See [smoke-check scripts](../scripts/README.md) for prerequisites, what each check covers, temporary resource cleanup and troubleshooting. They use prebuilt images and are not required for normal operation.

```sh
make image
make image-smoke
make compose-smoke
make image-multiarch
```

`image` builds `ko.local/tado-bridge:local` with ko without pushing. `image-smoke` verifies the declared nonroot user and starts the service with a read-only root, a temporary writable credentials directory and **runtime networking disabled**. It uses synthetic tokens, checks local liveness, stops the container and verifies exit code/lock cleanup. It does not publish ports or contact tado. Run it as a nonroot user so bind-mount ownership matches the tested nonroot runtime.

`compose-smoke` starts an isolated Compose project with synthetic credentials, no network and no published ports. It checks the configured healthcheck and lock cleanup on shutdown without using your real service.

`image-multiarch` writes Linux amd64/arm64 to the ignored `dist/oci` layout with pushing disabled. CI builds binaries and both Linux image variants through one GoReleaser snapshot job, then runs the container and Compose smoke scripts on the snapshot loaded into its local daemon. Runtime CI coverage is the loaded amd64 image on the Ubuntu runner; both architectures are built, and arm64 runtime was also validated locally. Only the two snapshot images are transferred as a one-day workflow artifact to the separate Helm integration job; product archives are not uploaded. Go-module/Action updates are proposed by Dependabot; keep the public base reference aligned in `.ko.yaml` and `.goreleaser.yaml`.

The separate version-tag release workflow publishes `ghcr.io/cpanato/tado-bridge:<tag>` for Linux amd64/arm64; stable versions also update `latest`. See [release operation](releases.md). Compose defaults remain local, and its `pull_policy: never` requires an image to be built or pulled separately before use.

For Kubernetes packaging, see the [Helm chart](../charts/tado-bridge/README.md). It requires a preinitialized writable credentials PVC and existing token Secret, fixes one replica with Recreate updates and exposes only a ClusterIP Service. No cluster deployment is performed by the local build commands.
