package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nerdylucifer/modelgauge/internal/client"
)

func TestHeartbeatBusyAndStop(t *testing.T) {
	updates := make(chan string, 100)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/nodes/test-node/heartbeat" || r.Header.Get("X-ModelGauge-Token") != "secret" {
			t.Error("incorrect heartbeat route or authentication")
		}
		var payload heartbeatRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		updates <- payload.Status
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	var busy atomic.Bool
	busy.Store(true)
	stop := keepHeartbeat(context.Background(), &client.Client{BaseURL: server.URL, AgentToken: "secret"}, "test-node", &busy, 10*time.Millisecond)
	defer stop()
	select {
	case status := <-updates:
		if status != "busy" {
			t.Fatalf("got %s, want busy", status)
		}
	case <-time.After(time.Second):
		t.Fatal("no busy heartbeat during workload")
	}
	busy.Store(false)
	deadline := time.After(time.Second)
	for {
		select {
		case status := <-updates:
			if status == "online" {
				stop()
				for len(updates) > 0 {
					<-updates
				}
				select {
				case <-updates:
					t.Fatal("heartbeat continued after stop")
				case <-time.After(30 * time.Millisecond):
					return
				}
			}
		case <-deadline:
			t.Fatal("no online heartbeat after workload")
		}
	}
}

func TestUnreachableNodeIsMarkedOfflineBeforeRetry(t *testing.T) {
	connection, _ := testSSHNode(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	connection.Address = listener.Addr().String()
	listener.Close()
	updates := make(chan string, 4)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/heartbeat") {
			var heartbeat heartbeatRequest
			if err := json.NewDecoder(r.Body).Decode(&heartbeat); err != nil {
				t.Error(err)
			}
			updates <- heartbeat.Status
		}
		w.WriteHeader(204)
	}))
	defer api.Close()
	_, err = RunOnce(context.Background(), Config{SSH: &connection, NodeID: "node", Token: "token", ControllerURL: api.URL, ResultsDir: t.TempDir()})
	if !errors.Is(err, errWorkerTransport) {
		t.Fatal(err)
	}
	if len(updates) != 1 {
		t.Fatal("missing offline heartbeat", len(updates))
	}
	if state := <-updates; state != "offline" {
		t.Fatal("unreachable node reported online", state)
	}
}
