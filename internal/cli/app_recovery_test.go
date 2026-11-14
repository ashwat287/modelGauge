package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
