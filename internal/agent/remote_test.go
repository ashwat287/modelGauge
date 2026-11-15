package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/nerdylucifer/modelgauge/internal/controller"
	"github.com/nerdylucifer/modelgauge/internal/platform"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRemoteWorkerHelper(t *testing.T) {
	if os.Getenv("MG_REMOTE_HELPER") != "1" {
		return
	}
	args := strings.Join(os.Args, " ")
	if strings.Contains(args, "systemctl --user stop") {
		_ = os.WriteFile(os.Getenv("MG_REMOTE_STOPPED"), []byte(args), 0600)
		os.Exit(0)
	}
	if !strings.Contains(args, "--property=KillMode=control-group") || !strings.Contains(args, "--property=RuntimeMaxSec=24h") {
		fmt.Fprintln(os.Stderr, "missing systemd supervision")
		os.Exit(2)
	}
	var request workerRequest
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		os.Exit(2)
	}
	if os.Getenv("MG_REMOTE_BLOCK") == "1" {
		_ = os.WriteFile(os.Getenv("MG_REMOTE_STARTED"), []byte("started"), 0600)
		select {}
	}
	response := workerResponse{Host: platform.Host{Hostname: "measured-vm", OS: "linux", Architecture: "arm64", CPUs: 2}, Models: []controller.NodeModel{{ID: "node-model", Path: "/node-only/model.gguf"}}}
	if request.Plan != nil {
		response.Result = protocol.Result{SchemaVersion: "benchmark-result/v1", RunID: request.RunID, PlanID: request.Plan.PlanID, Status: protocol.RunStatusCompleted}
	}
	if os.Getenv("MG_REMOTE_INTERRUPTED") == "1" {
		response.Interrupted = true
		response.Error = "executor authorization expired"
		response.Result.Status = protocol.RunStatusCancelled
	}
	_ = json.NewEncoder(os.Stdout).Encode(response)
	os.Exit(0)
}
func fakeLimaWorker(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(binary, "'", "'\\''") + "' -test.run=^TestRemoteWorkerHelper$ -- \"$@\"\n"
	if err = os.WriteFile(filepath.Join(dir, "limactl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MG_REMOTE_HELPER", "1")
}
func TestExternalCancellationStopsOnlyItsOwnedUnit(t *testing.T) {
	fakeLimaWorker(t)
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	stopped := filepath.Join(dir, "stopped")
	t.Setenv("MG_REMOTE_BLOCK", "1")
	t.Setenv("MG_REMOTE_STARTED", started)
	t.Setenv("MG_REMOTE_STOPPED", stopped)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := remoteWork(ctx, Config{TargetVM: "benchmark-node"}, workerRequest{Operation: "inspect"})
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancellation succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("executor did not stop")
	}
	data, err := os.ReadFile(stopped)
	if err != nil || !strings.Contains(string(data), "modelgauge-worker-") {
		t.Fatal("owned unit not stopped", string(data), err)
	}
}
