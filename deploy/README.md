# 🏗️ Deployment assets

This directory contains embedded Kubernetes manifests, deployment scripts, and Grafana dashboards. Database migrations live in
`internal/controller/migrations/`. Deployment documentation lives under `docs/`:

- [Control-plane bootstrap](../docs/deployment/BOOTSTRAP.md)
- [Benchmark-node bootstrap](../docs/deployment/NODE-BOOTSTRAP.md)
- [Manual k3s deployment](../docs/deployment/K3S.md)
- [Prometheus and Grafana](../docs/operations/MONITORING.md)
- [Job recovery and cancellation](../docs/operations/JOB-RECOVERY.md)
- [Releases and installation](../docs/releases/RELEASING.md)
- [Historical model-install validation](../docs/releases/MODEL-INSTALL-VALIDATION.md)

See the [project README](../README.md) for the complete documentation index.
