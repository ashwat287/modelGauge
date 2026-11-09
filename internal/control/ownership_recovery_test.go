package control

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestBootstrapFailureRollbackIsInvocationScoped(t *testing.T) {
	for _, reused := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "reused"}[reused], func(t *testing.T) {
			m, f := fixture(t)
			if reused {
				f.add("target")
			}
			other := f.add("unrelated")
			ctx, finish, err := m.provision(context.Background(), "target", "control", 2, 3, 20, 18080, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			failure := errors.New("deployment failed")
			completeProvision(ctx, finish, &failure)
			if !strings.Contains(failure.Error(), "deployment failed") {
				t.Fatal("original failure lost")
			}
			if (f.guests["target"] != nil) != reused {
				t.Fatal("incorrect VM rollback policy")
			}
			if f.guests["unrelated"] != other {
				t.Fatal("unrelated VM changed")
			}
			registry, err := m.load()
			if err != nil {
				t.Fatal(err)
			}
			if (len(registry.VMs) != 0) != reused {
				t.Fatal("incorrect ownership retention")
			}
		})
	}
}
func TestCancelledBootstrapUsesFreshRollbackContext(t *testing.T) {
	m, f := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	managed, finish, err := m.provision(ctx, "target", "benchmark", 2, 3, 20, 0, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	original := m.run
	m.run = func(ctx context.Context, args []string, input []byte) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return original(ctx, args, input)
	}
	cancel()
	failure := ctx.Err()
	completeProvision(managed, finish, &failure)
	if f.guests["target"] != nil {
		t.Fatal("rollback inherited cancelled context")
	}
	if !errors.Is(failure, context.Canceled) {
		t.Fatal("interruption cause lost")
	}
}
func TestFailedRollbackRetainsOwnershipForRetry(t *testing.T) {
	m, f := fixture(t)
	ctx, finish, err := m.provision(context.Background(), "target", "benchmark", 2, 3, 20, 0, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	f.failDelete = true
	failure := errors.New("bootstrap failure")
	completeProvision(ctx, finish, &failure)
	if !strings.Contains(failure.Error(), "rollback failed") || !strings.Contains(failure.Error(), "bootstrap failure") {
		t.Fatal(failure)
	}
	registry, _ := m.load()
	if len(registry.VMs) != 1 || f.guests["target"] == nil {
		t.Fatal("retry ownership lost")
	}
}
func TestPrepareFailureRollsBackNewVM(t *testing.T) {
	m, f := fixture(t)
	m.binary = func(context.Context, string) ([]byte, error) { return nil, errors.New("download failed") }
	_, _, err := m.provision(context.Background(), "target", "control", 2, 3, 20, 18080, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "download failed") {
		t.Fatal(err)
	}
	if f.guests["target"] != nil {
		t.Fatal("failed preparation leaked newly created VM")
	}
}
func TestRollbackRefusesChangedIdentity(t *testing.T) {
	m, f := fixture(t)
	ctx, finish, err := m.provision(context.Background(), "target", "benchmark", 2, 3, 20, 0, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	f.guests["target"].machine = strings.Repeat("f", 32)
	failure := errors.New("installation failed")
	completeProvision(ctx, finish, &failure)
	if f.guests["target"] == nil || !strings.Contains(failure.Error(), "identity changed") {
		t.Fatal("changed VM deleted")
	}
}
func TestRollbackPreservesPreviousConnection(t *testing.T) {
	m, f := fixture(t)
	if err := os.MkdirAll(m.directory, 0700); err != nil {
		t.Fatal(err)
	}
	previous := []byte(`{"vm":"existing","owner_id":"old"}`)
	path := m.directory + "/control.json"
	_ = os.WriteFile(path, previous, 0600)
	ctx, finish, err := m.provision(context.Background(), "target", "control", 2, 3, 20, 18080, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	registry, _ := m.load()
	_ = os.WriteFile(path, []byte(`{"vm":"target","owner_id":"`+registry.VMs[0].ID+`"}`), 0600)
	failure := errors.New("forward failed")
	completeProvision(ctx, finish, &failure)
	got, _ := os.ReadFile(path)
	if string(got) != string(previous) || f.guests["target"] != nil {
		t.Fatal("previous connection not restored")
	}
}

func TestCleanupResumesAfterGuestDeletionBeforeHostCheckpoint(t *testing.T) {
	m, f := fixture(t)
	prepareTestVM(t, m, f, "target", "benchmark")
	registry, err := m.load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.guest(context.Background(), registry.VMs[0], "clean", registry.VMs[0].Files); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash: guest roots are gone, while host Cleaned remains false.
	if registry.VMs[0].Cleaned {
		t.Fatal("fixture prematurely checkpointed")
	}
	if _, err = m.cleanup(context.Background(), CleanupOptions{DeleteVMs: true}); err != nil {
		t.Fatal("cleanup could not reconcile guest completion:", err)
	}
	if f.guests["target"] != nil {
		t.Fatal("VM not removed after resumed cleanup")
	}
}

func TestFailedUpgradeKeepsPreviouslyVerifiedHelper(t *testing.T) {
	m, f := fixture(t)
	g := prepareTestVM(t, m, f, "target", "benchmark")
	registry, _ := m.load()
	previous := registry.VMs[0]
	path := g.home + "/.local/bin/modelgauge"
	if err := os.WriteFile(path, []byte("manual replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	m.binary = func(context.Context, string) ([]byte, error) { return []byte("new helper version"), nil }
	if _, _, err := m.provision(context.Background(), "target", "benchmark", 2, 3, 20, 0, io.Discard); err == nil {
		t.Fatal("changed file accepted")
	}
	registry, _ = m.load()
	if registry.VMs[0].HelperSHA256 != previous.HelperSHA256 {
		t.Fatal("failed upgrade replaced working helper cache")
	}
	if _, err := m.readHelper(registry.VMs[0]); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "manual replacement" || f.guests["target"] == nil {
		t.Fatal("existing resource altered on refusal")
	}
}
