# ⚙️ Configuration, paths and Linux services

[Documentation home](../README.md) · [CLI reference](CLI.md) · [Operations](OPERATIONS.md)

## 🔌 Connection selection

Control bootstrap writes `~/.config/modelgauge/control.json` with mode `0600`.
It contains the active Lima VM, controller URL, admin token and enrollment token.
The CLI loads it for subsequent commands. Explicit environment variables override
saved values; flags override environment on commands that expose them. There is
one saved active connection, not a named multi-cluster configuration store.

```sh
export MODELGAUGE_CONTROLLER_URL=http://PRIVATE_CONTROL:31080
# Load MODELGAUGE_ADMIN_TOKEN from your private secret store/environment.
modelgauge node list
```

Manual deployment scripts save `control.env` on the control machine; that is a
shell environment file, not the host CLI JSON connection. These files have
different roles and are not interchangeable.

## 🗂️ Managed paths

| Path | Owner / purpose |
|---|---|
| `~/.config/modelgauge/ownership.json` | Host bootstrap/cleanup ownership registry (current source build) |
| `~/.config/modelgauge/control.json` | Host CLI active connection |
| `~/.config/modelgauge/control.env` | Control VM generated tokens from secret setup |
| `~/.kube/modelgauge-config` | Control VM kubeconfig created by bootstrap |
| `~/.config/modelgauge/agent.env` | Legacy manually enrolled Linux agent only; absent on new managed nodes |
| `~/.config/systemd/user/modelgauge-agent.service` | Linux user service |
| `~/.local/bin/modelgauge` | Managed guest executable |
| `~/.local/share/modelgauge/runtime/REVISION/` | Cached pinned llama.cpp build |
| `~/.local/share/modelgauge/runtime/current` | Active runtime symlink |
| `~/.local/share/modelgauge/models/` | Managed model cache |
| `~/.local/share/modelgauge/model-inventory.json` | Cached inventory checksums, refreshed when file size or modification time changes |
| `~/.local/share/modelgauge/results/` | Measured VM raw evidence and stage checkpoints |
| `/var/lib/modelgauge/connections/NODE.json` | Controller PVC: private SSH connection (mode 0600) |
| `/var/lib/modelgauge/runs/NODE/` | Controller PVC: pending journal and saved result for uploads |
| `~/.local/share/modelgauge/deploy/` | Control VM embedded deployment assets |
| `~/.cache/modelgauge/releases/` | Verified matching Linux release agents on CLI host |

Direct `benchmark run` defaults to a `results/` directory relative to the invoking
working directory. Manual `agent install` defaults to `~/modelgauge-results`;
Lima bootstrap uses a temporary worker and keeps its controller journal on the host. The legacy service uses its installed
environment file; foreground `agent run` reads the current shell environment.

## 🔑 Environment reference

| Variable | Used by | Meaning |
|---|---|---|
| `MODELGAUGE_CONTROLLER_URL` | CLI, agent | Reachable API URL |
| `MODELGAUGE_ADMIN_TOKEN` | CLI, server | Administrator credential |
| `MODELGAUGE_AGENT_TOKEN` | Agent, server | Shared agent credential |
| `MODELGAUGE_NODE_ID` | Agent | Stable registered node ID |
| `MODELGAUGE_NODE_NAME` | Agent | Display name; defaults to node ID |
| `MODELGAUGE_NODE_KIND` | Agent | `physical` by default, or `virtual` |
| `MODELGAUGE_EXECUTION_MODE` | Agent | `native-isolated` by default, or `lima-linux` |
| `MODELGAUGE_PROMETHEUS_TOKEN` | Export | Optional bearer token for Prometheus history requests; never included in bundles |
| `MODELGAUGE_RESULTS_DIR` | Agent | Foreground default `results`; installed service path may differ |
| `MODELGAUGE_DATABASE_URL` | Server | Full PostgreSQL connection URL |
| `DATABASE_HOST`, `DATABASE_PORT`, `DATABASE_NAME`, `DATABASE_USER`, `DATABASE_PASSWORD` | Server | Alternative connection components; port defaults to 5432 |
| `MODELGAUGE_RELEASE_BASE_URL` | Release CLI | HTTPS mirror; loopback HTTP permitted for local tests |
| `MODELGAUGE_CONTROL_CONFIG` | `create-secrets.sh` | Override output shell-token file only |
| `MODELGAUGE_TEST_DATABASE_HOST`, `MODELGAUGE_TEST_DATABASE_PASSWORD` | Integration tests | Test database connection, not production service configuration |

Prefer a full database URL when you need explicit PostgreSQL TLS settings.
Component-based connections currently use `sslmode=disable` for the internal lab.
Never commit tokens, connection files, kubeconfigs or generated Secret manifests.

## 🔌 Embedded controller execution

The bundled deployment sets `MODELGAUGE_EXECUTOR_STATE_DIR=/var/lib/modelgauge` and
mounts the `modelgauge-executor-data` PVC there. The server restores registered
SSH targets and pending journals on startup. Node bootstrap registers credentials
through the admin API. It uses a dedicated per-node private key, exact SSH host-key
verification and the existing Lima forwarded SSH port. Private keys belong only
to the controller; back up its PVC as credential-bearing data.

`--executor-state-dir PATH` configures the same behavior for manual server starts.
Without the flag/environment setting, legacy agent-driven servers remain supported
and connection enrollment returns 503. The current host `executor run` command is
removed. Upgrades and cleanup pause controller-owned workers; pending work blocks
them until it finishes. Old host-side journals are retained and never adopted.

## 🖥️ Legacy: enrolling a remote Linux node manually

This is for an already reachable Linux machine; automated `node bootstrap` only
manages a local Lima guest. Install the matching Linux ModelGauge binary and
GNU time, and provide a CPU llama-bench executable (managed path or PATH).
Place its models/results on adequate persistent storage.

On the Linux node, load the shared agent credential privately into
`MODELGAUGE_AGENT_TOKEN`, then:

```sh
modelgauge agent install \
  --controller http://PRIVATE_CONTROL:31080 \
  --node air-linux --name air-linux --kind physical \
  --execution-mode native-isolated \
  --results-dir "$HOME/.local/share/modelgauge/results"
sudo loginctl enable-linger "$(id -un)"
modelgauge agent status
```

For a Lima target choose `--kind virtual --execution-mode lima-linux` and an API
address reachable from that guest. On the CLI host, configure the matching admin
connection before `node list` and model/experiment commands. Do not copy the admin
token onto measured nodes. The shared agent token currently is not restricted to
one node identity; use this development setup only on trusted networks.

## 🔧 Agent service operations

Run these inside the measured Linux node as the service owner:

```sh
systemctl --user status modelgauge-agent --no-pager
journalctl --user -u modelgauge-agent -n 100 --no-pager
systemctl --user restart modelgauge-agent
```

Restarting active work invokes recovery rules; wait for idle state for a routine
upgrade. `agent uninstall` stops/disables/removes the unit but retains private
configuration, models and results. User lingering keeps the service alive after
logout. An absent systemd user session is an operating-system setup problem,
not a reason to run the whole benchmark agent as root.

## 🔁 Naming migration

ModelGauge uses its own executable/module names, `MODELGAUGE_*` variables,
`modelgauge-agent` unit, controller objects, config/cache paths and metric names.
Earlier deployed development VMs have not been migrated by the repository rename.
Do not start two differently named agents on the same measured machine. Back up
the database, tokens, model cache and evidence before a coordinated deployment
migration; update service configuration and Grafana/Prometheus queries together.
The current CLI has no automatic legacy migration command.

Model discovery prefers the managed model directory, then `~/models`, then
`~/benchmark-work/models` when filenames produce the same model ID. Inventory
fingerprints avoid repeated full-file scans; execution still verifies model
checksums before benchmarking.

## 📋 Benchmark declarations

Workload configuration lives in a `Benchmark` YAML file, separate from connection
credentials and VM provisioning settings. See the [complete schema and defaults](BENCHMARKING.md#️-declaration-fields-and-defaults)
and [example](../examples/benchmark.yaml). Use `validate -f FILE` or
`apply -f FILE --dry-run` before queuing work. Keep the accepted file to query or
cancel that immutable experiment; use a new `metadata.name` for another run.
