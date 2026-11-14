package guest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestControlUpgradeRequiresPausedQueuesButCleanupAlwaysProtectsQueuedJobs(t *testing.T) {
	for _, state := range []string{"queued", "leased", "running"} {
		for _, allowQueued := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/allow=%t", state, allowQueued), func(t *testing.T) {
				r, q := fileFixture(t)
				if err := r.WriteManaged(context.Background(), q.Files[0], 0600, strings.NewReader("kubeconfig")); err != nil {
					t.Fatal(err)
				}
				r.Run = func(_ context.Context, args, _ []string) ([]byte, error) {
					if strings.Contains(strings.Join(args, " "), " get ") {
						return []byte("pod/postgres"), nil
					}
					if strings.Contains(args[len(args)-1], "'"+state+"'") {
						return []byte("1\n0\n"), nil
					}
					return []byte("0\n0\n"), nil
				}
				q.CheckIdle = true
				q.AllowQueued = allowQueued
				_, err := r.Execute(context.Background(), q)
				if (err == nil) != (state == "queued" && allowQueued) {
					t.Fatalf("upgrade: state %s allow %t err %v", state, allowQueued, err)
				}
				if err = r.checkJobs(context.Background(), true); err == nil {
					t.Fatal("cleanup ignored unfinished work")
				}
			})
		}
	}
}

func TestControlJobGuardRejectsUnverifiableCounts(t *testing.T) {
	for _, output := range []string{"", "bad\n0", "-1\n0", "0\n0\n0"} {
		r := Runtime{Home: t.TempDir()}
		if err := os.Mkdir(filepath.Join(r.Home, ".kube"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(r.Home, ".kube/modelgauge-config"), []byte("config"), 0600); err != nil {
			t.Fatal(err)
		}
		r.Run = func(_ context.Context, args, _ []string) ([]byte, error) {
			if strings.Contains(strings.Join(args, " "), " get ") {
				return []byte("pod/postgres"), nil
			}
			return []byte(output), nil
		}
		if err := r.checkJobs(context.Background(), false); err == nil {
			t.Fatal("invalid counts accepted", output)
		}
	}
}
