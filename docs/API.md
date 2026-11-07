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
that recovery guarantee. See [recovery](../deploy/JOB-RECOVERY.md).

## 💾 Schemas and evidence

- `benchmark-plan/v1`: node, execution mode, runtime, model identity, defaults, throughput cases and memory case.
- `benchmark-result/v1`: run/plan/node identity, terminal state, cases, memory, runtime metadata and artifact hashes.
- `benchmark-campaign/v1`: local CLI manifest grouping generated plan files and submitted plan IDs.
- `modelgauge-release/v1`: version/repository/commit, image digest and executable size/checksums.

The generic benchmark plan/result schema names are not product branding and stay
versioned independently of the binary. Raw evidence files are not exposed through
an artifact-download endpoint. PostgreSQL keeps normalized results and metadata;
full raw JSON remains on the measured node.
