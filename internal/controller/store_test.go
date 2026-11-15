package controller

import (
	"context"
	"errors"
	"github.com/nerdylucifer/modelgauge/internal/benchmark"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
	"strings"
	"testing"
	"time"
)

func TestCompletedFilesSurviveRestartAndExport(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SaveNode(Node{ID: "node"}); err != nil {
		t.Fatal(err)
	}
	doc, err := benchmark.ParseDocument(strings.NewReader("apiVersion: modelgauge.io/v1alpha1\nkind: Benchmark\nmetadata: {name: baseline}\nspec: {threads: [1], contexts: [32], stages: [{profile: standard-cpu}]}"))
	if err != nil {
		t.Fatal(err)
	}
	plan := protocol.ExamplePlan()
	plan.NodeID = "node"
	plan.Model.ID = "model"
	for _, id := range []string{"run-first", "run-second"} {
		r := Record{Document: doc, Plan: plan, Result: protocol.Result{RunID: id, PlanID: id, NodeID: "node", Status: protocol.RunStatusCompleted, ExecutionMode: plan.ExecutionMode, StartedAt: time.Now(), CompletedAt: time.Now(), Cases: []protocol.CaseResult{{CaseID: "prompt", Threads: 1, PromptTokens: 32, TokensPerSecond: 20}, {CaseID: "generation", Threads: 2, GenerationTokens: 128, TokensPerSecond: 10}}, Memory: &protocol.MemoryResult{PeakRSSBytes: 100, SafeReservationBytes: 125}, Artifacts: []protocol.Artifact{{Name: "plan.json", SHA256: strings.Repeat("a", 64)}}}}
		if err = s.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Save(Record{Result: protocol.Result{RunID: "run-failed", Status: protocol.RunStatusFailed}}); err == nil {
		t.Fatal("failed work persisted")
	}
	s, err = Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListResults(ctx, "node")
	if err != nil || len(rows) != 2 || rows[0].RunID != "run-second" {
		t.Fatal(rows, err)
	}
	out, err := s.ExportNodeRun(ctx, "node", "run-first")
	if err != nil || len(out.Results) != 1 || len(out.Artifacts) != 1 {
		t.Fatal(out, err)
	}
	if _, err = s.ExportNodeRun(ctx, "other", ""); !errors.Is(err, ErrNotFound) {
		t.Fatal("wrong node exported", err)
	}
	recommendation, err := s.Recommend(ctx, "node", "model", string(plan.ExecutionMode), 32, 200)
	if err != nil || recommendation.FitStatus != "fits" || recommendation.GenerationThreads != 2 {
		t.Fatal(recommendation, err)
	}
	metrics, err := s.Metrics(ctx)
	if err != nil || !strings.Contains(metrics, "modelgauge_benchmark_tokens_per_second") || strings.Contains(metrics, "queued") {
		t.Fatal(metrics, err)
	}
}
