# 🚀 Your first benchmark

[Documentation home](../README.md) · [CLI reference](CLI.md) · [Troubleshooting](OPERATIONS.md)

## 1. 🧰 Choose your setup

The automated path uses two Lima VMs on one host. One VM owns k3s and control
services; another owns the measured workload. A two-machine setup gives better
physical separation, but enrollment of a remote Linux agent is currently manual.

Before starting, verify:

```sh
go version
limactl --version
limactl list
```

Install Go and Lima using their official installation instructions if either
command is missing. Source bootstrap needs Go on the host, and package/GitHub
access inside guests. Allow room for the VM allocations listed in the README,
model files, build trees and evidence. Start with one small GGUF model.

## 2. 🏗️ Build and deploy the control plane

From the repository root:

```sh
go build -o bin/modelgauge ./cmd/modelgauge
export PATH="$PWD/bin:$PATH"
modelgauge version --json
modelgauge control bootstrap --provider lima --source .
modelgauge control status
```

A fresh default VM is named `modelgauge-control`. Bootstrap installs the pinned
k3s version, creates namespaces and credentials, starts PostgreSQL/controller,
then deploys Prometheus and Grafana. It saves a private connection at
`~/.config/modelgauge/control.json` and forwards the API to `127.0.0.1:18080`.
Reapplying to the same installation preserves existing database PVCs and secrets.
A source build can import its controller image without a Docker daemon.

For another control VM, choose a distinct name and unused API port:

```sh
modelgauge control bootstrap --provider lima --source . \
  --name lab-control --port 18081 --cpus 4 --memory 6 --disk 50
```

This updates the active saved connection. It does not resize existing VMs.

## 3. 🖥️ Provision the measured node

```sh
modelgauge node bootstrap --provider lima --name benchmark-node --source .
modelgauge node list
modelgauge node status benchmark-node
```

The guest installs Linux tools and compiles the pinned CPU-only llama.cpp
revision. An agent is installed as a systemd user service with lingering enabled.
The default target gets 2 vCPUs, 3 GiB memory and a 30 GiB disk. The node should
register as `online` with `execution_mode: lima-linux`. No node directories,
runtime paths, addresses or enrollment tokens need to be supplied in this flow.

A first build can take time. If it fails, fix the reported network/package/build
error and retry the same command. It reuses the VM and a working cached runtime.
A VM containing k3s is refused as a benchmark node. Bootstrap also refuses an
active llama-bench process rather than upgrading during measurement.

## 4. 📦 Install one model

```sh
modelgauge model search qwen2.5
modelgauge model install qwen2.5-1.5b-instruct:q4_k_m --node benchmark-node
```

The built-in catalog initially covers Qwen2.5 1.5B Instruct in Q4_K_M, Q5_K_M
and Q8_0. Search covers catalog entries, not every model on Hugging Face.
Install resolves an immutable repository revision and publisher checksum, then
queues the node download. Remote installation does not transfer the GGUF through
the CLI. Save the returned `job_id` and poll it:

```sh
modelgauge model install status JOB_ID
modelgauge model list --node benchmark-node
```

Proceed when the job is `completed` and the ID appears in inventory. Inventory
may lag until the next agent poll. With `jq`, capture a real job ID directly:

```sh
JOB_ID="$(modelgauge model install qwen2.5-1.5b-instruct:q5_k_m \
  --node benchmark-node | jq -r .job_id)"
modelgauge model install status "$JOB_ID"
```

Omitting `--node` installs on the CLI machine instead. For an arbitrary public
GGUF URL, use `model install --id NAME --url HTTPS_URL --sha256 HASH --node NODE_ID`.
Replace URL/checksum placeholders with actual trusted model metadata.

## 5. 🧪 Create and submit a small campaign

```sh
modelgauge benchmark campaign create \
  --node benchmark-node --execution-mode lima-linux \
  --models qwen2.5-1.5b-instruct:q4_k_m \
  --contexts 512 --threads 1,2 \
  --output campaigns/first --submit
```

This resolves the node's model paths, writes a per-model plan and a local
`campaign.json`, and submits the plan. Use a new output directory for each
experiment. Without `--submit`, files are created but no workload is queued.
The agent leases the plan and runs cases sequentially.

```sh
modelgauge benchmark campaign status --file campaigns/first/campaign.json
modelgauge results show --node benchmark-node
```

Poll status manually until the plan reaches a terminal state. These are
one-shot queries, not watch commands. Standard CPU uses three warm-up repetitions
and five measured repetitions per throughput case, then one memory workload.
A short-context campaign can still take several minutes on a small CPU.
Do not start another workload to "help" warm the node during an experiment.

## 6. 📈 Read the evidence and recommendation

```sh
modelgauge results recommend \
  --node benchmark-node --model qwen2.5-1.5b-instruct:q4_k_m \
  --execution-mode lima-linux --context 512 --memory-budget-gib 2
```

The budget is RAM available to the model workload after OS/other overhead,
not the laptop's installed RAM. The response links `source_run_id` and reports
separate prompt/generation threads. `fits`, `does-not-fit` or `conditional`
expresses the measured memory reservation versus the supplied budget. A missing
budget produces `conditional`. Recommendation availability does not prove model
quality, service latency, or long-context decode fit.

An error about missing stable measurements means you should inspect results and
raw samples. Do not increase thresholds or discard inconvenient samples merely
to obtain a recommendation. See [methodology](BENCHMARKING.md).

## 7. 📊 Open Grafana

```sh
modelgauge monitoring open
```

Open `http://127.0.0.1:3000` if the browser did not launch. Username is `admin`;
retrieve the generated password using the [monitoring guide](../deploy/monitoring/README.md).
The control exporter is installed by bootstrap. A newly created benchmark VM
needs its exporter configured separately to expose Linux CPU/RAM charts;
benchmark result metrics come from the controller independently.

## 8. 🔁 Expand deliberately

After one successful run, install the other quantizations and compare:

```sh
modelgauge benchmark campaign create \
  --node benchmark-node --execution-mode lima-linux \
  --models qwen2.5-1.5b-instruct:q4_k_m,qwen2.5-1.5b-instruct:q5_k_m,qwen2.5-1.5b-instruct:q8_0 \
  --contexts 512,4096,8192 --threads 1,2 \
  --output campaigns/quantization-comparison --submit
```

Run this only after **all three models are installed**. Larger contexts can be
slow and need more RAM. The campaign measures one memory workload per model at
its largest requested prompt and highest requested thread count. It does not
create a memory curve automatically. See [methodology](BENCHMARKING.md) for
separate per-context memory experiments and thermal controls.

## ⏹️ Stop work or pause the lab

```sh
modelgauge benchmark cancel PLAN_ID
modelgauge model install cancel JOB_ID
```

Use the plan IDs from campaign JSON and the real install job ID. Active work
stops cooperatively when its lease renewal observes cancellation. Wait until
work is terminal before stopping VMs with `limactl stop VM_NAME`. Stopping keeps
VM disks; deleting a VM or PVC can destroy data. The CLI has no cluster teardown
or automatic backup command. See [operations](OPERATIONS.md).
