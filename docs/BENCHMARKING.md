# 🧪 Benchmark design and interpretation

[Documentation home](../README.md) · [CLI reference](CLI.md) · [Recovery](../deploy/JOB-RECOVERY.md)

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
