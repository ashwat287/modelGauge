# 🏗️ Control plane bootstrap

[📚 Documentation home](../../README.md)


Prerequisite: Lima must already be installed on the host. Run from the repository
to build and deploy the current controller without Docker Desktop:

```sh
go build -o bin/modelgauge ./cmd/modelgauge
bin/modelgauge control bootstrap --provider lima
bin/modelgauge control status
bin/modelgauge monitoring open
```

On first use, bootstrap defaults to `modelgauge-control`; subsequent calls default
to the saved active control VM and API port. New VM defaults are four CPUs, 6 GiB memory and 50 GiB
disk. It creates a Linux Ubuntu VM or starts an existing named VM. Resource
flags only affect a newly created VM. An existing k3s installation is started
without an upgrade. New installs pin k3s to `v1.37.1+k3s1`; override with
`--k3s-version` when needed.

The binary embeds the PostgreSQL, controller, monitoring, and dashboard assets.
It installs a Linux exporter on the control VM and discovers its address.
`--benchmark-target HOST:9100` optionally adds an already installed benchmark
node exporter to Prometheus. Installing the benchmark agent/node is separate.

```sh
bin/modelgauge control bootstrap --provider lima \
  --name benchmark-control --source . \
  --benchmark-target 192.168.105.1:9100
```

Versioned release binaries resolve their matching controller image automatically
from release metadata, pinned by digest. They work outside a checkout without
host Go or Docker. [v0.1.0 is published](https://github.com/ashwat287/modelGauge/releases/tag/v0.1.0);
see [release installation](../releases/RELEASING.md) for the download and checksum commands.

For development outside a Go checkout, use `--source /path/to/repo` (requires Go)
or `--image REGISTRY/CONTROLLER:VERSION` for an explicitly selected container.
Local builds produce an OCI image with a content-addressed tag and import it
directly into k3s, so Docker Desktop is not required.

Bootstrap preserves existing PostgreSQL PVCs, database passwords, agent tokens,
and Grafana credentials. It reapplies manifests and restarts the controller and
monitoring workloads. VM files are under the node owner's managed directory.
No cluster destruction or database reset is performed.

The host CLI stores the active VM, loopback API URL and credentials in
`~/.config/modelgauge/control.json` with mode 0600. It reads that connection by
default; explicit `MODELGAUGE_CONTROLLER_URL` and `MODELGAUGE_ADMIN_TOKEN`
environment variables override it. Agent credentials are retained for future
node enrollment but are not automatically exported to shell environments.

The API is forwarded to host loopback port 18080; `--port` chooses another port
for a second VM. The forward depends on the Lima VM remaining running.
`monitoring open` forwards Grafana to loopback port 3000 and opens the dashboard.
The forwards run as systemd user services in the control VM and restart after
target pod replacement. Ensure another application/VM is not already using the
chosen local port. The Lima VM still needs to be running for host port mapping.

Grafana uses username `admin`. Read its generated password inside the VM with:

```sh
kubectl --kubeconfig "$HOME/.kube/modelgauge-config" -n monitoring \
  get secret grafana-admin -o jsonpath='{.data.password}' | base64 --decode
```

`control status` reports cluster nodes, deployments and persistence. It never
prints credentials. Interrupted provisioning can be retried with the same
name and flags; inspect reported errors rather than deleting the VM.

## ✅ Historical integration paths

These checks predate the project rename. They describe earlier validated paths,
not proof that the renamed VM/service names have been deployed.

- Reused `benchmark-control` with existing PostgreSQL data, credentials and the
  benchmark-node exporter. Controller and monitoring deployments became ready;
  the host CLI listed node models using only its saved connection.
- Created a separate bootstrap-check VM from the Ubuntu template with 2 CPUs,
  3 GiB RAM and a 20 GiB disk. Installed k3s, generated credentials, deployed
  PostgreSQL/controller/Prometheus/Grafana and verified the host API tunnel on
  port 18081. A startup registration race was fixed with a bounded node wait.
- Restored the original active connection after validation. The separate
  validation VM is retained in stopped state for inspection.

## 🧹 Ownership and cleanup (current source build)

Updated bootstrap records VM creation/reuse and reserves managed guest paths
before installation. It refuses untracked directories and untracked k3s control
installations rather than guessing ownership. Ownership and evidence helpers run
inside the same Linux Go executable as the agent; Python is not required.
Bootstrap transfers the executable before preparing managed guest paths.
For a development CLI using `--image` outside a source checkout, also pass
`--helper-binary /path/to/matching-linux-modelgauge`. Release CLIs fetch the
matching Linux executable automatically.
See [cleanup](../operations/CLEANUP.md) for preview, VM deletion and legacy limits.
