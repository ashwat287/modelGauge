# ✅ Public release validation — v0.1.0

[Documentation home](../../README.md) · [Install this release](RELEASING.md)

## 📦 Published identity

- Release: [v0.1.0](https://github.com/ashwat287/modelGauge/releases/tag/v0.1.0).
- Source commit: `6e5d6d72f545ec90f55e244749b5ce9196f06092`.
- Release workflow: [successful run](https://github.com/ashwat287/modelGauge/actions/runs/42376235440).
- Validation date: 2026-11-07.
- Controller image: `ghcr.io/ashwat287/modelgauge@sha256:494182a3e00b8ffc794f26c97e8e73c1d730c0f8b015261adf4bf52ea329bf1e`.

## 🧪 Test setup

A public macOS ARM64 CLI was downloaded into a newly created temporary directory,
not run from a source checkout. No source, image, or agent-binary overrides and no
local release-feed override were supplied. Host `go` and `docker` were replaced
on PATH by failing stubs for bootstrap and benchmark commands. Lima itself was
already installed; its cached Ubuntu base image was reused. The VM disks, k3s
installation, runtime build, agent enrollment, database and model cache were new.

| Role | VM | vCPUs | RAM | Disk | API |
|---|---|---:|---:|---:|---|
| Control | `modelgauge-release-control` | 2 | 3 GiB | 20 GiB | Host port 18082 |
| Measured target | `modelgauge-release-node` | 2 | 3 GiB | 20 GiB | Agent polls control |

The host has 16 GiB RAM. Existing idle development VMs were stopped temporarily
to avoid memory contention. This is a release workflow check on ARM64 Linux VMs,
not an Intel Air performance baseline or proof of thermal stability.

## ✅ Completed checks

| Check | Result |
|---|---|
| CI and release tests | Passed, including PostgreSQL integration tests |
| Public executable downloads | All four assets matched release SHA-256 and size |
| Release manifest checksum | Matched published SHA256SUMS |
| Public controller image | Anonymous pull allowed; AMD64 and ARM64 manifests present |
| macOS CLI identity | ARM64 native and AMD64 under Rosetta reported v0.1.0 and the release commit |
| Fresh control bootstrap | k3s, PostgreSQL, controller, Prometheus and Grafana became ready |
| Controller health | v0.1.0, correct commit, database reachable |
| Fresh benchmark bootstrap | Pinned llama.cpp built and Linux ARM64 release agent enrolled |
| Installed agent integrity | Executable SHA-256 matched the public Linux ARM64 asset |
| Grafana | Database healthy; both dashboards provisioned; datasource returned both measured throughput series |
| Prometheus | Self/controller/control exporter healthy; both completed throughput series scraped |
| Model acquisition | Fresh Q4 download passed publisher checksum and inventory registration |
| Benchmark and results | Two throughput cases and memory measurement completed and persisted |
| Recommendation | Stable measured cases selected; reservation fits explicit 2 GiB budget |

The benchmark VM's Linux exporter is not installed by node bootstrap. This test
checks controller-derived benchmark metrics, not full measured-VM Linux charts.
Cross-host enrollment, native Linux AMD64 provisioning and native Intel macOS
execution were not exercised locally. The CI runner executes Linux AMD64 metadata
validation; downloading an asset alone is not a native runtime validation.

## 🔁 Reproduction

After downloading and verifying the published CLI using the installation guide,
run it from a directory without the source repository:

```sh
./modelgauge control bootstrap --provider lima --name modelgauge-release-control \
  --cpus 2 --memory 3 --disk 20 --port 18082
./modelgauge node bootstrap --provider lima --name modelgauge-release-node \
  --cpus 2 --memory 3 --disk 20
./modelgauge model install qwen2.5-1.5b-instruct:q4_k_m --node modelgauge-release-node
./modelgauge model install status JOB_ID
# Wait for completed and inventory registration before submitting.
./modelgauge model list --node modelgauge-release-node
./modelgauge benchmark campaign create \
  --node modelgauge-release-node --execution-mode lima-linux \
  --models qwen2.5-1.5b-instruct:q4_k_m --contexts 32 --threads 2 \
  --output campaign --submit
./modelgauge benchmark campaign status --file campaign/campaign.json
./modelgauge results show --node modelgauge-release-node
./modelgauge results recommend \
  --node modelgauge-release-node --model qwen2.5-1.5b-instruct:q4_k_m \
  --execution-mode lima-linux --context 32 --memory-budget-gib 2
./modelgauge monitoring open
```

Choose unused names/ports and a fresh campaign directory for another run. The
commands reuse named VMs if they exist; a genuine fresh-provisioning check must
start with names that have never been created. No source build or local OCI
import should be substituted when validating public release delivery.

## 📏 Recorded experiment

This checks one model/quantization, one prompt size and one thread count. It does
not validate the full multi-model/context matrix or sustained serving behavior.

- Model: `qwen2.5-1.5b-instruct:q4_k_m`.
- Publisher revision: `91cad51170dc346986eccefdc2dd33a9da36ead9`.
- Model SHA-256: `6a1a2eb6d15622bf3c96857206351ba97e1af16c30d7a74ee38970e434e9407e`.
- Download size: 1117320736 bytes.
- Runtime revision: `eb4e244be77a906a183cd4a00fd51d9ad3200955`.
- Plan: `63299770-e6aa-4364-9316-8a6c2f74ce7a`.
- Source run: `run-63299770-e6aa-4364-9316-8a6c2f74ce7a`.
- Warm-ups: 3 per case; measured repetitions: 5 per case; GPU layers: 0.

| Measurement | Result |
|---|---:|
| Generation, 128 tokens, 2 threads | 25.106758 tokens/s |
| Prompt, 32 tokens, 2 threads | 37.287973 tokens/s |
| Peak RSS | 1183068160 bytes |
| Reservation | 1478835200 bytes |

The recommender reported `fits` with a supplied 2 GiB workload budget. This
reservation is for the recorded memory workload and is not a claim about long
populated-context decode, concurrency or model quality. Raw evidence remains on
the validation node; normalized measurements are in its fresh PostgreSQL store.

## 🗂️ Retained validation environment

After validation, both release-test VMs were stopped and the original idle
lab VMs were restarted. No old database, service or agent was migrated/deleted.
The release-test disks and model/evidence files are retained. The host's new
ModelGauge connection points to the stopped release-test control VM on port 18082.
To resume that environment, first ensure sufficient free RAM, then:

```sh
limactl start --tty=false modelgauge-release-control
limactl start --tty=false modelgauge-release-node
# From your installed/released CLI:
modelgauge control status
modelgauge results show --node modelgauge-release-node
```

The two validation VMs allocate 6 GiB RAM together. Avoid running them alongside
all previous development VMs on this 16 GiB host. Stop idle VMs rather than
removing their disks merely to free memory. The validation CLI/files are also
retained temporarily under `/tmp/modelgauge-public-release.DS3urU`; these temporary
host files are not a durable evidence backup.
