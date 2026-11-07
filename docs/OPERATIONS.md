# 🛠️ Operations and troubleshooting

[Documentation home](../README.md) · [Configuration](CONFIGURATION.md) · [Recovery](operations/JOB-RECOVERY.md)

## 🩺 Start with read-only checks

On the CLI host:

```sh
modelgauge version --json
limactl list
modelgauge control status
modelgauge node list
modelgauge node status benchmark-node
curl -fsS http://127.0.0.1:18080/healthz
```

Use the active control VM name from your bootstrap output. In examples below,
`modelgauge-control` is the default new VM; an existing lab may use another name.
Run Kubernetes commands **inside the control VM**:

```sh
limactl shell --workdir / modelgauge-control
export KUBECONFIG="$HOME/.kube/modelgauge-config"
kubectl get nodes
kubectl -n benchmark-system get pods,svc
kubectl -n benchmark-system logs deployment/modelgauge-controller --tail=100
kubectl -n benchmark-data get pods,pvc
kubectl -n monitoring get pods,pvc
```

Inside the measured node:

```sh
limactl shell --workdir / benchmark-node
systemctl --user status modelgauge-agent --no-pager
journalctl --user -u modelgauge-agent -n 100 --no-pager
pgrep -af llama-bench
free -h
swapon --show
```

`pgrep` shows whether the measured process is currently active; the absence of a
process can also mean downloading, warming between commands, waiting, uploading,
or failure. Check campaign/job state and agent logs as well. Avoid killing the
wrapper or deleting a lock/journal to make a node appear idle.

## 🔎 Common failures

| Symptom | Check / action |
|---|---|
| `kubectl: command not found` on host | Use `modelgauge control status` or enter the control VM. `sudo k3s kubectl` works there when k3s exists. |
| `k3s.service` missing | Installation never completed; inspect bootstrap/network error and retry. |
| Cannot reach API at localhost | Ensure the active VM is running, port is free and its forward service is active. |
| Node offline | Agent unit/logs, reachable API URL, token and guest network; local Lima uses `host.lima.internal`, not guest localhost. |
| Model install queued | Agent needs to be online and free of another active lease. |
| Install completed but model missing | Wait for the next inventory poll; check agent logs and node ID. |
| Model unavailable for campaign | Use the exact installed ID from `model list --node ID`; wait for all models first. |
| `llama-bench` not found | Agent discovers its managed runtime; direct runs need `runtime.binary` or executable on PATH. |
| No stable recommendation | Check raw sample drift, exact prompt context and execution mode. The newest eligible run may be unstable. |
| Huge elapsed time / near-zero tokens/s | Check sleep/suspension, swap, host load and thermal conditions; preserve then rerun the experiment. |
| Pending PVC or pod | Inspect `kubectl describe`, local-path provisioner, disk space and image-pull/network events. |
| Image pull fails | Verify the actual reference and registry access. Release CLIs resolve a public digest-pinned image; the development `:dev` placeholder is not a published release. Source bootstrap imports its image. |
| Grafana has no Linux benchmark charts | Install/register that target's exporter; result metrics and Linux metrics have different sources. |
| Agent lock refused | Another same-user agent owns it. Inspect services/processes; do not start a second agent under another name. |

For controller forwarding, inside the control VM:

```sh
systemctl --user status modelgauge-forward-18080 --no-pager
journalctl --user -u modelgauge-forward-18080 -n 50 --no-pager
systemctl --user restart modelgauge-forward-18080
```

Substitute your API port. `monitoring open` creates/reuses the corresponding
Grafana forward on port 3000. The services restart on pod replacement, but Lima
and the host must remain running.

## 🔄 Restart, recovery and cancellation

Use `benchmark cancel PLAN_ID` / `model install cancel JOB_ID` for user-requested
cancellation. An active agent learns the request on renewal. Connectivity loss
can delay cancellation until recovery or lease expiry. Do not interpret a
returned cancellation request as proof that the child stopped immediately.

A normal agent restart preserves its pending journal. Within a valid lease,
completed matching cases can be reused and unfinished cases rerun. If ownership
expired, stale completion is fenced; the journal is retained as rejected evidence.
Read [job recovery](operations/JOB-RECOVERY.md) for exact semantics.

For ordinary maintenance, wait for terminal jobs and an idle node before
restarting services or reapplying control manifests. Bootstrap is retryable and
preserves resources in an existing same-name installation; it is not a backup,
rollback or legacy-name migration tool.

## 📦 Export before cleanup

Use the [export guide](operations/EXPORT.md) to save JSON/CSV results, current
metrics, optional Prometheus history and checksum-verified raw node files.
Verify the bundle before deleting source VMs. This is separate from a database backup.

## 💾 Back up PostgreSQL and node evidence

Inside the control VM, create a logical backup without printing credentials:

```sh
export KUBECONFIG="$HOME/.kube/modelgauge-config"
umask 077
mkdir -p "$HOME/modelgauge-backups"
BACKUP="$HOME/modelgauge-backups/benchmark-$(date -u +%Y%m%dT%H%M%SZ).dump"
kubectl -n benchmark-data exec benchmark-postgres-0 -- \
  pg_dump -U benchmark_app -d benchmark -Fc > "$BACKUP"
test -s "$BACKUP"
```

Copy backups off the VM, preserve private enrollment/connection files separately,
and copy measured node model/evidence directories if you need reproducibility.
For a consistent cross-file archive, stop accepting new jobs and wait for active
jobs/uploads to finish first. Protect backups as sensitive data. Test a restore
into a separate database before relying on the backup; no automated restore or
scheduled backup service is included.

The local-path PVC reclaim policy is normally `Delete`. Deleting PVCs, the k3s
VM, or its storage can remove the only database copy. Stopping a VM retains its
disk. The updated source build supports [ownership-verified cleanup](operations/CLEANUP.md).
Use `modelgauge cleanup --dry-run --delete-vms` to preview and
`modelgauge cleanup --delete-vms` to remove only its tracked created VMs.
Legacy installations are preserved. This is an end-of-experiment operation,
not a troubleshooting shortcut or backup.

## 📦 Release and deployment changes

Prefer matching CLI/controller/agent versions. A versioned CLI validates release
identity and checksum before installing the Linux agent. Explicit source/image
or agent-binary overrides are advanced paths that make compatibility your
responsibility. Inspect `version --json` and node `agent_version` after changes.
Do not change runtime/model identity mid-experiment.

The repository rename did not migrate the running lab. Plan that migration as a
separate coordinated change with backups, a single agent owner, updated private
environments, new service/object names and updated metric queries.
