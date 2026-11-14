package benchmark

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

func managedModelFixture(t *testing.T) (protocol.Plan, *servingPlan) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	script := filepath.Join(root, "llama-server")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec '"+strings.ReplaceAll(binary, "'", "'\"'\"'")+"' -test.run=TestModelServerProcess -- \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	model := filepath.Join(root, "fixture.gguf")
	if err := os.WriteFile(model, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	plan := protocol.Plan{SchemaVersion: protocol.BenchmarkPlanV1, NodeID: "node", ExecutionMode: protocol.ExecutionModeLima, Runtime: protocol.Runtime{Kind: "llama-server", Binary: script}, Model: protocol.Model{ID: "fixture", Path: model, SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("fixture")))}}
	s := &servingPlan{Server: &servingServer{Threads: 2, ContextTokens: 1024, Parallel: 2}, API: "llama.cpp", ServedModel: "fixture", RequestTimeout: 5, ReadinessTimeout: 5, SampleInterval: .1, Cases: []servingCase{{ID: "load", Scenario: "load", Concurrency: 1, Requests: 1, WarmupRequests: 1, OutputTokens: 8}}}
	if err := setConfig(&plan, "serving", s); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MG_TEST_MODEL_PROCESS", "serve")
	t.Setenv("MG_TEST_MODEL_ARGS", filepath.Join(root, "args.json"))
	return plan, s
}

// A real child process exercises launch/stop without requiring a model download.
func TestModelServerProcess(t *testing.T) {
	mode := os.Getenv("MG_TEST_MODEL_PROCESS")
	if mode == "" {
		return
	}
	if mode == "fail" {
		fmt.Fprintln(os.Stderr, "fixture load failed")
		os.Exit(42)
	}
	args := map[string]string{}
	if os.Getenv("MODELGAUGE_AGENT_TOKEN") != "" || os.Getenv("LLAMA_ARG_MODEL_URL") != "" {
		fmt.Fprintln(os.Stderr, "unrelated node environment leaked")
		os.Exit(3)
	}
	for i, arg := range os.Args {
		if strings.HasPrefix(arg, "--") && i+1 < len(os.Args) {
			args[arg] = os.Args[i+1]
		}
	}
	data, _ := json.Marshal(args)
	_ = os.WriteFile(os.Getenv("MG_TEST_MODEL_ARGS"), data, 0600)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			fmt.Fprint(w, `{"status":"ok"}`)
		case "/v1/models":
			fmt.Fprintf(w, `{"data":[{"id":%q}]}`, args["--alias"])
		case "/metrics":
			fmt.Fprint(w, "llamacpp:requests_processing 1\n")
		case "/completion":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"content\":\"yes\",\"tokens\":[1]}\n\n")
			w.(http.Flusher).Flush()
			time.Sleep(120 * time.Millisecond)
			fmt.Fprint(w, "data: {\"content\":\".\",\"tokens\":[2]}\n\ndata: {\"stop\":true,\"timings\":{\"prompt_n\":8,\"predicted_n\":2}}\n\n")
		default:
			http.NotFound(w, r)
		}
	})
	_ = http.ListenAndServe("127.0.0.1:"+args["--port"], handler)
	os.Exit(1)
}

func TestManagedModelProcessUsesNodeModelAndStopsOwnedGroup(t *testing.T) {
	plan, s := managedModelFixture(t)
	t.Setenv("MODELGAUGE_AGENT_TOKEN", "node-secret")
	t.Setenv("LLAMA_ARG_MODEL_URL", "https://example.invalid/unrelated-model")
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server, err := startModelServer(ctx, plan, s, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer server.stop()
	client := newServingClient(*s)
	defer client.close()
	if _, err := client.ready(ctx); err != nil {
		t.Fatal(err)
	}
	if s.LocalPID != server.cmd.Process.Pid || !strings.HasPrefix(s.Endpoint, "http://127.0.0.1:") {
		t.Fatal("not targeting owned model process", s)
	}
	data, err := os.ReadFile(os.Getenv("MG_TEST_MODEL_ARGS"))
	if err != nil {
		t.Fatal(err)
	}
	var args map[string]string
	_ = json.Unmarshal(data, &args)
	if args["--model"] != plan.Model.Path || args["--threads"] != "2" || args["--parallel"] != "2" || args["--ctx-size"] != "2048" || args["--host"] != "127.0.0.1" {
		t.Fatal(args)
	}
	cancel()
	select {
	case <-server.done:
	case <-time.After(5 * time.Second):
		t.Fatal("model survived cancellation")
	}
	if err := server.stop(); err != nil {
		t.Fatal(err)
	}
}

func TestManagedServingChecksModelBeforeStarting(t *testing.T) {
	plan, s := managedModelFixture(t)
	plan.Model.SHA256 = strings.Repeat("0", 64)
	if server, err := startModelServer(context.Background(), plan, s, t.TempDir()); err == nil || server != nil {
		t.Fatal("model checksum bypassed")
	}
	if _, err := os.Stat(os.Getenv("MG_TEST_MODEL_ARGS")); !os.IsNotExist(err) {
		t.Fatal("model process launched for changed model")
	}
}

func TestManagedServingProfileAndUnknownConfiguration(t *testing.T) {
	options := planOptions{NodeID: "node", ModelID: "model", ModelPath: "/node/model.gguf", ModelSHA256: strings.Repeat("a", 64), ExecutionMode: protocol.ExecutionModeLima, Threads: []int{1, 2}, Contexts: []int{128, 512}}
	plans, err := newPlans(context.Background(), "node-serving", options, nil)
	if err != nil || len(plans) != 2 {
		t.Fatal(len(plans), err)
	}
	for i, plan := range plans {
		s := servingConfig(plan)
		if s.Server.Threads != i+1 || s.Server.Parallel != 2 || s.Endpoint != "" || s.LocalPID != 0 || len(s.Cases) < 8 {
			t.Fatal(s)
		}
	}
	if _, err := newPlans(context.Background(), "node-serving", options, json.RawMessage(`{"endpoint":"http://remote:8080"}`)); err == nil {
		t.Fatal("external endpoint configuration accepted")
	}
	if _, err := newPlans(context.Background(), "node-serving", options, json.RawMessage(`{"concurrency":[0]}`)); err == nil {
		t.Fatal("invalid concurrency accepted")
	}
	if _, err := newPlans(context.Background(), "node-serving", options, json.RawMessage(`{"unknown":true}`)); err == nil {
		t.Fatal("unknown module config accepted")
	}
	custom, err := newPlans(context.Background(), "node-serving", options, json.RawMessage(`{"cases":[{"id":"only-case","scenario":"load","concurrency":3,"requests":1,"max_output_tokens":16}]}`))
	if err != nil || len(servingConfig(custom[0]).Cases) != 1 || servingConfig(custom[0]).Server.Parallel != 3 {
		t.Fatal("custom benchmark cases not applied", custom, err)
	}
	if _, err := newPlans(context.Background(), "node-serving", options, json.RawMessage(`{} {}`)); err == nil {
		t.Fatal("trailing module config accepted")
	}
}

func TestManagedServingRunMeasuresProcessAndRestartsOnResume(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("agent workloads execute on Linux")
	}
	t.Setenv("HOME", t.TempDir())
	plan, _ := managedModelFixture(t)
	dir := filepath.Join(t.TempDir(), "evidence")
	for i := 0; i < 2; i++ {
		result, _, err := Run(context.Background(), plan, Options{OutputDir: dir, Resume: i > 0})
		if err != nil {
			t.Fatal(err)
		}
		report := servingResult(result)
		if result.Status != protocol.RunStatusCompleted || report.StartupMS == nil || report.ShutdownMS == nil || report.Cases[0].Resources.SampledPeakRSSBytes == nil || report.Cases[0].Resources.CPUSeconds == nil {
			t.Fatal(result, report)
		}
		if i > 0 {
			archived, _ := filepath.Glob(filepath.Join(dir, "load.requests.jsonl.interrupted-*"))
			if len(archived) != 1 {
				t.Fatal("previous evidence not archived", archived)
			}
		}
	}
}

func TestManagedModelFailureAndBoundedLog(t *testing.T) {
	plan, s := managedModelFixture(t)
	t.Setenv("MG_TEST_MODEL_PROCESS", "fail")
	dir := t.TempDir()
	server, err := startModelServer(context.Background(), plan, s, dir)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-server.done:
	case <-time.After(5 * time.Second):
		t.Fatal("failed model process not reaped")
	}
	if server.running() == nil {
		t.Fatal("process failure hidden")
	}
	if err := server.stop(); err != nil {
		t.Fatal(err)
	}
	log, _ := os.ReadFile(filepath.Join(dir, "model-server.log"))
	if !strings.Contains(string(log), "fixture load failed") {
		t.Fatal("load failure evidence missing")
	}
	file, err := os.CreateTemp(t.TempDir(), "log")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	writer := modelServerLog{file: file}
	data := bytes.Repeat([]byte("x"), 9<<20)
	if n, err := writer.Write(data); err != nil || n != len(data) {
		t.Fatal(n, err)
	}
	info, _ := file.Stat()
	if info.Size() != 8<<20 {
		t.Fatal("unbounded runtime log", info.Size())
	}
}
