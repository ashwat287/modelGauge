package controller

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/nerdylucifer/modelgauge/internal/benchmark"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

func experimentFixture(t *testing.T, s *Store) benchmark.Document {
	t.Helper()
	ctx := context.Background()
	for _, node := range []string{"first", "second"} {
		if err := s.RegisterNode(ctx, Node{ID: node, DisplayName: node, Kind: "virtual", ExecutionMode: protocol.ExecutionModeLima}); err != nil {
			t.Fatal(err)
		}
		if err := s.ReplaceNodeModels(ctx, node, []NodeModel{{ID: "model", Path: "/node/" + node + "/model.gguf", SHA256: strings.Repeat("a", 64)}}); err != nil {
			t.Fatal(err)
		}
	}
	d, err := benchmark.ParseDocument(strings.NewReader(`apiVersion: modelgauge.io/v1alpha1
kind: Benchmark
metadata: {name: experiment}
spec:
  nodes: [first, second]
  models: [model]
  matrix: {threads: [1], contexts: [512]}
  stages: [{profile: standard-cpu}]
`))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestExperimentApplyIsAtomicConcurrentAndImmutable(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	d := experimentFixture(t, s)
	preview, err := s.ApplyExperiment(ctx, d, true)
	if err != nil || len(preview.Plans) != 2 || preview.CreatedAt != nil {
		t.Fatal(preview, err)
	}
	depth, err := s.QueueDepth(ctx)
	if err != nil || depth != 0 {
		t.Fatal(depth, err)
	}
	var wg sync.WaitGroup
	receipts := make(chan Experiment, 2)
	failures := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); e, err := s.ApplyExperiment(ctx, d, false); receipts <- e; failures <- err }()
	}
	wg.Wait()
	close(receipts)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	first := <-receipts
	second := <-receipts
	if first.Plans[0].PlanID != second.Plans[0].PlanID || first.Plans[1].PlanID != second.Plans[1].PlanID {
		t.Fatal("duplicate submissions", first, second)
	}
	depth, err = s.QueueDepth(ctx)
	if err != nil || depth != 2 {
		t.Fatal(depth, err)
	}
	// Inventory changes must not silently rewrite or block a retry after acceptance.
	if err = s.ReplaceNodeModels(ctx, "first", nil); err != nil {
		t.Fatal(err)
	}
	replay, err := s.ApplyExperiment(ctx, d, false)
	if err != nil || replay.Plans[0].Specification.Model.Path != "/node/first/model.gguf" {
		t.Fatal(replay, err)
	}
	d.Spec.Matrix.Threads = []int{2}
	if _, err = s.ApplyExperiment(ctx, d, false); !errors.Is(err, ErrConflict) {
		t.Fatal("edited specification accepted", err)
	}
	if _, err = s.Experiment(ctx, d.Metadata.Name, d.Digest(), true); !errors.Is(err, ErrConflict) {
		t.Fatal("changed file cancelled saved work", err)
	}
	// Active jobs observe existing lease cancellation; queued siblings never start.
	lease, err := s.LeasePlan(ctx, "first", JobTTL)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := s.Experiment(ctx, first.Name, first.Digest, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range cancelled.Plans {
		if p.Status != "cancelled" {
			t.Fatal(p)
		}
	}
	renewal, err := s.Renew(ctx, "first", lease.PlanID, lease.LeaseToken, false)
	if err != nil || !renewal.Cancelled {
		t.Fatal(renewal, err)
	}
	if _, err = s.LeasePlan(ctx, "second", JobTTL); !errors.Is(err, ErrNotFound) {
		t.Fatal("cancelled sibling leased", err)
	}
	replay, err = s.ApplyExperiment(ctx, first.Specification, false)
	if err != nil || replay.Plans[0].Status != "cancelled" {
		t.Fatal("cancelled experiment requeued", replay, err)
	}
}

func TestExperimentMissingTargetLeavesNoPartialJobs(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	d := experimentFixture(t, s)
	if err := s.ReplaceNodeModels(ctx, "second", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyExperiment(ctx, d, false); !errors.Is(err, ErrInvalidExperiment) {
		t.Fatal(err)
	}
	var count int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM benchmark_experiments`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	if depth, err := s.QueueDepth(ctx); err != nil || depth != 0 {
		t.Fatal("partially queued", depth, err)
	}
	if _, err := s.Experiment(ctx, "experiment", "", false); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestExperimentFailureAfterJobInsertsRollsBackEverything(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	d := experimentFixture(t, s)
	if _, err := s.pool.Exec(ctx, `ALTER TABLE benchmark_experiments ADD CONSTRAINT simulate_record_failure CHECK (name <> 'experiment')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyExperiment(ctx, d, false); err == nil {
		t.Fatal("simulated record failure did not fail apply")
	}
	if depth, err := s.QueueDepth(ctx); err != nil || depth != 0 {
		t.Fatal("jobs escaped rolled-back transaction", depth, err)
	}
	if _, err := s.Experiment(ctx, d.Metadata.Name, "", false); !errors.Is(err, ErrNotFound) {
		t.Fatal("failed experiment persisted", err)
	}
}
