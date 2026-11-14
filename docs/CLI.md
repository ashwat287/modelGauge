# 🧰 CLI reference

[Documentation home](../README.md) · [First benchmark](GETTING-STARTED.md) · [Configuration](CONFIGURATION.md)

## 🧭 Conventions

`modelgauge <command> --help` prints flags for flag-bearing commands;
`modelgauge help benchmark` prints a group overview. Commands use Go-style flags:
put a positional model ID **before** its flags, as in `model install MODEL_ID
--node NODE_ID`. Node IDs, job IDs, file names and URLs in uppercase are placeholders.
Use a new experiment name for each new measurement.

Human help and success messages use emojis. JSON commands have no decorative
prefixes. `version` alone remains a plain version string. Failures go to stderr.
Exit codes are `0` for success/help, `1` for operational failure and `2` for
usage/configuration errors; some plan load failures also return `1`.
Cancelled commands can return `130` (SIGINT), `143` (SIGTERM), or `129` (SIGHUP).

Remote commands use the saved active control connection unless overridden by
`MODELGAUGE_CONTROLLER_URL` / `MODELGAUGE_ADMIN_TOKEN`. Commands exposing
`--controller` / `--admin-token` accept explicit overrides. Prefer environment
or private saved credentials over tokens in shell history.

## 🏗️ Control plane

```sh
modelgauge control bootstrap --provider lima --source /absolute/checkout
modelgauge control status
modelgauge monitoring open
```

| Bootstrap flag | Default | Meaning |
|---|---|---|
| `--provider` | `lima` | Only automated provider |
| `--name` | saved active VM or `modelgauge-control` | Create/reuse VM |
| `--source` | auto checkout for development | Build controller locally using Go |
| `--timeout`, `--readiness-timeout` | `60m`, `10m` | Overall bootstrap and per-deployment rollout deadlines |
| `--helper-binary` | automatic matching Linux executable | Development override for the Linux guest helper when using an image without source |
| `--image` | matching release image for release CLI | Explicit prebuilt controller reference |
| `--cpus`, `--memory`, `--disk` | `4`, `6`, `50` | New VM resources; memory/disk in GiB |
| `--port` | saved API port or `18080` | Host loopback API port |
| `--k3s-version` | `v1.37.1+k3s1` | Pin for a new k3s installation |
| `--benchmark-target` | empty | Optional existing benchmark exporter `HOST:9100` |

Use `--source` or `--image`, not both. Development builds need source or an
explicit image outside a checkout. Published releases automatically resolve the
matching digest-pinned controller. Resource flags do not resize existing VMs.
`monitoring open` uses port 3000 and opens the overview if a browser opener exists.

## 🖥️ Nodes

```sh
modelgauge node bootstrap --provider lima --name benchmark-node --source /absolute/checkout
modelgauge node list
modelgauge node status benchmark-node
modelgauge node reconnect --name benchmark-node
# Run inside a Linux node, not on macOS:
modelgauge node inspect
```

Node bootstrap flags: `--provider lima`, `--name benchmark-node`, `--cpus 2`,
`--memory 3`, `--disk 30`, `--source REPO`, `--agent-binary LINUX_EXECUTABLE`,
`--llama-revision FULL_COMMIT`, `--timeout 45m`. Release CLIs download a matching Linux agent;
source builds compile it. The pinned llama.cpp revision is
`eb4e244be77a906a183cd4a00fd51d9ad3200955`. Node management queries return JSON.

## 📦 Models

```sh
modelgauge model search qwen2.5
modelgauge model install qwen2.5-1.5b-instruct:q4_k_m --node benchmark-node
modelgauge model install status JOB_ID
modelgauge model install cancel JOB_ID
modelgauge model list --node benchmark-node

# Advanced URL: supply a real file URL and its expected hash.
modelgauge model install --id custom-model --url HTTPS_GGUF_URL \
  --sha256 EXPECTED_SHA256 --node benchmark-node
```

- `search [QUERY]`: network-backed metadata for matching built-in catalog entries.
- `install MODEL_ID`: catalog resolution; without `--node`, installation is local to this CLI host.
- `install --id ID --url URL [--sha256 HASH]`: explicit source. Provide a trusted checksum for reproducibility.
- `install status JOB_ID`: JSON job state; no byte-progress API currently.
- `install cancel JOB_ID`: JSON cancellation response.
- `list --node ID`: JSON inventory with node-specific paths suppressed.

`--controller` and `--admin-token` are available for remote install/list/status/cancel.
A completed install may need one more agent poll before inventory is updated.

## 🧪 Declarative experiments

All workload settings belong in a versioned YAML file; connection credentials
remain in the saved control configuration, environment, or connection flags.
Start with [examples/benchmark.yaml](../examples/benchmark.yaml).

```sh
modelgauge benchmark profiles list
modelgauge validate -f benchmark.yaml
modelgauge apply -f benchmark.yaml --dry-run
modelgauge apply -f benchmark.yaml
modelgauge status -f benchmark.yaml
modelgauge cancel -f benchmark.yaml
```

| Command | Behavior |
|---|---|
| `validate -f FILE` | Strict YAML/module validation and installed node/model resolution; no jobs |
| `apply -f FILE --dry-run` | Same preflight; prints fully resolved plans and defaults as JSON |
| `apply -f FILE` | Atomically save the named experiment and queue every node/model pair |
| `status -f FILE` | Read saved plans and their current statuses |
| `cancel -f FILE` | Cancel every queued/active plan; completed plans and evidence remain |

All five return JSON. `--file` is an alias for `-f`. All accept `--controller` and
`--admin-token`; only apply accepts `--dry-run`. There are no workload override
flags such as `--threads`: edit the file instead. The commands are one-shot.

Names are controller-wide immutable run identities. Reapply the same declaration
from any CLI to get the original job IDs and frozen plans. An edited declaration
under the same name fails; create a new name for another run. Comments and key
order do not affect identity. Status/cancel also reject a file whose specification
has changed. Cancellation uses the existing lease mechanism; a running worker
stops after receiving cancellation, so it is not instantaneous. Closing the CLI
after apply does not cancel accepted work.

The controller resolves execution mode, model paths and checksums from inventory.
Every requested model must already be installed on every requested node. There
are no implicit downloads, VM changes, or inference endpoints in the declaration.
See [schema, defaults and examples](BENCHMARKING.md).

## 📋 Low-level JSON tools and historical compatibility

Flag-based `benchmark campaign create` and `benchmark plan create` are retired;
use YAML. Existing JSON plan APIs, stored results, exports, and historical
`benchmark campaign status --file campaign/campaign.json` remain supported.
Advanced diagnostics can still use `plan example`, `plan validate FILE`,
`benchmark submit --plan FILE`, `benchmark cancel PLAN_ID`, and Linux-only
`benchmark run --plan FILE --output DIR`. These tools do not create named
experiments; prefer the YAML flow for normal benchmarking.

## 📈 Results

```sh
modelgauge results show --node benchmark-node
modelgauge results recommend \
  --node benchmark-node --model qwen2.5-1.5b-instruct:q4_k_m \
  --execution-mode lima-linux --context 512 --memory-budget-gib 2
```

Both return JSON. Recommendation flags require `--node`, `--model`,
`--execution-mode` and positive `--context`. `--memory-budget-gib` is optional;
without it the fit status is conditional. The chosen source is a completed run
with an exact-context prompt case, not an extrapolation to an unmeasured context.
See [methodology](BENCHMARKING.md) before using fit for placement decisions.

## ⚙️ Agent and server

```sh
# Linux agent: token is read from MODELGAUGE_AGENT_TOKEN.
modelgauge agent install --controller http://PRIVATE_CONTROL:31080 \
  --node linux-node --kind physical --execution-mode native-isolated
modelgauge agent status
modelgauge agent uninstall

# Foreground run reads MODELGAUGE_* variables from its environment.
modelgauge agent run --once
modelgauge agent run --interval 30s

# Requires configured PostgreSQL URL and tokens.
modelgauge server --listen :8080 --migrate=true
modelgauge version
modelgauge version --json
```

Agent install also accepts `--name`, `--token`, `--results-dir` and `--interval`.
The default install result path is `~/modelgauge-results`; Lima bootstrap chooses
its managed path instead. Uninstall removes the service and retains files.
`agent run --once` attempts at most one work cycle, not the entire queue.
The server applies embedded migrations by default. Structured server/agent logs
remain JSON on stderr. See [configuration](CONFIGURATION.md) for environment variables.

## 🧹 Cleanup (current source build)

```sh
modelgauge cleanup --dry-run --delete-vms
modelgauge cleanup
modelgauge cleanup --delete-vms
```

Default cleanup removes verified managed guest paths and owned user services but
keeps VMs. `--delete-vms` additionally deletes only VMs created by this tracked
bootstrap. A dry run never starts stopped VMs. JSON reports distinguish reused,
untracked and owned targets. Pending/active work and ownership mismatches block
cleanup. Legacy v0.1.0 installations cannot be automatically adopted.
See [ownership and retry details](operations/CLEANUP.md).

## 📦 Export (current source CLI and controller)

```sh
modelgauge export --node benchmark-node --output exports/results
modelgauge export --node benchmark-node --run RUN_ID --output exports/one-run
modelgauge export --node benchmark-node --output exports/evidence --include-raw
modelgauge export --node benchmark-node --output exports/history \
  --prometheus-url http://127.0.0.1:9090 \
  --start 2026-11-07T10:00:00Z --end 2026-11-07T11:00:00Z --step 30s
```

`--output` must not exist. The bundle includes JSON/CSV results, node/plan/run
metadata, current controller-wide metrics and checksums. Use `--include-raw` for
a tracked running Lima node, or `--raw-from DIRECTORY` for copied local run
folders. History is optional and uses explicit RFC3339 times; repeat `--query`
to replace default PromQL selectors. Normal controller/admin overrides apply.
See [export contents, limits and verification](operations/EXPORT.md) before cleanup.

## 🔌 Controller execution module

Execution starts automatically with the bundled controller deployment. After
`node bootstrap`, submit model installs or YAML experiments and query their status.
The host CLI can exit while work continues. `executor run` has been removed.

For a manually deployed controller:

```sh
modelgauge server --listen :8080 --executor-state-dir /persistent/modelgauge
```

The directory stores private SSH connections and durable upload journals and must
survive process/container replacement. Node connections use authenticated admin
API routes documented in [API.md](API.md). The bundled deployment mounts its PVC
at `/var/lib/modelgauge` and supplies this setting automatically.

`deployment` remains the default profile: one direct stage, then serving stages.
Choose `standard-cpu` or `node-serving` for independent measurements.

`node reconnect --name NODE` refreshes a prepared managed Lima node’s current SSH
address and credentials without rebuilding llama.cpp or reinstalling models. The
node must be running and its executor must have no active or pending work. Queued
jobs remain queued until the refreshed connection succeeds. Node status includes
`executor.address`, `last_error`, `last_attempt_at` and `last_success_at` when available.
