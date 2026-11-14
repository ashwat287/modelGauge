package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nerdylucifer/modelgauge/internal/client"
	"github.com/nerdylucifer/modelgauge/internal/platform"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

func TestManagedModelRunsOnAgentAndUploadsServingReport(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("managed model execution requires Linux")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MODELGAUGE_INFERENCE_TOKEN", "inference-secret")
	inference := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("unrelated credentials leaked to the model process")
		}
		if r.Header.Get("X-ModelGauge-Token") != "" {
			t.Error("agent token leaked to inference endpoint")
		}
		if r.URL.Path == "/v1/models" {
			w.WriteHeader(200)
			return
		}
		if r.URL.Path == "/metrics" {
			fmt.Fprint(w, "llamacpp:requests_processing 1\n")
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Error("unexpected server operation", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: {\"choices\":[{\"finish_reason\":\"stop\",\"delta\":{}}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n"))
	}))
	defer inference.Close()
	var uploaded atomic.Bool
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/nodes/test/plans/plan/renew" {
			_ = json.NewEncoder(w).Encode(map[string]any{"expires_at": time.Now().Add(time.Minute), "cancelled": false})
			return
		}
		if r.URL.Path == "/v1/nodes/test/runs/run-plan/complete" {
			var request completeRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if request.Result.Status != protocol.RunStatusCompleted || request.Result.Benchmark == nil || request.Result.Benchmark.Kind != "serving" {
				t.Error("serving result was lost", request.Result)
			}
			uploaded.Store(true)
		}
		w.WriteHeader(204)
	}))
	defer controller.Close()
	t.Setenv("MG_TEST_PROXY", inference.URL)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "llama-server")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec '"+strings.ReplaceAll(binary, "'", "'\"'\"'")+"' -test.run=TestAgentModelProcess -- \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	model := filepath.Join(t.TempDir(), "model.gguf")
	if err := os.WriteFile(model, []byte("fixture model"), 0600); err != nil {
		t.Fatal(err)
	}
	plan := protocol.Plan{SchemaVersion: protocol.BenchmarkPlanV1, NodeID: "test", ExecutionMode: protocol.ExecutionModeLima, Runtime: protocol.Runtime{Kind: "llama-server", Binary: script}, Model: protocol.Model{ID: "test-model", Path: model, SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("fixture model")))}, Benchmark: &protocol.BenchmarkSpec{Kind: "serving", Config: json.RawMessage(`{"server":{"threads":1,"context_tokens":1024,"parallel":1},"api":"openai","served_model":"test-model","request_timeout_seconds":5,"readiness_timeout_seconds":5,"sample_interval_seconds":0.1,"cases":[{"id":"load","concurrency":1,"requests":1,"max_output_tokens":8}]}`)}}
	c := Config{NodeID: "test", ExecutionMode: protocol.ExecutionModeLima, ResultsDir: t.TempDir()}
	p := pendingJobForPlan(lease{PlanID: "plan", LeaseToken: "token", ExpiresAt: time.Now().Add(time.Minute), Specification: plan})
	if err := savePending(c, &p); err != nil {
		t.Fatal(err)
	}
	if err := processPending(context.Background(), &client.Client{BaseURL: controller.URL, AgentToken: "agent-secret"}, c, &p); err != nil {
		t.Fatal(err)
	}
	if !uploaded.Load() {
		t.Fatal("serving report not uploaded")
	}
	if _, err := os.Stat(pendingPath(c)); !os.IsNotExist(err) {
		t.Fatal("completed job remains pending", err)
	}
}

func TestRecoverSavedResultWithoutExecutingModel(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/nodes/test/runs/run-test-plan/complete" {
			t.Errorf("recovery tried executing or renewing completed work: %s", r.URL.Path)
			w.WriteHeader(400)
			return
		}
		var payload completeRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.Result.RunID != "run-test-plan" {
			t.Error("wrong saved result")
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	c := Config{NodeID: "test", ResultsDir: t.TempDir()}
	p := pendingJobForPlan(lease{PlanID: "test-plan", LeaseToken: "token", Specification: protocol.Plan{NodeID: "test"}})
	if err := savePending(c, &p); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(c.ResultsDir, "run-test-plan")
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	if err := platform.WriteJSON(filepath.Join(output, "result.json"), protocol.Result{RunID: "run-test-plan", Status: protocol.RunStatusCompleted}, 0600); err != nil {
		t.Fatal(err)
	}
	recovered, err := loadPending(c)
	if err != nil {
		t.Fatal(err)
	}
	if err = processPending(context.Background(), &client.Client{BaseURL: server.URL}, c, recovered); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("retry count %d", calls.Load())
	}
	if _, err = os.Stat(pendingPath(c)); !os.IsNotExist(err) {
		t.Fatal("journal not acknowledged", err)
	}
}

func TestPermanentlyRejectedResultIsArchivedAndReleasesQueue(t *testing.T) {
	c := Config{NodeID: "test", ResultsDir: t.TempDir()}
	for _, status := range []int{400, 404, 409, 413, 422} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "permanent rejection", status) }))
		result := protocol.Result{RunID: "run-test", Status: protocol.RunStatusCompleted}
		p := pendingJob{Kind: "benchmark", Plan: &lease{PlanID: "test", LeaseToken: "lease", Specification: protocol.Plan{NodeID: "test"}}, Result: &result}
		if err := savePending(c, &p); err != nil {
			t.Fatal(err)
		}
		api := &client.Client{BaseURL: s.URL}
		if err := processPending(context.Background(), api, c, &p); err == nil {
			t.Fatal("rejection hidden")
		}
		s.Close()
		if next, err := loadPending(c); err != nil || next != nil {
			t.Fatal("rejected result still blocks next job", next, err)
		}
	}
	archived, err := filepath.Glob(filepath.Join(c.ResultsDir, ".rejected-*.json"))
	if err != nil || len(archived) != 5 {
		t.Fatal("evidence not preserved", archived, err)
	}
}

func TestAgentModelProcess(t *testing.T) {
	upstream := os.Getenv("MG_TEST_PROXY")
	if upstream == "" {
		return
	}
	target, err := url.Parse(upstream)
	if err != nil {
		os.Exit(2)
	}
	port := ""
	for i, arg := range os.Args {
		if arg == "--port" && i+1 < len(os.Args) {
			port = os.Args[i+1]
		}
	}
	if _, err := strconv.Atoi(port); err != nil {
		os.Exit(2)
	}
	_ = http.ListenAndServe("127.0.0.1:"+port, httputil.NewSingleHostReverseProxy(target))
	os.Exit(1)
}
