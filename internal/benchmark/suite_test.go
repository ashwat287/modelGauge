package benchmark

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nerdylucifer/modelgauge/internal/platform"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

func TestDeploymentPlanUsesOneModelAndOrderedStages(t *testing.T) {
	o := planOptions{NodeID: "node", ExecutionMode: protocol.ExecutionModeLima, ModelID: "model", ModelPath: "/models/model.gguf", ModelSHA256: strings.Repeat("a", 64), Threads: []int{1, 2}, Contexts: []int{512}, Warmups: 1, Repetitions: 2}
	plans, err := newPlans(context.Background(), "deployment", o, json.RawMessage(`{"suite":"basic","requests":2}`))
	if err != nil || len(plans) != 1 {
		t.Fatal(plans, err)
	}
	suite, err := decodeSuite(plans[0])
	if err != nil || len(suite.Stages) != 3 {
		t.Fatal(suite, err)
	}
	if planKind(suite.Stages[0]) != "llama.cpp" || planKind(suite.Stages[1]) != "serving" || planKind(suite.Stages[2]) != "serving" {
		t.Fatal(suite)
	}
	bad := plans[0]
	suite.Stages[1].Model.ID = "foreign"
	_ = setConfig(&bad, "suite", suite)
	if ValidatePlan(bad) == nil {
		t.Fatal("foreign stage accepted")
	}
	suite.Stages[0] = plans[0]
	_ = setConfig(&bad, "suite", suite)
	if ValidatePlan(bad) == nil {
		t.Fatal("nested suite accepted")
	}
}
func TestSuiteHoldsOneLockStopsOnFailureAndResumesCompletedStages(t *testing.T) {
	for _, mode := range []string{"success", "failure", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			calls := []string{}
			secondFails := mode != "success"
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			const kind = "suite-test-module"
			implementations[kind] = implementation{run: func(ctx context.Context, p protocol.Plan, o Options) (protocol.Result, string, error) {
				if unlock, err := platform.Lock("execution"); err == nil {
					unlock()
					t.Fatal("whole-suite lock was not held")
				}
				var config struct {
					ID string `json:"id"`
				}
				_ = json.Unmarshal(p.Benchmark.Config, &config)
				calls = append(calls, config.ID)
				if config.ID == "second" && secondFails {
					if mode == "cancel" {
						cancel()
						return protocol.Result{}, o.OutputDir, ctx.Err()
					}
					return protocol.Result{}, o.OutputDir, errors.New("stage failed")
				}
				if err := os.MkdirAll(o.OutputDir, 0700); err != nil {
					t.Fatal(err)
				}
				r := protocol.Result{SchemaVersion: resultSchemaV1, RunID: o.RunID, PlanID: p.PlanID, NodeID: p.NodeID, ExecutionMode: p.ExecutionMode, Status: protocol.RunStatusCompleted, Cases: []protocol.CaseResult{{CaseID: config.ID, TokensPerSecond: 2}}}
				if err := platform.WriteJSON(filepath.Join(o.OutputDir, "result.json"), r, 0600); err != nil {
					t.Fatal(err)
				}
				return r, o.OutputDir, nil
			}}
			defer delete(implementations, kind)
			base := protocol.Plan{SchemaVersion: protocol.BenchmarkPlanV1, NodeID: "node", ExecutionMode: protocol.ExecutionModeLima, Model: protocol.Model{ID: "model"}}
			first, second, third := base, base, base
			_ = setConfig(&first, kind, map[string]string{"id": "first"})
			_ = setConfig(&second, kind, map[string]string{"id": "second"})
			_ = setConfig(&third, kind, map[string]string{"id": "third"})
			p := base
			p.Runtime.Kind = "suite"
			_ = setConfig(&p, "suite", suitePlan{Stages: []protocol.Plan{first, second, third}})
			o := Options{RunID: "run-test", OutputDir: filepath.Join(t.TempDir(), "run"), Resume: true}
			r, _, err := Run(ctx, p, o)
			if mode == "success" {
				if err != nil || strings.Join(calls, ",") != "first,second,third" || len(r.Cases) != 3 {
					t.Fatal(r, calls, err)
				}
				files, e := WriteReports([]protocol.Result{r})
				if e != nil || !strings.Contains(string(files["suite-results.csv"]), "llama.cpp") {
					t.Fatal(files, e)
				}
				return
			}
			if err == nil || strings.Join(calls, ",") != "first,second" {
				t.Fatal("later stage ran after failure", calls, err)
			}
			// An interrupted suite can resume, but never reruns a completed stage.
			secondFails = false
			calls = nil
			r, _, err = Run(context.Background(), p, o)
			if err != nil || strings.Join(calls, ",") != "second,third" || r.Status != protocol.RunStatusCompleted {
				t.Fatal(r, calls, err)
			}
		})
	}
}

func TestSuiteMetricsIncludeStageIdentity(t *testing.T) {
	report := suiteReport{Stages: []protocol.Result{}}
	for i := 0; i < 2; i++ {
		report.Stages = append(report.Stages, protocol.Result{Benchmark: &protocol.BenchmarkReport{Kind: "serving", Data: json.RawMessage(`{"schema_version":"modelgauge-serving/v1","model_id":"model","api":"llama.cpp","endpoint":"http://127.0.0.1:1234","cases":[],"process_start_to_ready_ms":20}`)}})
	}
	data, _ := json.Marshal(report)
	var out strings.Builder
	if err := WriteMetrics(&out, "node", "lima-linux", "model", &protocol.BenchmarkReport{Kind: "suite", Data: data}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `stage="1"`) || !strings.Contains(out.String(), `stage="2"`) {
		t.Fatal(out.String())
	}
}
