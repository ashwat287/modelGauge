package control

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/nerdylucifer/modelgauge/internal/guest"
)

type fakeGuest struct {
	instance      limaInstance
	home, machine string
}
type fakeLima struct {
	t          *testing.T
	root       string
	guests     map[string]*fakeGuest
	calls      [][]string
	failDelete bool
}

func fixture(t *testing.T) (*ownershipManager, *fakeLima) {
	t.Helper()
	f := &fakeLima{t: t, root: t.TempDir(), guests: map[string]*fakeGuest{}}
	return &ownershipManager{directory: filepath.Join(t.TempDir(), "config"), run: f.run, binary: func(context.Context, string) ([]byte, error) { return []byte("Go helper fixture"), nil }}, f
}
func (f *fakeLima) add(name string) *fakeGuest {
	f.t.Helper()
	dir := filepath.Join(f.root, "instances", name)
	home := filepath.Join(f.root, "guests", name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		f.t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(name))
	g := &fakeGuest{instance: limaInstance{Name: name, Dir: dir, Status: "Running"}, home: home, machine: hex.EncodeToString(hash[:16])}
	f.guests[name] = g
	return g
}
func (f *fakeLima) run(ctx context.Context, args []string, input []byte) ([]byte, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	switch args[0] {
	case "list":
		var out bytes.Buffer
		for _, g := range f.guests {
			_ = json.NewEncoder(&out).Encode(g.instance)
		}
		return out.Bytes(), nil
	case "create":
		var name string
		for i, arg := range args {
			if arg == "--name" {
				name = args[i+1]
			}
		}
		if f.guests[name] != nil {
			return nil, fmt.Errorf("already exists")
		}
		f.add(name)
		return nil, nil
	case "start":
		f.guests[args[len(args)-1]].instance.Status = "Running"
		return nil, nil
	case "stop":
		f.guests[args[1]].instance.Status = "Stopped"
		return nil, nil
	case "delete":
		if f.failDelete {
			return nil, fmt.Errorf("injected delete failure")
		}
		name := args[len(args)-1]
		delete(f.guests, name)
		return nil, nil
	case "shell":
		g := f.guests[args[3]]
		if g == nil {
			return nil, fmt.Errorf("missing VM")
		}
		if args[5] == "cat" {
			return []byte(g.machine + "\n"), nil
		}
		if args[5] == "uname" {
			return []byte("aarch64\n"), nil
		}
		payload := input
		if strings.Contains(args[len(args)-1], "tar -xf") {
			var err error
			payload, err = decodeHelperEnvelope(input)
			if err != nil {
				return nil, err
			}
		}
		var request guest.Request
		if err := json.Unmarshal(payload, &request); err != nil {
			return nil, err
		}
		runtime := guest.Runtime{
			Home: g.home, MachineID: g.machine,
			Executable:  func() (string, error) { return filepath.Join(g.instance.Dir, ".modelgauge-helper-"+request.ID), nil },
			HasCommand:  func(string) bool { return false },
			MountPoints: func() ([]string, error) { return nil, nil },
			Run: func(_ context.Context, command, _ []string) ([]byte, error) {
				switch command[0] {
				case "pgrep":
					return nil, inactiveProcess{}
				case "systemctl":
					return nil, nil
				case "kubectl":
					if exists(filepath.Join(g.home, "simulate-active-jobs")) {
						if strings.Contains(strings.Join(command, " "), " get ") {
							return []byte("pod/benchmark-postgres-0"), nil
						}
						return []byte("1\n0\n"), nil
					}
					return nil, nil
				}
				return nil, fmt.Errorf("unexpected guest command %v", command)
			},
		}
		state, err := runtime.Execute(ctx, request)
		if err != nil {
			return nil, err
		}
		return json.Marshal(state)
	}
	return nil, fmt.Errorf("unexpected Lima command %v", args)
}
func prepareTestVM(t *testing.T, m *ownershipManager, f *fakeLima, name, role string) *fakeGuest {
	t.Helper()
	_, finish, err := m.provision(context.Background(), name, role, 2, 3, 20, 18080, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	g := f.guests[name]
	registry, err := m.load()
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, v := range registry.VMs {
		if v.Name == name {
			files = v.Files
		}
	}
	for _, relative := range files {
		path := filepath.Join(g.home, relative)
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, []byte("owned content"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := finish(); err != nil {
		t.Fatal(err)
	}
	return g
}
func exists(path string) bool { _, err := os.Lstat(path); return err == nil }
func TestCleanupPreservesReusedVMAndForeignFiles(t *testing.T) {
	m, f := fixture(t)
	g := f.add("reused")
	outside := filepath.Join(g.home, "unrelated.txt")
	_ = os.WriteFile(outside, []byte("keep"), 0600)
	// A pre-existing kubeconfig is never reserved or deleted.
	_ = os.MkdirAll(filepath.Join(g.home, ".kube"), 0700)
	_ = os.WriteFile(filepath.Join(g.home, ".kube/config"), []byte("foreign kubeconfig"), 0600)
	prepareTestVM(t, m, f, "reused", "benchmark")
	f.add("untracked")
	report, err := m.cleanup(context.Background(), CleanupOptions{DeleteVMs: true})
	if err != nil {
		t.Fatal(err)
	}
	if f.guests["reused"] == nil || f.guests["untracked"] == nil || !exists(outside) || !exists(filepath.Join(g.home, ".kube/config")) {
		t.Fatal("foreign/reused resources removed")
	}
	if exists(filepath.Join(g.home, ".local/share/modelgauge")) {
		t.Fatal("owned directory retained")
	}
	if report.Targets[0].Delete || len(report.Untracked) != 1 {
		t.Fatal("incorrect ownership report")
	}
	if exists(m.path()) {
		t.Fatal("completed reused record was retained")
	}
}
func TestDryRunAndCreatedVMDeletion(t *testing.T) {
	m, f := fixture(t)
	g := prepareTestVM(t, m, f, "owned", "benchmark")
	before, _ := os.ReadFile(m.path())
	f.calls = nil
	report, err := m.cleanup(context.Background(), CleanupOptions{DryRun: true, DeleteVMs: true})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(m.path())
	if !bytes.Equal(before, after) || !exists(filepath.Join(g.home, ".local/share/modelgauge")) || !report.Targets[0].Delete {
		t.Fatal("dry run changed state")
	}
	for _, args := range f.calls {
		if args[0] != "list" && args[0] != "shell" {
			t.Fatalf("dry run mutated VMs: %v", args)
		}
	}
	if _, err = m.cleanup(context.Background(), CleanupOptions{}); err != nil {
		t.Fatal(err)
	}
	if f.guests["owned"] == nil {
		t.Fatal("default cleanup deleted VM")
	}
	registry, _ := m.load()
	if len(registry.VMs) != 1 || !registry.VMs[0].Cleaned {
		t.Fatal("VM deletion provenance lost")
	}
	if _, err = m.cleanup(context.Background(), CleanupOptions{DeleteVMs: true}); err != nil {
		t.Fatal(err)
	}
	if f.guests["owned"] != nil || exists(m.path()) {
		t.Fatal("owned VM/record retained")
	}
	if _, err = m.cleanup(context.Background(), CleanupOptions{DeleteVMs: true}); err != nil {
		t.Fatal("repeated cleanup not idempotent:", err)
	}
}
func TestCleanupRefusesUnsafeTargetsBeforeDeletion(t *testing.T) {
	for _, scenario := range []string{"marker", "machine", "symlink", "changed-file", "pending", "protected", "outside-manifest", "execution-lock", "agent-lock"} {
		t.Run(scenario, func(t *testing.T) {
			m, f := fixture(t)
			g := prepareTestVM(t, m, f, "owned", "benchmark")
			switch scenario {
			case "execution-lock", "agent-lock":
				name := "execution.lock"
				if scenario == "agent-lock" {
					name = "agent.lock"
				}
				path := filepath.Join(g.home, ".local/share/modelgauge/locks", name)
				_ = os.MkdirAll(filepath.Dir(path), 0700)
				file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
				if err != nil {
					t.Fatal(err)
				}
				if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN); _ = file.Close() })
			case "marker":
				_ = os.WriteFile(markerPath(g.instance.Dir), []byte(strings.Repeat("f", 32)), 0600)
			case "machine":
				g.machine = strings.Repeat("f", 32)
			case "symlink":
				_ = os.Rename(filepath.Join(g.home, ".local/share/modelgauge"), filepath.Join(g.home, "original"))
				_ = os.Symlink(filepath.Join(g.home, "original"), filepath.Join(g.home, ".local/share/modelgauge"))
			case "changed-file":
				_ = os.WriteFile(filepath.Join(g.home, ".local/bin/modelgauge"), []byte("user replacement"), 0600)
			case "pending":
				_ = os.MkdirAll(filepath.Join(g.home, ".local/share/modelgauge/results"), 0700)
				_ = os.WriteFile(filepath.Join(g.home, ".local/share/modelgauge/results/.pending.json"), []byte("{}"), 0600)
			case "protected":
				g.instance.Protected = true
			case "outside-manifest":
				p := filepath.Join(g.home, ".local/share/modelgauge/.ownership.json")
				var data map[string]any
				raw, _ := os.ReadFile(p)
				_ = json.Unmarshal(raw, &data)
				data["directories"] = []string{".."}
				raw, _ = json.Marshal(data)
				_ = os.WriteFile(p, raw, 0600)
			}
			f.calls = nil
			if _, err := m.cleanup(context.Background(), CleanupOptions{DeleteVMs: true}); err == nil {
				t.Fatal("unsafe target accepted")
			}
			if f.guests["owned"] == nil || !exists(m.path()) {
				t.Fatal("unsafe VM or evidence was deleted")
			}
			for _, args := range f.calls {
				if args[0] == "delete" || args[0] == "stop" {
					t.Fatal("destructive action before all checks passed")
				}
			}
		})
	}
}
func TestStoppedDryRunAndDeleteRetry(t *testing.T) {
	m, f := fixture(t)
	g := prepareTestVM(t, m, f, "owned", "benchmark")
	g.instance.Status = "Stopped"
	f.calls = nil
	report, err := m.cleanup(context.Background(), CleanupOptions{DryRun: true, DeleteVMs: true})
	if err != nil {
		t.Fatal(err)
	}
	if report.Targets[0].Status != "guest_verification_required" || len(f.calls) != 1 {
		t.Fatal("stopped dry run started VM")
	}
	f.failDelete = true
	if _, err = m.cleanup(context.Background(), CleanupOptions{DeleteVMs: true}); err == nil {
		t.Fatal("delete failure not surfaced")
	}
	registry, _ := m.load()
	if len(registry.VMs) != 1 || !registry.VMs[0].Cleaned {
		t.Fatal("retry state was not checkpointed")
	}
	f.failDelete = false
	if _, err = m.cleanup(context.Background(), CleanupOptions{DeleteVMs: true}); err != nil {
		t.Fatal(err)
	}
}
func TestUnknownDirectoriesAreNotAdopted(t *testing.T) {
	m, f := fixture(t)
	g := f.add("existing")
	p := filepath.Join(g.home, ".config/modelgauge")
	_ = os.MkdirAll(p, 0700)
	_ = os.WriteFile(filepath.Join(p, "mine"), []byte("keep"), 0600)
	if _, _, err := m.provision(context.Background(), "existing", "benchmark", 2, 3, 20, 0, io.Discard); err == nil {
		t.Fatal("unknown directory adopted")
	}
	if !exists(filepath.Join(p, "mine")) || exists(filepath.Join(p, ".modelgauge-owner")) {
		t.Fatal("foreign directory changed")
	}
}
func TestHostConnectionOwnership(t *testing.T) {
	m, f := fixture(t)
	prepareTestVM(t, m, f, "owned", "benchmark")
	registry, _ := m.load()
	path := filepath.Join(m.directory, "control.json")
	for _, id := range []string{"other", registry.VMs[0].ID} {
		raw, _ := json.Marshal(Config{VM: "owned", OwnerID: id})
		_ = os.WriteFile(path, raw, 0600)
		if err := m.removeConnection(registry.VMs[0]); err != nil {
			t.Fatal(err)
		}
		if exists(path) != (id == "other") {
			t.Fatal("wrong connection removed/preserved")
		}
	}
}

func TestControlForwardOwnershipAndQueuedJobs(t *testing.T) {
	m, f := fixture(t)
	g := f.add("control")
	foreignConfig := filepath.Join(g.home, ".kube/config")
	_ = os.MkdirAll(filepath.Dir(foreignConfig), 0700)
	_ = os.WriteFile(foreignConfig, []byte("user kubeconfig"), 0600)
	ctx, finish, err := m.provision(context.Background(), "control", "control", 2, 3, 20, 18080, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	registry, _ := m.load()
	for _, path := range registry.VMs[0].Files {
		p := filepath.Join(g.home, path)
		_ = os.MkdirAll(filepath.Dir(p), 0700)
		_ = os.WriteFile(p, []byte("owned"), 0600)
	}
	forwardFinish, err := reserveForward(ctx, "control", 3000)
	if err != nil {
		_ = finish()
		t.Fatal(err)
	}
	p := filepath.Join(g.home, ".config/systemd/user/modelgauge-forward-3000.service")
	_ = os.WriteFile(p, []byte("owned forward"), 0600)
	if err = forwardFinish(); err != nil {
		t.Fatal(err)
	}
	if err = finish(); err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(g.home, "simulate-active-jobs")
	_ = os.WriteFile(active, []byte("yes"), 0600)
	if _, err = m.cleanup(context.Background(), CleanupOptions{DeleteVMs: true}); err == nil {
		t.Fatal("queued experiment did not block cleanup")
	}
	if !exists(filepath.Join(g.home, ".local/share/modelgauge")) {
		t.Fatal("deleted resources with queued work")
	}
	_ = os.Remove(active)
	report, err := m.cleanup(context.Background(), CleanupOptions{DeleteVMs: true})
	if err != nil {
		t.Fatal(err)
	}
	if f.guests["control"] == nil || !exists(foreignConfig) || report.Targets[0].Note == "" {
		t.Fatal("reused control VM was not preserved with a retention notice")
	}
	if exists(p) {
		t.Fatal("monitoring forward was not cleaned")
	}
}

type inactiveProcess struct{}

func (inactiveProcess) Error() string { return "no process" }
func (inactiveProcess) ExitCode() int { return 1 }

// decodeHelperEnvelope is also used by the fake provider tests to execute the
// same Go request handler without running a Linux VM or changing host services.
func decodeHelperEnvelope(input []byte) ([]byte, error) {
	reader := tar.NewReader(bytes.NewReader(input))
	for {
		entry, err := reader.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("helper request missing")
		}
		if err != nil {
			return nil, err
		}
		if entry.Name == "request.json" {
			return io.ReadAll(reader)
		}
	}
}
