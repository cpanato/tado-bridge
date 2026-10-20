#!/usr/bin/env bash
# Check the prebuilt image runtime and graceful shutdown in isolation.
# Run from the repository root; IMAGE selects a local image (no build/push).
# Prerequisites, isolation and troubleshooting: scripts/README.md.
set -euo pipefail

# Match private bind-mount ownership without weakening permissions or using root.
if [ "$(id -u)" -eq 0 ]; then
  printf 'Run the container smoke test as a nonroot host user.\n' >&2
  exit 1
fi

image="${IMAGE:-ko.local/tado-bridge:local}"
container="tado-bridge-smoke-$$"
fixture_dir="$(mktemp -d)"
# Cleanup is restricted to this invocation's uniquely named test resources.
cleanup() {
  docker rm --force "$container" >/dev/null 2>&1 || true
  rm -rf "$fixture_dir"
}
trap cleanup EXIT

# Synthetic tokens only. Networking is disabled for the entire runtime test.
printf '%s\n' '{"access_token":"fixture-access","refresh_token":"fixture-refresh","expires_at":"2999-01-01T00:00:00Z"}' > "$fixture_dir/credentials.json"
chmod 600 "$fixture_dir/credentials.json"

test "$(docker image inspect --format '{{.Config.User}}' "$image")" = '65532:65532'
entrypoint="$(docker image inspect --format '{{index .Config.Entrypoint 0}}' "$image")"
docker run --rm --read-only --network none "$image" --help >/dev/null
docker run --detach --name "$container" \
  --read-only --network none \
  --cap-drop ALL --security-opt no-new-privileges=true \
  --user "$(id -u):$(id -g)" \
  --mount "type=bind,src=$fixture_dir,dst=/var/lib/tado-bridge" \
  --env TADO_CREDENTIALS_FILE=/var/lib/tado-bridge/credentials.json \
  "$image" serve >/dev/null

healthy=false
for _ in {1..30}; do
  if docker exec "$container" "$entrypoint" healthcheck >/dev/null 2>&1; then
    healthy=true
    break
  fi
  sleep 1
done
if [ "$healthy" != true ]; then
  docker logs "$container"
  exit 1
fi

docker stop --time 10 "$container" >/dev/null
test "$(docker inspect --format '{{.State.ExitCode}}' "$container")" = 0
test ! -e "$fixture_dir/credentials.json.lock"
printf 'Container liveness, nonroot/read-only runtime and shutdown passed.\n'
