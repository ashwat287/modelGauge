# 🧰 CLI reference

[Documentation home](../README.md) · [First benchmark](GETTING-STARTED.md) · [Configuration](CONFIGURATION.md)

## 🧭 Conventions

`modelgauge <command> --help` prints flags for flag-bearing commands;
`modelgauge help benchmark` prints a group overview. Commands use Go-style flags:
put a positional model ID **before** its flags, as in `model install MODEL_ID
--node NODE_ID`. Node IDs, job IDs, file names and URLs in uppercase are placeholders.
Choose a fresh campaign output directory rather than overwriting evidence.

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

## 🧪 Benchmark campaigns

```sh
modelgauge benchmark profiles list
modelgauge benchmark campaign create \
  --node benchmark-node --execution-mode lima-linux \
  --models qwen2.5-1.5b-instruct:q4_k_m,qwen2.5-1.5b-instruct:q5_k_m \
  --contexts 512,4096 --threads 1,2 --output campaigns/comparison --submit
modelgauge benchmark campaign status --file campaigns/comparison/campaign.json
```

`create` resolves **installed model IDs**, not paths or arbitrary search terms.
It requires controller access even without `--submit`. Defaults are contexts
`512,4096,8192`, threads `1,2`, execution mode `native-isolated`, and an automatically
named local output directory. Set `--execution-mode lima-linux` for Lima targets.
Optional `--binary` selects a llama-bench path on the target. `--submit` queues
one plan per model; without it, plans are written only. Repeated `--submit`
creates new jobs. A campaign is not an atomic database batch: a later failure
can leave earlier plans queued. Use `--resume` with the same output and flags
to reconcile saved request IDs without duplicating accepted plans.

`status --file FILE` reads its local campaign manifest and queries submitted plan
states. It returns JSON; it does not block until completion or cancel the campaign.
Cancel individual plan IDs with `benchmark cancel PLAN_ID`.

## 📋 Single plans and direct runs

```sh
modelgauge plan example > plan.json
modelgauge plan validate plan.json

# Run creation where this GGUF file is readable.
modelgauge benchmark plan create \
  --node linux-node --model /absolute/path/model.gguf --model-id custom-model \
  --profile standard-cpu --execution-mode native-isolated \
  --contexts 512,4096 --threads 1,2 --warmups 3 --repetitions 5 \
  --output custom-plan.json
modelgauge benchmark submit --plan custom-plan.json

# Or run locally on Linux, bypassing the controller:
modelgauge benchmark run --plan custom-plan.json --output results/local-example
modelgauge benchmark cancel PLAN_ID
```

`plan example` is an editable JSON template; its placeholder paths are not executable.
`plan validate` checks structure, not model existence or thermal state.
`benchmark plan create` hashes a file visible to the CLI, so a remote node-only
path cannot be used from the host. Use campaigns for managed remote models.
Available flags also include `--binary` and `--overwrite` (explicit replacement
of an existing generated plan); `--profile` currently accepts only
`standard-cpu`. Generated zero `--warmups` / `--repetitions` values fall back to
profile defaults; use a custom validated plan for other supported case shapes.

`benchmark submit` returns JSON with `plan_id` and queue status. `benchmark run`
prints a human completion/artifact path and writes evidence locally; it does not
upload results to PostgreSQL. Local execution requires Linux, GNU `/usr/bin/time`,
a readable GGUF and a working llama-bench. Direct execution uses the explicit `runtime.binary` or `llama-bench` on PATH.
The polling agent additionally discovers the managed runtime before execution.

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
