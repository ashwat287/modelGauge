# 📦 Releases and installation

[📚 Documentation home](../../README.md)


[ModelGauge v0.1.0](https://github.com/ashwat287/modelGauge/releases/tag/v0.1.0)
is published from commit `6e5d6d72f545ec90f55e244749b5ce9196f06092`.
The [release workflow](https://github.com/ashwat287/modelGauge/actions/runs/42376235440)
passed, including PostgreSQL tests and anonymous GHCR image access. The
public-release smoke test also passed on fresh
ARM64 Lima VMs, including model download, benchmark, results and Grafana metrics. All four
public binary downloads and the release manifest passed their SHA-256 checks.

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

## 🧰 Prerequisites

### 💻 Host requirements for a published binary

| Requirement | What you need |
|---|---|
| Supported CLI platform | macOS or Linux on ARM64 or AMD64; download the matching executable |
| Lima | A working `limactl` installation with virtualization configured for its selected VM driver |
| Download tools | `curl` and either `shasum` or `sha256sum` |
| Shell | A normal Unix shell and standard utilities such as `awk`, `wc`, `tr`, `chmod` and `mv` |
| Network access | Host and guests must reach GitHub, GHCR, Ubuntu images/package mirrors, container registries and the chosen model publisher; the built-in catalog uses Hugging Face |
| Guest permissions | The bootstrap user needs `sudo` inside the Linux guests; the default Lima Ubuntu setup supplies it |
| Available local ports | API port `18080` by default, configurable with `control bootstrap --port`; Grafana uses port `3000` |

The measured runtime is Linux. A Mac runs the CLI and hosts Linux guests through
Lima; benchmarks do not run directly on macOS. Keep the host and control VM
running while the local benchmark agent uses the forwarded API.

### 💾 CPU, memory and disk budget

The automated setup creates two separate VMs:

| VM | Default vCPUs | Default RAM | Default disk | Successful release smoke-test allocation |
|---|---:|---:|---:|---|
| Control plane | 4 | 6 GiB | 50 GiB | 2 vCPUs, 3 GiB RAM, 20 GiB disk |
| Benchmark node | 2 | 3 GiB | 30 GiB | 2 vCPUs, 3 GiB RAM, 20 GiB disk |

The default pair allocates **9 GiB RAM**, plus the host OS and other applications.
The smaller tested pair allocates **6 GiB RAM**; it is evidence for a small-model
smoke test, not a universal minimum for every model or workload. Leave host RAM
headroom and disk space for Linux packages, C++ builds, GGUF files and results.
VM disk sizes are configured capacities; actual host usage grows with data.

Use `--cpus`, `--memory` and `--disk` when creating a VM to choose its allocation.
These flags do not resize an existing VM. For an 8 GiB laptop, place control
services on the stronger machine and budget the measured environment separately;
cross-host Linux-agent enrollment is currently a
[manual setup](../CONFIGURATION.md).

### 📦 What bootstrap installs

You do not need to install host Go, Docker Desktop, kubectl, PostgreSQL,
Prometheus or Grafana to use the published CLI. Bootstrap installs k3s and
control services in the control guest, and Git, CMake, C/C++ build tools, GNU
time and the pinned llama.cpp runtime in the benchmark guest. Updated source
bootstrap uses the matching Linux Go executable for ownership and raw evidence;
Python is not required. Models are
downloaded separately with `model install`.

For an existing Linux target enrolled manually, provide systemd user-service
support, GNU `/usr/bin/time` and a working CPU llama-bench executable yourself;
see [agent configuration](../CONFIGURATION.md).

### 🛠️ Additional requirements for source builds

To clone and build the project instead of downloading a release, install
**Git and Go 1.23 or newer** on the host. The module declares toolchain
`go1.23.6`, which Go may download when needed. Lima is still required for the
automated VM setup. Source bootstrap builds/imports its controller image
without requiring Docker Desktop.

### ✅ Check before installing

Run these on the CLI host:

```sh
uname -s
uname -m
limactl --version
limactl list
curl --version
command -v sha256sum || command -v shasum
```

An empty Lima instance list is fine: bootstrap creates the VMs. If Lima or the
download/checksum commands are missing, install them before continuing. For a
source build, also check `git --version` and `go version`.

## 🚀 Installing a published CLI

Install Lima first. The following uses the published repository and tag; the
shell example supports macOS and Linux, on ARM64 and AMD64:

```sh
set -eu
REPOSITORY='ashwat287/modelGauge'
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
in [control bootstrap](../deployment/BOOTSTRAP.md) and [node bootstrap](../deployment/NODE-BOOTSTRAP.md).

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
The public GitHub/GHCR workflow and production Docker build subsequently passed
for v0.1.0. Public image visibility and all executable downloads were verified
independently of the earlier local feed.
