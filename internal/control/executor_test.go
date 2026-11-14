package control

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nerdylucifer/modelgauge/internal/platform"
)

func TestCleanupPreservesNodeWhileExternalExecutorOwnsWork(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	m, f := fixture(t)
	prepareTestVM(t, m, f, "benchmark", "benchmark")
	unlock, err := platform.Lock("agent-benchmark")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.cleanup(context.Background(), CleanupOptions{DeleteVMs: true}); err == nil || !strings.Contains(err.Error(), "stop the executor") {
		t.Fatal("active executor did not block cleanup", err)
	}
	unlock()
	directory := filepath.Join(home, ".local/share/modelgauge/executors/benchmark")
	if err = os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, ".pending.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = m.cleanup(context.Background(), CleanupOptions{DeleteVMs: true}); err == nil || !strings.Contains(err.Error(), "pending work") {
		t.Fatal("pending upload did not block cleanup", err)
	}
	if f.guests["benchmark"] == nil {
		t.Fatal("node was deleted")
	}
}

func TestCleanupMissingVMsDoesNotRequireDeadController(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	m, f := fixture(t)
	m.directory = filepath.Join(home, ".config/modelgauge")
	prepareTestVM(t, m, f, "control", "control")
	prepareTestVM(t, m, f, "node", "benchmark")
	registry, err := m.load()
	if err != nil {
		t.Fatal(err)
	}
	var owner string
	for _, v := range registry.VMs {
		if v.Role == "control" {
			owner = v.ID
		}
	}
	calls := atomic.Int32{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "controller should not be contacted", 503)
	}))
	defer api.Close()
	if err = Save(Config{VM: "control", OwnerID: owner, ControllerURL: api.URL, AdminToken: "token"}); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err = os.WriteFile(filepath.Join(bin, "limactl"), []byte("#!/bin/sh\nif [ \"$1\" = list ]; then exit 0; fi\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	report, err := Cleanup(context.Background(), CleanupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("cleanup attempted to pause missing nodes")
	}
	for _, target := range report.Targets {
		if target.Status != "already_absent" {
			t.Fatal(target)
		}
	}
	if _, err = os.Stat(m.path()); !os.IsNotExist(err) {
		t.Fatal("stale registry retained", err)
	}
	// The test's fake disk directories must not be removed when Lima reports no instances.
	for _, g := range f.guests {
		if _, err = os.Stat(g.instance.Dir); err != nil {
			t.Fatal("cleanup removed unlisted data", err)
		}
	}
}
