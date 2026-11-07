# 🗺️ Roadmap and delivery status

[Documentation home](../README.md) · [Architecture](ARCHITECTURE.md)

This page separates implemented functionality from planned work. It is a direction,
not a promise of dates or support for unimplemented commands.

## ✅ Implemented foundation

- Go CLI/controller/Linux agent with versioned benchmark plan/result messages.
- CPU GGUF model catalog/download/inventory and multi-model campaign generation.
- Fixed warm-ups, repetition samples, prompt/generation throughput and GNU-time RSS.
- PostgreSQL-backed queues, leases, renewal, cancellation and recovery journals.
- Results queries and deterministic stable-case/thread/memory recommendations.
- Lima control and measured-node bootstrap; pinned Linux runtime builds.
- Prometheus/Grafana manifests and dashboards; optional Linux exporters.
- Cross-platform binary packaging and a matching image/manifest release workflow.

## 🚢 Next: first public release

1. Review/commit the repository and connect the chosen GitHub remote.
2. Run CI, create a version tag and publish matching executable/image artifacts.
3. Ensure the GHCR package is public and assets are downloadable anonymously.
4. Test a downloaded CLI from an empty directory with freshly created Lima VMs.
5. Validate install → inventory → multi-model benchmark → results → Grafana.
6. Migrate the earlier development installation separately, preserving its evidence.

## 📏 Better measurement evidence

- Controlled thermal/steady-state qualification and richer environment capture.
- Populated-context generation and combined prefill/decode workloads.
- Memory measurements across explicit workload shapes, repeated RSS samples and stronger fit validity rules.
- Exportable evidence bundles and stronger reproducibility/comparability checks.
- Independent quality evaluation for quantization/model trade-offs.

## 🔐 Production and scale

- Per-node enrollment credentials, TLS, rotation, artifact signing and provenance.
- Multi-cluster/remote deployment lifecycle, documented upgrades and migration tooling.
- Automated database/evidence backups, restore validation and retention controls.
- Additional runtimes, GPU backends and accelerator-aware measurement profiles.
- Scheduler integration based on evidence and workload requirements.

Until those features exist, avoid treating the current recommender as a general
capacity oracle or exposing the development controller as a public service.
