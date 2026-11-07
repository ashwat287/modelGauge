package controller

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

// Tests use an isolated temporary schema, never the live benchmark tables.
func testStore(t *testing.T) *Store {
	t.Helper()
	host := os.Getenv("MODELGAUGE_TEST_DATABASE_HOST")
	if host == "" {
		t.Skip("set MODELGAUGE_TEST_DATABASE_HOST for isolated PostgreSQL integration tests")
	}
	uri := url.URL{Scheme: "postgres", Host: host, Path: "benchmark", User: url.UserPassword("benchmark_app", os.Getenv("MODELGAUGE_TEST_DATABASE_PASSWORD")), RawQuery: "sslmode=disable"}
	ctx := context.Background()
	base, err := Open(ctx, uri.String())
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("nb_recovery_test_%d", time.Now().UnixNano())
	ident := pgx.Identifier{schema}.Sanitize()
	if _, err = base.pool.Exec(ctx, "CREATE SCHEMA "+ident); err != nil {
		t.Fatal(err)
	}
	q := uri.Query()
	q.Set("search_path", schema)
	uri.RawQuery = q.Encode()
	store, err := Open(ctx, uri.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		store.Close()
		_, err := base.pool.Exec(ctx, "DROP SCHEMA "+ident+" CASCADE")
		if err != nil {
			t.Error(err)
		}
		base.Close()
	})
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return store
}
func setupPlan(t *testing.T, s *Store, node string) string {
	t.Helper()
	ctx := context.Background()
	if err := s.RegisterNode(ctx, Node{ID: node, DisplayName: node, Kind: "virtual", ExecutionMode: protocol.ExecutionModeLima}); err != nil {
		t.Fatal(err)
	}
	p := protocol.ExamplePlan()
	p.NodeID = node
	p.ExecutionMode = protocol.ExecutionModeLima
	id, err := s.QueuePlan(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestJobExclusivity(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	setupPlan(t, s, "race")
	setupPlan(t, s, "race")
	if _, err := s.QueueInstall(ctx, InstallJob{NodeID: "race", ModelID: "test", SourceURL: "https://example.com/model.gguf"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				_, err := s.LeasePlan(ctx, "race", JobTTL)
				results <- err
			} else {
				_, err := s.LeaseInstall(ctx, "race")
				results <- err
			}
		}(i)
	}
	wg.Wait()
	close(results)
	claimed := 0
	for err := range results {
		if err == nil {
			claimed++
		} else if !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
	}
	if claimed != 1 {
		t.Fatalf("claimed %d jobs concurrently, want 1", claimed)
	}
}
func TestCompletionReplayRenewalAndExpiry(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	id := setupPlan(t, s, "replay")
	lease, err := s.LeasePlan(ctx, "replay", JobTTL)
	if err != nil {
		t.Fatal(err)
	}
	renewed, err := s.Renew(ctx, "replay", id, lease.LeaseToken, false)
	if err != nil || renewed.ExpiresAt.Before(lease.ExpiresAt) {
		t.Fatalf("renewal: %+v %v", renewed, err)
	}
	if err = s.StartRun(ctx, "replay", "run-"+id, id, lease.LeaseToken, protocol.ExecutionModeLima, nil); err != nil {
		t.Fatal(err)
	}
	if err = s.StartRun(ctx, "replay", "run-"+id, id, lease.LeaseToken, protocol.ExecutionModeLima, nil); err != nil {
		t.Fatal("start replay", err)
	}
	result := protocol.Result{RunID: "run-" + id, Status: protocol.RunStatusCompleted, RuntimeMetadata: map[string]string{"recovered": "true"}, Cases: []protocol.CaseResult{{CaseID: "one", Threads: 1, GenerationTokens: 128, Repetitions: 3, TokensPerSecond: 20}}}
	for i := 0; i < 2; i++ {
		if err = s.CompleteRun(ctx, "replay", id, lease.LeaseToken, result); err != nil {
			t.Fatal("completion replay", err)
		}
	}
	var count int
	listed, listErr := s.ListResults(ctx, "replay")
	if listErr != nil || len(listed) != 1 || !listed[0].Recovered {
		t.Fatalf("recovery metadata not readable: %+v %v", listed, listErr)
	}
	if err = s.pool.QueryRow(ctx, `SELECT count(*) FROM benchmark_cases WHERE run_id=$1`, result.RunID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate cases: %d %v", count, err)
	}
	expired := setupPlan(t, s, "expired")
	old, err := s.LeasePlan(ctx, "expired", JobTTL)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.StartRun(ctx, "expired", "run-"+expired, expired, old.LeaseToken, protocol.ExecutionModeLima, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, `UPDATE benchmark_plans SET lease_expires_at=now()-interval '1 second' WHERE plan_id=$1`, expired); err != nil {
		t.Fatal(err)
	}
	if err = s.ReapExpired(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteRun(ctx, "expired", expired, old.LeaseToken, protocol.Result{RunID: "run-" + expired, Status: protocol.RunStatusCompleted}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale result accepted: %v", err)
	}
}
func TestCancellationAndInstallOwnership(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	id := setupPlan(t, s, "cancel")
	lease, err := s.LeasePlan(ctx, "cancel", JobTTL)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.StartRun(ctx, "cancel", "run-"+id, id, lease.LeaseToken, protocol.ExecutionModeLima, nil); err != nil {
		t.Fatal(err)
	}
	if err = s.Cancel(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	renewal, err := s.Renew(ctx, "cancel", id, lease.LeaseToken, false)
	if err != nil || !renewal.Cancelled {
		t.Fatalf("cancellation not observed: %+v %v", renewal, err)
	}
	if err = s.CompleteRun(ctx, "cancel", id, lease.LeaseToken, protocol.Result{RunID: "run-" + id, Status: protocol.RunStatusCancelled}); err != nil {
		t.Fatal(err)
	}
	jobID, err := s.QueueInstall(ctx, InstallJob{NodeID: "cancel", ModelID: "test", SourceURL: "https://example.com/model.gguf"})
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.LeaseInstall(ctx, "cancel")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteInstall(ctx, "wrong-node", jobID, job.LeaseToken, ""); !errors.Is(err, ErrConflict) {
		t.Fatal("wrong owner accepted", err)
	}
	for i := 0; i < 2; i++ {
		if err = s.CompleteInstall(ctx, "cancel", jobID, job.LeaseToken, ""); err != nil {
			t.Fatal(err)
		}
	}
}
