# 🧪 Historical Linux benchmark evidence

[Documentation home](../README.md) · [Methodology](BENCHMARKING.md) · [Node bootstrap](deployment/NODE-BOOTSTRAP.md)

This report preserves the historical `benchmark-node-temp` Lima experiments
previously stored under `fixtures/lima-benchmark-node-temp/`. It replaces that
raw-file directory with a readable summary and an expandable source appendix.
These are archived observations, not current release acceptance criteria or
expected performance for another machine. No experiments were rerun to create
this document.

## 🧾 Provenance and scope

The source files were read from repository commit `62d21ffe8cbe944e50c0b3010affc2c39bc62021`.
All **27 original files** are reproduced in the appendix, with original
paths and SHA-256 hashes. JSON fields, warm-up samples, timing records and
pre/postflight snapshots are preserved rather than regenerated.

The measurements cover one Qwen2.5 1.5B Instruct Q4_K_M model in an ARM64 Linux
VM. They do not describe the Intel MacBook Air, a comparison across model
families/quantizations, or the later installation-to-cleanup validation.

## 🖥️ Recorded environment

| Item | Recorded value |
|---|---|
| Guest | `benchmark-node-temp`, Lima / Apple virtualization |
| OS / architecture | Ubuntu 25.10 / ARM64 (`aarch64`) |
| CPUs visible to guest | 2 |
| Guest RAM visible in snapshots | 2.8 GiB |
| Swap in pre/postflight snapshots | 0 B |
| Root filesystem capacity | 29 GiB |
| Baseline kernel | `6.17.0-19-generic` in the October metadata |
| November run kernel | `6.17.0-41-generic` |
| llama.cpp revision | `eb4e244be77a906a183cd4a00fd51d9ad3200955` |
| llama-bench build | 12001, reported commit `eb4e244be` |
| Backend / GPU layers | CPU / 0 |
| Recorded CMake options | `GGML_CPU_ALL_VARIANTS=OFF`, `GGML_NATIVE=OFF` |
| Model type | `qwen2 1.5B Q4_K - Medium` |
| Model tensor size reported by llama-bench | 1,111,370,240 bytes |
| Model parameter count reported by llama-bench | 1,777,088,000 |
| Throughput workloads | Prompt: 512 tokens; generation: 128 tokens with zero prompt |
| Threads | 1 and 2 |
| Repetitions | 3 warm-up samples, then 5 measured samples per throughput case |

The metadata does not establish the exact host chip, host contention, power mode,
thermal state, or an immutable model checksum. Model tensor size is the
`model_size` field, not a measurement of process RAM or necessarily the GGUF's
on-disk byte count. The build snapshot records only the options shown above;
do not infer all current bootstrap build options from it.

## 📏 Measured throughput

Values below copy the reported `avg_ts` and `stddev_ts` fields. Standard deviation
is in tokens/second; it is not a percentage or a confidence interval. Warm-up
results are kept separately in the appendix and are excluded from these tables.

### October baseline

| Workload | Threads | Mean tokens/s | Stddev tokens/s | Samples | Recorded test time (UTC) |
|---|---:|---:|---:|---:|---|
| Prompt 512 | 1 | 19.444680 | 0.112207 | 5 | `2026-10-29T18:56:13Z` |
| Prompt 512 | 2 | 33.398616 | 0.139853 | 5 | `2026-10-29T18:59:52Z` |
| Generate 128 | 1 | 15.064741 | 0.127310 | 5 | `2026-10-29T19:01:50Z` |
| Generate 128 | 2 | 23.969199 | 0.395770 | 5 | `2026-10-29T19:02:48Z` |

Original run directory: `q4-baseline-20261029T185428Z`. Its preflight started
at `2026-10-30T00:24:28+05:30`; the postflight was captured at
`2026-10-30T00:33:53+05:30`. The memory result was recorded later, as shown
in its JSON; the directory name does not establish one continuous capture window.

### November recorded run

| Workload | Threads | Mean tokens/s | Stddev tokens/s | Samples | Recorded test time (UTC) |
|---|---:|---:|---:|---:|---|
| Prompt 512 | 1 | 18.946606 | 0.102672 | 5 | `2026-11-04T14:33:26Z` |
| Prompt 512 | 2 | 30.656719 | 1.004406 | 5 | `2026-11-04T14:37:11Z` |
| Generate 128 | 1 | 13.408158 | 0.783271 | 5 | `2026-11-04T14:39:19Z` |
| Generate 128 | 2 | 19.208614 | 0.257940 | 5 | `2026-11-04T14:40:27Z` |

Original run directory: `run-acff3a63-5e17-4974-a6e2-d3a64620b4b1`.
Preflight: `2026-11-04T14:31:41Z`; postflight: `2026-11-04T14:41:46Z`.

## 🧠 Process memory measurements

GNU `/usr/bin/time -v` wrapped a separate invocation with two threads, zero GPU
layers, one repetition, `--n-prompt 512`, and `--n-gen 128`.

| Capture | Maximum RSS (GNU time kbytes / KiB) | Converted bytes | GiB | Exit status |
|---|---:|---:|---:|---:|
| October baseline | 1,229,112 | 1,258,610,688 | 1.172 | 0 |
| November recorded run | 1,229,168 | 1,258,668,032 | 1.172 | 0 |

These are process peak resident-set measurements for those exact invocations,
not total VM/host memory, a scheduler reservation, or proof of long-context fit.
Both memory JSON files contain two entries: prompt-only (`n_prompt=512`,
`n_gen=0`) and generation-only (`n_prompt=0`, `n_gen=128`). The filenames and
command flags alone must not be described as a generation measurement after
retaining a 512-token prompt. Their single-sample throughput values are not
included in the repeated-throughput tables.

## 🔎 Interpretation and limits

- Two threads had higher reported prompt and generation throughput than one
  thread within each captured run.
- The November two-thread prompt samples decreased from 31.9984 to 29.5371
  tokens/s. Fixed warm-ups did not establish thermal equilibrium; these files
  do not identify why performance changed.
- The November generation results were also lower than the October results.
  The kernel changed, and host load/temperature were not continuously recorded.
  These records alone cannot attribute the difference to a software regression.
- Swap was absent in the guest snapshots and GNU time recorded zero swaps.
  This does not establish that the macOS host avoided compression or swapping.
- The standalone `results/q4-generation-t2-warmup.json` is an additional warm-up
  capture. It is not a measured baseline case and is not pooled into either run.
- Raw samples are retained as recorded, including potentially unstable samples.
  These experiments do not assess answer quality, HTTP latency, concurrent
  serving, or throughput at larger contexts.

## 📚 Original evidence appendix

Each expandable entry contains one original text/JSON file. Paths are historical
provenance, not paths required in a current node installation. Hashes apply to
the original file bytes, excluding Markdown wrappers. Recover an original file
from Git when machine-readable input is needed:

```sh
git show 62d21ffe8cbe944e50c0b3010affc2c39bc62021:fixtures/lima-benchmark-node-temp/metadata/llama-build.txt
```

<details>
<summary>metadata/llama-build.txt</summary>

Original path: `fixtures/lima-benchmark-node-temp/metadata/llama-build.txt`  
SHA-256: `07d85e9e55c3e80775be1e3e7ef58497e810ef9b5afa7a24719bc77cb02feb74`

```text
2026-10-30T00:09:25+05:30
eb4e244be77a906a183cd4a00fd51d9ad3200955
CMAKE_CXX_COMPILER:FILEPATH=/usr/bin/c++
CMAKE_CXX_COMPILER_AR:FILEPATH=/usr/bin/gcc-ar-15
CMAKE_CXX_COMPILER_RANLIB:FILEPATH=/usr/bin/gcc-ranlib-15
GGML_CPU_ALL_VARIANTS:BOOL=OFF
GGML_NATIVE:BOOL=OFF
//ADVANCED property for variable: CMAKE_CXX_COMPILER
CMAKE_CXX_COMPILER-ADVANCED:INTERNAL=1
//ADVANCED property for variable: CMAKE_CXX_COMPILER_AR
CMAKE_CXX_COMPILER_AR-ADVANCED:INTERNAL=1
//ADVANCED property for variable: CMAKE_CXX_COMPILER_RANLIB
CMAKE_CXX_COMPILER_RANLIB-ADVANCED:INTERNAL=1
```

</details>

<details>
<summary>metadata/node-baseline.txt</summary>

Original path: `fixtures/lima-benchmark-node-temp/metadata/node-baseline.txt`  
SHA-256: `b997851a40d26ec8405b27d804c0b752f7e3ebc61cc20ca99eb00ff06b0fd130`

```text
2026-10-30T00:02:05+05:30
 Static hostname: benchmark-node-temp
       Icon name: computer-vm
         Chassis: vm 🖴
      Machine ID: 830d8f9610c24689a00cef9429d24b3e
         Boot ID: 4ea4cd08f1a24a58966172709fc1fc20
    AF_VSOCK CID: 3
  Virtualization: apple
Operating System: Ubuntu 25.10
          Kernel: Linux 6.17.0-19-generic
    Architecture: arm64
 Hardware Vendor: Apple Inc.
  Hardware Model: Apple Virtualization Generic Platform
Firmware Version: 2103.100.6.0.0
   Firmware Date: Sat 2026-04-18
    Firmware Age: 6month 1w 5d
Linux benchmark-node-temp 6.17.0-19-generic #19-Ubuntu SMP PREEMPT_DYNAMIC Fri Mar  6 14:47:42 UTC 2026 aarch64 GNU/Linux
Architecture:                            aarch64
CPU op-mode(s):                          64-bit
Byte Order:                              Little Endian
CPU(s):                                  2
On-line CPU(s) list:                     0,1
Vendor ID:                               Apple
Model name:                              -
Model:                                   0
Thread(s) per core:                      1
Core(s) per cluster:                     2
Socket(s):                               -
Cluster(s):                              1
Stepping:                                0x0
BogoMIPS:                                48.00
Flags:                                   fp asimd evtstrm aes pmull sha1 sha2 crc32 atomics fphp asimdhp cpuid asimdrdm jscvt fcma lrcpc dcpop sha3 asimddp sha512 asimdfhm dit uscat ilrcpc flagm sb paca pacg dcpodp flagm2 frint i8mm bf16 bti afp sme smei16i64 smef64f64 smei8i32 smef16f32 smeb16f32 smef32f32 ebf16 sme2 sme2p1 smei16i32 smebi32i32 smeb16b16 smef16f16
NUMA node(s):                            1
NUMA node0 CPU(s):                       0,1
Vulnerability Gather data sampling:      Not affected
Vulnerability Ghostwrite:                Not affected
Vulnerability Indirect target selection: Not affected
Vulnerability Itlb multihit:             Not affected
Vulnerability L1tf:                      Not affected
Vulnerability Mds:                       Not affected
Vulnerability Meltdown:                  Not affected
Vulnerability Mmio stale data:           Not affected
Vulnerability Old microcode:             Not affected
Vulnerability Reg file data sampling:    Not affected
Vulnerability Retbleed:                  Not affected
Vulnerability Spec rstack overflow:      Not affected
Vulnerability Spec store bypass:         Vulnerable
Vulnerability Spectre v1:                Mitigation; __user pointer sanitization
Vulnerability Spectre v2:                Mitigation; CSV2, but not BHB
Vulnerability Srbds:                     Not affected
Vulnerability Tsa:                       Not affected
Vulnerability Tsx async abort:           Not affected
Vulnerability Vmscape:                   Not affected
               total        used        free      shared  buff/cache   available
Mem:           2.8Gi       436Mi       1.4Gi       1.0Mi       1.2Gi       2.4Gi
Swap:             0B          0B          0B
Filesystem      Size  Used Avail Use% Mounted on
/dev/vda1        29G  2.7G   26G  10% /
```

</details>

<details>
<summary>results/q4-baseline-20261029T185428Z/postflight.txt</summary>

Original path: `fixtures/lima-benchmark-node-temp/results/q4-baseline-20261029T185428Z/postflight.txt`  
SHA-256: `ea25fa514b6a5ec935c05253802bc43edc000b39e338be1aab7e9387b275ec4d`

```text
finished_at=2026-10-30T00:33:53+05:30
--- uptime ---
 00:33:53 up 44 min,  1 user,  load average: 1.81, 1.35, 0.74
--- memory ---
               total        used        free      shared  buff/cache   available
Mem:           2.8Gi       436Mi       223Mi       1.0Mi       2.4Gi       2.4Gi
Swap:             0B          0B          0B
--- swap ---
--- peak RSS ---
```

</details>

<details>
<summary>results/q4-baseline-20261029T185428Z/preflight.txt</summary>

Original path: `fixtures/lima-benchmark-node-temp/results/q4-baseline-20261029T185428Z/preflight.txt`  
SHA-256: `c61ec737d181d5201aac01c15462394865339c339e5c7b82d9da5d82fb2376aa`

```text
run_id=q4-baseline-20261029T185428Z
started_at=2026-10-30T00:24:28+05:30
hostname=benchmark-node-temp
cpu_count=2
--- uptime ---
 00:24:28 up 35 min,  1 user,  load average: 0.00, 0.05, 0.09
--- memory ---
               total        used        free      shared  buff/cache   available
Mem:           2.8Gi       435Mi       126Mi       1.0Mi       2.5Gi       2.4Gi
Swap:             0B          0B          0B
--- swap ---
--- disk ---
Filesystem      Size  Used Avail Use% Mounted on
/dev/vda1        29G  4.5G   24G  16% /
```

</details>

<details>
<summary>results/q4-baseline-20261029T185428Z/q4-generation-t1-warmup.json</summary>

Original path: `fixtures/lima-benchmark-node-temp/results/q4-baseline-20261029T185428Z/q4-generation-t1-warmup.json`  
SHA-256: `5ea80ccf656090b8001b846645f99deac4b60607290554a18e4c79e6fe70481a`

```json
[
  {
    "build_commit": "eb4e244be",
    "build_number": 12001,
    "cpu_info": "CPU",
    "gpu_info": "",
    "backends": "CPU",
    "model_filename": "/home/nerdylucifer.guest/benchmark-work/models/qwen2.5-1.5b-instruct-q4_k_m.gguf",
    "model_type": "qwen2 1.5B Q4_K - Medium",
    "model_size": 1111370240,
    "model_n_params": 1777088000,
    "n_batch": 2048,
    "n_ubatch": 512,
    "n_threads": 1,
    "cpu_mask": "0x0",
    "cpu_strict": false,
    "poll": 50,
    "type_k": "f16",
    "type_v": "f16",
    "n_gpu_layers": 0,
    "n_cpu_moe": 0,
    "split_mode": "layer",
    "main_gpu": 0,
    "no_kv_offload": false,
    "flash_attn": -1,
    "devices": "auto",
    "tensor_split": "0.00",
    "tensor_buft_overrides": "none",
    "load_mode": "auto",
    "lazy_mode": "auto",
    "embeddings": false,
    "no_op_offload": 0,
    "no_host": false,
    "repack": true,
    "fit_target": 0,
    "fit_min_ctx": 0,
    "n_prompt": 0,
    "n_gen": 128,
    "n_depth": 0,
    "test_time": "2026-10-29T19:01:24Z",
    "avg_ns": 8474329647,
    "stddev_ns": 69952324,
    "avg_ts": 15.105123,
    "stddev_ts": 0.124119,
    "samples_ns": [ 8427558893, 8554747398, 8440682651 ],
    "samples_ts": [ 15.1883, 14.9625, 15.1647 ]
  }
]
```

</details>

<details>
<summary>results/q4-baseline-20261029T185428Z/q4-generation-t1.json</summary>

Original path: `fixtures/lima-benchmark-node-temp/results/q4-baseline-20261029T185428Z/q4-generation-t1.json`  
SHA-256: `6e48074f3f33a6efae2ae0acb1e75631f186172c346bcde47346f6a095e215ba`

```json
[
  {
    "build_commit": "eb4e244be",
    "build_number": 12001,
    "cpu_info": "CPU",
    "gpu_info": "",
    "backends": "CPU",
    "model_filename": "/home/nerdylucifer.guest/benchmark-work/models/qwen2.5-1.5b-instruct-q4_k_m.gguf",
    "model_type": "qwen2 1.5B Q4_K - Medium",
    "model_size": 1111370240,
    "model_n_params": 1777088000,
    "n_batch": 2048,
    "n_ubatch": 512,
    "n_threads": 1,
    "cpu_mask": "0x0",
    "cpu_strict": false,
    "poll": 50,
    "type_k": "f16",
    "type_v": "f16",
    "n_gpu_layers": 0,
    "n_cpu_moe": 0,
    "split_mode": "layer",
    "main_gpu": 0,
    "no_kv_offload": false,
    "flash_attn": -1,
    "devices": "auto",
    "tensor_split": "0.00",
    "tensor_buft_overrides": "none",
    "load_mode": "auto",
    "lazy_mode": "auto",
    "embeddings": false,
    "no_op_offload": 0,
    "no_host": false,
    "repack": true,
    "fit_target": 0,
    "fit_min_ctx": 0,
    "n_prompt": 0,
    "n_gen": 128,
    "n_depth": 0,
    "test_time": "2026-10-29T19:01:50Z",
    "avg_ns": 8497145212,
    "stddev_ns": 71575548,
    "avg_ts": 15.064741,
    "stddev_ts": 0.127310,
    "samples_ns": [ 8458682585, 8544306629, 8394697888, 8574831526, 8513207435 ],
    "samples_ts": [ 15.1324, 14.9807, 15.2477, 14.9274, 15.0355 ]
  }
]
```

</details>

<details>
<summary>results/q4-baseline-20261029T185428Z/q4-generation-t2-warmup.json</summary>

Original path: `fixtures/lima-benchmark-node-temp/results/q4-baseline-20261029T185428Z/q4-generation-t2-warmup.json`  
SHA-256: `4d45e5e0148f455af487951e8a708446f8da1c434f567cc5dc2231220b2ede8e`

```json
[
  {
    "build_commit": "eb4e244be",
    "build_number": 12001,
    "cpu_info": "CPU",
    "gpu_info": "",
    "backends": "CPU",
    "model_filename": "/home/nerdylucifer.guest/benchmark-work/models/qwen2.5-1.5b-instruct-q4_k_m.gguf",
    "model_type": "qwen2 1.5B Q4_K - Medium",
    "model_size": 1111370240,
    "model_n_params": 1777088000,
    "n_batch": 2048,
    "n_ubatch": 512,
    "n_threads": 2,
    "cpu_mask": "0x0",
    "cpu_strict": false,
    "poll": 50,
    "type_k": "f16",
    "type_v": "f16",
    "n_gpu_layers": 0,
    "n_cpu_moe": 0,
    "split_mode": "layer",
    "main_gpu": 0,
    "no_kv_offload": false,
    "flash_attn": -1,
    "devices": "auto",
    "tensor_split": "0.00",
    "tensor_buft_overrides": "none",
    "load_mode": "auto",
    "lazy_mode": "auto",
    "embeddings": false,
    "no_op_offload": 0,
    "no_host": false,
    "repack": true,
    "fit_target": 0,
    "fit_min_ctx": 0,
    "n_prompt": 0,
    "n_gen": 128,
    "n_depth": 0,
    "test_time": "2026-10-29T19:02:32Z",
    "avg_ns": 5224184102,
    "stddev_ns": 50478588,
    "avg_ts": 24.502964,
    "stddev_ts": 0.237556,
    "samples_ns": [ 5168686268, 5236503496, 5267362544 ],
    "samples_ts": [ 24.7645, 24.4438, 24.3006 ]
  }
]
```

</details>

<details>
<summary>results/q4-baseline-20261029T185428Z/q4-generation-t2.json</summary>

Original path: `fixtures/lima-benchmark-node-temp/results/q4-baseline-20261029T185428Z/q4-generation-t2.json`  
SHA-256: `2426f041ecf10f34130b6ccef396b139ee3df727d2c650843d9ca8b97dc06649`

```json
[
  {
    "build_commit": "eb4e244be",
    "build_number": 12001,
    "cpu_info": "CPU",
    "gpu_info": "",
    "backends": "CPU",
    "model_filename": "/home/nerdylucifer.guest/benchmark-work/models/qwen2.5-1.5b-instruct-q4_k_m.gguf",
    "model_type": "qwen2 1.5B Q4_K - Medium",
    "model_size": 1111370240,
    "model_n_params": 1777088000,
    "n_batch": 2048,
    "n_ubatch": 512,
    "n_threads": 2,
    "cpu_mask": "0x0",
    "cpu_strict": false,
    "poll": 50,
    "type_k": "f16",
    "type_v": "f16",
    "n_gpu_layers": 0,
    "n_cpu_moe": 0,
    "split_mode": "layer",
    "main_gpu": 0,
    "no_kv_offload": false,
    "flash_attn": -1,
    "devices": "auto",
    "tensor_split": "0.00",
    "tensor_buft_overrides": "none",
    "load_mode": "auto",
    "lazy_mode": "auto",
    "embeddings": false,
    "no_op_offload": 0,
    "no_host": false,
    "repack": true,
    "fit_target": 0,
    "fit_min_ctx": 0,
    "n_prompt": 0,
    "n_gen": 128,
    "n_depth": 0,
    "test_time": "2026-10-29T19:02:48Z",
    "avg_ns": 5341341576,
    "stddev_ns": 87429769,
    "avg_ts": 23.969199,
    "stddev_ts": 0.395770,
    "samples_ns": [ 5205542396, 5332889009, 5344555451, 5379404440, 5444316587 ],
    "samples_ts": [ 24.5892, 24.002, 23.9496, 23.7945, 23.5108 ]
  }
]
```

</details>

<details>
<summary>results/q4-baseline-20261029T185428Z/q4-memory-workload.json</summary>

Original path: `fixtures/lima-benchmark-node-temp/results/q4-baseline-20261029T185428Z/q4-memory-workload.json`  
SHA-256: `86e7884a44c67b3f5f0ee4cb7100232f2023d6a84908422e341d0c7a6651a46e`

```json
[
  {
    "build_commit": "eb4e244be",
    "build_number": 12001,
    "cpu_info": "CPU",
    "gpu_info": "",
    "backends": "CPU",
    "model_filename": "/home/nerdylucifer.guest/benchmark-work/models/qwen2.5-1.5b-instruct-q4_k_m.gguf",
    "model_type": "qwen2 1.5B Q4_K - Medium",
    "model_size": 1111370240,
    "model_n_params": 1777088000,
    "n_batch": 2048,
    "n_ubatch": 512,
    "n_threads": 2,
    "cpu_mask": "0x0",
    "cpu_strict": false,
    "poll": 50,
    "type_k": "f16",
    "type_v": "f16",
    "n_gpu_layers": 0,
    "n_cpu_moe": 0,
    "split_mode": "layer",
    "main_gpu": 0,
    "no_kv_offload": false,
    "flash_attn": -1,
    "devices": "auto",
    "tensor_split": "0.00",
    "tensor_buft_overrides": "none",
    "load_mode": "auto",
    "lazy_mode": "auto",
    "embeddings": false,
    "no_op_offload": 0,
    "no_host": false,
    "repack": true,
    "fit_target": 0,
    "fit_min_ctx": 0,
    "n_prompt": 512,
    "n_gen": 0,
    "n_depth": 0,
    "test_time": "2026-10-29T19:11:14Z",
    "avg_ns": 14013117267,
    "stddev_ns": 0,
    "avg_ts": 36.537195,
    "stddev_ts": 0.000000,
    "samples_ns": [ 14013117267 ],
    "samples_ts": [ 36.5372 ]
  },
  {
    "build_commit": "eb4e244be",
    "build_number": 12001,
    "cpu_info": "CPU",
    "gpu_info": "",
    "backends": "CPU",
    "model_filename": "/home/nerdylucifer.guest/benchmark-work/models/qwen2.5-1.5b-instruct-q4_k_m.gguf",
    "model_type": "qwen2 1.5B Q4_K - Medium",
    "model_size": 1111370240,
    "model_n_params": 1777088000,
    "n_batch": 2048,
    "n_ubatch": 512,
    "n_threads": 2,
    "cpu_mask": "0x0",
    "cpu_strict": false,
    "poll": 50,
    "type_k": "f16",
    "type_v": "f16",
    "n_gpu_layers": 0,
    "n_cpu_moe": 0,
    "split_mode": "layer",
    "main_gpu": 0,
    "no_kv_offload": false,
    "flash_attn": -1,
    "devices": "auto",
    "tensor_split": "0.00",
    "tensor_buft_overrides": "none",
    "load_mode": "auto",
    "lazy_mode": "auto",
    "embeddings": false,
    "no_op_offload": 0,
    "no_host": false,
    "repack": true,
    "fit_target": 0,
    "fit_min_ctx": 0,
    "n_prompt": 0,
    "n_gen": 128,
    "n_depth": 0,
    "test_time": "2026-10-29T19:11:41Z",
    "avg_ns": 4792556565,
    "stddev_ns": 0,
    "avg_ts": 26.708083,
    "stddev_ts": 0.000000,
    "samples_ns": [ 4792556565 ],
    "samples_ts": [ 26.7081 ]
  }
]
```

</details>

<details>
<summary>results/q4-baseline-20261029T185428Z/q4-memory-workload.time.txt</summary>

Original path: `fixtures/lima-benchmark-node-temp/results/q4-baseline-20261029T185428Z/q4-memory-workload.time.txt`  
SHA-256: `2a3132b09829212c4b6d62701c6d3f027fd9107b72038b9e285a48e29a7db75b`

```text
	Command being timed: "/home/nerdylucifer.guest/benchmark-work/src/llama.cpp/build/bin/llama-bench --model /home/nerdylucifer.guest/benchmark-work/models/qwen2.5-1.5b-instruct-q4_k_m.gguf --threads 2 --n-gpu-layers 0 --repetitions 1 --n-prompt 512 --n-gen 128 --output json"
	User time (seconds): 64.49
	System time (seconds): 0.09
	Percent of CPU this job got: 199%
	Elapsed (wall clock) time (h:mm:ss or m:ss): 0:32.43
	Average shared text size (kbytes): 0
	Average unshared data size (kbytes): 0
	Average stack size (kbytes): 0
	Average total size (kbytes): 0
	Maximum resident set size (kbytes): 1229112
	Average resident set size (kbytes): 0
	Major (requiring I/O) page faults: 0
	Minor (reclaiming a frame) page faults: 57902
	Voluntary context switches: 108
	Involuntary context switches: 902
	Swaps: 0
	File system inputs: 0
	File system outputs: 16
	Socket messages sent: 0
	Socket messages received: 0
	Signals delivered: 0
	Page size (bytes): 4096
	Exit status: 0
```

</details>

<details>
<summary>results/q4-baseline-20261029T185428Z/q4-prompt512-t1-warmup.json</summary>

Original path: `fixtures/lima-benchmark-node-temp/results/q4-baseline-20261029T185428Z/q4-prompt512-t1-warmup.json`  
SHA-256: `c2091e88fb48f974ea37743f249b0c46d786fedaf3aa7da75c5489a11e46cc79`

```json
[
  {
    "build_commit": "eb4e244be",
    "build_number": 12001,
    "cpu_info": "CPU",
    "gpu_info": "",
    "backends": "CPU",
    "model_filename": "/home/nerdylucifer.guest/benchmark-work/models/qwen2.5-1.5b-instruct-q4_k_m.gguf",
    "model_type": "qwen2 1.5B Q4_K - Medium",
    "model_size": 1111370240,
    "model_n_params": 1777088000,
    "n_batch": 2048,
    "n_ubatch": 512,
    "n_threads": 1,
    "cpu_mask": "0x0",
    "cpu_strict": false,
    "poll": 50,
    "type_k": "f16",
    "type_v": "f16",
    "n_gpu_layers": 0,
    "n_cpu_moe": 0,
    "split_mode": "layer",
    "main_gpu": 0,
    "no_kv_offload": false,
    "flash_attn": -1,
    "devices": "auto",
    "tensor_split": "0.00",
    "tensor_buft_overrides": "none",
    "load_mode": "auto",
    "lazy_mode": "auto",
    "embeddings": false,
    "no_op_offload": 0,
    "no_host": false,
    "repack": true,
    "fit_target": 0,
    "fit_min_ctx": 0,
    "n_prompt": 512,
    "n_gen": 0,
    "n_depth": 0,
    "test_time": "2026-10-29T18:54:28Z",
    "avg_ns": 26555904725,
    "stddev_ns": 913199721,
    "avg_ts": 19.295358,
    "stddev_ts": 0.666520,
    "samples_ns": [ 25609732364, 26625875445, 27432106368 ],
    "samples_ts": [ 19.9924, 19.2294, 18.6643 ]
  }
]
```

</details>

<details>
<summary>results/q4-baseline-20261029T185428Z/q4-prompt512-t1.json</summary>

Original path: `fixtures/lima-benchmark-node-temp/results/q4-baseline-20261029T185428Z/q4-prompt512-t1.json`  
SHA-256: `0833e2c76697e362ec22d1c20c4f26a56f52439651a09102ab217d91449701d9`

```json
[
  {
    "build_commit": "eb4e244be",
    "build_number": 12001,
    "cpu_info": "CPU",
    "gpu_info": "",
    "backends": "CPU",
    "model_filename": "/home/nerdylucifer.guest/benchmark-work/models/qwen2.5-1.5b-instruct-q4_k_m.gguf",
    "model_type": "qwen2 1.5B Q4_K - Medium",
    "model_size": 1111370240,
    "model_n_params": 1777088000,
    "n_batch": 2048,
    "n_ubatch": 512,
    "n_threads": 1,
    "cpu_mask": "0x0",
    "cpu_strict": false,
    "poll": 50,
    "type_k": "f16",
    "type_v": "f16",
    "n_gpu_layers": 0,
    "n_cpu_moe": 0,
    "split_mode": "layer",
    "main_gpu": 0,
    "no_kv_offload": false,
    "flash_attn": -1,
    "devices": "auto",
    "tensor_split": "0.00",
    "tensor_buft_overrides": "none",
    "load_mode": "auto",
    "lazy_mode": "auto",
    "embeddings": false,
    "no_op_offload": 0,
    "no_host": false,
    "repack": true,
    "fit_target": 0,
    "fit_min_ctx": 0,
    "n_prompt": 512,
    "n_gen": 0,
    "n_depth": 0,
    "test_time": "2026-10-29T18:56:13Z",
    "avg_ns": 26331813710,
    "stddev_ns": 152418701,
    "avg_ts": 19.444680,
    "stddev_ts": 0.112207,
    "samples_ns": [ 26257834069, 26166446800, 26279512223, 26563782529, 26391492930 ],
    "samples_ts": [ 19.4989, 19.567, 19.4829, 19.2744, 19.4002 ]
  }
]
```

</details>

<details>
<summary>results/q4-baseline-20261029T185428Z/q4-prompt512-t2-warmup.json</summary>

Original path: `fixtures/lima-benchmark-node-temp/results/q4-baseline-20261029T185428Z/q4-prompt512-t2-warmup.json`  
SHA-256: `887766fe38d480860b3165edaa30986de945e6bdcd84b345e796ca82a94b1070`

```json
[
  {
    "build_commit": "eb4e244be",
    "build_number": 12001,
    "cpu_info": "CPU",
    "gpu_info": "",
    "backends": "CPU",
    "model_filename": "/home/nerdylucifer.guest/benchmark-work/models/qwen2.5-1.5b-instruct-q4_k_m.gguf",
    "model_type": "qwen2 1.5B Q4_K - Medium",
    "model_size": 1111370240,
    "model_n_params": 1777088000,
    "n_batch": 2048,
    "n_ubatch": 512,
    "n_threads": 2,
    "cpu_mask": "0x0",
    "cpu_strict": false,
    "poll": 50,
    "type_k": "f16",
    "type_v": "f16",
    "n_gpu_layers": 0,
    "n_cpu_moe": 0,
    "split_mode": "layer",
    "main_gpu": 0,
    "no_kv_offload": false,
    "flash_attn": -1,
    "devices": "auto",
    "tensor_split": "0.00",
    "tensor_buft_overrides": "none",
    "load_mode": "auto",
    "lazy_mode": "auto",
    "embeddings": false,
    "no_op_offload": 0,
    "no_host": false,
    "repack": true,
    "fit_target": 0,
    "fit_min_ctx": 0,
    "n_prompt": 512,
    "n_gen": 0,
    "n_depth": 0,
    "test_time": "2026-10-29T18:58:52Z",
    "avg_ns": 15133864869,
    "stddev_ns": 77421801,
    "avg_ts": 33.832002,
    "stddev_ts": 0.173202,
    "samples_ns": [ 15053101910, 15141048258, 15207444441 ],
    "samples_ts": [ 34.0129, 33.8154, 33.6677 ]
  }
]
```

</details>

<details>
<summary>results/q4-baseline-20261029T185428Z/q4-prompt512-t2.json</summary>

Original path: `fixtures/lima-benchmark-node-temp/results/q4-baseline-20261029T185428Z/q4-prompt512-t2.json`  
SHA-256: `929c44d1cb496a5858c53f65c058d04cb8888a3a91ebd738ce876d20e21382af`

```json
[
  {
    "build_commit": "eb4e244be",
    "build_number": 12001,
    "cpu_info": "CPU",
    "gpu_info": "",
    "backends": "CPU",
    "model_filename": "/home/nerdylucifer.guest/benchmark-work/models/qwen2.5-1.5b-instruct-q4_k_m.gguf",
    "model_type": "qwen2 1.5B Q4_K - Medium",
    "model_size": 1111370240,
    "model_n_params": 1777088000,
    "n_batch": 2048,
    "n_ubatch": 512,
    "n_threads": 2,
    "cpu_mask": "0x0",
    "cpu_strict": false,
    "poll": 50,
    "type_k": "f16",
    "type_v": "f16",
    "n_gpu_layers": 0,
    "n_cpu_moe": 0,
    "split_mode": "layer",
    "main_gpu": 0,
    "no_kv_offload": false,
    "flash_attn": -1,
    "devices": "auto",
    "tensor_split": "0.00",
    "tensor_buft_overrides": "none",
    "load_mode": "auto",
    "lazy_mode": "auto",
    "embeddings": false,
    "no_op_offload": 0,
    "no_host": false,
    "repack": true,
    "fit_target": 0,
    "fit_min_ctx": 0,
    "n_prompt": 512,
    "n_gen": 0,
    "n_depth": 0,
    "test_time": "2026-10-29T18:59:52Z",
    "avg_ns": 15330190973,
    "stddev_ns": 64059238,
    "avg_ts": 33.398616,
    "stddev_ts": 0.139853,
    "samples_ns": [ 15300649289, 15343349003, 15235020383, 15392631100, 15379305093 ],
    "samples_ts": [ 33.4626, 33.3695, 33.6068, 33.2627, 33.2915 ]
  }
]
```

</details>

<details>
<summary>results/q4-generation-t2-warmup.json</summary>

Original path: `fixtures/lima-benchmark-node-temp/results/q4-generation-t2-warmup.json`  
SHA-256: `4729a1e4c1a4f6030abd8189e36e8b796ea80baf252d921586db5b6b36adcd98`

```json
[
  {
    "build_commit": "eb4e244be",
    "build_number": 12001,
    "cpu_info": "CPU",
    "gpu_info": "",
    "backends": "CPU",
    "model_filename": "/home/nerdylucifer.guest/benchmark-work/models/qwen2.5-1.5b-instruct-q4_k_m.gguf",
    "model_type": "qwen2 1.5B Q4_K - Medium",
    "model_size": 1111370240,
    "model_n_params": 1777088000,
    "n_batch": 2048,
    "n_ubatch": 512,
    "n_threads": 2,
    "cpu_mask": "0x0",
    "cpu_strict": false,
    "poll": 50,
    "type_k": "f16",
    "type_v": "f16",
    "n_gpu_layers": 0,
    "n_cpu_moe": 0,
    "split_mode": "layer",
    "main_gpu": 0,
    "no_kv_offload": false,
    "flash_attn": -1,
    "devices": "auto",
    "tensor_split": "0.00",
    "tensor_buft_overrides": "none",
    "load_mode": "auto",
    "lazy_mode": "auto",
    "embeddings": false,
    "no_op_offload": 0,
    "no_host": false,
    "repack": true,
    "fit_target": 0,
    "fit_min_ctx": 0,
    "n_prompt": 0,
    "n_gen": 128,
    "n_depth": 0,
    "test_time": "2026-10-29T18:49:01Z",
    "avg_ns": 4540331685,
    "stddev_ns": 111412511,
    "avg_ts": 28.203186,
    "stddev_ts": 0.697684,
    "samples_ns": [ 4418530796, 4565369122, 4637095137 ],
    "samples_ts": [ 28.9689, 28.0372, 27.6035 ]
  }
]
```

</details>

<details>
<summary>results/run-acff3a63-5e17-4974-a6e2-d3a64620b4b1/generation-128-t1-warmup.json</summary>

Original path: `fixtures/lima-benchmark-node-temp/results/run-acff3a63-5e17-4974-a6e2-d3a64620b4b1/generation-128-t1-warmup.json`  
SHA-256: `e678eda73c08c16bd273f04b89802e5f2ad2c8c7ce68cf2fbf9f3251fc52a906`

```json
[
  {
    "build_commit": "eb4e244be",
    "build_number": 12001,
    "cpu_info": "CPU",
    "gpu_info": "",
    "backends": "CPU",
    "model_filename": "/home/nerdylucifer.guest/benchmark-work/models/qwen2.5-1.5b-instruct-q4_k_m.gguf",
    "model_type": "qwen2 1.5B Q4_K - Medium",
    "model_size": 1111370240,
    "model_n_params": 1777088000,
    "n_batch": 2048,
    "n_ubatch": 512,
    "n_threads": 1,
    "cpu_mask": "0x0",
    "cpu_strict": false,
    "poll": 50,
    "type_k": "f16",
    "type_v": "f16",
    "n_gpu_layers": 0,
    "n_cpu_moe": 0,
    "split_mode": "layer",
    "main_gpu": 0,
    "no_kv_offload": false,
    "flash_attn": -1,
    "devices": "auto",
    "tensor_split": "0.00",
    "tensor_buft_overrides": "none",
    "load_mode": "auto",
    "lazy_mode": "auto",
    "embeddings": false,
    "no_op_offload": 0,
    "no_host": false,
    "repack": true,
    "fit_target": 0,
    "fit_min_ctx": 0,
    "n_prompt": 0,
    "n_gen": 128,
    "n_depth": 0,
    "test_time": "2026-11-04T14:38:51Z",
    "avg_ns": 9518215487,
    "stddev_ns": 857127790,
    "avg_ts": 13.517636,
    "stddev_ts": 1.161851,
    "samples_ns": [ 8919409647, 10500071133, 9135165681 ],
    "samples_ts": [ 14.3507, 12.1904, 14.0118 ]
  }
]
```

</details>

<details>
<summary>results/run-acff3a63-5e17-4974-a6e2-d3a64620b4b1/generation-128-t1.json</summary>

Original path: `fixtures/lima-benchmark-node-temp/results/run-acff3a63-5e17-4974-a6e2-d3a64620b4b1/generation-128-t1.json`  
SHA-256: `ca5e547bf0912d8e99ee18e1ef2b25187c64eb4a029f65aea0557d35611dc878`

```json
[
  {
    "build_commit": "eb4e244be",
    "build_number": 12001,
    "cpu_info": "CPU",
    "gpu_info": "",
    "backends": "CPU",
    "model_filename": "/home/nerdylucifer.guest/benchmark-work/models/qwen2.5-1.5b-instruct-q4_k_m.gguf",
    "model_type": "qwen2 1.5B Q4_K - Medium",
    "model_size": 1111370240,
    "model_n_params": 1777088000,
    "n_batch": 2048,
    "n_ubatch": 512,
    "n_threads": 1,
    "cpu_mask": "0x0",
    "cpu_strict": false,
    "poll": 50,
    "type_k": "f16",
    "type_v": "f16",
    "n_gpu_layers": 0,
    "n_cpu_moe": 0,
    "split_mode": "layer",
    "main_gpu": 0,
    "no_kv_offload": false,
    "flash_attn": -1,
    "devices": "auto",
    "tensor_split": "0.00",
    "tensor_buft_overrides": "none",
    "load_mode": "auto",
    "lazy_mode": "auto",
    "embeddings": false,
    "no_op_offload": 0,
    "no_host": false,
    "repack": true,
    "fit_target": 0,
    "fit_min_ctx": 0,
    "n_prompt": 0,
    "n_gen": 128,
    "n_depth": 0,
    "test_time": "2026-11-04T14:39:19Z",
    "avg_ns": 9574663948,
    "stddev_ns": 604313983,
    "avg_ts": 13.408158,
    "stddev_ts": 0.783271,
    "samples_ns": [ 9218647973, 9439254326, 10645736989, 9268944563, 9300735891 ],
    "samples_ts": [ 13.8849, 13.5604, 12.0236, 13.8096, 13.7624 ]
  }
]
```

</details>

<details>
<summary>results/run-acff3a63-5e17-4974-a6e2-d3a64620b4b1/generation-128-t2-warmup.json</summary>

Original path: `fixtures/lima-benchmark-node-temp/results/run-acff3a63-5e17-4974-a6e2-d3a64620b4b1/generation-128-t2-warmup.json`  
SHA-256: `3b708d495ffb3e0165aa061f8ad87dc39eda74b82ad3deecf206f071af3cd9a8`

```json
[
  {
    "build_commit": "eb4e244be",
    "build_number": 12001,
    "cpu_info": "CPU",
    "gpu_info": "",
    "backends": "CPU",
    "model_filename": "/home/nerdylucifer.guest/benchmark-work/models/qwen2.5-1.5b-instruct-q4_k_m.gguf",
    "model_type": "qwen2 1.5B Q4_K - Medium",
    "model_size": 1111370240,
    "model_n_params": 1777088000,
    "n_batch": 2048,
    "n_ubatch": 512,
    "n_threads": 2,
    "cpu_mask": "0x0",
    "cpu_strict": false,
    "poll": 50,
    "type_k": "f16",
    "type_v": "f16",
    "n_gpu_layers": 0,
    "n_cpu_moe": 0,
    "split_mode": "layer",
    "main_gpu": 0,
    "no_kv_offload": false,
    "flash_attn": -1,
    "devices": "auto",
    "tensor_split": "0.00",
    "tensor_buft_overrides": "none",
    "load_mode": "auto",
    "lazy_mode": "auto",
    "embeddings": false,
    "no_op_offload": 0,
    "no_host": false,
    "repack": true,
    "fit_target": 0,
    "fit_min_ctx": 0,
    "n_prompt": 0,
    "n_gen": 128,
    "n_depth": 0,
    "test_time": "2026-11-04T14:40:07Z",
    "avg_ns": 6548903670,
    "stddev_ns": 94423275,
    "avg_ts": 19.547976,
    "stddev_ts": 0.282968,
    "samples_ns": [ 6633307787, 6446929430, 6566473793 ],
    "samples_ts": [ 19.2966, 19.8544, 19.493 ]
  }
]
```

</details>

<details>
<summary>results/run-acff3a63-5e17-4974-a6e2-d3a64620b4b1/generation-128-t2.json</summary>

Original path: `fixtures/lima-benchmark-node-temp/results/run-acff3a63-5e17-4974-a6e2-d3a64620b4b1/generation-128-t2.json`  
SHA-256: `a08bf961044ce55c486669244a2cb8a50ab96c4be24fe52e4d9edae0994d54ff`

```json
[
  {
    "build_commit": "eb4e244be",
    "build_number": 12001,
    "cpu_info": "CPU",
    "gpu_info": "",
    "backends": "CPU",
    "model_filename": "/home/nerdylucifer.guest/benchmark-work/models/qwen2.5-1.5b-instruct-q4_k_m.gguf",
    "model_type": "qwen2 1.5B Q4_K - Medium",
    "model_size": 1111370240,
    "model_n_params": 1777088000,
    "n_batch": 2048,
    "n_ubatch": 512,
    "n_threads": 2,
    "cpu_mask": "0x0",
    "cpu_strict": false,
    "poll": 50,
    "type_k": "f16",
    "type_v": "f16",
    "n_gpu_layers": 0,
    "n_cpu_moe": 0,
    "split_mode": "layer",
    "main_gpu": 0,
    "no_kv_offload": false,
    "flash_attn": -1,
    "devices": "auto",
    "tensor_split": "0.00",
    "tensor_buft_overrides": "none",
    "load_mode": "auto",
    "lazy_mode": "auto",
    "embeddings": false,
    "no_op_offload": 0,
    "no_host": false,
    "repack": true,
    "fit_target": 0,
    "fit_min_ctx": 0,
    "n_prompt": 0,
    "n_gen": 128,
    "n_depth": 0,
    "test_time": "2026-11-04T14:40:27Z",
    "avg_ns": 6664631445,
    "stddev_ns": 88840214,
    "avg_ts": 19.208614,
    "stddev_ts": 0.257940,
    "samples_ns": [ 6705205810, 6534822734, 6743835141, 6727742360, 6611551182 ],
    "samples_ts": [ 19.0896, 19.5874, 18.9803, 19.0257, 19.3601 ]
  }
]
```

</details>

<details>
<summary>results/run-acff3a63-5e17-4974-a6e2-d3a64620b4b1/memory-p512-g128-t2.json</summary>

Original path: `fixtures/lima-benchmark-node-temp/results/run-acff3a63-5e17-4974-a6e2-d3a64620b4b1/memory-p512-g128-t2.json`  
SHA-256: `8cb035c75248ca14dac99858a5154bf4e1a8c98cd4f0f9d164b1998b34d9deac`

```json
[
  {
    "build_commit": "eb4e244be",
    "build_number": 12001,
    "cpu_info": "CPU",
    "gpu_info": "",
    "backends": "CPU",
    "model_filename": "/home/nerdylucifer.guest/benchmark-work/models/qwen2.5-1.5b-instruct-q4_k_m.gguf",
    "model_type": "qwen2 1.5B Q4_K - Medium",
    "model_size": 1111370240,
    "model_n_params": 1777088000,
    "n_batch": 2048,
    "n_ubatch": 512,
    "n_threads": 2,
    "cpu_mask": "0x0",
    "cpu_strict": false,
    "poll": 50,
    "type_k": "f16",
    "type_v": "f16",
    "n_gpu_layers": 0,
    "n_cpu_moe": 0,
    "split_mode": "layer",
    "main_gpu": 0,
    "no_kv_offload": false,
    "flash_attn": -1,
    "devices": "auto",
    "tensor_split": "0.00",
    "tensor_buft_overrides": "none",
    "load_mode": "auto",
    "lazy_mode": "auto",
    "embeddings": false,
    "no_op_offload": 0,
    "no_host": false,
    "repack": true,
    "fit_target": 0,
    "fit_min_ctx": 0,
    "n_prompt": 512,
    "n_gen": 0,
    "n_depth": 0,
    "test_time": "2026-11-04T14:41:01Z",
    "avg_ns": 18898468953,
    "stddev_ns": 0,
    "avg_ts": 27.092142,
    "stddev_ts": 0.000000,
    "samples_ns": [ 18898468953 ],
    "samples_ts": [ 27.0921 ]
  },
  {
    "build_commit": "eb4e244be",
    "build_number": 12001,
    "cpu_info": "CPU",
    "gpu_info": "",
    "backends": "CPU",
    "model_filename": "/home/nerdylucifer.guest/benchmark-work/models/qwen2.5-1.5b-instruct-q4_k_m.gguf",
    "model_type": "qwen2 1.5B Q4_K - Medium",
    "model_size": 1111370240,
    "model_n_params": 1777088000,
    "n_batch": 2048,
    "n_ubatch": 512,
    "n_threads": 2,
    "cpu_mask": "0x0",
    "cpu_strict": false,
    "poll": 50,
    "type_k": "f16",
    "type_v": "f16",
    "n_gpu_layers": 0,
    "n_cpu_moe": 0,
    "split_mode": "layer",
    "main_gpu": 0,
    "no_kv_offload": false,
    "flash_attn": -1,
    "devices": "auto",
    "tensor_split": "0.00",
    "tensor_buft_overrides": "none",
    "load_mode": "auto",
    "lazy_mode": "auto",
    "embeddings": false,
    "no_op_offload": 0,
    "no_host": false,
    "repack": true,
    "fit_target": 0,
    "fit_min_ctx": 0,
    "n_prompt": 0,
    "n_gen": 128,
    "n_depth": 0,
    "test_time": "2026-11-04T14:41:38Z",
    "avg_ns": 7231661415,
    "stddev_ns": 0,
    "avg_ts": 17.699944,
    "stddev_ts": 0.000000,
    "samples_ns": [ 7231661415 ],
    "samples_ts": [ 17.6999 ]
  }
]
```

</details>

<details>
<summary>results/run-acff3a63-5e17-4974-a6e2-d3a64620b4b1/memory-p512-g128-t2.time.txt</summary>

Original path: `fixtures/lima-benchmark-node-temp/results/run-acff3a63-5e17-4974-a6e2-d3a64620b4b1/memory-p512-g128-t2.time.txt`  
SHA-256: `921d8c4e3f7be536959fdb414e28960a2a9d68c85e0c59383c2b556a8ec25e89`

```text
	Command being timed: "/home/nerdylucifer.guest/benchmark-work/src/llama.cpp/build/bin/llama-bench --model /home/nerdylucifer.guest/benchmark-work/models/qwen2.5-1.5b-instruct-q4_k_m.gguf --threads 2 --n-gpu-layers 0 --repetitions 1 --n-prompt 512 --n-gen 128 --output json"
	User time (seconds): 89.61
	System time (seconds): 0.09
	Percent of CPU this job got: 198%
	Elapsed (wall clock) time (h:mm:ss or m:ss): 0:45.11
	Average shared text size (kbytes): 0
	Average unshared data size (kbytes): 0
	Average stack size (kbytes): 0
	Average total size (kbytes): 0
	Maximum resident set size (kbytes): 1229168
	Average resident set size (kbytes): 0
	Major (requiring I/O) page faults: 0
	Minor (reclaiming a frame) page faults: 42380
	Voluntary context switches: 168
	Involuntary context switches: 884
	Swaps: 0
	File system inputs: 0
	File system outputs: 8
	Socket messages sent: 0
	Socket messages received: 0
	Signals delivered: 0
	Page size (bytes): 4096
	Exit status: 0
```

</details>

<details>
<summary>results/run-acff3a63-5e17-4974-a6e2-d3a64620b4b1/postflight.txt</summary>

Original path: `fixtures/lima-benchmark-node-temp/results/run-acff3a63-5e17-4974-a6e2-d3a64620b4b1/postflight.txt`  
SHA-256: `1806f8c550c6d6404d20acb80059a1693224f040aac90a81c6de3790b19953a7`

```text
timestamp=2026-11-04T14:41:46Z
--- system ---
Linux benchmark-node-temp 6.17.0-41-generic #41-Ubuntu SMP PREEMPT_DYNAMIC Sat Jun 20 17:41:03 UTC 2026 aarch64 GNU/Linux
--- CPU ---
2
--- memory ---
               total        used        free      shared  buff/cache   available
Mem:           2.8Gi       404Mi       885Mi       1.0Mi       1.7Gi       2.4Gi
Swap:             0B          0B          0B
--- swap ---
--- disk ---
Filesystem      Size  Used Avail Use% Mounted on
/dev/vda1        29G  5.0G   24G  18% /
```

</details>

<details>
<summary>results/run-acff3a63-5e17-4974-a6e2-d3a64620b4b1/preflight.txt</summary>

Original path: `fixtures/lima-benchmark-node-temp/results/run-acff3a63-5e17-4974-a6e2-d3a64620b4b1/preflight.txt`  
SHA-256: `c73decb6f39f23422a50aeecbf6015facc7ef06acad55ee44bd93e1555c0745d`

```text
timestamp=2026-11-04T14:31:41Z
--- system ---
Linux benchmark-node-temp 6.17.0-41-generic #41-Ubuntu SMP PREEMPT_DYNAMIC Sat Jun 20 17:41:03 UTC 2026 aarch64 GNU/Linux
--- CPU ---
2
--- memory ---
               total        used        free      shared  buff/cache   available
Mem:           2.8Gi       397Mi       1.9Gi       1.0Mi       674Mi       2.4Gi
Swap:             0B          0B          0B
--- swap ---
--- disk ---
Filesystem      Size  Used Avail Use% Mounted on
/dev/vda1        29G  5.0G   24G  18% /
```

</details>

<details>
<summary>results/run-acff3a63-5e17-4974-a6e2-d3a64620b4b1/prompt-512-t1-warmup.json</summary>

Original path: `fixtures/lima-benchmark-node-temp/results/run-acff3a63-5e17-4974-a6e2-d3a64620b4b1/prompt-512-t1-warmup.json`  
SHA-256: `dfe0d993a3ae7f8d653ba8f46a6afe6d869d2ef354ecec3c2ab0829c536fc721`

```json
[
  {
    "build_commit": "eb4e244be",
    "build_number": 12001,
    "cpu_info": "CPU",
    "gpu_info": "",
    "backends": "CPU",
    "model_filename": "/home/nerdylucifer.guest/benchmark-work/models/qwen2.5-1.5b-instruct-q4_k_m.gguf",
    "model_type": "qwen2 1.5B Q4_K - Medium",
    "model_size": 1111370240,
    "model_n_params": 1777088000,
    "n_batch": 2048,
    "n_ubatch": 512,
    "n_threads": 1,
    "cpu_mask": "0x0",
    "cpu_strict": false,
    "poll": 50,
    "type_k": "f16",
    "type_v": "f16",
    "n_gpu_layers": 0,
    "n_cpu_moe": 0,
    "split_mode": "layer",
    "main_gpu": 0,
    "no_kv_offload": false,
    "flash_attn": -1,
    "devices": "auto",
    "tensor_split": "0.00",
    "tensor_buft_overrides": "none",
    "load_mode": "auto",
    "lazy_mode": "auto",
    "embeddings": false,
    "no_op_offload": 0,
    "no_host": false,
    "repack": true,
    "fit_target": 0,
    "fit_min_ctx": 0,
    "n_prompt": 512,
    "n_gen": 0,
    "n_depth": 0,
    "test_time": "2026-11-04T14:31:42Z",
    "avg_ns": 26220048369,
    "stddev_ns": 223707884,
    "avg_ts": 19.527988,
    "stddev_ts": 0.166153,
    "samples_ns": [ 26168654433, 26026509933, 26464980743 ],
    "samples_ts": [ 19.5654, 19.6722, 19.3463 ]
  }
]
```

</details>

<details>
<summary>results/run-acff3a63-5e17-4974-a6e2-d3a64620b4b1/prompt-512-t1.json</summary>

Original path: `fixtures/lima-benchmark-node-temp/results/run-acff3a63-5e17-4974-a6e2-d3a64620b4b1/prompt-512-t1.json`  
SHA-256: `24dd274c7550a104d5350b61fcdb82b50b0cb4d193a84559d61915dd1b33ca6a`

```json
[
  {
    "build_commit": "eb4e244be",
    "build_number": 12001,
    "cpu_info": "CPU",
    "gpu_info": "",
    "backends": "CPU",
    "model_filename": "/home/nerdylucifer.guest/benchmark-work/models/qwen2.5-1.5b-instruct-q4_k_m.gguf",
    "model_type": "qwen2 1.5B Q4_K - Medium",
    "model_size": 1111370240,
    "model_n_params": 1777088000,
    "n_batch": 2048,
    "n_ubatch": 512,
    "n_threads": 1,
    "cpu_mask": "0x0",
    "cpu_strict": false,
    "poll": 50,
    "type_k": "f16",
    "type_v": "f16",
    "n_gpu_layers": 0,
    "n_cpu_moe": 0,
    "split_mode": "layer",
    "main_gpu": 0,
    "no_kv_offload": false,
    "flash_attn": -1,
    "devices": "auto",
    "tensor_split": "0.00",
    "tensor_buft_overrides": "none",
    "load_mode": "auto",
    "lazy_mode": "auto",
    "embeddings": false,
    "no_op_offload": 0,
    "no_host": false,
    "repack": true,
    "fit_target": 0,
    "fit_min_ctx": 0,
    "n_prompt": 512,
    "n_gen": 0,
    "n_depth": 0,
    "test_time": "2026-11-04T14:33:26Z",
    "avg_ns": 27023943185,
    "stddev_ns": 146130308,
    "avg_ts": 18.946606,
    "stddev_ts": 0.102672,
    "samples_ns": [ 26809741696, 27058472471, 27192157560, 26959032543, 27100311657 ],
    "samples_ts": [ 19.0975, 18.922, 18.829, 18.9918, 18.8928 ]
  }
]
```

</details>

<details>
<summary>results/run-acff3a63-5e17-4974-a6e2-d3a64620b4b1/prompt-512-t2-warmup.json</summary>

Original path: `fixtures/lima-benchmark-node-temp/results/run-acff3a63-5e17-4974-a6e2-d3a64620b4b1/prompt-512-t2-warmup.json`  
SHA-256: `fccaa99f0d49577fb5d5dd758b48f515dcc943f0834181327b8e7626f603ac3e`

```json
[
  {
    "build_commit": "eb4e244be",
    "build_number": 12001,
    "cpu_info": "CPU",
    "gpu_info": "",
    "backends": "CPU",
    "model_filename": "/home/nerdylucifer.guest/benchmark-work/models/qwen2.5-1.5b-instruct-q4_k_m.gguf",
    "model_type": "qwen2 1.5B Q4_K - Medium",
    "model_size": 1111370240,
    "model_n_params": 1777088000,
    "n_batch": 2048,
    "n_ubatch": 512,
    "n_threads": 2,
    "cpu_mask": "0x0",
    "cpu_strict": false,
    "poll": 50,
    "type_k": "f16",
    "type_v": "f16",
    "n_gpu_layers": 0,
    "n_cpu_moe": 0,
    "split_mode": "layer",
    "main_gpu": 0,
    "no_kv_offload": false,
    "flash_attn": -1,
    "devices": "auto",
    "tensor_split": "0.00",
    "tensor_buft_overrides": "none",
    "load_mode": "auto",
    "lazy_mode": "auto",
    "embeddings": false,
    "no_op_offload": 0,
    "no_host": false,
    "repack": true,
    "fit_target": 0,
    "fit_min_ctx": 0,
    "n_prompt": 512,
    "n_gen": 0,
    "n_depth": 0,
    "test_time": "2026-11-04T14:36:08Z",
    "avg_ns": 15747345014,
    "stddev_ns": 35497430,
    "avg_ts": 32.513528,
    "stddev_ts": 0.073385,
    "samples_ns": [ 15706441612, 15770084862, 15765508569 ],
    "samples_ts": [ 32.5981, 32.4665, 32.476 ]
  }
]
```

</details>

<details>
<summary>results/run-acff3a63-5e17-4974-a6e2-d3a64620b4b1/prompt-512-t2.json</summary>

Original path: `fixtures/lima-benchmark-node-temp/results/run-acff3a63-5e17-4974-a6e2-d3a64620b4b1/prompt-512-t2.json`  
SHA-256: `ac14b13d9d35c017f03777e12157c0157b27a7a0be9469f5c24ee2dc4391356a`

```json
[
  {
    "build_commit": "eb4e244be",
    "build_number": 12001,
    "cpu_info": "CPU",
    "gpu_info": "",
    "backends": "CPU",
    "model_filename": "/home/nerdylucifer.guest/benchmark-work/models/qwen2.5-1.5b-instruct-q4_k_m.gguf",
    "model_type": "qwen2 1.5B Q4_K - Medium",
    "model_size": 1111370240,
    "model_n_params": 1777088000,
    "n_batch": 2048,
    "n_ubatch": 512,
    "n_threads": 2,
    "cpu_mask": "0x0",
    "cpu_strict": false,
    "poll": 50,
    "type_k": "f16",
    "type_v": "f16",
    "n_gpu_layers": 0,
    "n_cpu_moe": 0,
    "split_mode": "layer",
    "main_gpu": 0,
    "no_kv_offload": false,
    "flash_attn": -1,
    "devices": "auto",
    "tensor_split": "0.00",
    "tensor_buft_overrides": "none",
    "load_mode": "auto",
    "lazy_mode": "auto",
    "embeddings": false,
    "no_op_offload": 0,
    "no_host": false,
    "repack": true,
    "fit_target": 0,
    "fit_min_ctx": 0,
    "n_prompt": 512,
    "n_gen": 0,
    "n_depth": 0,
    "test_time": "2026-11-04T14:37:11Z",
    "avg_ns": 16715323581,
    "stddev_ns": 543962475,
    "avg_ts": 30.656719,
    "stddev_ts": 1.004406,
    "samples_ns": [ 16000786388, 16348270478, 16791353404, 17102058961, 17334148675 ],
    "samples_ts": [ 31.9984, 31.3183, 30.4919, 29.9379, 29.5371 ]
  }
]
```

</details>

