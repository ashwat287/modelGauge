# 🔄 Job recovery and cancellation

[📚 Documentation home](../README.md)


```sh
bin/modelgauge benchmark cancel PLAN_ID
bin/modelgauge model install cancel JOB_ID
bin/modelgauge benchmark campaign status --file campaign.json
bin/modelgauge results show --node benchmark-node
```

The controller serializes claims on each node row across benchmark and model
installation queues. A node cannot claim another job while it owns an active
lease. The agent also holds a host/user process lock across polling; downloads
and direct/agent benchmarks share an execution lock. Separate Unix users or
non-ModelGauge workloads remain outside those local locks.

Leases last two minutes and are renewed every five seconds while executing a
job. Renewal requests time out in five seconds. A temporary controller outage
does not immediately interrupt work; losing ownership or reaching the last
acknowledged expiry stops execution. A controller sweeper checks expired jobs
every 15 seconds, including when the original node is offline.

Expired, unstarted benchmark leases return to queued. Expired running
benchmarks become failed with an explicit interruption reason; they are not
silently rerun as new measurements. Expired downloads return to queued and
can resume their partial file. Lease tokens fence stale owners. Result and
download completion are replay-safe for the same owner and terminal status.

The agent syncs an owner-only `.pending.json` journal in its results directory
before starting a claimed job. Results are saved before upload. On restart the
agent handles that journal before polling for new work. A complete result is
uploaded without executing the model again; transient uploads retry with
bounded backoff. A controller rejection retains the journal as a `.rejected-*`
evidence file instead of repeatedly executing stale work.

Within a still-valid lease, an interrupted benchmark resumes finished case
files only when the saved plan, runtime checksum, case shape and repetition
count match. The unfinished case is warmed and measured again. A durable memory
checkpoint avoids rerunning a completed memory measurement. Preflight evidence
is retained, and resumed execution is tagged `recovered` in runtime metadata and
results output. This produces a recovered run rather than claiming an uninterrupted
thermal baseline; raw evidence and the system logs should accompany analysis.

SIGTERM during a benchmark/download retains pending work for recovery. Process
groups stop GNU time and its benchmark child together, so cancellation cannot
release the execution lock while leaving a measured child running. Partial
HTTP downloads resume with validated byte ranges; a checksum-verified complete
cache entry skips the network.

Cancellation of queued work is immediate. Active work notices cancellation on
its next successful renewal and stops; its terminal state is cancelled. A
controller outage delays that signal until connectivity returns or the lease
expires. Cancellation is cooperative at the protocol level and forced for the
measured process group. Failure reasons are exposed with stored results.

The local controller/Grafana tunnels are systemd user services in the control
VM with automatic restart and user lingering, so pod replacement does not
require manually rebuilding the host connection.

Database integration tests use an isolated temporary schema when
`MODELGAUGE_TEST_DATABASE_HOST` and `MODELGAUGE_TEST_DATABASE_PASSWORD` are set.
They check cross-queue concurrency, ownership, renewal, expiry, cancellation and
replayed completion without modifying the live benchmark tables. Unit tests
exercise cancelled/resumed downloads, saved-result upload retries, and Linux
wrapper-child termination.

## ✅ Integration validation

Historical development checks below predate the rename. Use newly returned IDs
when reproducing them; do not reuse these plan IDs.

On the existing Lima setup, plan `4af94f16-b640-4145-ab23-18d1bcbf1a32`
was interrupted by restarting the active agent after its prompt case finished.
The prompt file kept the same SHA-256 and modification time; only the unfinished
generation case restarted. The controller was then scaled down while execution
finished. A complete result was saved locally, and the agent was restarted again
with that upload pending. After the controller returned, the saved result was
acknowledged, the journal cleared, and PostgreSQL contained exactly two cases.
The result and measurement files kept their timestamps during upload recovery.

A second agent process on the same node was refused by the host lock. Plan
`42eafef9-ee72-4064-9025-74f881a2fe7c` was cancelled during active execution;
the measured process stopped and its stored reason was `cancelled by user`.
The GNU-time-style wrapper/child cancellation test passed inside Linux.
These runs validate lifecycle behavior rather than sustained throughput.
