package controller

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

func TestServingResultsPersistWithoutSchemaChanges(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.RegisterNode(ctx, Node{ID: "serving", DisplayName: "serving", Kind: "virtual", ExecutionMode: protocol.ExecutionModeLima}); err != nil {
		t.Fatal(err)
	}
	plan := protocol.Plan{SchemaVersion: protocol.BenchmarkPlanV1, NodeID: "serving", ExecutionMode: protocol.ExecutionModeLima, Runtime: protocol.Runtime{Kind: "http"}, Model: protocol.Model{ID: "model"}, Benchmark: &protocol.BenchmarkSpec{Kind: "serving", Config: json.RawMessage(`{"endpoint":"http://localhost:8080","api":"openai","request_timeout_seconds":1,"readiness_timeout_seconds":1,"sample_interval_seconds":1,"cases":[{"id":"load","concurrency":1,"requests":1,"max_output_tokens":16}]}`)}}
	id, err := s.QueuePlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := s.LeasePlan(ctx, "serving", JobTTL)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.StartRun(ctx, "serving", "run-serving", id, lease.LeaseToken, protocol.ExecutionModeLima, nil); err != nil {
		t.Fatal(err)
	}
	report := &protocol.BenchmarkReport{Kind: "serving", Data: json.RawMessage(`{"schema_version":"modelgauge-serving/v1","model_id":"model","api":"openai","endpoint":"http://localhost:8080","cases":[{"id":"load","scenario":"load","concurrency":1,"elapsed_seconds":1,"offered":1,"dispatched":1,"successful":1,"output_tokens_per_second":12.5,"ttft_ms":{"count":1,"mean":20,"p95":20}}]}`)}
	result := protocol.Result{RunID: "run-serving", Status: protocol.RunStatusCompleted, Benchmark: report}
	if err = s.CompleteRun(ctx, "serving", id, lease.LeaseToken, result); err != nil {
		t.Fatal(err)
	}
	stored, err := s.ListResults(ctx, "serving")
	if err != nil || len(stored) != 1 || stored[0].Benchmark == nil || !strings.Contains(string(stored[0].Benchmark.Data), `"load"`) {
		t.Fatal(stored, err)
	}
	if _, err = s.Recommend(ctx, "serving", "model", "lima-linux", 128, 1<<30); !errors.Is(err, ErrNotFound) {
		t.Fatal("serving data contaminated engine recommendations", err)
	}
	exported, err := s.ExportNode(ctx, "serving")
	if err != nil || exported.Results[0].Benchmark == nil {
		t.Fatal(exported, err)
	}
	metrics, err := s.Metrics(ctx)
	if err != nil || !strings.Contains(metrics, "modelgauge_serving_latency_seconds") || !strings.Contains(metrics, "12.5") {
		t.Fatal(metrics, err)
	}
}
