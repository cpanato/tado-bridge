#!/usr/bin/env bash
# Check compose.yaml against a prebuilt image with synthetic configuration.
# Run from the repository root; IMAGE selects a local image (no build/push).
# Prerequisites, isolation and troubleshooting: scripts/README.md.
set -euo pipefail

# Match private bind-mount ownership without weakening permissions or using root.
if [ "$(id -u)" -eq 0 ]; then
  printf 'Run the Compose smoke test as a nonroot host user.\n' >&2
  exit 1
fi

fixture_dir="$(mktemp -d)"
project="tado-bridge-compose-smoke-$$"
# Override runtime settings so the caller's real tokens/webhook are never used.
export TADO_CREDENTIALS_DIR="$fixture_dir"
export TADO_BRIDGE_UID="$(id -u)"
export TADO_BRIDGE_GID="$(id -g)"
export TADO_BRIDGE_IMAGE="${IMAGE:-ko.local/tado-bridge:local}"
export API_TOKEN=synthetic-compose-token
export TADO_BRIDGE_PORT=8080
export POLL_INTERVAL=30m
export MAX_SNAPSHOT_AGE=2h
export TADO_DAILY_REQUEST_BUDGET=90
export METRICS_ENABLED=false
export TRMNL_INTERVAL=30m
export TRMNL_ENABLED=false
export TRMNL_WEBHOOK_URL=
export TRMNL_ROOMS=
compose=(docker compose --project-name "$project" -f compose.yaml -f "$fixture_dir/override.yaml")
# Cleanup is restricted to this invocation's uniquely named test resources.
cleanup() {
  "${compose[@]}" down --timeout 10 >/dev/null 2>&1 || true
  rm -rf "$fixture_dir"
}
trap cleanup EXIT

printf '%s\n' '{"access_token":"fixture-access","refresh_token":"fixture-refresh","expires_at":"2999-01-01T00:00:00Z"}' > "$fixture_dir/credentials.json"
chmod 600 "$fixture_dir/credentials.json"
# Exercise the real Compose configuration without host ports or upstream access.
cat > "$fixture_dir/override.yaml" <<'YAML'
services:
  tado-bridge:
    ports: !reset []
    network_mode: none
    healthcheck:
      interval: 1s
      start_period: 0s
YAML

"${compose[@]}" config --quiet
"${compose[@]}" up --detach --wait --wait-timeout 30
container="$("${compose[@]}" ps --quiet tado-bridge)"
test "$(docker inspect --format '{{.State.Health.Status}}' "$container")" = healthy
test "$(docker inspect --format '{{.HostConfig.NetworkMode}}' "$container")" = none
test "$(docker inspect --format '{{len .HostConfig.PortBindings}}' "$container")" = 0
test "$(docker inspect --format '{{.HostConfig.ReadonlyRootfs}}' "$container")" = true
"${compose[@]}" stop --timeout 10 >/dev/null
test "$(docker inspect --format '{{.State.ExitCode}}' "$container")" = 0
test ! -e "$fixture_dir/credentials.json.lock"
printf 'Compose healthcheck, isolated runtime and shutdown passed.\n'
