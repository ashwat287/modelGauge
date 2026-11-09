package control

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Explicit opt-in: run only inside a disposable Linux VM with a user manager.
func TestGuestSystemdScopeStopsDescendants(t *testing.T) {
	if os.Getenv("MODELGAUGE_TEST_SYSTEMD_SCOPE") != "1" {
		t.Skip("requires an explicitly opted-in disposable Linux systemd user session")
	}
	id, err := newScopeID()
	if err != nil {
		t.Fatal(err)
	}
	scope := "modelgauge-provision-" + id + ".scope"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "child.pid")
	cmd := exec.CommandContext(ctx, "bash", "-lc", scopeCommand(scope, `sleep 60 & printf '%s' "$!" > "$MODELGAUGE_TEST_PID_FILE"; wait`))
	cmd.Env = append(os.Environ(), "MODELGAUGE_TEST_PID_FILE="+path)
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	manager := &ownershipManager{run: func(ctx context.Context, args []string, input []byte) ([]byte, error) {
		return exec.CommandContext(ctx, args[5], args[6:]...).CombinedOutput()
	}}
	defer func() { _ = manager.stopScope(context.Background(), "unused", scope); _ = cmd.Wait() }()
	deadline := time.Now().Add(5 * time.Second)
	pid := 0
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(path)
		pid, _ = strconv.Atoi(string(data))
		if pid > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("scope did not start descendant")
	}
	if err = manager.stopScope(ctx, "unused", scope); err != nil {
		t.Fatal(err)
	}
	data, _ := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	state := strings.TrimSpace(string(data))
	if state != "" && !strings.HasPrefix(state, "Z") {
		t.Fatal("scope left a live descendant:", state)
	}
}
