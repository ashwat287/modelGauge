# 📦 Export results and metrics

[Documentation home](../../README.md) · [CLI reference](../CLI.md) · [Cleanup](CLEANUP.md)

Export is available in the current source build and requires an updated
controller. Published v0.1.0 predates the export command/API. Build and deploy
the updated source before using it. No VMs are created or started by export.

## 🚀 Save one node's experiment history

```sh
go build -o bin/modelgauge ./cmd/modelgauge
bin/modelgauge export --node benchmark-node --output exports/baseline
```

The saved controller connection supplies authentication. Explicit `--controller`
and `--admin-token` flags or the normal `MODELGAUGE_*` connection environment
variables work too. Prefer saved/private credentials to tokens in shell history.

The output must be a **new directory**. Existing files/directories are never
overwritten. Use a fresh name for each export. Output files have owner-only
permissions. The bundle contains all recorded models and terminal runs for that
node, not only the latest successful experiment. Failed/cancelled records are
retained. Active/queued plans appear as metadata with a warning; their final
results require another export after completion.

## 🗂️ Bundle contents

| File | Contents |
|---|---|
| `manifest.json` | Schema, node ID, capture times, scopes, warnings, file sizes and SHA-256 hashes |
| `SHA256SUMS` | Checksums for exported files and the manifest itself |
| `results.json` | Normalized terminal run/case throughput and memory results |
| `results.csv` | One row per case; failed runs without cases still get a row |
| `node.json` | Registered identity, hardware and agent version |
| `plans.json`, `runs.json`, `cases.json` | Stored specifications, statuses, runtime metadata and case records |
| `models.json`, `memory.json` | Model IDs/checksums/sizes and stored memory measurements |
| `host-snapshots.json` | Snapshots actually stored in the controller database; may be empty |
| `model-installs.json` | Install IDs, model IDs, checksums, states and timestamps |
| `stored-recommendations.json` | Persisted recommendation rows; on-demand recommendation responses are not automatically stored |
| `artifacts.json` | Raw file references and their recorded hashes; not file contents |
| `metrics.prom` | Current controller-wide Prometheus exposition snapshot |
| `raw/` | Optional checksum-verified, manifest-listed node files |
| `prometheus/` | Optional history-query JSON responses |

CSV memory values are bytes; throughput is tokens/second. Text fields that could
be interpreted as spreadsheet formulas are escaped with an apostrophe in CSV;
JSON preserves the original strings. Missing memory/case measurements are blank
in CSV rather than invented zero-throughput samples.

Database records are read in one repeatable-read, read-only transaction.
`metrics.prom` is collected separately and contains **controller-wide latest
operational/result gauges**, including other nodes. It is not a scrape history
or a filtered copy of this node's experiment history. Capture times and scope
are recorded so these observations are not presented as one atomic global snapshot.

## 🧪 Include raw evidence from a managed Lima target

For a large experiment history, export one run at a time:

```sh
bin/modelgauge export --node benchmark-node --run RUN_ID \
  --output exports/one-run --include-raw
```

`--run` selects the run, its plan, measurements, and registered evidence. Current
node/model metadata remains included; unrelated installation jobs are omitted.
The manifest records the selection. This keeps exports within the existing
64 MiB response limit; it requires an updated controller. `--start` and `--end`
continue to select Prometheus history only.

```sh
bin/modelgauge export --node benchmark-node --output exports/with-evidence --include-raw
```

ModelGauge identifies the guest using the ownership registry, verifies the VM
and guest identity, and reads its managed results directory. No guest paths or
SSH commands are required. The target must already be running, be a tracked Lima
benchmark node, and have intact ownership metadata. The ownership lock prevents
cleanup while raw files are being transferred.

Only files listed in the controller's artifact inventory are copied, with their
recorded hashes checked. This normally preserves repetition samples, warm-up JSON,
plans, runtime hashes, GNU time output and pre/postflight files. Unlisted files
(including a node's `result.json` when not registered as an artifact) are not
copied. The exporter does not separately scan model, credential, kubeconfig or
service directories. Custom files placed inside run artifact directories may
be included if the agent registered them.
Normalized result data is exported separately in JSON/CSV.

If files were removed or changed, requested raw export fails rather than claiming
a complete bundle. A zero-length artifact inventory means no raw contents can be
inventoried; the manifest reports that explicitly. Symlinks, traversal names,
non-regular files and unexpected archive members are refused. Per-file raw size
is limited to 128 MiB, total raw data to 1 GiB and automatic Lima requests to
10,000 files.

For a manually managed node, first copy its run directories locally, then:

```sh
bin/modelgauge export --node linux-node --output exports/copied-evidence \
  --raw-from /absolute/path/to/copied-results
```

The local root must contain `RUN_ID/ARTIFACT_NAME` paths matching the inventory.
`--raw-from` and `--include-raw` are mutually exclusive. Raw exports read node
files; they do not download or back up the model weights themselves.

## 📈 Export Prometheus history

Make Prometheus reachable before exporting. For the default Lima control VM,
leave this command running in another terminal (substitute your actual VM name):

```sh
limactl shell --workdir / modelgauge-control -- sh -lc \
  'exec kubectl --kubeconfig "$HOME/.kube/modelgauge-config" -n monitoring port-forward svc/prometheus 9090:9090'
```

The guest resolves its own home directory; no guest username or absolute path
needs to be supplied. Keep that forward running while exporting from another
host terminal.

Then, from the host, request an explicit time window with a timezone:

```sh
bin/modelgauge export --node benchmark-node --output exports/history \
  --prometheus-url http://127.0.0.1:9090 \
  --start 2026-11-07T10:00:00Z --end 2026-11-07T11:00:00Z --step 30s
```

Replace those illustrative timestamps with your experiment's actual window.
The implementation uses Prometheus's [range-query HTTP API](https://prometheus.io/docs/prometheus/latest/querying/api/#range-queries).
The default queries select this node's throughput, peak RSS, Linux available RAM,
and CPU rate. Linux RAM/CPU series require that node's exporter and matching
`node` labels; node bootstrap does not install/register its exporter automatically.

Repeat `--query` to replace the defaults with chosen PromQL expressions:

```sh
bin/modelgauge export --node benchmark-node --output exports/custom-history \
  --prometheus-url http://127.0.0.1:9090 \
  --start 2026-11-07T10:00:00Z --end 2026-11-07T11:00:00Z \
  --query 'modelgauge_memory_reservation_bytes{node="benchmark-node"}' \
  --query 'up'
```

Queries, response warnings and returned series counts are recorded in the
manifest. Empty history is reported explicitly; it may mean missing targets,
label mismatches, the wrong time window or expired retention. Prometheus cannot
recover samples it no longer retains. This is a selected query export, not a
TSDB backup. The time window applies to Prometheus only; database results still
include the node's full recorded terminal history.

At most 20 queries and 11,000 intervals per query are accepted, with a step of at
least one second. Controller snapshot responses are capped at 64 MiB, current
metrics at 16 MiB, and each history response at 32 MiB. Excessive or failed
requests fail explicitly. If your Prometheus gateway needs bearer authentication,
load `MODELGAUGE_PROMETHEUS_TOKEN` privately; it is sent only to Prometheus, never
written into the bundle. Embedded URL credentials are refused.

## ✅ Verify before cleanup

```sh
cd exports/with-evidence
shasum -a 256 -c SHA256SUMS
# On Linux, sha256sum -c SHA256SUMS is equivalent.
```

A successful export has `manifest.json` and `SHA256SUMS` and **no `INCOMPLETE`
marker**. Inspect manifest warnings and keep the bundle outside the VMs you will
delete. After verifying it, return to your checkout and run:

```sh
bin/modelgauge cleanup --dry-run --delete-vms
bin/modelgauge cleanup --delete-vms
```

Cleanup does not automatically create or validate an export for you. A failure
after the output directory is created leaves `INCOMPLETE` and partial files for
inspection; do not delete source evidence based on that directory. Retry into a
new directory. Export is not a restorable PostgreSQL backup and does not include
the controller connection file, lease tokens or private model download URLs as
export fields. It does not back up model weights or arbitrary node directories.
Custom metadata/artifact contents, node identities and model/runtime paths remain
experiment data in the bundle.

## 🧪 Verification scope

Tests cover JSON/CSV contents, checksums, no-overwrite behavior, raw streaming,
missing/corrupt/unsafe evidence, Prometheus errors and empty history, distinct
source authentication and incomplete-export markers. PostgreSQL snapshot/scope
and credential-exclusion tests run when the integration database is configured.
The feature was tested locally without recreating the deleted Lima lab.
