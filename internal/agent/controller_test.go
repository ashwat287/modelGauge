package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestControllerExecutorPersistsConnectionsAndPauseAcrossRestart(t *testing.T) {
	connection, _ := testSSHNode(t)
	registered := make(chan struct{}, 30)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/nodes/register" {
			registered <- struct{}{}
		}
		w.WriteHeader(204)
	}))
	defer api.Close()
	directory := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e, err := NewControllerExecutor(ctx, directory, api.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err = e.Set("node", connection); err != nil {
		t.Fatal(err)
	}
	select {
	case <-registered:
	case <-time.After(3 * time.Second):
		t.Fatal("controller did not run executor")
	}
	file := filepath.Join(directory, "connections/node.json")
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	status, err := e.Status("node")
	if err != nil || status["location"] != "controller" || status["configured"] != true {
		t.Fatal(status, err)
	}
	if err = e.Pause("node"); err != nil {
		t.Fatal(err)
	}
	e.Close()
	restarted, err := NewControllerExecutor(ctx, directory, api.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	status, _ = restarted.Status("node")
	if status["configured"] != false {
		t.Fatal("paused node restarted", status)
	}
	if err = restarted.Resume("node"); err != nil {
		t.Fatal(err)
	}
	status, _ = restarted.Status("node")
	if status["configured"] != true {
		t.Fatal("resume failed", status)
	}
	if err = os.MkdirAll(restarted.state("node"), 0700); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(restarted.state("node"), ".pending.json")
	// Pausing must never discard a pending plan or upload.
	if err = os.WriteFile(journal, []byte(`{"kind":"benchmark"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err = restarted.Pause("node"); err == nil {
		t.Fatal("pending work was paused")
	}
	if err = restarted.Remove("node"); err == nil {
		t.Fatal("pending work was removed")
	}
	data, _ := os.ReadFile(file)
	var saved SSHConnection
	if err = json.Unmarshal(data, &saved); err != nil || saved != connection {
		t.Fatal("connection lost", err)
	}
	if err = os.Remove(journal); err != nil {
		t.Fatal(err)
	}
	if err = restarted.Remove("node"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("connection retained", err)
	}
	if err = e.Set("node", connection); err == nil {
		t.Fatal("closed executor accepted work")
	}
}

func TestExecutorStatusExplainsFailuresWithoutExposingCredentials(t *testing.T) {
	connection, _ := testSSHNode(t)
	const token = "private-agent-token"
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "denied "+token, 403) }))
	defer api.Close()
	e, err := NewControllerExecutor(context.Background(), t.TempDir(), api.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err = e.Set("node", connection); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status, err := e.Status("node")
		if err != nil {
			t.Fatal(err)
		}
		if text, _ := status["last_error"].(string); text != "" {
			data, _ := json.Marshal(status)
			if strings.Contains(string(data), token) || strings.Contains(string(data), "private_key") || strings.Contains(string(data), connection.PrivateKey) {
				t.Fatal("status leaked credentials")
			}
			if status["address"] != connection.Address || status["last_attempt_at"] == nil {
				t.Fatal("missing connection diagnosis", status)
			}
			if !strings.Contains(text, "[REDACTED]") {
				t.Fatal("failure not reported", status)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("connection failure absent from status")
}
