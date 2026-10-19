# Security policy

This project is pre-1.0. Security fixes target the latest code on `main`; older commits and versions are not maintained as separate release lines. Use the current patched stable Go toolchain and current dependency updates. CI runs race tests, static analysis, module verification and govulncheck; these checks do not replace careful configuration or review.

## Reporting

Use GitHub private vulnerability reporting if it is available for this repository. Otherwise open an issue requesting a private reporting channel without describing the vulnerability publicly. Never include credentials, device codes, API tokens, webhook URLs, account identifiers or raw home/room payloads in an issue, PR, workflow output or attachment.

## Operational boundaries

The bridge only reads upstream data. JSON handlers and metrics read the cache and cannot cause tado calls. No thermostat controls, outbound integrations, automatic public ingress, releases or deployments are enabled.

The binary defaults to loopback. A non-loopback bind requires explicit acknowledgement and bearer authentication. The Compose example binds the service to its container interface but publishes the host port only on `127.0.0.1`; API_TOKEN is required. Keep it local or use an operator-managed authenticated proxy/TLS boundary for remote clients. Health/readiness are unauthenticated internal endpoints with generic status only.

Metrics are disabled by default and share the data API's bearer protection when enabled. They omit account/home/room identifiers, display names, device serials, token values and error messages. Aggregate temperatures and humidity are still private climate data. Prometheus may persist them according to its own retention policy.

OAuth credentials are stored unencrypted with owner-only permissions. Mount the containing private directory, not an individual credentials file: token rotation uses same-directory temporary files and atomic rename. The service needs write access to that directory and holds its lock until shutdown. Use one canonical credentials path and do not let another integration rotate the same refresh token. A forced kill may leave a lock; verify no command is running before removing it.

The runtime image defaults to UID/GID 65532, has no shell and includes only the binary, runtime CA/system data and license. Compose drops capabilities, disallows privilege escalation and uses a read-only root filesystem. Only the credentials directory is writable. For bind mounts, run with a matching nonroot host UID/GID; otherwise credential reads/rotation will fail.

The rolling upstream budget is process-local. Restarts and other clients are not included in its history and do not reset tado's account quota. Cache freshness refers to collection time, not proof of a fresh sensor reading. Stale cached data remain accessible; clients must check timestamps/readiness.

Images and Actions are pinned to immutable digests/commit SHAs. Dependabot checks Go modules, Actions and Docker bases. CI builds and exercises containers without pushing them, using synthetic credentials and disabled runtime networking. There is no registry login, publish, tag or deployment workflow.
