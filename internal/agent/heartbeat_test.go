package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
