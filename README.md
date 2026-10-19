# tado-bridge

Small, read-only Go CLI for tado° V3/V3+ and X. This repository implements M1 OAuth and home/zone discovery, plus an explicitly requested one-shot measurement command ahead of the M2 service. No server, polling, cache, metrics or thermostat controls yet.

## Prerequisites and local use

Go 1.27.2 or newer, a tado° account, internet access and a browser for authorization. The CLI uses [Cobra](https://github.com/spf13/cobra); provider and credential handling use the Go standard library. Both V3/V3+ and X cloud discovery are supported; local Matter access is outside this milestone.

Use the latest stable Go release and the latest stable versions of any dependencies when adding or updating them; verify current upstream releases and run the project checks after upgrades.

```sh
go build -o bin/tado-bridge ./cmd/tado-bridge
export TADO_CREDENTIALS_FILE="$HOME/.config/tado-bridge/credentials.json"
./bin/tado-bridge auth login
./bin/tado-bridge auth status
./bin/tado-bridge discover
./bin/tado-bridge read
```

Login prints a complete verification URL and user code to stderr. Open the entire URL (including its query parameters), enter the code if prompted and authorize with tado°. The URL includes the client ID and user code; opening the bare `/oauth2/device` URL can produce a `missing_client_id` error. The CLI waits at the upstream polling interval, handles pending/slow-down responses and stops at expiry or Ctrl-C. It never asks for your account password. Do not share the authorization URL, code or tokens.

Run `./bin/tado-bridge` or `./bin/tado-bridge auth` to see available commands. Every command supports `--help`/`-h`, for example `./bin/tado-bridge read --help` or `./bin/tado-bridge help auth login`. Help works without credentials and never contacts tado°. Operational commands still require `TADO_CREDENTIALS_FILE` and reject unexpected arguments before accessing credentials.

`discover` prints filtered JSON to stdout, with IDs as strings:

```json
{
  "provider": "tado",
  "homes": [{"id": "123", "name": "Example home", "zones": [{"id": "1", "name": "Living room"}]}]
}
```

Discovery automatically checks each home's generation: V3/V3+ uses the zones endpoint; X uses the rooms-and-devices endpoint. X rooms are exposed as `zones` with the same ID/name fields, including in accounts with mixed home generations. Only IDs and names are copied by `discover`; email, addresses, account metadata, device serials and credentials are excluded. Names and stable IDs are still private: take care when saving or sharing this output.

Request counts are printed separately to stderr, including on failure. A discovery with H homes makes `1 + 2*H` REST requests: one home list, then generation and zone/room discovery per home. Expired credentials add one OAuth refresh. There are no automatic API retries, including on 401 or 429. Correct an authorization problem with `auth login`; wait for quota recovery after 429. M2 will handle budgets, Retry-After and scheduled retries.

## One-shot measurements

`read` makes one read-only collection and prints a normalized snapshot to stdout:

```sh
./bin/tado-bridge read
```

Each zone includes `temperature_c`, `target_temperature_c`, `humidity_percent`, `heating_active` and `temperature_measured_at`. Unknown fields are explicit JSON `null`; zero and false remain valid readings. Temperature is Celsius and humidity is percent. A room configured as `OFF` has no active target, so `target_temperature_c` is `null` even if upstream retains an old setpoint. Heating activity reflects an explicit upstream heating-power percentage above zero, not a comparison of target and measured temperatures or confirmation that a boiler is firing.

`collected_at` is the UTC RFC3339 completion time of the entire collection. `temperature_measured_at` preserves a sensor timestamp only when upstream supplies it; it is `null` otherwise. Collection time does not prove sensor freshness, including for disconnected devices; this command does not infer freshness from a successful API response.

An X home takes three REST requests in total (`/me`, generation, all room states), regardless of room count. A V3/V3+ home with Z zones takes `3 + Z` REST requests (home list, generation, zone list, then one state request per zone). Mixed accounts cost `1 + 2*H + number of V3 zones`. An expired access token adds one OAuth refresh. Any failed request aborts the command without printing a partial snapshot. Nothing is cached and no background polling starts. Repeated executions consume upstream quota.

## Credentials

The credentials path is required explicitly. New parent directories use `0700`; files are written with `0600`, synced and atomically replaced in the same directory. Existing credential files must be regular files without group/other permissions; symlinks are rejected when loading. Choose a directory controlled only by your user, outside the repository, and exclude it from shared backups. Secrets are not encrypted at rest.

Access tokens are reused until within 30 seconds of their reported expiry. Rotated refresh credentials are saved before discovery starts. A credentials lock serializes commands that use the same path; use one canonical path and do not share the file with another integration. If a process is forcibly killed, verify no command is running before manually removing the `<credentials-file>.lock` directory. A failed save after upstream rotation can require a new login.

`auth status` checks the local file and expiry only; it does not contact tado° or prove that authorization remains valid. `auth logout` deletes local credentials only, without revoking upstream authorization:

```sh
./bin/tado-bridge auth logout
```

## Development and verification

```sh
go test ./...
go vet ./...
go test -race ./...
go mod verify
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
```

Tests use synthetic fixtures and local HTTP servers; no real credentials or upstream calls are required. If your environment restricts the default Go build cache, set `GOCACHE` to a writable directory.

GitHub Actions runs formatting checks, race tests, vet and module verification on pushes to `main` and pull requests, using the latest stable Go release. It also cross-compiles Linux and macOS binaries for amd64/arm64. A separate Zizmor workflow audits the Actions configuration. Actions are pinned by commit SHA; Dependabot checks Go modules and Actions weekly, without an update cooldown.

See [upstream verification](docs/authentication.md) for endpoints, sources, quota assumptions and live acceptance results. M1 has been merged; the one-shot readings extend the CLI while the periodic collector and local HTTP API remain pending for M2.
