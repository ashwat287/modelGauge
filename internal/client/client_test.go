package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSubmissionRefusesControllerWithoutReplayProtocol(t *testing.T) {
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			posts++
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer server.Close()
	api := Client{BaseURL: server.URL, AdminToken: "secret"}
	var response Submission
	if _, err := api.SubmitJSON(context.Background(), "/v1/plans", strings.Repeat("a", 32), map[string]string{"plan": "test"}, &response); err == nil {
		t.Fatal("old controller accepted")
	}
	if posts != 0 {
		t.Fatal("unsafe submission reached old controller")
	}
}
func TestSubmissionIdentitySurvivesLostResponse(t *testing.T) {
	var keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			_ = json.NewEncoder(w).Encode(map[string]string{"submission_protocol": "idempotent-v1"})
			return
		}
		keys = append(keys, r.Header.Get("X-ModelGauge-Request-ID"))
		if len(keys) == 1 {
			http.Error(w, "lost response", 503)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"plan_id": "accepted", "status": "queued"})
	}))
	defer server.Close()
	id := strings.Repeat("a", 32)
	api := Client{BaseURL: server.URL, AdminToken: "secret"}
	var response Submission
	for attempt := 0; attempt < 2; attempt++ {
		_, err := api.SubmitJSON(context.Background(), "/v1/plans", id, map[string]string{"plan": "test"}, &response)
		if (err != nil) != (attempt == 0) {
			t.Fatalf("unexpected submission outcome on attempt %d: %v", attempt, err)
		}
	}
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Fatal("retry changed request ID")
	}
}
