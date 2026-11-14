package benchmark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

func TestNewBenchmarkNeedsOnlyRegistration(t *testing.T) {
	const kind = "test-benchmark"
	implementations[kind] = implementation{
		profile: "test-profile",
		plan: func(ctx context.Context, options planOptions, config json.RawMessage) ([]protocol.Plan, error) {
			return []protocol.Plan{{SchemaVersion: protocol.BenchmarkPlanV1, NodeID: options.NodeID, ExecutionMode: options.ExecutionMode, Model: protocol.Model{ID: options.ModelID}, Benchmark: &protocol.BenchmarkSpec{Kind: kind, Config: config}}}, ctx.Err()
		},
		validate: func(plan protocol.Plan) error {
			var config struct {
				Samples int `json:"samples"`
			}
			if err := json.Unmarshal(plan.Benchmark.Config, &config); err != nil {
				return err
			}
			if config.Samples < 1 {
				return errors.New("samples must be positive")
			}
			return nil
		},
		run: func(ctx context.Context, plan protocol.Plan, options Options) (protocol.Result, string, error) {
			if err := ctx.Err(); err != nil {
				return protocol.Result{}, "", err
			}
			return protocol.Result{SchemaVersion: resultSchemaV1, RunID: options.RunID, NodeID: plan.NodeID, Status: protocol.RunStatusCompleted, Benchmark: &protocol.BenchmarkReport{Kind: kind, Data: json.RawMessage(`{"schema_version":"test/v1","measured":7}`)}}, options.OutputDir, nil
		},
		validateReport: func(data json.RawMessage) error {
			var value struct {
				Measured int `json:"measured"`
			}
			if err := json.Unmarshal(data, &value); err != nil {
				return err
			}
			if value.Measured != 7 {
				return errors.New("unexpected measurement")
			}
			return nil
		},
		csv: func(out io.Writer, results []protocol.Result) error {
			_, err := fmt.Fprintln(out, "run_id,measured\n"+results[0].RunID+",7")
			return err
		},
		metrics: func(out *strings.Builder, node, mode, model string, data json.RawMessage) error {
			fmt.Fprintf(out, "test_measured{node=%q} 7\n", node)
			return nil
		},
	}
	t.Cleanup(func() { delete(implementations, kind) })
	generated, err := newPlans(context.Background(), "test-profile", planOptions{NodeID: "test", ExecutionMode: protocol.ExecutionModeLima, ModelID: "test"}, json.RawMessage(`{"samples":1}`))
	if err != nil || len(generated) != 1 || !strings.Contains(strings.Join(Profiles(), ","), "test-profile") {
		t.Fatal("module planning required platform changes", generated, err)
	}
	plan := protocol.Plan{SchemaVersion: protocol.BenchmarkPlanV1, NodeID: "test", ExecutionMode: protocol.ExecutionModeLima, Model: protocol.Model{ID: "test"}, Benchmark: &protocol.BenchmarkSpec{Kind: kind, Config: json.RawMessage(`{"samples":1}`)}}
	// Same JSON transport used by the CLI, queue and agent.
	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var transported protocol.Plan
	if err = json.Unmarshal(data, &transported); err != nil {
		t.Fatal(err)
	}
	result, dir, err := Run(context.Background(), transported, Options{RunID: "test-run", OutputDir: "evidence"})
	if err != nil || dir != "evidence" || result.Benchmark == nil {
		t.Fatal(result, dir, err)
	}
	if err = ValidateResult(result); err != nil {
		t.Fatal(err)
	}
	files, err := WriteReports([]protocol.Result{result})
	if err != nil || !strings.Contains(string(files[kind+"-results.csv"]), "test-run,7") {
		t.Fatal(files, err)
	}
	var metrics strings.Builder
	if err = WriteMetrics(&metrics, "test", "lima-linux", "test", result.Benchmark); err != nil || !strings.Contains(metrics.String(), "test_measured") {
		t.Fatal(metrics.String(), err)
	}
	plan.Benchmark.Config = json.RawMessage(`{"samples":0}`)
	if ValidatePlan(plan) == nil {
		t.Fatal("module validation bypassed")
	}
	result.Benchmark.Data = json.RawMessage(`{"measured":8}`)
	if ValidateResult(result) == nil {
		t.Fatal("module result validation bypassed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err = Run(ctx, transported, Options{}); !errors.Is(err, context.Canceled) {
		t.Fatal("context not passed to benchmark", err)
	}
	delete(implementations, kind)
	if _, err := newPlans(context.Background(), "test-profile", planOptions{}, json.RawMessage(`{}`)); err == nil {
		t.Fatal("removed profile still available")
	}
	if ValidatePlan(transported) == nil {
		t.Fatal("removed benchmark still accepted jobs")
	}
	files, err = WriteReports([]protocol.Result{result})
	if err != nil || len(files) != 0 {
		t.Fatal("removed formatter blocked historical export", err)
	}
	data, err = json.Marshal(result)
	if err != nil || !strings.Contains(string(data), `"measured":8`) {
		t.Fatal("historical report lost after removal", string(data), err)
	}
}

func TestExistingEnginePlanValidationStillRejectsInvalidWorkloads(t *testing.T) {
	plan := protocol.ExamplePlan()
	if err := ValidatePlan(plan); err != nil {
		t.Fatal(err)
	}
	plan.Cases[0].Threads = 0
	if ValidatePlan(plan) == nil {
		t.Fatal("engine workload validation bypassed")
	}
}

func TestServingModuleRejectsUnknownReportFields(t *testing.T) {
	result := protocol.Result{Benchmark: &protocol.BenchmarkReport{Kind: "serving", Data: json.RawMessage(`{"schema_version":"modelgauge-serving/v1","endpoint":"http://localhost:8080","cases":[],"unknown":1}`)}}
	if ValidateResult(result) == nil {
		t.Fatal("moving validation out of the server weakened report validation")
	}
}

func TestEngineResumeAcceptsRuntimeDiscoveredByOlderAgent(t *testing.T) {
	current := protocol.ExamplePlan()
	previous := current
	previous.Runtime.Binary = "/managed/llama-bench"
	if !sameEngineResumePlan(previous, current, "/managed/llama-bench") {
		t.Fatal("moving discovery broke existing interrupted jobs")
	}
	if sameEngineResumePlan(previous, current, "/other/llama-bench") {
		t.Fatal("different runtime path accepted")
	}
	previous.Defaults.MeasureRepetitions++
	if sameEngineResumePlan(previous, current, "/managed/llama-bench") {
		t.Fatal("changed workload accepted")
	}
}

func TestServingMetricsAreOwnedByModuleAndOmitUnknownValues(t *testing.T) {
	var out strings.Builder
	writeServingMetrics(&out, "node", "lima-linux", "model", servingReport{Cases: []servingCaseResult{{ID: "load", Concurrency: 1, Scenario: "load", Successful: 1, RequestsPerSecond: 2, TTFTMS: &distributionStats{Count: 1, Mean: 1000, P95: 1000}}}})
	text := out.String()
	if !strings.Contains(text, `measurement="ttft"`) || strings.Contains(text, "modelgauge_serving_output_tokens_per_second{") || strings.Contains(text, "modelgauge_serving_sampled_peak_rss_bytes{") {
		t.Fatal(text)
	}
	out.Reset()
	writeServingMetrics(&out, "node", "lima-linux", "model", servingReport{StartupMS: floatPointer(250), ShutdownMS: floatPointer(100)})
	if !strings.Contains(out.String(), "modelgauge_serving_process_start_to_ready_seconds{") || !strings.Contains(out.String(), "} 0.25\n") || !strings.Contains(out.String(), "modelgauge_serving_process_shutdown_seconds{") {
		t.Fatal(out.String())
	}
}
