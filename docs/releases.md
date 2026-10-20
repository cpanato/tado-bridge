# Build and release policy

Merging a PR does not publish a release, create a Git tag, push a container or deploy the service. CI builds **local snapshots only**, following the ko integration reviewed in [github-actions-dashboard](https://github.com/cpanato/github-actions-dashboard). GoReleaser is configured to publish an explicitly authorized tagged binary release to `cpanato/tado-bridge` with native GitHub release notes. CI has no publishing trigger or release write permission; container registry publication remains unconfigured. Helm packages stay local too.

## Local builds

Versions verified against official upstream sources on 2026-10-20:

| Tool | Stable version | Source |
|---|---|---|
| Go | 1.27.3 | [Official download feed](https://go.dev/dl/?mode=json) |
| GoReleaser | 2.18.3 | [Official release](https://github.com/goreleaser/goreleaser/releases/tag/v2.18.3) |
| ko | 0.19.1 | [Official release](https://github.com/ko-build/ko/releases/tag/v0.19.1) |
| Helm | 4.3.1 | [Official release](https://github.com/helm/helm/releases/tag/v4.3.1) |
| chart-testing | 3.15.0 | [Official release](https://github.com/helm/chart-testing/releases/tag/v3.15.0) |
| Kind | 0.33.0 | [Official release](https://github.com/kubernetes-sigs/kind/releases/tag/v0.33.0) |
| kubectl | 1.37.1 | [Official releases](https://kubernetes.io/releases/) |
| yamllint | 1.38.0 | [PyPI](https://pypi.org/project/yamllint/1.38.0/) |
| Yamale | 6.1.0 | [PyPI](https://pypi.org/project/yamale/6.1.0/) |

```sh
make release-check
make release-snapshot
make image
make image-smoke
make image-multiarch
make helm-check helm-package
```

GoReleaser emits Linux/macOS amd64/arm64 binaries, tar archives with LICENSE/README and checksums under ignored `dist/`. Its built-in [ko integration](https://goreleaser.com/customization/package/ko/) builds Linux amd64/arm64 image variants and loads a snapshot image into the local Docker daemon as **`ko.local:snapshot`**. The daemon-local snapshot selects an image variant; use `make image-multiarch` for a multi-platform OCI layout. Snapshot mode with `--skip=publish,sign` prevents GitHub release publication; local-only ko repositories/domain keep container builds in the local daemon. GoReleaser telemetry is not configured; its optional Pro tracing is outside this build setup.

`make image` uses standalone ko to load **`ko.local/tado-bridge:local`** for the host architecture; `PLATFORM=linux/amd64` or `linux/arm64` selects another architecture. Both ko and GoReleaser use `CGO_ENABLED=0`, trim paths, run as 65532:65532 and use the same `cgr.dev/chainguard/static:latest` base. `make image-multiarch` uses `--push=false` with an intentionally nonresolvable destination and writes both architectures under `dist/oci`; it needs no registry credentials or push. Docker must be running for daemon-local image/snapshot targets. A source build can download modules and pull the public base, but runtime smoke tests disable networking and use only synthetic credentials.

Snapshots also build `tadofake` and load **`ko.local:fixture`** for deployment tests. This helper is disabled for non-snapshot builds and excluded from product archives; it is not a production service. The HTTP handler also runs directly in Go integration tests. See [test utilities](../internal/testutil/tadofake/README.md) and [script instructions](../scripts/README.md).

All supported image build targets set the OCI label `org.opencontainers.image.created` to the build execution time in UTC RFC3339. GoReleaser uses its `.Date` template; standalone ko Make targets pass `BUILD_DATE` (defaults to the current UTC time). Set `BUILD_DATE=2026-10-20T12:00:00Z` when a fixed label is required for those Make targets. This label describes build time rather than commit time.

Container entrypoints are `/ko-app/tado-bridge`; Compose and Helm pass `serve` explicitly because standalone ko does not add a default command. Images are built exclusively with ko, directly or through GoReleaser. No source files, credential directories or `.local` logs are copied: ko packages the compiled binary and the main package's optional `kodata`, which this project does not use. `make compose-up` validates configuration and completes the ko build before starting Compose; `IMAGE` or `TADO_BRIDGE_IMAGE` selects the same local tag for both steps.

CI uses a single GoReleaser snapshot job for every binary and container build; it has no separate ko/container or cross-build jobs. The resulting daemon-local snapshot is reused by container and Compose smoke tests without rebuilding. Both Linux image variants are built, while runtime CI smoke coverage uses the loaded amd64 image on the Ubuntu runner. A dedicated Helm job depends on the GoReleaser job and downloads its two snapshot images through a same-run artifact with one-day retention; no rebuild or registry push is needed. It validates/packages Helm and uses chart-testing-action with Kind to install against synthetic HTTPS APIs, verify readiness and token persistence, then restart the application. The test cluster exposes no application ports to the host and uses no real credentials. Jobs use `contents: read`, SHA-pinned actions, no publication trigger, no release artifacts and no external cluster credentials or registry write permissions. CI uses Go `stable` with `check-latest: true`. Keep `go.mod` updated against the official Go download feed and ensure the Actions stable alias satisfies its required patch. GoReleaser follows the latest stable release in CI; the tool versions above record local verification. Its embedded ko version is controlled by that release, while standalone ko remains available through local Make targets. Keep the base-image reference in `.ko.yaml` and `.goreleaser.yaml` synchronized. The `latest` tag is resolved at build time. Dependabot tracks Go modules and Actions weekly with a seven-day cooldown for version updates; security updates are exempt from cooldown.

## GitHub release notes

`changelog.use: github-native` delegates release notes to GitHub's release-notes generation API. GoReleaser publishes binary archives and checksums to `cpanato/tado-bridge` only when an authorized operator runs a tagged release with a suitable `GITHUB_TOKEN`. The fixture helper remains snapshot-only, and container builds still use local ko destinations. There is no tag-triggered publishing workflow in this change.

GitHub controls the generated notes and supports optional repository customization through `.github/release.yml`; no separate GoReleaser changelog groups, sorting or filters are configured. See [GoReleaser's native changelog documentation](https://goreleaser.com/customization/publish/changelog/).

## Publishing authorization

Every release, Git tag, remote registry push or deployment requires an explicit operator instruction naming the intended action and target. No publishing workflow is currently configured. Before adding one, review the intended commit, SemVer version, changelog, registry namespace, signing/provenance requirements, supported configurations and passing CI. Verify Linux amd64/arm64 behavior and current Go/dependency/base updates. Recheck source and artifact contents for credentials/private home data and document remaining acceptance limits. This repository is pre-1.0; API changes must be called out clearly.

Git commits can use gitsign with the operator's selected certificate identity. That identity is separate from Git author/committer metadata. Do not change author or committer identities to match a signing certificate. Rewriting a published branch requires authorization and `--force-with-lease`.
