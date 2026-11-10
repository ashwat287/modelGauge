# 🧪 Benchmark design and interpretation

[Documentation home](../README.md) · [CLI reference](CLI.md) · [Recovery](operations/JOB-RECOVERY.md)

The [historical benchmark report](HISTORICAL-BENCHMARKS.md) includes archived
Linux VM measurements, limitations, and original raw evidence in Markdown.

## 🎯 Choose the question first

Compare one model at multiple quantizations, compare thread counts on one node,
or compare nodes under the same workload. Record exact model files/checksums,
llama.cpp revision/build, execution mode, CPU allocation and case shapes. Changing
these changes the experiment. A higher quantization can be faster on a particular
CPU/kernel; file size alone does not predict throughput.

Start with a single short-context campaign. Add contexts and models after the
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

## 🌐 Benchmarks against an existing inference endpoint

These commands are in the current source; v0.2.2 predates serving benchmarks.
Build the updated application to use them. No Lima, Kubernetes, monitoring,
database schema or inference-server provisioning changes are required. Queued
plans require matching updated controller and agent application binaries.

The serving runner connects to an endpoint you already operate. It does not
start, stop, restart, load, unload or reconfigure an inference server. Model
selection is a request to an already configured endpoint/router, not a verified
model-switch operation. Requested and observed model aliases are recorded.

### 🚀 Small serving experiment

Use the model alias accepted by your existing server. The endpoint must be
reachable from the machine executing the benchmark. For an agent job, localhost
means the agent's Linux node, not your CLI laptop or controller.

```sh
go build -o bin/modelgauge ./cmd/modelgauge

bin/modelgauge benchmark serving plan \
  --endpoint http://127.0.0.1:8080 --model my-model \
  --concurrency 1,2 --requests 20 --output-tokens 128 \
  --ttft-ms 3000 --tpot-ms 200 --latency-ms 60000 \
  --output serving.json

bin/modelgauge plan validate serving.json
bin/modelgauge benchmark serving run --plan serving.json --output results/serving
bin/modelgauge benchmark serving report --file results/serving/result.json --format csv
```

The example latency targets are illustrative, not recommended universal limits.
The JSON result reports failed requests as measurements: a completed experiment
does not mean every request succeeded or its quality/latency targets passed.

For existing-node execution, generate the plan with the registered `--node` and
matching `--execution-mode`, then use the normal safe submission path:

```sh
bin/modelgauge benchmark serving plan \
  --endpoint http://127.0.0.1:8080 --model my-model \
  --node benchmark-node --execution-mode lima-linux \
  --output serving-node.json
bin/modelgauge benchmark submit --plan serving-node.json
bin/modelgauge results show --node benchmark-node
```

Provide inference authentication through `MODELGAUGE_INFERENCE_TOKEN` on the
executing client/agent. Tokens are not plan fields. An optional metrics endpoint
uses `MODELGAUGE_INFERENCE_METRICS_TOKEN`; the inference token is reused only for
the same scheme/host/port. Redirects and credentials/query strings in endpoint
URLs are refused. Generated answers and tool arguments are omitted from raw
evidence unless `--record-outputs` is selected. Prompt fixtures themselves are
stored in the private plan: treat evidence as potentially sensitive.

### 📋 Deployment suite and experiment dimensions

```sh
bin/modelgauge benchmark serving plan \
  --endpoint http://127.0.0.1:8080 --model my-model --api llama.cpp \
  --suite deployment --contexts 128,512,4096 --concurrency 1,2 \
  --requests 20 --sustain-duration 2m --request-timeout 20m \
  --metrics-url http://127.0.0.1:8080/metrics \
  --output deployment.json
```

`deployment` adds sustained load, fixed-rate bursts, a small JSON-format quality
smoke test, and cancellation followed by a recovery request. Native llama.cpp
also gets cache-off/cache-on cases. OpenAI-compatible endpoints get a small
multi-turn conversation instead: cache policy is server-controlled in that API.
Explicit native `cache_prompt` settings are never claimed for a generic OpenAI
endpoint. Add `--idle-before 1m` for a first request after idle, or
`--switch-models model-a,model-b,model-a` to observe sequential requested aliases.
Neither experiment proves a cold model load or unload occurred.

Synthetic `--contexts` require the endpoint's llama.cpp `/tokenize` and
`/detokenize` routes. Native completion uses exact input token IDs. Chat input
length is a **content-token target**: template/history overhead can add tokens;
actual server-reported usage is recorded. For other servers, supply real fixture
texts/messages and use their reported counts. Word counts are never substituted
for token counts. Compare different hardware/runtime settings using separately
configured endpoints/runs; this runner does not change server thread counts,
batch sizes, GPU offload, KV types, power modes or physical-host workloads.

Closed-loop cases keep up to `concurrency` requests active. A positive
`--request-rate` schedules arrivals independently, with concurrency as a strict
in-flight cap. Excess arrivals become **client capacity drops**, not imaginary
server requests. Dispatch delay and scheduled latency expose generator lag.
`requests` and `duration_seconds`, when both positive, stop dispatch at the first
limit; in-flight requests then drain under their request deadlines. Time-based
cases have a hard 100,000-request evidence limit. Throughput uses full measured
wall time including drain, excluding warmups, idle delay, readiness and recovery
probes. Each evidence file is bounded to 128 MiB and aggregate stream timing
observations to one million per case; exceeding a bound fails explicitly.

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

```sh
bin/modelgauge benchmark serving plan \
  --endpoint http://127.0.0.1:8080 --model my-model \
  --dataset fixtures.json --output quality.json
```

Hand-edit `benchmark.config.cases` to build task mixes or conversation sequences. With
`continue_conversation: true`, each closed-loop worker retains its own history,
cycles prompts in order and resets history at the start of each fixture cycle.
Failed requests count as failed fixture evaluations when checks are present.

### 💾 Resource observations and unavailable measurements

`--metrics-url` samples an existing endpoint's queue/KV/cache/process/GPU metrics
from a bounded allowlist. Labels are discarded; queue gauges sum labelled series,
other selected metrics retain the maximum across series. Histograms are not
converted into per-request queue latency. The metrics endpoint must already be
enabled; no scrape targets or dashboard/deployment files are modified.

When the benchmark runs on the Linux server node, an optional
`--local-server-pid PID` adds read-only process RSS/PSS, lifetime RSS high-water
mark, CPU time, I/O, page faults and context switches; node available RAM/swap,
CPU frequency and temperature; readable CPU-package energy; and optional
`nvidia-smi` GPU memory/utilization/power/temperature/clock observations. PID
start identity is checked so a replacement process is not silently measured.
The operator supplies the PID-to-endpoint association. No process is signalled.

Sampled RSS peaks are lower-bound observations, not guaranteed maximum RSS.
Lifetime high-water RSS can predate this case. Node/GPU power includes other
workloads; package energy and GPU energy are separate and are never added
blindly. Observation windows bracket the workload and can include collection
overhead: inspect `observation_seconds`, sample timestamps and the chosen
sampling interval. These are gross node energy observations, not idle-subtracted
model energy. No local PID means no client-machine hardware values are presented
as remote server measurements. Missing sensors/permissions/token usage are
null or explicitly unavailable, never zero-valued substitutes.

Cold process startup, verified load/unload times, memory bandwidth, automated
hardware tuning and general semantic-quality scoring remain outside this
endpoint-only benchmark. Physical-host telemetry is not inferred from a VM.

### 📁 Results, export and interruption

`result.json` contains a `benchmark` envelope with `kind: "serving"` and the report
under `data`; `serving-summary.json`, per-case
request/warmup/resource JSONL files and case checkpoints retain evidence. Existing
engine-only result CSV remains unchanged. Node export adds `serving-results.csv`
when serving data exists, and `results.json` contains the complete report.
Summary data uses the existing database JSON fields, with no schema migration.
Serving measurements never feed the engine thread/memory-fit recommender.
Older top-level `serving` plan/results and stored reports remain readable.
Summaries are limited to 2.5 MiB for the existing completion API. If a summary
exceeds that limit, the run fails explicitly and retains the detailed report on
the execution node; the agent uploads the failure and evidence hashes instead
of repeatedly retrying an oversized payload. Split large matrices into plans.

The controller exposes `modelgauge_serving_requests`,
`modelgauge_serving_latency_seconds` (measurement/statistic labels), request/token
throughput, goodput, fixture pass ratio, sampled RSS and CPU-package energy gauges.
They describe the latest completed serving run per node/model/execution mode,
not continuous inference speed. Existing monitoring collects `/metrics` normally.

On interruption, owned HTTP requests close and partial evidence is retained;
the external server is not restarted or killed. Agent retries reuse completed
case checkpoints and archive interrupted case files before remeasurement.
Externally managed endpoint identity cannot be guaranteed unchanged across
recovery; the report records that limitation. A local CLI run requires a fresh
output directory. Keep the raw evidence and use a new plan for a new experiment.

### 🔧 Adding or removing a benchmark

Benchmark modules live in `internal/benchmark`. Add a module and tests, then one
entry in `implementations` in `registry.go`. The module owns config validation,
execution with the supplied context, result validation, and optional CSV/metrics
formatters. Use the common `benchmark.kind/config` and `benchmark.kind/data`
envelopes; the existing `benchmark run` and submission paths dispatch them.
No agent, controller, database schema, exporter or infrastructure branch is needed.
Removing an entry rejects new jobs of that kind; historical JSON stays readable
and exportable, while its removed CSV/metrics formatter is skipped. Custom CLI
plan builders are optional. Existing engine fields remain for compatibility.

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
at several prompt sizes, create separate one-context campaigns:

```sh
modelgauge benchmark campaign create --node benchmark-node --execution-mode lima-linux \
  --models qwen2.5-1.5b-instruct:q4_k_m --contexts 512 --threads 1,2 \
  --output campaigns/memory-512 --submit
modelgauge benchmark campaign create --node benchmark-node --execution-mode lima-linux \
  --models qwen2.5-1.5b-instruct:q4_k_m --contexts 4096 --threads 1,2 \
  --output campaigns/memory-4096 --submit
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
