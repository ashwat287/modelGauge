# 📦 Releases and installation

[📚 Documentation home](../README.md)


The release pipeline is implemented, but this working tree has not been published.
The examples below require an actual published version in your chosen repository.
No public download URL is claimed to exist yet.

## 📋 What ships

Each tagged release contains `modelgauge_darwin_amd64`, `modelgauge_darwin_arm64`,
`modelgauge_linux_amd64`, `modelgauge_linux_arm64`, `release.json`, and `SHA256SUMS`.
The Linux executables also run as agents and controllers. A multi-architecture
controller image is published to GHCR using those same Linux binaries.

All components carry the same version, commit, build date and repository.
`modelgauge version --json` reports that identity. The manifest pins the controller
image by digest and each agent by size and SHA-256. Bootstrap refuses mismatched
release metadata or corrupt agent downloads, and rechecks cached executables.
Checksums detect corruption; artifact signing and provenance verification remain
future work. Existing explicit `--source`, `--image`, and `--agent-binary`
overrides remain available for development and troubleshooting.

## 🚀 Installing a published CLI

Install Lima first. Select your actual repository and published tag; the following
shell example supports macOS and Linux, on ARM64 and AMD64:

```sh
set -eu
REPOSITORY='YOUR_ACCOUNT/YOUR_REPOSITORY'
VERSION='v0.1.0'
case "$(uname -s)" in Darwin) os=darwin ;; Linux) os=linux ;; *) exit 1 ;; esac
case "$(uname -m)" in arm64|aarch64) arch=arm64 ;; x86_64) arch=amd64 ;; *) exit 1 ;; esac
asset="modelgauge_${os}_${arch}"
base="https://github.com/$REPOSITORY/releases/download/$VERSION"
mkdir -p modelgauge-download
cd modelgauge-download
curl -fL "$base/$asset" -o "$asset"
curl -fL "$base/SHA256SUMS" -o SHA256SUMS
awk -v asset="$asset" '$2 == asset { print }' SHA256SUMS > selected.sha256
test "$(wc -l < selected.sha256 | tr -d ' ')" = 1
if command -v sha256sum >/dev/null; then
  sha256sum -c selected.sha256
else
  shasum -a 256 -c selected.sha256
fi
chmod 755 "$asset"
mv "$asset" modelgauge
./modelgauge version --json
./modelgauge control bootstrap --provider lima
./modelgauge node bootstrap --provider lima --name benchmark-node
./modelgauge node status benchmark-node
```

These commands run without a checkout, host Go, or Docker Desktop. The host needs
Lima and internet access. Guests need package, container-registry, and GitHub
access; the benchmark guest compiles the pinned llama.cpp runtime with Linux
build tools. Model acquisition is a separate command. Keep the host and control
VM running while agents use its forwarded API. Default VM budgets are documented
in [control bootstrap](BOOTSTRAP.md) and [node bootstrap](NODE-BOOTSTRAP.md).

## 📋 Publishing

1. Choose the GitHub repository, commit the reviewed source, and push it. The
   workflow derives download URLs and image names from `github.repository`.
2. Allow Actions to publish repository contents and GHCR packages. The workflow
   uses its scoped `GITHUB_TOKEN`; no personal token is stored in this repo.
3. Push a new semantic version tag such as `v0.1.0` on the intended commit.
4. On the first run, make the created GHCR package public. The workflow checks
   anonymous manifest access before publishing GitHub Release assets. If that
   check fails, set package visibility and rerun the workflow before a release
   exists. Prerelease tags produce prerelease GitHub releases.
5. Verify installation with the published CLI from an empty directory on a host
   with Lima, including both fresh VMs and reuse of existing VMs.

`.github/workflows/release.yml` runs race-enabled tests, including PostgreSQL and
Linux process recovery, builds four executables, publishes the controller image,
finalizes the digest-pinned manifest, then publishes assets and checksums. Existing
GitHub releases are refused rather than overwritten. Tags/releases should remain
immutable. CI also runs tests and vet independently of a release.

The release builder requires a clean tree and the exact HEAD commit:

```sh
go run ./tools/release --version v0.1.0 --repository YOUR_ACCOUNT/YOUR_REPOSITORY
# After publishing Dockerfile.release for both Linux architectures:
go run ./tools/release --finalize --image 'ghcr.io/your_account/your_repository@sha256:ACTUAL_DIGEST'
```

The initial manifest is incomplete until finalization and cannot be used for
bootstrap. The output directory must be empty. `dist/` is ignored by Git.

## ✅ Local release validation

Uncommitted development snapshots are deliberately marked `snapshot` and are
never published by the tag workflow. To create a local fixture without Docker:

```sh
go run ./tools/release --snapshot --version v0.0.0-local.1 \
  --repository nerdylucifer/modelgauge --local-controller-arch arm64 \
  --output dist/local-v0.0.0-local.1
```

This optionally emits a scratch OCI controller archive for local import into
k3s; it is a test fixture, not the production distroless image. Serve the directory
on loopback and set `MODELGAUGE_RELEASE_BASE_URL` to that HTTP URL when testing the
downloaded CLI. Snapshots enable automatic resolution only with this explicit
override. Production mirrors require HTTPS. The override changes the transport
location, not the version/commit/repository checks.

Local validation covers all four executable builds and checksum verification,
corrupt download rejection, mismatched metadata rejection, and cache integrity.
Before the project rename, a snapshot was downloaded into an empty host directory with
host `go` and `docker` replaced by failing stubs. Both bootstrap commands
succeeded against existing Lima VMs without source/image/binary overrides.
The controller reported the snapshot version and reachable database; the agent
downloaded from the local feed, registered the same version, and stayed active.
The prior live controller image and agent executable were restored afterward.
This exercised VM reuse, not fresh provisioning or a new model download.
After the rename, all four ModelGauge executables were rebuilt and their
checksums verified. The renamed deployment has not been applied to the existing
VMs. ModelGauge uses `MODELGAUGE_*` environment variables,
`~/.config/modelgauge`, `~/.local/share/modelgauge`, the `modelgauge-agent`
systemd service, and `modelgauge_*` metrics. Existing installations require a
coordinated configuration, service, and deployment migration; changing a binary's
filename alone does not migrate them. Preserve the database and model/result
directories when planning that migration.
The public GitHub/GHCR workflow and production Docker build still need their
first authenticated repository run; a local feed cannot validate registry
permissions, public visibility or public download availability.
