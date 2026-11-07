# 🧹 Owned-resource cleanup

[Documentation home](../../README.md) · [Operations](../OPERATIONS.md) · [Bootstrap](../deployment/BOOTSTRAP.md)

Cleanup is available in the current source build. The published **v0.1.0 binary
predates this command**. Build the updated CLI before using it; bootstrap with
this updated build to establish ownership metadata.

## 🚀 Commands

```sh
go build -o bin/modelgauge ./cmd/modelgauge

# Preview exactly the mode you intend to use. No VMs are started or changed.
bin/modelgauge cleanup --dry-run --delete-vms

# Remove verified managed guest directories/files and user services; keep VMs.
bin/modelgauge cleanup

# Also stop/delete only the Lima VMs ModelGauge originally created.
bin/modelgauge cleanup --delete-vms
```

Each command returns a JSON report. There is no interactive prompt; `--delete-vms`
is the explicit request to delete owned VM disks. A VM disk deletion also removes
anything subsequently added to that VM, including files outside ModelGauge's
managed directories. [Export and verify evidence](EXPORT.md) you want to keep before cleanup.
The export destination must be outside any VM/directory you intend to delete.

The command covers **all tracked Lima installations for the current host user**,
not only the active saved controller. It does not clean a remote physical machine
or a manually installed agent automatically. `agent uninstall` remains available
for removing a manually installed Linux agent's service.

## 🧾 Ownership rules

Bootstrap writes a private host registry at
`~/.config/modelgauge/ownership.json`, a unique marker in newly created Lima
instance directories, and guest ownership manifests/markers. It records whether
it created or reused a VM and binds the installation to that guest's machine ID.
A matching VM name alone is never sufficient for deletion.

The guest manifest reserves exact files before bootstrap writes them. Directory
roots receive ownership markers, while files outside those roots are sealed
with SHA-256 fingerprints. Bootstrap refuses to adopt pre-existing untracked
managed directories or overwrite untracked reserved files. An existing k3s
installation without ownership metadata is refused as a control installation.

| Resource | Cleanup behavior |
|---|---|
| `~/.local/share/modelgauge/` in a tracked guest | Remove only with matching directory and installation markers; includes runtime, models and results |
| `~/.config/modelgauge/` in a tracked guest | Remove only with matching markers |
| Managed binaries, kubeconfig and systemd user units | Remove only individually recorded paths whose fingerprints still match |
| Pre-existing `~/.kube/config` | Preserve; only a file created by bootstrap is recorded |
| Reused/pre-existing Lima VM | Always preserve, including with `--delete-vms` |
| VM created by this tracked bootstrap | Preserve by default; delete with `--delete-vms` after identity checks |
| Untracked or legacy VM/directory | Preserve; no name-based guessing or automatic adoption |
| Local active connection | Remove only when bound to a successfully cleaned installation ID |
| Host download caches, source repository, Go/Lima, guest OS packages | Preserve |

On a **reused control VM**, k3s, Kubernetes workloads/PVC data and system packages
outside the managed directory roots are retained. Cleanup is not a shared-cluster
uninstaller. For a disposable control plane, let ModelGauge create a dedicated VM
and use `--delete-vms` to remove the entire owned environment. After directory-only
control cleanup, use VM deletion/recreation for a fresh control installation;
cleanup does not automatically re-adopt the remaining k3s installation.

## 🛑 Safety checks and active work

Cleanup first checks every tracked target. It refuses missing/mismatched markers,
a changed machine identity, unsafe symlink paths, mounted filesystems inside the
managed directories, protected VMs selected for deletion, changed/unsealed files,
and active execution locks. Pending agent work—including downloads and result
uploads—blocks cleanup. The control database is checked for queued/leased/running
plans and installs when its configured database pod is present.

Finish or cancel experiments and allow the agent to acknowledge them before
cleanup. Do not submit new work, run another agent or edit managed resources
concurrently. Once initial checks pass, cleanup stops owned user services,
checks activity again, then removes benchmark targets before the control target.
It never bypasses ownership failures with a force-delete flag.

A stopped VM is not booted by `--dry-run`. Its report says
`guest_verification_required`, lists reserved paths, and explains that actual
cleanup must start it to inspect guest identity and work state. Actual cleanup
starts stopped tracked guests for those checks; cleanup attempts to return retained guests
to their previous stopped state afterward and reports a failure to do so. A starting control cluster may need
time to become ready before its job state can be verified; retry after readiness.

## 🔁 Failure, retry and older installations

Ownership state is checkpointed after guest cleanup and after each successful
VM deletion. A failed deletion retains its record so the same command can be
retried. Directory-only cleanup retains the provenance of created VMs so a later
`cleanup --delete-vms` can still remove them. Untracked/missing resources are
never replaced just to make cleanup succeed.

If cleanup stops on a safety or service error, some owned services may already
be stopped. Inspect the reported error; finish/recover pending work before
retrying. Restart the recorded agent/forward units if they are needed for that
recovery. Do not delete ownership manifests, edit fingerprints or replace
markers to bypass the check.

An interrupted creation before ownership could be durably recorded, or a crash
leaving unsealed files, can require manual inspection. Cleanup chooses refusal
rather than claiming an uncertain resource. Older installations (including
v0.1.0) lack these records and are left untouched. There is no `--adopt` option.

## ✅ Verification scope

Tests execute the Go guest filesystem code against temporary guest homes
and simulate Lima/service boundaries. They cover created vs reused VMs, untracked
files, directory/file identity, symlinks, pending work and process locks, dry-run
behavior, protected VMs, failed deletion retry, repeated cleanup and connection
ownership. No new live Lima VMs were created for this feature's local tests.

### 🐹 Go guest helpers

Ownership checks and raw evidence streaming run in the same Linux Go executable
as the agent. Bootstrap transfers it first and installs a checksum-tracked helper
inside the owned data directory. A private, checksum-verified copy in the Lima
instance directory lets later cleanup run without rebuilding from source.
Python is not required on the host or guest.

A normal dry run invokes the installed helper without staging files. If the VM is
stopped or its managed directories were already cleaned, the preview reports that
guest verification still requires actual cleanup. Actual preparation or cleanup
may stage the Go executable in a temporary guest directory, removed on exit.
The cache is removed when a reused VM's ownership record is retired, or with a
created VM when it is deleted. Directory-only cleanup retains the cache and VM
provenance for a later `--delete-vms` invocation.

Older ownership records without a Go helper must be bootstrapped again using the
current CLI before cleanup. Untracked installations remain protected.
