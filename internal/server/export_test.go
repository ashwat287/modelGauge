package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

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
