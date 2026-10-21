# Build and release policy

Merging a PR does not publish a release, create a Git tag, push a container or deploy the service. CI builds **local snapshots only**, following the ko integration reviewed in [github-actions-dashboard](https://github.com/cpanato/github-actions-dashboard). GoReleaser is configured to publish an explicitly authorized tagged release to `cpanato/tado-bridge` with native GitHub release notes. The separate `release` workflow publishes only on an operator-pushed `v*` tag; regular CI has no release write permission. That release also publishes the Linux amd64/arm64 image to `ghcr.io/cpanato/tado-bridge`. Helm packages stay local too.

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

GoReleaser emits Linux/macOS amd64/arm64 binaries, tar archives with LICENSE/README and checksums under ignored `dist/`. Its built-in [ko integration](https://goreleaser.com/customization/package/ko/) builds Linux amd64/arm64 image variants and loads a snapshot image into the local Docker daemon as **`ko.local:snapshot`**. The daemon-local snapshot selects an image variant; use `make image-multiarch` for a multi-platform OCI layout. Snapshot mode with `--skip=publish,sign` prevents GitHub release publication; ko uses `local_domain: ko.local` for snapshots, keeping those image builds in the local daemon even though tagged releases target GHCR. GoReleaser telemetry is not configured; its optional Pro tracing is outside this build setup.

`make image` uses standalone ko to load **`ko.local/tado-bridge:local`** for the host architecture; `PLATFORM=linux/amd64` or `linux/arm64` selects another architecture. Both ko and GoReleaser use `CGO_ENABLED=0`, trim paths, run as 65532:65532 and use the same `cgr.dev/chainguard/static:latest` base. `make image-multiarch` uses `--push=false` with an intentionally nonresolvable destination and writes both architectures under `dist/oci`; it needs no registry credentials or push. Docker must be running for daemon-local image/snapshot targets. A source build can download modules and pull the public base, but runtime smoke tests disable networking and use only synthetic credentials.

Snapshots also build `tadofake` and load **`ko.local:fixture`** for deployment tests. This helper is disabled for non-snapshot builds and excluded from product archives; it is not a production service. The HTTP handler also runs directly in Go integration tests. See [test utilities](../internal/testutil/tadofake/README.md) and [script instructions](../scripts/README.md).

All supported image build targets set the OCI label `org.opencontainers.image.created` to the build execution time in UTC RFC3339. GoReleaser uses its `.Date` template; standalone ko Make targets pass `BUILD_DATE` (defaults to the current UTC time). Set `BUILD_DATE=2026-10-20T12:00:00Z` when a fixed label is required for those Make targets. This label describes build time rather than commit time.

Container entrypoints are `/ko-app/tado-bridge`; Compose and Helm pass `serve` explicitly because standalone ko does not add a default command. Images are built exclusively with ko, directly or through GoReleaser. No source files, credential directories or `.local` logs are copied: ko packages the compiled binary and the main package's optional `kodata`, which this project does not use. `make compose-up` validates configuration and completes the ko build before starting Compose; `IMAGE` or `TADO_BRIDGE_IMAGE` selects the same local tag for both steps.

CI uses a single GoReleaser snapshot job for every binary and container build; it has no separate ko/container or cross-build jobs. The resulting daemon-local snapshot is reused by container and Compose smoke tests without rebuilding. Both Linux image variants are built, while runtime CI smoke coverage uses the loaded amd64 image on the Ubuntu runner. A dedicated Helm job depends on the GoReleaser job and downloads its two snapshot images through a same-run artifact with one-day retention; no rebuild or registry push is needed. It validates/packages Helm and uses chart-testing-action with Kind to install against synthetic HTTPS APIs, verify readiness and token persistence, then restart the application. The test cluster exposes no application ports to the host and uses no real credentials. Jobs use `contents: read`, SHA-pinned actions, no publication trigger, no release artifacts and no external cluster credentials or registry write permissions in the snapshot jobs. CI uses Go `stable` with `check-latest: true`; the Helm job uses the required version from `go.mod`. Keep `go.mod` updated against the official Go download feed and ensure the Actions stable alias satisfies its required patch. GoReleaser follows the latest stable release in CI; the tool versions above record local verification. Its embedded ko version is controlled by that release, while standalone ko remains available through local Make targets. Keep the base-image reference in `.ko.yaml` and `.goreleaser.yaml` synchronized. The `latest` tag is resolved at build time. Dependabot tracks Go modules and Actions weekly with a seven-day cooldown for version updates; security updates are exempt from cooldown.

## GitHub release notes

`changelog.use: github-native` delegates release notes to GitHub's release-notes generation API. GoReleaser publishes binary archives and checksums to `cpanato/tado-bridge` only when an authorized operator runs a tagged release with a suitable `GITHUB_TOKEN`. The fixture helper remains snapshot-only. The release workflow authenticates to GHCR using `GITHUB_TOKEN` and publishes the Linux amd64/arm64 image through GoReleaser’s ko integration. The image receives the exact version tag (for example `v1.2.3`); stable releases also update `latest`, while prereleases do not. Snapshot CI continues using only `ko.local:snapshot` and `ko.local:fixture` and does not log into GHCR.

GitHub controls the generated notes and supports optional repository customization through `.github/release.yml`; no separate GoReleaser changelog groups, sorting or filters are configured. See [GoReleaser's native changelog documentation](https://goreleaser.com/customization/publish/changelog/).

## Cosign signatures

The packaging CI job and release job install cosign using `sigstore/cosign-installer` with no inputs or overrides. The release job obtains its signing identity through GitHub OIDC; there is no stored signing key or password and no custom Sigstore configuration. Snapshot CI keeps `--skip=publish,sign`, so pull requests do not request signing identities, contact Sigstore for signing or upload image signatures.

GoReleaser signs each application binary before archiving and publishes its verification bundle as `tado-bridge_<os>_<arch>.sigstore.json`. Only build ID `binary` is selected; the synthetic helper is excluded. It also signs `checksums.txt`, producing `checksums.txt.sigstore.json`. These bundles contain the signature, signing certificate and transparency verification material. The tar archives remain covered by the signed checksum list.

The ko multi-platform image manifest is signed at its immutable digest, and cosign stores that signature in GHCR. Signing runs during the real tagged release; a local snapshot build does not prove that GitHub OIDC or registry signing will succeed.

After downloading the release assets, verify the checksum bundle against the exact workflow/tag identity before checking archive hashes:

```sh
version=v1.2.3 # Replace with the release tag you downloaded.
identity="https://github.com/cpanato/tado-bridge/.github/workflows/release.yaml@refs/tags/${version}"
issuer=https://token.actions.githubusercontent.com

cosign verify-blob --bundle checksums.txt.sigstore.json \
  --certificate-identity "$identity" --certificate-oidc-issuer "$issuer" \
  checksums.txt
sha256sum --check --ignore-missing checksums.txt

# Optional: verify the extracted Linux amd64 executable directly.
cosign verify-blob --bundle tado-bridge_linux_amd64.sigstore.json \
  --certificate-identity "$identity" --certificate-oidc-issuer "$issuer" \
  ./tado-bridge

# Replace <digest> with the full sha256 digest of the published image manifest.
cosign verify --certificate-identity "$identity" \
  --certificate-oidc-issuer "$issuer" \
  'ghcr.io/cpanato/tado-bridge@sha256:<digest>'
```

See the official [GoReleaser cosign integration](https://goreleaser.com/customization/sign/sign/#signing-with-cosign) and [installer defaults](https://github.com/sigstore/cosign-installer).

## Publishing authorization

The `.github/workflows/release.yaml` workflow runs when an operator pushes a `v*` tag to `cpanato/tado-bridge`. It checks out the full history, installs stable Go and cosign, validates GoReleaser configuration and publishes the tagged GitHub release and GHCR image with the repository-provided `GITHUB_TOKEN`. Only its release job has `contents: write` and `packages: write`; it also has `id-token: write` for keyless cosign signing and no deployment permission. Concurrent runs for the same tag are serialized without cancelling an active publication. GoReleaser validates the version tag; prerelease versions are marked as GitHub prereleases.

Before pushing a release tag, select an authorized version and a merged commit with passing CI. Create/sign the SemVer tag with the operator’s configured identity, then push that specific tag to trigger publication. A merge or ordinary branch push does not publish. Re-running the workflow can modify release artifacts and image tags, so review a failed run before retrying.

Every release, Git tag, remote registry push or deployment requires an explicit operator instruction naming the intended action and target. Adding this workflow does not create or push a tag. Review the intended commit, SemVer version, changelog, registry namespace, signing/provenance requirements, supported configurations and passing CI. Verify Linux amd64/arm64 behavior and current Go/dependency/base updates. Recheck source and artifact contents for credentials/private home data and document remaining acceptance limits. This repository is pre-1.0; API changes must be called out clearly.

Git commits can use gitsign with the operator's selected certificate identity. That identity is separate from Git author/committer metadata. Do not change author or committer identities to match a signing certificate. Rewriting a published branch requires authorization and `--force-with-lease`.
