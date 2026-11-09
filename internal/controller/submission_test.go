package controller

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

func TestSubmissionReplayIsTransactionalAndRejectsChangedContent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.RegisterNode(ctx, Node{ID: "test", DisplayName: "test", Kind: "virtual", ExecutionMode: protocol.ExecutionModeLima, Hardware: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	j := InstallJob{NodeID: "test", ModelID: "model", SourceURL: "https://example.test/model", SHA256: strings.Repeat("a", 64)}
	key := strings.Repeat("b", 64)
	var wg sync.WaitGroup
	results := make(chan string, 2)
	failures := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); id, err := s.QueueInstallRequest(ctx, j, key); results <- id; failures <- err }()
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	var original string
	for id := range results {
		if original != "" && id != original {
			t.Fatal("duplicate job created")
		}
		original = id
	}
	var count int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM model_install_jobs`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	j.ModelID = "replacement"
	if _, err := s.QueueInstallRequest(ctx, j, key); !errors.Is(err, ErrConflict) {
		t.Fatal("changed request accepted:", err)
	}
}
