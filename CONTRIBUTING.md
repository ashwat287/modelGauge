# 🛠️ Contributing and development

[Documentation home](README.md) · [Architecture](docs/ARCHITECTURE.md) · [Release guide](docs/releases/RELEASING.md)

## 🧰 Local setup

Use Go 1.23 or newer (the module declares its toolchain). Lima is needed only
for provisioning/integration checks, not for ordinary unit tests.

```sh
go build -o bin/modelgauge ./cmd/modelgauge
bin/modelgauge --help
go test -race ./...
go vet ./...
```

Source bootstrap can use `--source /absolute/checkout`. A source-built controller
OCI archive can be imported directly into k3s, without Docker Desktop. macOS can
build the CLI, but Linux process tests/benchmarks need Linux.

## 🧪 Test layers

| Layer | Runs where | Covers |
|---|---|---|
| Ordinary Go tests | Host | Plans, downloads, recovery helpers, CLI output, release integrity |
| Linux process tests | Linux / CI | Cancellation stops wrapper and child processes |
| PostgreSQL integration tests | Host/Linux with test database | Cross-queue claims, ownership, expiry, replay, cancellation |
| Lima smoke test | Host + control/benchmark VMs | Provisioning, runtime, enrollment, model inventory, submit/result flow |
| Public release smoke test | Clean host environment | Published CLI, public image and release-agent downloads |

For integration tests, provide a disposable PostgreSQL database named `benchmark`
with user `benchmark_app`, then load its password privately:

```sh
export MODELGAUGE_TEST_DATABASE_HOST=127.0.0.1:5432
# Set MODELGAUGE_TEST_DATABASE_PASSWORD from your test secret source.
go test -race ./internal/controller ./internal/agent ./internal/benchmark
```

Database tests create/drop a temporary schema and require permission to do so.
Without the host variable these database tests skip; a green local test run alone
does not imply PostgreSQL or Linux-only paths ran. GitHub CI provisions PostgreSQL
and executes tests on Linux. Use a disposable test database rather than a live lab.

## 📐 Change guidelines

Keep machine-readable output free of progress banners/emojis. Human help and
success messages may use clear, sparse emoji section markers. Document a new flag
or environment variable where it is actually consumed. Update docs, sample plans,
embedded manifests and dashboards together when changing deployment identifiers.

Protocol changes need explicit schema/version consideration. Preserve ownership
and idempotence rules in the job lifecycle. Never put run IDs or URLs in telemetry
labels. Keep credentials, private connection files, GGUFs, build trees and generated
release artifacts outside commits. Preserve archived measurements and their
original evidence in [the historical benchmark report](docs/HISTORICAL-BENCHMARKS.md);
do not rewrite samples to make a new expectation pass. Keep future raw experiment
exports outside source control and document selected results with their provenance.

For a pull request, describe the user-visible change, why it is needed, what was
tested and what remains unverified. Tests should exercise meaningful behavior,
not duplicate implementation line by line. Publishing is handled separately by
the tag workflow, not by ordinary test/build commands.

## 📦 Before a release

Verify the documented quick start, CLI help/JSON output, all four binary targets,
checksums, digest-pinned image, anonymous GHCR access and a fresh user workflow.
The v0.1.0 public workflow, image access and all binary download checks passed.
Repeat the fresh-user smoke test for future releases; a successful build alone
does not validate installation or model execution on every target platform.
