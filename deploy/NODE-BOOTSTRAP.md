# 🖥️ Benchmark node bootstrap

[📚 Documentation home](../README.md)


After control-plane bootstrap, run from the Go checkout:

```sh
go build -o bin/modelgauge ./cmd/modelgauge
bin/modelgauge node bootstrap --provider lima --name benchmark-node
bin/modelgauge node list
bin/modelgauge node status benchmark-node
bin/modelgauge model install qwen2.5-1.5b-instruct:q4_k_m --node benchmark-node
```

No guest directories, controller addresses, tokens or runtime paths need to be supplied.
The saved active control connection supplies enrollment details. The guest
contacts the controller through `host.lima.internal` and the existing API tunnel.
The host and control VM must remain running for that connection.

Bootstrap defaults to two vCPUs, 3 GiB RAM and a 30 GiB disk. `--cpus`,
`--memory`, and `--disk` change new VM resources. Existing named VMs are reused;
VM resource settings are not resized. A VM containing k3s is refused to avoid
benchmark/control-plane interference. An active llama-bench process also blocks
an upgrade.

The command installs Git, CMake, the GNU build tools and GNU time in the Linux
VM, then builds CPU-only llama-bench at upstream commit
`eb4e244be77a906a183cd4a00fd51d9ad3200955`. Native CPU tuning, OpenMP, BLAS, CUDA
and Vulkan are disabled for a reproducible initial CPU profile. Select another
full commit with `--llama-revision`. This pin matches the revision used in the
repository's earlier Linux benchmark fixtures; older measurements with different
build flags must still be compared separately.

The runtime is cached by revision under ModelGauge's managed runtime directory.
An already built revision is reused after its executable passes `--help`.
The polling agent discovers the current managed runtime before PATH/legacy locations.
Direct local execution requires an explicit runtime binary or llama-bench on PATH.
With a versioned CLI, the matching Linux agent is downloaded for the guest's
architecture, checked against the release SHA-256 and size, validated as a Linux
executable, transferred through Lima, and installed as a systemd user service.
Development builds compile the agent on the host instead. The owner is configured
for systemd lingering so polling survives the SSH session. Configuration is
private; results and models use managed storage in the guest.

For a host outside the checkout, supply `--source /path/to/repo`. This requires
Go on the host. `--agent-binary /path/to/linux-modelgauge` accepts a compatible
prebuilt Linux executable instead. A Linux host matching the guest architecture
can reuse its running binary. Release binaries need neither override nor host Go.
The Linux VM still needs build tools to compile the pinned llama.cpp runtime.

The initial enrollment protocol still uses the saved shared agent credential;
per-node credential issuance remains separate security work. `node list/status`
show controller-observed state, last contact, OS/architecture and VM resources,
never the credential. If enrollment times out, inspect `systemctl --user status
modelgauge-agent` inside the guest. A previous idle agent is restarted after a
failed upgrade. Rerunning bootstrap reuses the VM/runtime and configuration.

Benchmark execution is independent of k3s scheduling: the measured VM runs the
agent/runtime while k3s owns the controller/database/monitoring. Host node-exporter
registration for newly provisioned benchmark VMs is currently a separate
monitoring setup step; benchmark result metrics appear through the controller.

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
