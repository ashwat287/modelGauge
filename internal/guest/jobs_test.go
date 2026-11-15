package guest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanupChecksControllerActivity(t *testing.T) {
	for _, active := range []bool{false, true} {
		r, _ := fileFixture(t)
		if err := os.MkdirAll(filepath.Join(r.Home, ".kube"), 0700); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(r.Home, ".kube/modelgauge-config"), []byte("config"), 0600)
		r.Run = func(_ context.Context, args, env []string) ([]byte, error) {
			if strings.Contains(strings.Join(args, " "), " get ") {
				return []byte("deployment/modelgauge-controller"), nil
			}
			if active {
				return nil, errors.New("operation active")
			}
			return nil, nil
		}
		if err := r.checkJobs(context.Background()); (err != nil) != active {
			t.Fatal(active, err)
		}
	}
}
