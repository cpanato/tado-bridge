# Release policy

This repository is pre-1.0. Merging a PR does not publish a release, create a Git tag, push a container or deploy the service. CI tests source and builds images only on its temporary runner. Local image names such as `tado-bridge:local` are for development and are not registry releases.

Every release, Git tag, registry push or deployment requires an explicit operator instruction naming the intended action and target. No automated publishing workflow is configured. Release tooling or registry credentials should not be added implicitly as part of routine CI changes.

Before an authorized release, review the intended commit, changelog, supported configurations and passing CI. Verify Linux amd64/arm64 behavior and current Go/dependency/base-image updates. Recheck source and image contents for credentials and private home data. Document versions/digests and any remaining live-acceptance limitations. Versioning will follow SemVer; pre-1.0 API changes must be called out clearly.

Commits may be signed with gitsign using the operator's selected certificate identity. Certificate identity is separate from Git author/committer metadata. Do not change author or committer identities to match a signing certificate. Rewriting a published branch requires operator authorization and a push protected with `--force-with-lease`.
