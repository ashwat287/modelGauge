# 🧪 Benchmark design and interpretation

## 📋 Declare one experiment

Save this as `benchmark.yaml` (also available as
[examples/benchmark.yaml](../examples/benchmark.yaml)):

```yaml
apiVersion: modelgauge.io/v1alpha1
kind: Benchmark
metadata:
  # An experiment name identifies one immutable run. Change it for a new run.
  name: qwen-baseline-001
spec:
  nodes: [benchmark-node]
  # Install every model on every listed node before applying.
  models: [qwen2.5-1.5b-instruct:q4_k_m]
  matrix:
    threads: [1, 2]
    contexts: [512]
  # Stages execute in this order for each node/model pair, under one lock.
  stages:
    - profile: standard-cpu
      options:
        warmups: 3
        repetitions: 5
    - profile: node-serving
      options:
        warmups: 2
      config:
        suite: basic
        concurrency: [1, 2]
        requests: 10
        output_tokens: 128
```

```sh
modelgauge validate -f benchmark.yaml
modelgauge apply -f benchmark.yaml --dry-run
modelgauge apply -f benchmark.yaml
modelgauge status -f benchmark.yaml
# Stop queued/active work, preserving completed evidence:
modelgauge cancel -f benchmark.yaml
```

The controller resolves installed model paths, checksums and node execution mode;
users do not need to know node directories. Install every model on every listed
node first. Preflight validates the entire matrix before atomically saving the
experiment and queuing any jobs. Dry-run returns the resolved plans and defaults
without creating jobs. It does not launch a model or prove thermal stability.

`metadata.name` identifies one immutable measurement across CLI machines. Reapply
the same file to retrieve the same jobs, including after an interrupted submission
or lost response. Editing the specification under that name fails. Change the name
for a new run. Comments and YAML key order do not change identity. The controller
keeps the original specification, resolved defaults, model paths/checksums and job
IDs. Retries reuse that snapshot even if inventory or module defaults change.
Status and cancel compare the file with the saved specification. Existing named
experiments remain queryable/cancellable if a module is removed from the registry.

Each node/model pair becomes one ordered suite. Stages expand through the benchmark
registry: `standard-cpu` adds direct throughput/RSS, `node-serving` adds a temporary
server per thread setting, and `deployment` is a convenience profile combining
both. Suites from a profile are flattened into the requested order. The same
GGUF/checksum is used by every stage. One node lock spans the whole suite; each
runtime exits and is reaped before the next starts. Failure/cancellation prevents
later stages. Different models on one node are serialized; separate physical
nodes can run independently. VMs on the same host still share its thermal/memory
budget. The local load generator shares the measured VM's CPU and RAM.

### ⚙️ Declaration fields and defaults

- `apiVersion: modelgauge.io/v1alpha1`, `kind: Benchmark`: versioned user schema.
- `metadata.name`: controller-wide name, 1–63 lowercase letters/digits/hyphens,
  beginning with a letter or digit.
- `spec.nodes`, `spec.models`: unique IDs, 1–32 each, at most 128 pairs.
- `spec.matrix.threads`: defaults to `[1, 2]`; unique positive integers, at most 16.
- `spec.matrix.contexts`: defaults to `[512, 4096, 8192]`; unique positive integers,
  at most 16. For node-serving prompt fixtures, use an explicit `contexts: []`.
- `spec.stages`: ordered profiles, 1–32 expanded module plans per pair.
- `stages[].options.warmups`: positive warmup count; default 3 direct repetitions
  or 2 serving requests. These prepare caches/load, not guaranteed thermal equilibrium.
- `stages[].options.repetitions`: measured direct repetitions, default 5.
  Serving uses `config.requests` instead; an unsupported repetitions setting fails.
- `stages[].config`: module-owned YAML object. Unknown settings fail in that module;
  `standard-cpu` currently accepts no config. Examples below cover node-serving.

YAML must contain one document, at most 2 MiB. Duplicate/unknown fields, wrong
scalar types, aliases/merges, and unsupported tags fail. Expanded snapshots are
limited to 16 MiB. There is no templating, file watching, CRD/operator, implicit
model download, credential field, VM sizing, or inference URL in the declaration.
The existing JSON execution protocol remains unchanged. Flag-based campaign/plan
creation is retired; historical JSON plans, results and exports remain supported.

Results contain separate typed stage reports. Direct cases and memory remain
available to the existing recommendation logic. Exports include `suite-results.csv`
with complete stage JSON; serving Prometheus metrics carry `stage` labels. Raw
artifacts are stored under stage directories in the node's run evidence bundle.
The local load generator shares the measured VM's CPU and RAM with the model.


[Documentation home](../README.md) · [CLI reference](CLI.md) · [Recovery](operations/JOB-RECOVERY.md)

The [historical benchmark report](HISTORICAL-BENCHMARKS.md) includes archived
Linux VM measurements, limitations, and original raw evidence in Markdown.

## 🎯 Choose the question first

Compare one model at multiple quantizations, compare thread counts on one node,
or compare nodes under the same workload. Record exact model files/checksums,
llama.cpp revision/build, execution mode, CPU allocation and case shapes. Changing
these changes the experiment. A higher quantization can be faster on a particular
CPU/kernel; file size alone does not predict throughput.

Start with a single short-context experiment. Add contexts and models after the
workflow is healthy. The default large matrix can take a long time on the Intel Air.

## 📋 Standard CPU matrix

The `standard-cpu` profile uses these defaults:

| Dimension | Default |
|---|---|
| Prompt lengths | 512, 4096, 8192 tokens |
| Threads | 1 and 2 |
| Warm-up repetitions | 3 per throughput case |
| Measured repetitions | 5 per throughput case |
| GPU layers | 0 |
| Generation cases | 128 generated tokens, zero prompt, each thread count |
| Memory workload | largest prompt, 128 generation tokens, highest thread count, one repetition |

For three contexts and two thread counts there are six prompt cases, two
generation cases and one separately timed memory workload per model. Warm-up
files are separate from measured files. The profile is synthetic throughput,
not a chat conversation or quality evaluation.

## 🌡️ Warm is not automatically steady

Fixed warm-ups prepare caches and apply load. They do not ensure temperature,
frequency, fan speed or power limits have settled. ModelGauge does not currently
read host thermal sensors or reject runs based on a thermal-equilibrium rule.
Pre/post snapshots record lightweight Linux facts and swap output, not complete
thermal telemetry or continuous memory-pressure history.

Before a baseline:

1. Keep power mode, VM sizing, runtime and model revision fixed.
2. Stop unrelated VMs/jobs, model serving and heavy host applications.
3. Prevent sleep during the experiment using your host's normal controls.
4. Inspect raw repetition throughput in order, not only mean/stddev.
5. Repeat a drifting case after sustained load and label the thermal condition.
6. Repeat comparisons in a different order to check ordering effects.

A low standard deviation over five samples is evidence of local consistency,
not proof that a machine will sustain the same rate for hours. Do not silently
replace failed or sleep-contaminated results; retain their reason and rerun with
a new output directory. Recovered runs retain interruption metadata.

## 📏 Throughput and variability

llama-bench emits raw `avg_ts`, `stddev_ts`, `samples_ts` and timing samples.
Normalized result cases retain mean throughput and standard deviation. Compare
prompt-only cases with matching prompt lengths and generation-only cases with
matching generation settings. A value of zero standard deviation from one sample
does not establish stability.

The recommender currently accepts a case when:

```text
mean tokens/s > 0
relative standard deviation = stddev tokens/s / mean tokens/s <= 0.10
```

It selects the latest completed run with an exact requested prompt context,
then the fastest qualifying prompt and generation cases within that run.
It does not search all older runs when the newest eligible run is unstable,
and it does not currently enforce a minimum sample count beyond plan validation.
Use at least the standard repetitions for comparisons; keep the raw samples.

## 🧪 Serving benchmarks on the measured node

`node-serving` uses the installed GGUF model inventory and the normal controller/agent queue.
The CLI does not generate inference traffic. The benchmark module starts a temporary
`llama-server` on the measured Linux node, binds it to a free loopback port, verifies the
model checksum and records the runtime checksum, warms it up, measures it and stops only its own process group.
The model process and load generator share the node's resources; these measurements
include the overhead of that local load generator.

New node bootstraps build llama-server alongside llama-bench. Existing nodes need an
updated agent/runtime: rerun `node bootstrap` for the same managed node before using this
profile. No Kubernetes manifests, database migrations or additional services are needed.
Use matching updated CLI, controller and agent binaries. v0.3.0's standalone endpoint
commands are replaced by YAML declarations; old stored reports still export normally.

For serving only, keep just the `node-serving` stage in a new named YAML file:

```sh
modelgauge apply -f serving.yaml --dry-run
modelgauge apply -f serving.yaml
modelgauge status -f serving.yaml
modelgauge results show --node benchmark-node
```

Each thread count gets its own process/stage. Models, including different quantizations,
are tested sequentially by the agent. Each process has parallel slots matching the
highest requested concurrency; configured context is per slot and the server receives
its total across slots. Automatic runtime fitting and the additional RAM prompt cache
are disabled; normal per-slot KV reuse is still available to explicit cache probes.
CPU-only is the default. Context is allocated to fit the largest
requested input plus output/headroom, so this profile consumes more RAM than llama-bench.
Start with a small model, context and concurrency on an 8 GiB host.

### ⚙️ Module configuration

Place these settings under a node-serving stage’s `config` object (JSON notation
is also valid inside YAML):

```json
{
  "api": "llama.cpp",
  "suite": "deployment",
  "concurrency": [1, 2],
  "requests": 10,
  "output_tokens": 128,
  "sustain_seconds": 60,
  "request_timeout_seconds": 120,
  "readiness_timeout_seconds": 300,
  "sample_interval_seconds": 1,
  "gpu_layers": 0,
  "targets": {"ttft_ms": 3000, "tpot_ms": 200, "latency_ms": 60000}
}
```

Include the object directly under `spec.stages[].config`; no separate config file is needed.
An optional `cases` array replaces the generated case suite, so you can select or remove
workloads without changing platform code. Case fields follow `benchmark.config.cases`
in the generated plan. The targets above are examples, not universal performance requirements. `basic` runs only
the context/concurrency load cases. `deployment` also runs sustained load, an open-loop
burst with recovery, cache-on/cache-off probes (native API), a format smoke check,
cancellation/recovery and idle-resume. Selecting `api: "openai"` uses chat completions
and replaces the explicit cache probes with a conversation check.

Cases are sequential. Closed-loop load caps in-flight requests at concurrency. Burst
cases schedule arrivals independently and count excess arrivals as client capacity drops.
In-flight requests drain under their deadlines. Throughput excludes warmups, startup,
idle delays and recovery probes but includes request drain. A completed run may still
contain failed requests or failed fixture checks: inspect the report before comparing.

Process startup is measured from launching the process until readiness, including runtime
initialization/model loading. It does not establish cold filesystem cache. Shutdown time
measures process termination, not a separately instrumented model-unload operation.
Cancellation, lease loss and errors stop the owned model process and retain evidence.
Linux parent-death signaling also stops that process if the agent is abruptly killed.
Recovery starts a fresh model process and reruns cases, archiving earlier evidence rather
than merging different process/cache states.

### ⏱️ Reported measurements

| Measurements | Definition |
|---|---|
| TTFT | Request dispatch to first generated content/reasoning/tool fragment; role-only chunks and pings are ignored |
| First answer | Dispatch to first visible content fragment; separate from reasoning/tool activity |
| End-to-end latency | Dispatch to completed stream |
| TPOT | First-to-last productive streamed output interval divided by reported output tokens minus one; null without sufficient token usage |
| Stream chunk gaps / longest stall | Client-observed gaps between productive streamed chunks |
| Observed token gaps | Available only when native stream token IDs establish one token per event and agree with final token usage |
| Goodput | Successful requests satisfying all enabled latency targets and any fixture checks, divided by measured wall time; null with no targets or unknown required timings |
| Token rates | Successful input/output tokens divided by measured wall time, only with complete successful-request token accounting |
| Reliability | Successful, errored, timed-out, incomplete, cancelled, output-limited, context-truncated and client-dropped request counts |
| Cache reuse | Reported cached tokens, requests reporting cache accounting, and requests with known cache hits |
| Quality | Counts and ratio passing the supplied fixture assertions; null when there are no checks |
| Sustained windows | Completed-request counts, mean TTFT and completed-output token rates in up to 64 time windows; not live per-token rates |
| Cancellation | Client cancellation acknowledgement delay and a separately timed post-load recovery request; does not attest that backend work was reclaimed |

Latency distributions include count, mean, sample standard deviation, min/max,
and nearest-rank p50/p90/p95/p99. Small-sample percentiles are descriptive; the
report warns below 100 successful requests. No confidence intervals or universal
stability claims are fabricated. Raw request timestamps and stream timings are
preserved in `CASE.requests.jsonl`; warmups are separate.

### ✅ Task-specific quality fixtures

Provide a JSON array with `id`, either `text` or `messages`, and optional `expect`.
Checks support exact text, required/forbidden substrings, a Go regular expression,
JSON validity, recursive JSON-object subsets, and exact tool name/JSON-argument
matches. Tool declarations use the OpenAI-compatible API; tools are never executed.
The format smoke case is not a general reasoning, factuality or coding score.

```json
[
  {
    "id": "extract-city",
    "messages": [{"role": "user", "content": "Return only JSON with the city from: Alice lives in Paris."}],
    "expect": {"json": true, "json_subset": {"city": "Paris"}}
  }
]
```

Include the fixture array as `prompts` in the module configuration, select
`api: "openai"` for chat messages/tools, and use `matrix.contexts: []` to disable synthetic
inputs. Prompt text and checks are stored in the private plan. Generated outputs/tool
arguments are omitted from raw evidence unless `record_outputs: true` is configured.


Hand-edit `benchmark.config.cases` to build task mixes or conversation sequences. With
`continue_conversation: true`, each closed-loop worker retains its own history,
cycles prompts in order and resets history at the start of each fixture cycle.
Failed requests count as failed fixture evaluations when checks are present.

### 💾 Resource observations and unavailable measurements

The module enables the owned server's metrics endpoint and samples its queue/KV/cache
metrics from a bounded allowlist. Labels are discarded; queue gauges sum series and
other selected gauges retain maxima. Histograms do not become invented per-request
queue latency. No monitoring deployment or scrape configuration changes are required.

The managed model PID automatically supplies process RSS/PSS, lifetime peak RSS, CPU
time, I/O, page faults and context switches. Available node RAM/swap, CPU frequency,
temperature, readable CPU-package energy and existing NVIDIA telemetry are sampled too.
PID start identity prevents silently sampling a replacement process. Missing sensors or
permissions remain unavailable; VM telemetry describes the guest, not the physical host.

Sampled RSS peaks are lower-bound observations, not guaranteed maximum RSS.
Lifetime high-water RSS can predate this case. Node/GPU power includes other
workloads; package energy and GPU energy are separate and are never added
blindly. Observation windows bracket the workload and can include collection
overhead: inspect `observation_seconds`, sample timestamps and the chosen
sampling interval. These are gross node energy observations, not idle-subtracted
model energy. Process measurements belong to the owned model PID on the benchmark node. Missing sensors/permissions/token usage are
null or explicitly unavailable, never zero-valued substitutes.

Separate cold-disk load timing, per-request backend queue instrumentation and general
semantic quality grading remain unavailable. Neither cancellation acknowledgement nor
shutdown duration proves a particular backend memory-reclamation policy.

### 📁 Results, export and interruption

`result.json` contains a `benchmark` envelope with `kind: "serving"` and the report
under `data`; `serving-summary.json`, per-case
request/warmup/resource JSONL files and case checkpoints retain evidence. Existing
engine-only result CSV remains unchanged. Node export adds `serving-results.csv`
when serving data exists, and `results.json` contains the complete report.
Summary data uses the existing database JSON fields, with no schema migration.
Serving measurements never feed the engine thread/memory-fit recommender.
Historical results and stored reports remain readable; old endpoint plans must be regenerated as managed-node plans.
Summaries are limited to 2.5 MiB for the existing completion API. If a summary
exceeds that limit, the run fails explicitly and retains the detailed report on
the execution node; the agent uploads the failure and evidence hashes instead
of repeatedly retrying an oversized payload. Split large matrices into plans.

The controller exposes `modelgauge_serving_requests`,
`modelgauge_serving_latency_seconds` (measurement/statistic labels), request/token
throughput, goodput, fixture pass ratio, sampled RSS and CPU-package energy gauges.
They describe the latest completed serving run per node/model/execution mode,
not continuous inference speed. Existing monitoring collects `/metrics` normally.

On interruption, owned HTTP requests close, the owned model process stops, and partial
evidence is retained. Agent recovery archives prior case files and remeasures all cases
with a fresh process. A local direct run requires a fresh output directory. Keep raw
evidence and use a new declaration name for a new experiment.

### 🔧 Adding or removing a benchmark

Benchmark modules live in `internal/benchmark`. Add a module and tests, then one
entry in `implementations` in `registry.go`. The module owns its profile name and planner, config validation,
execution with the supplied context, result validation, and optional CSV/metrics
formatters. Use the common `benchmark.kind/config` and `benchmark.kind/data`
envelopes; the existing `benchmark run` and submission paths dispatch them.
No agent, controller, database schema, exporter or infrastructure branch is needed.
Removing an entry rejects new jobs of that kind; historical JSON stays readable
and exportable, while its removed CSV/metrics formatter is skipped. Module-specific
settings arrive through the YAML stage’s `config`; there is no benchmark-specific CLI to update.
Existing engine fields remain for compatibility.

## 💾 Memory measurement

GNU `/usr/bin/time -v` records maximum resident set size for its child workload.
Linux's reported kbytes are multiplied by 1024 to store `peak_rss_bytes`.
The runner computes `safe_reservation_bytes = ceil(peak_rss_bytes × 1.25)`.

This is process RSS, not the total VM or host footprint. It excludes guest OS
and unrelated services, and it is not equivalent to GGUF file size, allocated
context capacity, or all future requests. The `n_prompt=P, n_gen=G` flags in the
current memory command produce llama-bench prompt/generation tests; they do not
by themselves prove a populated-P-token decode session. Check the raw JSON row
shapes when interpreting long-context claims. The normal generation cases begin
with zero prompt, so they do not measure long-context decode throughput.

The standard profile stores one memory measurement at its largest selected
context/highest selected threads. The current recommendation reuses that run's
reservation; it does not interpolate a context-memory curve. To compare memory
at several prompt sizes, create separate one-context declarations with different
names (for example `memory-512` and `memory-4096`), a `standard-cpu` stage, and
`spec.matrix.contexts: [512]` or `[4096]`:

```sh
modelgauge apply -f memory-512.yaml
modelgauge apply -f memory-4096.yaml
```

Inspect memory workload files and future combined-prefill/decode experiments
before treating these measurements as serving-context guarantees.

## 📁 Evidence on the measured node

A run directory normally includes:

Fresh engine runs require a new output directory. Case IDs must be simple
filenames and must not collide with other evidence files or reserved names such
as `plan`, `result`, `preflight`, and `postflight`.

```text
plan.json                  exact executed plan
runtime.sha256             runtime executable identity
preflight.json             original lightweight node snapshot / swap
CASE-warmup.json            raw warm-up samples
CASE.json                  raw measured samples
MEMORY_CASE.json            raw memory-workload output
MEMORY_CASE.time.txt        GNU time report
memory.checkpoint.json     reusable memory checkpoint
postflight.json            final node snapshot
result.json                normalized result and artifact hashes
```

Not every file is present after an early failure. The runner captures its raw
JSON/timing files; inspect the implementation/result directory for exact IDs.
Artifact names and hashes are included in normalized results; raw contents stay
on the node and are not uploaded as a downloadable evidence bundle. Preserve
both PostgreSQL records and node files for an auditable experiment.

## 🧮 Reading memory fit

`results recommend` returns:

| Field | Meaning |
|---|---|
| `source_run_id` | Run supporting this recommendation |
| `prompt_threads`, `generation_threads` | Independently chosen stable case threads |
| `required_memory_bytes` | Stored reservation from that run |
| `memory_budget_bytes` | Caller-supplied usable budget |
| `fit_status` | `fits`, `does-not-fit`, or `conditional` without a budget |
| `algorithm_version` | Current deterministic algorithm identity |

This is a measured configuration recommendation, not a general hardware capacity
predictor or an active scheduler. A larger context, different KV type, GPU backend,
serving concurrency or another runtime needs new measurements. Quality differences
between quantizations must be evaluated separately.
