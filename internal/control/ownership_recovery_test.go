package control

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreationFailureRollsBackBeforeGuestPreparation(t *testing.T) {
	for _, partial := range []bool{false, true} {
		for _, interrupted := range []bool{false, true} {
			t.Run(fmt.Sprintf("partial=%t/interrupted=%t", partial, interrupted), func(t *testing.T) {
				m, f := fixture(t)
				other := f.add("unrelated")
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				cause := errors.New("image download failed")
				if interrupted {
					cause = context.Canceled
				}
				original := m.run
				m.run = func(ctx context.Context, args []string, input []byte) ([]byte, error) {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
					if args[0] != "create" {
						return original(ctx, args, input)
					}
					registry, err := m.load()
					if err != nil || len(registry.VMs) != 1 || registry.VMs[0].BootstrapState != "creating" {
						t.Fatalf("creation started without durable intent: %+v, %v", registry, err)
					}
					if partial {
						if _, err = original(ctx, args, input); err != nil {
							t.Fatal(err)
						}
					}
					if interrupted {
						cancel()
					}
					return nil, cause
				}
				_, _, err := m.provision(ctx, "target", "control", 2, 3, 20, 18080, io.Discard)
				if !errors.Is(err, cause) || !strings.Contains(err.Error(), "rollback completed:") {
					t.Fatal("original failure or rollback outcome lost:", err)
				}
				registry, err := m.load()
				if err != nil || len(registry.VMs) != 0 || f.guests["target"] != nil || f.guests["unrelated"] != other {
					t.Fatal("creation leaked or unrelated VM changed:", registry, err)
				}
				for _, args := range f.calls {
					if args[0] == "start" || args[0] == "shell" {
						t.Fatal("partial VM booted or inspected through guest:", args)
					}
				}
			})
		}
	}
}

func TestPartialCreationCleanupRetryDoesNotBootGuest(t *testing.T) {
	m, f := fixture(t)
	original := m.run
	m.run = func(ctx context.Context, args []string, input []byte) ([]byte, error) {
		out, err := original(ctx, args, input)
		if args[0] == "create" && err == nil {
			return nil, errors.New("image download failed")
		}
		return out, err
	}
	f.failDelete = true
	_, _, err := m.provision(context.Background(), "target", "control", 2, 3, 20, 18080, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "rollback failed") {
		t.Fatal(err)
	}
	registry, err := m.load()
	if err != nil || len(registry.VMs) != 1 || registry.VMs[0].BootstrapState != "creating" {
		t.Fatal("creation intent lost on failed deletion:", registry, err)
	}
	f.calls = nil
	report, err := m.cleanup(context.Background(), CleanupOptions{DryRun: true, DeleteVMs: true})
	if err != nil || len(report.Targets) != 1 || report.Targets[0].Status != "creation_incomplete" {
		t.Fatal(report, err)
	}
	if _, err = m.cleanup(context.Background(), CleanupOptions{}); err == nil || f.guests["target"] == nil {
		t.Fatal("partial VM removed without explicit VM deletion")
	}
	f.failDelete = false
	if _, err = m.cleanup(context.Background(), CleanupOptions{DeleteVMs: true}); err != nil {
		t.Fatal("could not retry partial creation cleanup:", err)
	}
	if f.guests["target"] != nil || exists(m.path()) {
		t.Fatal("partial VM or ownership record retained after successful retry")
	}
	for _, args := range f.calls {
		if args[0] == "start" || args[0] == "shell" {
			t.Fatal("partial VM cleanup tried to boot/use a guest:", args)
		}
	}
}

func TestPartialCreationRollbackRefusesUnprovenOwnership(t *testing.T) {
	for _, changed := range []string{"foreign", "wrong-owner", "protected", "changed-marker", "symlink"} {
		t.Run(changed, func(t *testing.T) {
			m, f := fixture(t)
			original := m.run
			m.run = func(ctx context.Context, args []string, input []byte) ([]byte, error) {
				if args[0] != "create" {
					return original(ctx, args, input)
				}
				if _, err := original(ctx, args, input); err != nil {
					return nil, err
				}
				g := f.guests["target"]
				switch changed {
				case "foreign":
					g.instance.Config.Env = nil
				case "wrong-owner":
					g.instance.Config.Env[limaOwnerEnv] = strings.Repeat("f", 32)
				case "protected":
					g.instance.Protected = true
				case "changed-marker":
					if err := os.WriteFile(markerPath(g.instance.Dir), []byte("foreign owner"), 0600); err != nil {
						t.Fatal(err)
					}
				case "symlink":
					link := filepath.Join(f.root, "replaced")
					if err := os.Symlink(g.instance.Dir, link); err != nil {
						t.Fatal(err)
					}
					g.instance.Dir = link
				}
				return nil, errors.New("create failed")
			}
			_, _, err := m.provision(context.Background(), "target", "control", 2, 3, 20, 18080, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "rollback failed") || f.guests["target"] == nil {
				t.Fatal("unproven VM removed:", err)
			}
			if _, err = m.cleanup(context.Background(), CleanupOptions{DeleteVMs: true}); err == nil {
				t.Fatal("cleanup accepted unproven creation ownership")
			}
			registry, err := m.load()
			if err != nil || len(registry.VMs) != 1 {
				t.Fatal("manual recovery evidence lost:", registry, err)
			}
			for _, args := range f.calls {
				if args[0] == "stop" || args[0] == "delete" || args[0] == "start" || args[0] == "shell" {
					t.Fatal("unproven VM touched:", args)
				}
			}
		})
	}
}

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
