# Releasing

Releases are fully driven by immutable semantic-version tags. The workflow
publishes both GitHub Release assets and the corresponding GHCR image.

## Before tagging

1. Confirm the intended commit is on `main` and the CI workflow is green.
2. Confirm the working tree is clean.
3. Choose a tag matching `vMAJOR.MINOR.PATCH`. Pre-release identifiers such as
   `v1.2.0-rc.1` are supported.

GitHub creates the first GHCR package as private. During the first release only,
wait for the image job to push it, change the package visibility to **Public**
in [GitHub package settings](https://github.com/orgs/mss-boot-ai/packages/container/package/s3-copy-gateway/settings),
and rerun the failed workflow jobs. The anonymous image smoke tests
intentionally prevent the GitHub Release from publishing until this is done.
Package visibility remains public for later releases.

## Publish

Create and push an annotated tag:

```bash
git switch main
git pull --ff-only
git tag -a v1.2.3 -m "Release v1.2.3"
git push origin v1.2.3
```

The `Release` GitHub Actions workflow then:

1. Validates the tag and reruns tests, race detection, static analysis, and
   vulnerability scanning.
2. Builds deterministic Linux `amd64` and `arm64` archives.
3. Publishes a multi-architecture image to
   `ghcr.io/mss-boot-ai/s3-copy-gateway` with provenance and an SBOM.
4. Pulls both image architectures anonymously and verifies startup, health,
   hardening, and embedded version metadata.
5. Creates `SHA256SUMS`, verifies every asset, and publishes the GitHub Release
   with tag-based and immutable-digest image pull instructions.

Every release receives only exact Git-tag and full semantic-version image tags,
for example `v1.2.3` and `1.2.3`. Mutable `latest` and release-line tags are not
published.

## Verify

Check that every workflow job passed, then verify:

```bash
gh release view v1.2.3 --repo mss-boot-ai/s3-copy-gateway
docker buildx imagetools inspect ghcr.io/mss-boot-ai/s3-copy-gateway:v1.2.3
docker run --rm ghcr.io/mss-boot-ai/s3-copy-gateway:v1.2.3 --version
```

The repository enforces immutable Releases. Fix a failed or incorrect release
with a new patch version; do not delete, recreate, or move the original tag.
