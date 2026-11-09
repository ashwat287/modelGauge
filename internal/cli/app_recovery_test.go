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
	"time"

	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

func TestCancellationReachesReadRequests(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	var out, stderr bytes.Buffer
	go func() {
		done <- RunContext(ctx, []string{"model", "list", "--node", "test", "--controller", server.URL, "--admin-token", "private"}, &out, &stderr)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case code := <-done:
		if code != 130 {
			t.Fatalf("cancel returned %d: %s", code, &stderr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read request ignored cancellation")
	}
}
func TestFailureResponseRedactsCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "private-secret https://example.test/file?token=hidden", 500)
	}))
	defer server.Close()
	var out, stderr bytes.Buffer
	code := RunContext(context.Background(), []string{"benchmark", "cancel", "test", "--controller", server.URL, "--admin-token", "private-secret"}, &out, &stderr)
	if code == 0 {
		t.Fatal("failure reported success")
	}
	if strings.Contains(stderr.String(), "private-secret") || strings.Contains(stderr.String(), "token=hidden") {
		t.Fatal("credential in failure response")
	}
	if !strings.Contains(stderr.String(), "controller returned") {
		t.Fatal("original failure not reported")
	}
}
func TestCampaignSavesPartialSubmissionAndResumesSameRequest(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "campaign")
	submissions := map[string]string{}
	drop := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			_ = json.NewEncoder(w).Encode(map[string]string{"submission_protocol": "idempotent-v1"})
			return
		}
		if r.Method == "GET" {
			_ = json.NewEncoder(w).Encode([]catalogModel{{ID: "first", Path: "/models/first", SHA256: strings.Repeat("a", 64)}, {ID: "second", Path: "/models/second", SHA256: strings.Repeat("b", 64)}})
			return
		}
		var plan protocol.Plan
		_ = json.NewDecoder(r.Body).Decode(&plan)
		id := r.Header.Get("X-ModelGauge-Request-ID")
		if id == "" {
			t.Error("missing stable submission identity")
		}
		submissions[id] = plan.Model.ID
		if plan.Model.ID == "second" && drop {
			drop = false
			http.Error(w, "response lost after acceptance", 503)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"plan_id": "plan-" + plan.Model.ID, "status": "queued"})
	}))
	defer server.Close()
	args := []string{"benchmark", "campaign", "create", "--node", "node", "--models", "first,second", "--contexts", "512", "--threads", "1", "--output", directory, "--submit", "--controller", server.URL, "--admin-token", "secret"}
	var out, stderr bytes.Buffer
	if code := RunContext(context.Background(), args, &out, &stderr); code == 0 {
		t.Fatal("submission failure hidden")
	}
	raw, err := os.ReadFile(filepath.Join(directory, "campaign.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved campaignManifest
	_ = json.Unmarshal(raw, &saved)
	if saved.Plans[0].PlanID != "plan-first" || saved.Plans[1].RequestID == "" {
		t.Fatal("accepted plan or pending request lost")
	}
	out.Reset()
	stderr.Reset()
	args = append(args, "--resume")
	if code := RunContext(context.Background(), args, &out, &stderr); code != 0 {
		t.Fatalf("resume: %s", &stderr)
	}
	if len(submissions) != 2 {
		t.Fatal("resume used a new request identity")
	}
	if _, err = os.Stat(filepath.Join(directory, "INCOMPLETE")); !os.IsNotExist(err) {
		t.Fatal("completed campaign remains incomplete")
	}
}
