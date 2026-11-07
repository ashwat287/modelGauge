package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
	"reflect"
	"strings"
	"testing"
)

func TestNodeExportScopeAndCredentialExclusion(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	id := setupPlan(t, s, "export-node")
	setupPlan(t, s, "foreign-node")
	lease, err := s.LeasePlan(ctx, "export-node", JobTTL)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.StartRun(ctx, "export-node", "run-export", id, lease.LeaseToken, protocol.ExecutionModeLima, map[string]string{"agent_version": "test"}); err != nil {
		t.Fatal(err)
	}
	result := protocol.Result{RunID: "run-export", PlanID: id, NodeID: "export-node", ExecutionMode: protocol.ExecutionModeLima, Status: protocol.RunStatusCompleted, Cases: []protocol.CaseResult{{CaseID: "prompt", Threads: 2, PromptTokens: 32, Repetitions: 5, TokensPerSecond: 25, TokensPerSecondStddev: .2}}, Memory: &protocol.MemoryResult{CaseID: "memory", PeakRSSBytes: 100, SafeReservationBytes: 125}, Artifacts: []protocol.Artifact{{Name: "prompt.json", SHA256: strings.Repeat("a", 64)}}}
	if err = s.CompleteRun(ctx, "export-node", id, lease.LeaseToken, result); err != nil {
		t.Fatal(err)
	}
	if _, err = s.QueueInstall(ctx, InstallJob{NodeID: "export-node", ModelID: "pending-model", SourceURL: "https://example.com/model.gguf?private=do-not-export"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.ExportNode(ctx, "export-node")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Results) != 1 || len(snapshot.Results[0].Cases) != 1 || len(snapshot.Cases) != 1 || len(snapshot.Memory) != 1 || len(snapshot.Artifacts) != 1 || len(snapshot.Plans) != 1 || len(snapshot.InstallJobs) != 1 {
		t.Fatalf("incomplete snapshot: %+v", snapshot)
	}
	raw, _ := json.Marshal(snapshot)
	for _, forbidden := range []string{"lease_token", "lease_expires_at", lease.LeaseToken, "do-not-export", "source_url", "foreign-node"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("export included %s", forbidden)
		}
	}
	if _, err = s.ExportNode(ctx, "missing-node"); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing node was not rejected:", err)
	}
}

// A transaction cannot issue nested queries while prior rows are still open.
type singleConnection struct {
	busy    bool
	queries int
}
type exportRows struct {
	pgx.Rows
	owner    *singleConnection
	values   []any
	consumed bool
}

func (r *exportRows) Next() bool {
	if r.consumed {
		return false
	}
	r.consumed = true
	return true
}
func (r *exportRows) Close()     { r.owner.busy = false }
func (r *exportRows) Err() error { return nil }
func (r *exportRows) Scan(dest ...any) error {
	if len(dest) != len(r.values) {
		return fmt.Errorf("unexpected scan shape")
	}
	for i, v := range r.values {
		reflect.ValueOf(dest[i]).Elem().Set(reflect.ValueOf(v))
	}
	return nil
}
func (q *singleConnection) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	if q.busy {
		return nil, fmt.Errorf("conn busy")
	}
	q.busy = true
	q.queries++
	values := []any{"run", "plan", "node", "model", "lima-linux", "completed", "2026-11-07T10:00:00Z", int64(100), int64(125), "", false}
	if q.queries > 1 {
		values = []any{"case", 2, 32, 0, 5, float64(25), float64(.2)}
	}
	return &exportRows{owner: q, values: values}, nil
}
func TestExportResultQueriesDrainSingleConnection(t *testing.T) {
	q := &singleConnection{}
	results, err := listResults(context.Background(), q, "node")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || len(results[0].Cases) != 1 || q.busy {
		t.Fatal("incomplete transaction result")
	}
}
