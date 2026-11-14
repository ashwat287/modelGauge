package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nerdylucifer/modelgauge/internal/benchmark"
	"github.com/nerdylucifer/modelgauge/internal/controller"
	"github.com/nerdylucifer/modelgauge/internal/platform"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
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
func TestExternalExecutorUsesTargetHardwareAndInventory(t *testing.T) {
	fakeLimaWorker(t)
	t.Setenv("HOME", t.TempDir())
	registered, synced := false, false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/nodes/register":
			var v registerRequest
			_ = json.NewDecoder(r.Body).Decode(&v)
			if v.Hardware["hostname"] != "measured-vm" {
				t.Error("executor host registered as target", v)
			}
			registered = true
			w.WriteHeader(200)
		case strings.HasSuffix(r.URL.Path, "/models"):
			var v []controller.NodeModel
			_ = json.NewDecoder(r.Body).Decode(&v)
			if len(v) != 1 || v[0].Path != "/node-only/model.gguf" {
				t.Error(v)
			}
			synced = true
			w.WriteHeader(200)
		default:
			w.WriteHeader(204)
		}
	}))
	defer server.Close()
	c := Config{TargetVM: "benchmark-node", NodeID: "benchmark-node", ControllerURL: server.URL, Token: "private-controller-token", ResultsDir: t.TempDir()}
	if worked, err := RunOnce(context.Background(), c); err != nil || worked || !registered || !synced {
		t.Fatal(worked, err, registered, synced)
	}
	p := protocol.ExamplePlan()
	p.PlanID = "job"
	r, _, err := benchmarkTarget(context.Background(), c, p, benchmark.Options{RunID: "run-job", OutputDir: filepath.Join(c.ResultsDir, "run-job")})
	if err != nil || r.RunID != "run-job" {
		t.Fatal(r, err)
	}
	if _, err = os.Stat(filepath.Join(c.ResultsDir, "run-job/result.json")); err != nil {
		t.Fatal(err)
	}
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

func TestCompletedUploadDoesNotRequireNodeConnection(t *testing.T) {
	// No Lima binary or Linux host inspection is needed to upload saved results.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	uploaded := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/complete") {
			uploaded = true
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	c := Config{TargetVM: "node", NodeID: "node", ControllerURL: server.URL, Token: "token", ResultsDir: t.TempDir()}
	p := protocol.ExamplePlan()
	p.NodeID = "node"
	p.PlanID = "job"
	pending := pendingJobForPlan(lease{PlanID: "job", Specification: p})
	pending.Result = &protocol.Result{SchemaVersion: "benchmark-result/v1", RunID: "run-job", PlanID: "job", Status: protocol.RunStatusCompleted}
	if err := savePending(c, &pending); err != nil {
		t.Fatal(err)
	}
	if worked, err := RunOnce(context.Background(), c); err != nil || !worked || !uploaded {
		t.Fatal(worked, uploaded, err)
	}
	if _, err := os.Stat(pendingPath(c)); !os.IsNotExist(err) {
		t.Fatal("acknowledged journal retained", err)
	}
}

func TestConnectionInterruptionRetainsPendingPlan(t *testing.T) {
	fakeLimaWorker(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MG_REMOTE_INTERRUPTED", "1")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/renew") {
			_ = json.NewEncoder(w).Encode(controller.Renewal{ExpiresAt: time.Now().Add(time.Minute)})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/complete") {
			t.Error("interrupted attempt uploaded as terminal")
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	c := Config{TargetVM: "node", NodeID: "node", ControllerURL: server.URL, Token: "token", ResultsDir: t.TempDir(), ExecutionMode: protocol.ExecutionModeLima}
	p := protocol.ExamplePlan()
	p.NodeID = "node"
	p.ExecutionMode = protocol.ExecutionModeLima
	pending := pendingJobForPlan(lease{PlanID: "job", Specification: p, LeaseToken: "lease"})
	if err := savePending(c, &pending); err != nil {
		t.Fatal(err)
	}
	if _, err := RunOnce(context.Background(), c); !errors.Is(err, errWorkerTransport) {
		t.Fatal("interruption not recoverable", err)
	}
	recovered, err := loadPending(c)
	if err != nil || recovered == nil || recovered.Result != nil {
		t.Fatal("pending work was lost", recovered, err)
	}
	if _, err := os.Stat(filepath.Join(c.ResultsDir, "run-job/result.json")); !os.IsNotExist(err) {
		t.Fatal("interrupted result cached as final", err)
	}
}
