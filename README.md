# tado-bridge

Small, read-only Go CLI for tado° V3/V3+ and X. This repository implements **M1 only** from the [design](docs/design/tado-bridge-design.md): OAuth device login and home/zone discovery. No server, polling, cache, metrics or thermostat controls yet.

## Prerequisites and local use

Go 1.27.2 or newer, a tado° account, internet access and a browser for authorization. No third-party Go dependencies. Both V3/V3+ and X cloud discovery are supported; local Matter access is outside this milestone.

Use the latest stable Go release and the latest stable versions of any dependencies when adding or updating them; verify current upstream releases and run the project checks after upgrades.

```sh
go build -o bin/tado-bridge ./cmd/tado-bridge
export TADO_CREDENTIALS_FILE="$HOME/.config/tado-bridge/credentials.json"
./bin/tado-bridge auth login
./bin/tado-bridge auth status
./bin/tado-bridge discover
```

Login prints a complete verification URL and user code to stderr. Open the entire URL (including its query parameters), enter the code if prompted and authorize with tado°. The URL includes the client ID and user code; opening the bare `/oauth2/device` URL can produce a `missing_client_id` error. The CLI waits at the upstream polling interval, handles pending/slow-down responses and stops at expiry or Ctrl-C. It never asks for your account password. Do not share the authorization URL, code or tokens.

`discover` prints filtered JSON to stdout, with IDs as strings:

```json
{
  "provider": "tado",
  "homes": [{"id": "123", "name": "Example home", "zones": [{"id": "1", "name": "Living room"}]}]
}
```

Discovery automatically checks each home's generation: V3/V3+ uses the zones endpoint; X uses the rooms-and-devices endpoint. X rooms are exposed as `zones` with the same ID/name fields, including in accounts with mixed home generations. Only IDs and names are copied from upstream; email, addresses, account metadata, device serials and credentials are excluded. Names and stable IDs are still private: take care when saving or sharing this output. Measurements and timestamps belong to the subsequent collection milestone.

Request counts are printed separately to stderr, including on failure. A discovery with H homes makes `1 + 2*H` REST requests: one home list, then generation and zone/room discovery per home. Expired credentials add one OAuth refresh. There are no automatic API retries, including on 401 or 429. Correct an authorization problem with `auth login`; wait for quota recovery after 429. M2 will handle budgets, Retry-After and scheduled retries.

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
```

Tests use synthetic fixtures and local HTTP servers; no real credentials or upstream calls are required. If your environment restricts the default Go build cache, set `GOCACHE` to a writable directory.

GitHub Actions runs formatting checks, race tests, vet and module verification on pushes to `main` and pull requests, using the latest stable Go release. It also cross-compiles Linux and macOS binaries for amd64/arm64. A separate Zizmor workflow audits the Actions configuration. Actions are pinned by commit SHA; Dependabot checks Go modules and Actions weekly, without an update cooldown.

See [upstream verification](docs/authentication.md) for endpoints, sources, quota assumptions and live acceptance results. Stop at M1: test login/discovery locally before committing the milestone or starting M2.
