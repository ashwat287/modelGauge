package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/nerdylucifer/modelgauge/internal/client"
	"github.com/nerdylucifer/modelgauge/internal/platform"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

func TestRecoverSavedResultWithoutExecutingModel(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/nodes/test/runs/run-test-plan/complete" {
			t.Errorf("recovery tried executing or renewing completed work: %s", r.URL.Path)
			w.WriteHeader(400)
			return
		}
		var payload completeRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.Result.RunID != "run-test-plan" {
			t.Error("wrong saved result")
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	c := Config{NodeID: "test", ResultsDir: t.TempDir()}
	p := pendingJobForPlan(lease{PlanID: "test-plan", LeaseToken: "token", Specification: protocol.Plan{NodeID: "test"}})
	if err := savePending(c, &p); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(c.ResultsDir, "run-test-plan")
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	if err := platform.WriteJSON(filepath.Join(output, "result.json"), protocol.Result{RunID: "run-test-plan", Status: protocol.RunStatusCompleted}, 0600); err != nil {
		t.Fatal(err)
	}
	recovered, err := loadPending(c)
	if err != nil {
		t.Fatal(err)
	}
	if err = processPending(context.Background(), &client.Client{BaseURL: server.URL}, c, recovered); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("retry count %d", calls.Load())
	}
	if _, err = os.Stat(pendingPath(c)); !os.IsNotExist(err) {
		t.Fatal("journal not acknowledged", err)
	}
}
