package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/nerdylucifer/modelgauge/internal/control"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const yamlPlan = "apiVersion: modelgauge.io/v1alpha1\nkind: Benchmark\nmetadata: {name: foreground}\nspec: {threads: [1], contexts: [32], stages: [{profile: standard-cpu}]}"

func TestForegroundCLIStreamsOnceAndReportsFailure(t *testing.T) {
	for _, operation := range []string{"install", "run"} {
		for _, fail := range []bool{false, true} {
			t.Run(operation+map[bool]string{true: "/failure", false: "/completed"}[fail], func(t *testing.T) {
				t.Setenv("HOME", t.TempDir())
				label := "Benchmark"
				if operation == "install" {
					label = "Installation"
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("X-ModelGauge-Admin-Token") != "secret" {
						t.Error("missing auth")
					}
					if r.URL.Path == "/v1/nodes" {
						json.NewEncoder(w).Encode([]map[string]string{{"node_id": "node"}})
						return
					}
					encoder := json.NewEncoder(w)
					encoder.Encode(protocol.Event{Log: label + ": running"})
					encoder.Encode(protocol.Event{Log: label + ": running"})
					if fail {
						encoder.Encode(protocol.Event{Error: "fixture failed"})
						return
					}
					encoder.Encode(protocol.Event{Log: label + ": completed"})
					result := json.RawMessage(`{"model_id":"model","status":"completed"}`)
					if operation == "run" {
						result = json.RawMessage(`{"name":"run-test","plans":[{"status":"completed"}]}`)
					}
					encoder.Encode(protocol.Event{Result: result})
				}))
				defer server.Close()
				t.Setenv("MODELGAUGE_CONTROLLER_URL", server.URL)
				t.Setenv("MODELGAUGE_ADMIN_TOKEN", "secret")
				if err := control.Save(control.Config{Model: "model", ControllerURL: server.URL, AdminToken: "secret"}); err != nil {
					t.Fatal(err)
				}
				file := filepath.Join(t.TempDir(), "benchmark.yaml")
				os.WriteFile(file, []byte(yamlPlan), 0600)
				args := []string{"run", file, "--json"}
				if operation == "install" {
					args = []string{"model", "install", "model", "--url", "https://example.test/model", "--json"}
				}
				var out, progress bytes.Buffer
				code := Run(args, &out, &progress)
				if (code != 0) != fail {
					t.Fatal(code, &progress)
				}
				if strings.Count(progress.String(), label+": running") != 1 {
					t.Fatal("running repeated", &progress)
				}
				if !fail && (!json.Valid(out.Bytes()) || strings.Count(progress.String(), label+": completed") != 1) {
					t.Fatal(&out, &progress)
				}
			})
		}
	}
}
func TestInterruptWaitsForCancellationWithoutRetry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	started := make(chan struct{}, 1)
	stopped := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/cancel":
			stopped <- struct{}{}
			w.WriteHeader(204)
		default:
			json.NewEncoder(w).Encode(protocol.Event{Log: "Benchmark: running"})
			w.(http.Flusher).Flush()
			started <- struct{}{}
			<-r.Context().Done()
		}
	}))
	defer server.Close()
	t.Setenv("MODELGAUGE_CONTROLLER_URL", server.URL)
	t.Setenv("MODELGAUGE_ADMIN_TOKEN", "secret")
	control.Save(control.Config{Model: "model"})
	file := filepath.Join(t.TempDir(), "benchmark.yaml")
	os.WriteFile(file, []byte(yamlPlan), 0600)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	var out, progress bytes.Buffer
	go func() { done <- RunContext(ctx, []string{"run", file}, &out, &progress) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("operation did not start")
	}
	cancel()
	select {
	case code := <-done:
		if code != 130 {
			t.Fatal(code, &progress)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("interrupt did not finish")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("worker stop was not requested")
	}
}
func TestRemovedBackgroundCommandsAreRejected(t *testing.T) {
	for _, command := range []string{"status", "logs", "cancel", "agent"} {
		var out, stderr bytes.Buffer
		if code := Run([]string{command}, &out, &stderr); code != 2 {
			t.Fatal(command, code)
		}
	}
}
