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
- Current source: checksummed JSON/CSV result/metric exports, optional raw evidence and Prometheus query history.
- Current source: ownership-tracked Lima bootstrap and scoped cleanup with dry-run/created-VM deletion.

## 🚢 v0.1.0 published

The [first public release](https://github.com/ashwat287/modelGauge/releases/tag/v0.1.0)
is available. CI/release tests passed, the multi-architecture controller is
public, and all four executable downloads passed checksum verification.

The [v0.1.0 downloaded-CLI smoke test](releases/RELEASE-VALIDATION.md) passed on fresh
ARM64 Lima VMs with a real model download and two-case benchmark.

Repeat a downloaded-CLI smoke test for each release with fresh VMs and a real
install → inventory → benchmark → results → Grafana workflow. Keep the earlier
development installation migration separate and preserve its evidence.

## 📏 Better measurement evidence

- Controlled thermal/steady-state qualification and richer environment capture.
- Populated-context generation and combined prefill/decode workloads.
- Memory measurements across explicit workload shapes, repeated RSS samples and stronger fit validity rules.
- Broader remote artifact transport, restorable backups and stronger reproducibility/comparability checks.
- Independent quality evaluation for quantization/model trade-offs.

## 🔐 Production and scale

- Per-node enrollment credentials, TLS, rotation, artifact signing and provenance.
- Multi-cluster/remote deployment lifecycle, documented upgrades and migration tooling.
- Automated database/evidence backups, restore validation and retention controls.
- Additional runtimes, GPU backends and accelerator-aware measurement profiles.
- Scheduler integration based on evidence and workload requirements.

Until those features exist, avoid treating the current recommender as a general
capacity oracle or exposing the development controller as a public service.
