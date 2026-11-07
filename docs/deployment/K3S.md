# ☸️ k3s control-plane deployment

[📚 Documentation home](../../README.md)


The assets in `deploy/k3s/` deploy the ModelGauge Go controller and its PostgreSQL store to a
single-node k3s cluster. They deliberately do not contain credentials.

## 🔧 Prerequisites

- A working `kubectl` context pointed at the control-plane k3s cluster.
- A controller image available to that cluster. The default image is
  `ghcr.io/nerdylucifer/modelgauge:dev`; pass a different reference as the
  first argument to `deploy.sh`.
- k3s's default `local-path` storage class (or edit `postgres.yaml`).

## 🚀 First deployment

From the repository root, enter the deployment asset directory first:

```sh
cd deploy/k3s
chmod +x create-secrets.sh deploy.sh
./create-secrets.sh
./deploy.sh ghcr.io/YOUR_GITHUB_USER/modelgauge:TAG
```

For the local development rollout, where the image has been imported directly
into k3s as `modelgauge:local`, use `./deploy.sh modelgauge:local`.

`create-secrets.sh` creates long random PostgreSQL, agent, and administrator
tokens. It saves the two controller client tokens in
`~/.config/modelgauge/control.env`. Keep that file on the control machine; it
is outside the repository and has owner-only permissions.

The service is exposed as the fixed k3s NodePort `31080`. For the Lima control
VM used in development, an agent can use:

```sh
export MODELGAUGE_CONTROLLER_URL=http://CONTROL_VM_IP:31080
```

For example, the earlier private Lima network used `192.168.105.3`, so the URL
was `http://192.168.105.3:31080`. Confirm the current VM address rather than
assuming it is unchanged.

## ✅ Verification

```sh
kubectl -n benchmark-data get pods,pvc,service
kubectl -n benchmark-system get deploy,pods,service
curl -fsS http://CONTROL_VM_IP:31080/healthz
curl -fsS http://CONTROL_VM_IP:31080/metrics
```

The controller runs database migrations at startup. Do not manually apply
`001-init-schema.sql` unless you are recovering a database outside ModelGauge.

## 📋 Agent connection

On the measured Linux node, load a securely transferred **agent-only**
credential file. The generated control.env also contains an admin token, so do
not copy it wholesale onto a measured node. Provide a reachable API URL and
llama-bench on PATH (or the managed runtime path). For example:

```sh
export MODELGAUGE_CONTROLLER_URL=http://PRIVATE_CONTROL:31080
# MODELGAUGE_AGENT_TOKEN must already be loaded privately.
export MODELGAUGE_NODE_ID=benchmark-node-temp
export MODELGAUGE_NODE_NAME=benchmark-node-temp
export MODELGAUGE_EXECUTION_MODE=lima-linux
export MODELGAUGE_RESULTS_DIR=$HOME/modelgauge-results
modelgauge agent run --once
```

`agent run` is foreground and reads exported environment variables. For a
persistent service use [manual agent enrollment](../CONFIGURATION.md).

The current development authentication model uses one shared agent token.
Per-node credentials, TLS, image signing, and secret rotation are production
hardening work, not yet implemented.
