package server

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nerdylucifer/modelgauge/internal/agent"
)

func TestExecutorConnectionsRequireAdminAndDoNotExposeCredentials(t *testing.T) {
	executor, err := agent.NewControllerExecutor(context.Background(), t.TempDir(), "http://127.0.0.1:1", "agent-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer executor.Close()
	api := New(nil, Config{AdminToken: "admin", AgentToken: "agent-secret", Executor: executor}).Handler()
	for _, test := range []struct {
		method, token, body string
		want                int
	}{{"GET", "", "", 401}, {"GET", "agent-secret", "", 401}, {"GET", "admin", "", 200}, {"PUT", "admin", `{"address":"x","private_key":"secret"}`, 400}, {"DELETE", "admin", "", 200}} {
		req := httptest.NewRequest(test.method, "/v1/nodes/node/connection", strings.NewReader(test.body))
		req.Header.Set("X-ModelGauge-Admin-Token", test.token)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)
		if w.Code != test.want {
			t.Fatal(test, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "agent-secret") || strings.Contains(w.Body.String(), "private_key") {
			t.Fatal("credentials leaked", w.Body.String())
		}
	}
}
