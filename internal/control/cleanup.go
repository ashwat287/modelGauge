package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type CleanupOptions struct {
	DryRun    bool
	DeleteVMs bool
}
type CleanupTarget struct {
	Name        string             `json:"vm"`
	Created     bool               `json:"created_by_modelgauge"`
	Delete      bool               `json:"delete_vm"`
	Status      string             `json:"status"`
	Directories []string           `json:"directories,omitempty"`
	Files       map[string]*string `json:"files,omitempty"`
	Note        string             `json:"note,omitempty"`
}
type CleanupReport struct {
	DryRun    bool            `json:"dry_run"`
	Targets   []CleanupTarget `json:"targets"`
	Untracked []string        `json:"untracked_vms_preserved"`
}

func Cleanup(ctx context.Context, options CleanupOptions) (CleanupReport, error) {
	m, err := newOwnershipManager()
	if err != nil {
		return CleanupReport{}, err
	}
	return m.cleanup(ctx, options)
}
func (m *ownershipManager) cleanup(ctx context.Context, o CleanupOptions) (report CleanupReport, err error) {
	report = CleanupReport{DryRun: o.DryRun, Targets: []CleanupTarget{}, Untracked: []string{}}
	// A missing manifest is a no-op, not a reason to create directories or scan
	// names for things that look like ours. Legacy installs are never adopted.
	if _, e := os.Lstat(m.path()); errors.Is(e, os.ErrNotExist) {
		return report, nil
	} else if e != nil {
		return report, e
	}
	if !o.DryRun {
		unlock, e := m.lock()
		if e != nil {
			return report, e
		}
		defer unlock()
	}
	registry, err := m.load()
	if err != nil {
		return report, err
	}
	instances, err := m.instances(ctx)
	if err != nil {
		return report, err
	}
	tracked := map[string]bool{}
	for _, v := range registry.VMs {
		tracked[v.Name] = true
	}
	for name := range instances {
		if !tracked[name] {
			report.Untracked = append(report.Untracked, name)
		}
	}
	sort.Strings(report.Untracked)
	// All targets must pass ownership and activity checks before deleting any.
	// Stopped guests are never started by a dry run.
	restarted := []string{}
	defer func() {
		for _, name := range restarted {
			if _, exists := instances[name]; exists {
				restoreCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				_, restoreErr := m.run(restoreCtx, []string{"stop", name}, nil)
				cancel()
				if restoreErr != nil {
					err = errors.Join(err, fmt.Errorf("could not return %s to its stopped state; stop it manually: %w", name, restoreErr))
				}
			}
		}
	}()
	for index, v := range registry.VMs {
		target := CleanupTarget{Name: v.Name, Created: v.Created, Delete: o.DeleteVMs && v.Created}
		vm, exists := instances[v.Name]
		if !exists {
			target.Status = "already_absent"
			report.Targets = append(report.Targets, target)
			continue
		}
		if e := verifyVM(v, vm); e != nil {
			return report, e
		}
		if target.Delete && vm.Protected {
			return report, fmt.Errorf("VM %s is protected; refusing deletion", v.Name)
		}
		if o.DryRun && len(v.Scopes) > 0 {
			target.Status = "provisioning_recovery_required"
			target.Note = "Interrupted provisioning scopes must be stopped by actual cleanup before guest verification"
			report.Targets = append(report.Targets, target)
			continue
		}
		if vm.Status != "Running" || o.DryRun && (!v.Prepared || v.Cleaned) {
			if o.DryRun {
				target.Directories = []string{".local/share/modelgauge", ".config/modelgauge"}
				target.Files = map[string]*string{}
				for _, file := range v.Files {
					target.Files[file] = nil
				}
				target.Status = "guest_verification_required"
				target.Note = "Guest/helper verification requires actual cleanup; dry run neither starts VMs nor stages helpers"
				report.Targets = append(report.Targets, target)
				continue
			}
			if _, e := m.run(ctx, []string{"start", "--tty=false", v.Name}, nil); e != nil {
				return report, e
			}
			restarted = append(restarted, v.Name)
		}
		if len(v.Scopes) > 0 {
			raw, e := m.run(ctx, []string{"shell", "--workdir", "/", v.Name, "--", "cat", "/etc/machine-id"}, nil)
			if e != nil {
				return report, e
			}
			if strings.TrimSpace(string(raw)) != v.MachineID {
				return report, fmt.Errorf("guest identity changed before provisioning recovery")
			}
			for _, scope := range v.Scopes {
				if e = m.stopScope(ctx, v.Name, scope); e != nil {
					return report, e
				}
			}
			registry.VMs[index].Scopes = nil
			if e = m.save(registry); e != nil {
				return report, e
			}
		}
		inspection := "inspect-cleanup"
		if o.DryRun {
			inspection = "inspect"
		}
		data, e := m.guest(ctx, v, inspection, v.Files)
		if e != nil {
			return report, e
		}
		var snapshot struct {
			Cleaned     bool               `json:"cleaned"`
			Directories []string           `json:"directories"`
			Files       map[string]*string `json:"files"`
		}
		if e = json.Unmarshal(data, &snapshot); e != nil {
			return report, e
		}
		if snapshot.Cleaned && !o.DryRun {
			registry.VMs[index].Cleaned = true
			if e = m.save(registry); e != nil {
				return report, e
			}
		}
		target.Directories = snapshot.Directories
		target.Files = snapshot.Files
		target.Status = "verified"
		if !v.Created && v.Role == "control" {
			target.Note = "Reused VM retained: system packages, k3s installation and cluster data outside managed directories are not removed"
		}
		report.Targets = append(report.Targets, target)
	}
	if o.DryRun {
		return report, nil
	}
	// Quiesce all pollers/owned user services, then recheck every target. New work
	// that raced the first inspection must prevent deletion rather than be lost.
	for _, v := range registry.VMs {
		if _, exists := instances[v.Name]; exists && !v.Cleaned {
			if _, e := m.guest(ctx, v, "quiesce", v.Files); e != nil {
				return report, e
			}
		}
	}
	for _, v := range registry.VMs {
		if _, exists := instances[v.Name]; exists {
			if _, e := m.guest(ctx, v, "inspect", v.Files); e != nil {
				return report, e
			}
		}
	}
	// Benchmark nodes are removed first; the controller remains available through
	// final activity checks. Missing/deleted VMs are pruned only after success.
	sort.SliceStable(registry.VMs, func(i, j int) bool { return registry.VMs[i].Role == "benchmark" && registry.VMs[j].Role != "benchmark" })
	remaining := append([]ownedVM(nil), registry.VMs...)
	for _, v := range registry.VMs {
		_, exists := instances[v.Name]
		if exists && !v.Cleaned {
			if _, e := m.guest(ctx, v, "clean", v.Files); e != nil {
				return report, e
			}
			for i := range remaining {
				if remaining[i].ID == v.ID {
					remaining[i].Cleaned = true
				}
			}
			registry.VMs = remaining
			if e := m.save(registry); e != nil {
				return report, e
			}
		}
		if exists && o.DeleteVMs && v.Created {
			// Revalidate the host-side identity immediately before destructive CLI use.
			current, e := m.instances(ctx)
			if e != nil {
				return report, e
			}
			if instance, present := current[v.Name]; present {
				if e = verifyVM(v, instance); e != nil {
					return report, e
				}
				if _, e = m.run(ctx, []string{"stop", v.Name}, nil); e != nil {
					return report, e
				}
				stopped, checkErr := m.instances(ctx)
				if checkErr != nil {
					return report, checkErr
				}
				if latest, present := stopped[v.Name]; present {
					if latest.Protected {
						return report, fmt.Errorf("VM became protected during cleanup")
					}
					if e = verifyVM(v, latest); e != nil {
						return report, e
					}
				} else {
					return report, fmt.Errorf("VM disappeared during cleanup; retry to reconcile ownership")
				}
				if _, e = m.run(ctx, []string{"delete", "--tty=false", v.Name}, nil); e != nil {
					return report, e
				}
			}
			delete(instances, v.Name)
		}
		if e := m.removeConnection(v); e != nil {
			return report, e
		}
		if !exists || (o.DeleteVMs && v.Created) || !v.Created {
			if err := m.removeHelper(v); err != nil {
				return report, err
			}
			next := []ownedVM{}
			for _, entry := range remaining {
				if entry.ID != v.ID {
					next = append(next, entry)
				}
			}
			remaining = next
			registry.VMs = remaining
			if e := m.save(registry); e != nil {
				return report, e
			}
		}
		for i := range report.Targets {
			if report.Targets[i].Name == v.Name {
				if !exists || o.DeleteVMs && v.Created {
					report.Targets[i].Status = "vm_removed"
				} else {
					report.Targets[i].Status = "managed_resources_removed_vm_preserved"
				}
			}
		}
	}
	if len(remaining) == 0 {
		if e := os.Remove(m.path()); e != nil && !errors.Is(e, os.ErrNotExist) {
			return report, e
		}
	}
	return report, nil
}
func (m *ownershipManager) removeConnection(v ownedVM) error {
	path := filepath.Join(m.directory, "control.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refusing to remove non-regular control connection")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var c Config
	if err = json.Unmarshal(data, &c); err != nil {
		return err
	}
	if c.OwnerID == v.ID && c.VM == v.Name {
		return os.Remove(path)
	}
	return nil
}
