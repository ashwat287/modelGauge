package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nerdylucifer/modelgauge/internal/benchmark"
	"github.com/nerdylucifer/modelgauge/internal/controller"
)

const yamlPlan = `apiVersion: modelgauge.io/v1alpha1
kind: Benchmark
metadata: {name: retry-test}
spec:
  nodes: [benchmark-node]
  models: [qwen:q4]
  matrix: {threads: [1], contexts: [512]}
  stages: [{profile: standard-cpu}]
`

func TestYAMLCommandsUseOneControllerContractAndSurviveLostResponse(t *testing.T) {
	var saved *controller.Experiment
	dropped := false
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-ModelGauge-Admin-Token") != "secret" {
			t.Error("missing authentication")
		}
		requests = append(requests, r.Method+" "+r.URL.Path)
		if r.URL.Path == "/v1/experiments" {
			var doc benchmark.Document
			if err := json.NewDecoder(r.Body).Decode(&doc); err != nil {
				t.Error(err)
			}
			if err := doc.Validate(); err != nil {
				t.Error(err)
			}
			if r.URL.Query().Get("dry_run") == "true" {
				_ = json.NewEncoder(w).Encode(controller.Experiment{Name: doc.Metadata.Name, Digest: doc.Digest()})
				return
			}
			if saved == nil {
				saved = &controller.Experiment{Name: doc.Metadata.Name, Digest: doc.Digest(), Specification: doc, Plans: []controller.ExperimentPlan{{PlanID: "stable", Status: "queued"}}}
			}
			if saved.Digest != doc.Digest() {
				http.Error(w, "changed content", 409)
				return
			}
			if !dropped {
				dropped = true
				http.Error(w, "response lost after commit", 503)
				return
			}
		} else {
			if saved == nil || r.URL.Query().Get("spec_sha256") != saved.Digest {
				http.Error(w, "changed file", 409)
				return
			}
			if strings.HasSuffix(r.URL.Path, "/cancel") {
				saved.Plans[0].Status = "cancelled"
			}
		}
		_ = json.NewEncoder(w).Encode(saved)
	}))
	defer server.Close()
	file := filepath.Join(t.TempDir(), "benchmark.yaml")
	if err := os.WriteFile(file, []byte(yamlPlan), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(command string, extra ...string) (int, string, string) {
		var out, stderr bytes.Buffer
		args := append([]string{command, "-f", file, "--controller", server.URL, "--admin-token", "secret"}, extra...)
		code := RunContext(context.Background(), args, &out, &stderr)
		return code, out.String(), stderr.String()
	}
	for _, command := range []string{"validate", "apply"} {
		extra := []string{}
		if command == "apply" {
			extra = append(extra, "--dry-run")
		}
		if code, _, err := run(command, extra...); code != 0 {
			t.Fatal(command, code, err)
		}
	}
	if saved != nil {
		t.Fatal("dry run submitted work")
	}
	if code, _, err := run("apply"); code == 0 || !strings.Contains(err, "Retry the same file") {
		t.Fatal(code, err)
	}
	// Retrying requires no CLI-local state or knowledge of directories on the node.
	if code, out, err := run("apply"); code != 0 || !strings.Contains(out, "stable") {
		t.Fatal(code, out, err)
	}
	if code, out, err := run("status"); code != 0 || !strings.Contains(out, "queued") {
		t.Fatal(code, out, err)
	}
	if code, out, err := run("cancel"); code != 0 || !strings.Contains(out, "cancelled") {
		t.Fatal(code, out, err)
	}
	if len(requests) != 6 {
		t.Fatal(requests)
	}
}

func TestYAMLValidationFailsBeforeNetworkAndRejectsWorkloadFlags(t *testing.T) {
	file := filepath.Join(t.TempDir(), "benchmark.yaml")
	if err := os.WriteFile(file, []byte(yamlPlan+"unexpected: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := Run([]string{"apply", "-f", file, "--controller", "http://127.0.0.1:1", "--admin-token", "secret"}, &out, &stderr); code != 2 || strings.Contains(stderr.String(), "connect") {
		t.Fatal(code, stderr.String())
	}
	for _, args := range [][]string{{"apply", "-f", file, "--threads", "2"}, {"status", "-f", file, "--dry-run"}, {"apply", "-f", file, "extra"}} {
		if code := Run(args, &out, &stderr); code != 2 {
			t.Fatal(args, code, stderr.String())
		}
	}
}
