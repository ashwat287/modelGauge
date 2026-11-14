# 📈 Prometheus and Grafana

[📚 Documentation home](../../README.md) · [Architecture](../ARCHITECTURE.md) · [Operations](../OPERATIONS.md)

## 🧩 What is monitored

Prometheus and Grafana run in the control VM's `monitoring` namespace. Linux node
exporters run as user services on private VM IPs; benchmark nodes do not join k3s.
Bootstrap installs the control exporter and deploys monitoring. Exporter setup
for a newly provisioned benchmark VM is separate. Result metrics come from the
controller even when that node's exporter is absent.

| Source | Data | Limitation |
|---|---|---|
| Linux node exporter | CPU, available RAM, swap, filesystem and system counters | Guest Linux view, not macOS host-wide metrics or host thermal sensors |
| Controller `/metrics` | Plans/installs/runs by state, last agent contact | PostgreSQL-backed gauges, not event counters |
| Controller result gauges | Latest throughput, relative stddev, RSS and reservation | Latest selected measurements, not live token streams or full history |
| Prometheus self | Scrape/TSDB service metrics | Monitoring health, not model performance |

## 🚀 Open dashboards

From the CLI host, with the saved active control connection:

```sh
modelgauge monitoring open
```

Grafana is forwarded to `http://127.0.0.1:3000`; username is **admin**. Read its
generated password privately **inside the control VM**:

```sh
export KUBECONFIG="$HOME/.kube/modelgauge-config"
kubectl -n monitoring get secret grafana-admin \
  -o jsonpath='{.data.password}' | base64 --decode
```

Do not paste the password into issue reports. The CLI forward runs as a systemd
user service and reconnects after pod replacement. The host/VM must remain running.
Dashboards are provisioned in Grafana's **ModelGauge** folder; changes to dashboard
JSON should be made in the repository and redeployed rather than edited ad hoc.

For a manual Prometheus forward, run inside the control VM and keep it active:

```sh
kubectl --kubeconfig "$HOME/.kube/modelgauge-config" -n monitoring \
  port-forward svc/prometheus 9090:9090
```

Lima normally forwards guest localhost to the host. Open `http://127.0.0.1:9090`.
Check port conflicts if another VM/application already uses that port.

## 🖥️ Register a benchmark node exporter

First inspect the measured VM's current private address:

```sh
limactl shell --workdir / benchmark-node -- ip -brief address
```

Choose its reachable private interface IP, not loopback, then from the repository
root stream the installer into that guest:

```sh
# Replace the value after inspecting the interface list.
BENCHMARK_IP='YOUR_PRIVATE_VM_IP'
limactl shell --workdir / benchmark-node -- bash -s -- "$BENCHMARK_IP" \
  < deploy/monitoring/install-node-exporter.sh
```

The script downloads a pinned node_exporter archive, verifies its published hash,
installs `~/.local/bin/node_exporter`, and enables `modelgauge-node-exporter` as a
user service listening on that private IP, port 9100. Run the guest's usual
lingering setup if you manually enrolled it. Do not bind to a public interface.

From source, reapply control bootstrap with the target:

```sh
modelgauge control bootstrap --provider lima --source . \
  --benchmark-target "$BENCHMARK_IP:9100"
```

This reapplies/restarts control workloads, so do it when experiments are idle.
For manual deployment edit `stack.yaml` scrape targets and labels first; the
checked-in IPs are historical examples, not discovery. Then run, from a machine
with the cluster kubectl context, `bash deploy/monitoring/deploy.sh`. Inspect the
rendered/managed scrape configuration if bootstrap previously supplied dynamic IPs.

## 📊 Useful PromQL

```promql
up

time() - modelgauge_agent_last_seen_seconds

modelgauge_benchmark_tokens_per_second

modelgauge_benchmark_relative_stddev

modelgauge_peak_rss_bytes / 1024 / 1024

node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes
```

Use node/model/mode/workload/thread labels to compare equivalent measurements.
No run IDs, artifact paths, download URLs or tokens belong in metric labels.
Consult PostgreSQL/`results show` for experiment history and the measured node
for raw repetition/evidence files.

## 💾 Retention and budgets

| Workload | CPU request / limit | RAM request / limit | PVC |
|---|---|---|---|
| Prometheus | 100m / 1 CPU | 256 MiB / 768 MiB | 4 GiB |
| Grafana | 100m / 500m | 128 MiB / 384 MiB | 1 GiB |

Prometheus scrapes/evaluates every 30 seconds and retains seven days or 2 GB of
samples, whichever is reached first. Reapplying preserves PVCs and the Grafana
admin Secret. Exporters and scraping add background work; record monitoring as
part of your benchmark environment. Resource limits do not provide physical
isolation from a benchmark on the same host.

## 🚨 Alerts and verification

Rules cover failed scrape targets, stale agent heartbeats, and low available
Linux memory. Prometheus evaluates rules; no Alertmanager or external delivery
channel is deployed. A firing rule therefore does not imply email/Slack delivery.

Verify Prometheus **Status → Targets**, or its `/api/v1/targets` endpoint. Expected
healthy targets are Prometheus, controller, control exporter, and each explicitly
configured benchmark exporter. Do not assume four targets if only one exporter
is installed. Grafana `/api/health` checks its database. Apply a real YAML experiment
and inspect operations/result gauges after completion; dashboards do not show
live tokens generated per second while a case is running.

Memory fit remains a CLI recommendation using a workload budget. A dashboard's
"available memory" is not a guarantee that a particular long-context model fits.
