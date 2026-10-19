# Local container operation

The Dockerfile builds a static Linux binary with Go 1.27.2 and runs it in the supported distroless Debian 13 nonroot image. Both bases are pinned by multi-architecture digest. Their sources were checked against [Docker's official Go image metadata](https://github.com/docker-library/official-images/blob/master/library/golang) and [distroless's supported images](https://github.com/GoogleContainerTools/distroless). Only Go source, module files and LICENSE enter the build context; credentials, `.git`, local binaries and documentation are excluded by the allowlist in `.dockerignore`.

## Compose

Run these commands as your regular nonroot user. The example expects `credentials.json` in an existing private directory. If you already logged in with this path, reuse it and stop the host service before starting the container.

```sh
make build
export TADO_CREDENTIALS_FILE="$HOME/.config/tado-bridge/credentials.json"
./bin/tado-bridge auth login

export TADO_CREDENTIALS_DIR="$HOME/.config/tado-bridge"
export TADO_BRIDGE_UID="$(id -u)"
export TADO_BRIDGE_GID="$(id -g)"
export API_TOKEN="$(openssl rand -hex 32)"

docker compose config --quiet
docker compose up --build
```

Use the same API_TOKEN in your clients; keep it private. It is passed only as runtime configuration, never as a build argument or image layer. Compose config output can contain environment values, so use `--quiet` when validating it and do not paste expanded configuration into issues.

The root filesystem is read-only. The whole credentials directory is mounted read/write, allowing atomic refresh-token rotation and locking. Do not mount only the file, copy the token into the image, or share it with another running process. Matching the host UID/GID preserves existing `0600` permissions and lets the container rotate tokens. The image itself defaults to nonroot UID/GID 65532 when no Compose override is supplied.

Inside the container, LISTEN_ADDR is `0.0.0.0:8080` and the network acknowledgement is explicit, but the host port is published only on `127.0.0.1:8080`. API_TOKEN is required, including for metrics when enabled. No public interface, registry or deployment is configured. Collection and quota settings are the same as the standalone binary. Set METRICS_ENABLED=true before startup to enable metrics.

The image healthcheck calls `/tado-bridge healthcheck`, which checks only `/healthz` with a two-second timeout and does not load credentials or call tado. A healthy container may still have stale data or an upstream failure; use `/readyz` to check collection readiness. Stop with Ctrl-C or `docker compose down`; Compose allows ten seconds for graceful shutdown.

## Local image checks

```sh
make image
make image-smoke
make image-multiarch
```

`image` builds `tado-bridge:local` without pushing. `image-smoke` verifies the declared nonroot user and starts the service with a read-only root, a temporary writable credentials directory and **runtime networking disabled**. It uses synthetic tokens, checks local liveness, stops the container and verifies exit code/lock cleanup. It does not publish ports or contact tado. Run it as a nonroot user so bind-mount ownership matches the tested nonroot runtime.

`image-multiarch` cross-builds Linux amd64/arm64 to BuildKit's local cache only. It neither loads a release image nor publishes anything. CI separately builds and loads each architecture and runs the same smoke script, using QEMU where needed. Image build records/artifacts are not uploaded. Docker-base updates are proposed by Dependabot for review.
