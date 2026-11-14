# 🔌 Controller HTTP API

[Documentation home](../README.md) · [Architecture](ARCHITECTURE.md) · [Configuration](CONFIGURATION.md)

The Go CLI is the supported user interface. The API below documents the current
implementation for automation/integration; there is no generated OpenAPI document
yet. Versioned plan/result schemas live in `pkg/protocol`. Do not treat error text
or implementation-specific database fields as a permanent client contract.

## 🔑 Authentication and response basics

| Header | Purpose |
|---|---|
| `X-ModelGauge-Admin-Token` | Plans, installs, node inventory, results and recommendations |
| `X-ModelGauge-Token` | Registration, heartbeat, ownership, renewal and completion |

`GET /healthz` and `GET /metrics` do not require a token. The current shared
agent token is a development trust model, not per-node authorization. HTTP does
not encrypt tokens; expose only on a trusted private network until TLS is added.
Health includes database reachability and, on current builds, version/commit.
Metrics use Prometheus text format; normal API bodies are JSON.

## 📋 Administrative routes

| Method | Path | Meaning |
|---|---|---|
| `GET` | `/v1/nodes` | Registered nodes |
| `GET` | `/v1/nodes/{nodeID}` | State, last contact, hardware |
| `GET` | `/v1/nodes/{nodeID}/models` | Installed model inventory |
| `POST` | `/v1/nodes/{nodeID}/model-installs` | Queue a verified download specification |
| `GET` | `/v1/model-installs/{jobID}` | Install state/error |
| `POST` | `/v1/model-installs/{jobID}/cancel` | Cancel an install |
| `POST` | `/v1/plans` | Queue a `benchmark-plan/v1` document |
| `GET` | `/v1/plans/{planID}` | Plan state/specification |
| `POST` | `/v1/plans/{planID}/cancel` | Cancel a benchmark |
| `GET` | `/v1/nodes/{nodeID}/export` | Authenticated repeatable-read snapshot; optional `?run=RUN_ID` selects one run and its related records |
| `GET` | `/v1/nodes/{nodeID}/results` | Normalized terminal results |
| `GET` | `/v1/nodes/{nodeID}/recommendation` | Query parameters: `model`, `execution_mode`, `context`, `memory_budget_bytes` |

Example using a controller/admin credential already loaded privately:

```sh
curl -fsS "$MODELGAUGE_CONTROLLER_URL/healthz"
curl -fsS "$MODELGAUGE_CONTROLLER_URL/v1/nodes" \
  -H "X-ModelGauge-Admin-Token: $MODELGAUGE_ADMIN_TOKEN"
curl -fsS -X POST "$MODELGAUGE_CONTROLLER_URL/v1/plans" \
  -H "X-ModelGauge-Admin-Token: $MODELGAUGE_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' --data-binary @plan.json
```

`POST /v1/plans` returns a generated `plan_id` and state. Submitting the same
specification twice creates separate work; callers must avoid accidental repeat
submission. Use the CLI for catalog resolution/download job payloads rather than
constructing unverified model metadata manually.

## 🤖 Agent routes

| Method | Path | Meaning |
|---|---|---|
| `POST` | `/v1/nodes/register` | Node identity, version and hardware |
| `POST` | `/v1/nodes/{nodeID}/heartbeat` | Online/busy state |
| `POST` | `/v1/nodes/{nodeID}/models` | Sync installed inventory |
| `POST` | `/v1/nodes/{nodeID}/lease` | Claim queued benchmark |
| `POST` | `/v1/nodes/{nodeID}/plans/{planID}/renew` | Renew benchmark ownership |
| `POST` | `/v1/nodes/{nodeID}/runs/{runID}/start` | Start owned run |
| `POST` | `/v1/nodes/{nodeID}/runs/{runID}/complete` | Upload owned result |
| `POST` | `/v1/nodes/{nodeID}/model-installs/lease` | Claim queued installation |
| `POST` | `/v1/nodes/{nodeID}/model-installs/{jobID}/renew` | Renew install ownership |
| `POST` | `/v1/nodes/{nodeID}/model-installs/{jobID}/complete` | Complete installation |

Let the shipped agent drive this protocol. Claims carry lease tokens and expiry;
renewal/completion must use the owning token and matching node/job/run. A stale
agent must not retry by pretending it owns a new lease. Completion replay is safe
for the same owner/terminal result; arbitrary submissions are not equivalent to
that recovery guarantee. See [recovery](operations/JOB-RECOVERY.md).

## 💾 Schemas and evidence

- `benchmark-plan/v1`: node, execution mode, runtime, model identity, defaults, throughput cases and memory case.
- `benchmark-result/v1`: run/plan/node identity, terminal state, cases, memory, runtime metadata and artifact hashes.

Managed serving plans supply `benchmark: {"kind":"serving","config":{...}}`,
`runtime.kind: "llama-server"`, an installed model path/SHA-256 and a `server`
configuration inside the module config. Engine cases are not used. The module
validates its configuration and owns the model process on the measured node.
These plans use the same submission/lease/completion routes. Results carry
`benchmark: {"kind":"serving","data":{...}}` with schema `modelgauge-serving/v1`.
Historical top-level `serving` reports remain readable; regenerate old endpoint
plans using the node-serving profile. Generic reports persist in the existing
JSON metadata without a database migration. Update CLI, controller and agent
binaries together. No external inference endpoint or inference credentials are
needed. See [serving methodology](BENCHMARKING.md#-serving-benchmarks-on-the-measured-node).

- `benchmark-campaign/v1`: historical local CLI manifests; new experiments use controller-owned Benchmark declarations.
- `modelgauge-release/v1`: version/repository/commit, image digest and executable size/checksums.

The generic benchmark plan/result schema names are not product branding and stay
versioned independently of the binary. Raw evidence files are not exposed through
an artifact-download endpoint. PostgreSQL keeps normalized results and metadata;
full raw JSON remains on the measured node.

## 🔌 Controller-owned node connections

Admin authentication is required for every connection operation:

| Method and path | Behavior |
|---|---|
| `GET /v1/nodes/{nodeID}/connection` | Configured/pending status; never returns credentials |
| `PUT /v1/nodes/{nodeID}/connection` | Enroll an SSH target and start controller execution |
| `DELETE /v1/nodes/{nodeID}/connection` | Stop an idle executor and remove its connection |
| `POST /v1/nodes/{nodeID}/connection/pause` | Persistently pause an idle target for upgrade/cleanup |
| `POST /v1/nodes/{nodeID}/connection/resume` | Resume its saved connection |

PUT accepts `address` (`host:port`), `user`, `private_key` (unencrypted PEM) and
`host_key` (OpenSSH public host key). Node bootstrap handles this automatically.
Private credentials are stored as mode-0600 files on the controller PVC; GET
returns `node_id`, `configured`, `pending`, `paused`, `location: controller` and,
for a running executor, `address`, `last_error`, `last_attempt_at` and
`last_success_at`. Timestamps are omitted until an iteration completes.
`GET /v1/nodes/{nodeID}` includes these diagnostics under `executor`, alongside
the existing node fields. Credentials are never returned. Node status becomes
`offline` after 90 seconds without a heartbeat.
Invalid credentials return 400; pending-work conflicts return 409. Servers without
executor storage configured return 503. Connection changes never discard journals.

## 📋 Named declarative experiments

Admin authentication is required for these endpoints:

| Method and path | Behavior |
|---|---|
| `POST /v1/experiments` | Body: JSON representation of the YAML Benchmark declaration; atomically resolve, freeze, queue all plans |
| `POST /v1/experiments?dry_run=true` | Resolve/validate only; no experiment/jobs created |
| `GET /v1/experiments/{name}` | Read frozen declaration, resolved plans, current job statuses |
| `POST /v1/experiments/{name}/cancel` | Cancel queued/active plans; retain completed jobs/evidence |

Responses contain `name`, `spec_sha256`, `specification`, `created_at` (accepted
experiments), and `plans` with `plan_id`, current `status`, and resolved
`specification`. GET/cancel accept optional `spec_sha256` to reject a stale/edited
file with HTTP 409. Changed content under an existing name also returns 409;
unknown names return 404; invalid declarations or missing installed targets return
400. Credentials are supplied through the existing admin header, never YAML.
An accepted reapply returns the original snapshot even if node inventory has changed.
Migration `004-experiments` adds the controller-owned record; infrastructure
manifests and the worker execution protocol are unchanged by YAML support.
