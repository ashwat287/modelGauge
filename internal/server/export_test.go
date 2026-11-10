package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInvalidServingCompletionIsRejectedBeforeDatabaseAccess(t *testing.T) {
	s := New(nil, Config{AgentToken: "agent-secret"})
	request := httptest.NewRequest("POST", "/v1/nodes/test/runs/run-test/complete", strings.NewReader(`{"plan_id":"plan","lease_token":"token","result":{"run_id":"run-test","status":"completed","serving":{"schema_version":"wrong","endpoint":"http://localhost:8080"}}}`))
	request.Header.Set("X-ModelGauge-Token", "agent-secret")
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	if response.Code != 400 {
		t.Fatal(response.Code, response.Body.String())
	}
}

func TestExportRequiresAdminNotAgentCredentials(t *testing.T) {
	s := New(nil, Config{AdminToken: "admin-secret", AgentToken: "agent-secret"})
	request := httptest.NewRequest("GET", "/v1/nodes/example/export", nil)
	request.Header.Set("X-ModelGauge-Token", "agent-secret")
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatal("export must require admin auth:", response.Code)
	}
}
