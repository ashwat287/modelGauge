# 🖥️ Benchmark node bootstrap

[📚 Documentation home](../../README.md)


After control-plane bootstrap, run from the Go checkout:

```sh
go build -o bin/modelgauge ./cmd/modelgauge
bin/modelgauge node bootstrap --provider lima --name benchmark-node
bin/modelgauge node list
bin/modelgauge node status benchmark-node
bin/modelgauge model install qwen2.5-1.5b-instruct:q4_k_m --node benchmark-node
```

No guest directories, controller addresses, tokens or runtime paths need to be supplied.
The saved active control connection supplies enrollment details. The controller starts and owns the executor. It reaches the guest over a pinned
SSH connection through Lima's existing loopback forward. The guest runs temporary
workers without controller credentials. Keep Lima and both VMs running.

Bootstrap defaults to two vCPUs, 3 GiB RAM and a 30 GiB disk. `--cpus`,
`--memory`, and `--disk` change new VM resources. Existing named VMs are reused;
VM resource settings are not resized. A VM containing k3s is refused to avoid
benchmark/control-plane interference. An active llama-bench or llama-server process also blocks
an upgrade.

The command installs Git, CMake, the GNU build tools and GNU time in the Linux
VM, then builds CPU-only llama-bench and llama-server at upstream commit
`eb4e244be77a906a183cd4a00fd51d9ad3200955`. Native CPU tuning, OpenMP, BLAS, CUDA
and Vulkan are disabled for a reproducible initial CPU profile. Select another
full commit with `--llama-revision`. This pin matches the revision used in the
[archived Linux benchmark evidence](../HISTORICAL-BENCHMARKS.md); older measurements
with different build flags must still be compared separately.

The runtime is cached by revision under ModelGauge's managed runtime directory.
An already built revision is reused when both executables are present and pass `--help`.
Rerunning bootstrap upgrades older cached revisions that contain only llama-bench.
The worker discovers the current managed runtime before PATH/legacy locations.
Direct local execution requires an explicit runtime binary or llama-bench on PATH.
A released CLI downloads its matching Linux worker for the guest architecture,
checks its SHA-256 and size, validates it as Linux ELF, and transfers it through
Lima. Source builds compile it on the host instead. Systemd lingering allows
short-lived supervised workers; no permanent polling agent is installed.
Models and raw evidence use managed storage inside the guest. Private SSH credentials and pending upload records stay on the controller PVC.

For a host outside the checkout, supply `--source /path/to/repo`. This requires
Go on the host. `--agent-binary /path/to/linux-modelgauge` accepts a compatible
prebuilt Linux executable instead. A Linux host matching the guest architecture
can reuse its running binary. Release binaries need neither override nor host Go.
The Linux VM still needs build tools to compile the pinned llama.cpp runtime.

Bootstrap registers a dedicated SSH key with the controller, pins the node's
existing SSH host key and records only a restricted public key on the node.
The private key never enters the benchmark VM. After enrollment succeeds, old
controller keys are retired; unrelated SSH keys remain unchanged. Cleanup removes
only the public-key lines recorded by ModelGauge. The node's original SSH access
is preserved.

No host executor command is needed. Inspect controller logs if a model install
stays queued. `node list/status` show controller-observed hardware and last contact.
The existing shared agent credential remains an internal lease-API credential;
per-node API credential issuance is separate work.

Upgrades first pause controller execution and refuse pending work. On failure,
the old connection is resumed where possible; successful enrollment replaces it.
A managed older node's resident polling service/token is retired. Update the
control deployment using this source version before upgrading benchmark nodes,
because older controllers do not expose connection enrollment.

The existing Lima host-loopback networking must be enabled. Bootstrap discovers
the forwarded SSH port and host gateway rather than assuming a guest directory or
IP. New benchmark VMs receive a fixed SSH forwarding port; existing VM configurations
are preserved. Bootstrap probes that forward from the control VM before compiling
the runtime. A changed SSH host key is rejected. After restarting an existing VM
whose forwarded port changed, use `modelgauge node reconnect --name NODE` to refresh
the idle executor without rebuilding or reinstalling models. SSH transport is implemented in Go;
controller images require neither an SSH executable nor limactl.

K3s continues to own the controller, database and monitoring. The controller adds
one 1 GiB PVC for connections and recovery journals. Node exporter registration
remains separate; result metrics are still exposed through the controller.

## ✅ Historical deployment validation

These checks predate the project rename; they have not been repeated on a
newly deployed ModelGauge installation. Recorded IDs are historical evidence.

Created `benchmark-node` from a fresh Lima Ubuntu VM with 2 CPUs and 3 GiB RAM.
The pinned runtime built, the systemd user service enrolled automatically, and
rerunning bootstrap reused the existing runtime and preserved the model cache.
The earlier validated Q4 file was copied into the cache for the execution check
and matched its publisher checksum; this check did not download a new model.

Plan `200fe46e-2e6e-4e36-833b-5d4ffc777496` completed through the polling agent
using the managed runtime with a 32-token prompt, 128-token generation, and
memory workload at 2 threads. Two throughput cases and peak RSS were persisted
to PostgreSQL and read through `results show`. The node returned online.
This is a workflow check rather than a sustained-performance baseline.

## 🧹 Ownership and cleanup (current source build)

Updated Lima bootstrap records managed directories/files and the guest identity.
A reused VM is never eligible for VM deletion. The transferred Linux Go
executable handles ownership and raw evidence without a Python runtime. Use the [cleanup guide](../operations/CLEANUP.md)
when experiments and pending uploads are finished. Manual/legacy agent installs
are not automatically enrolled into this ownership registry.
