package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExperimentEndpointsRequireAdminAndRejectInvalidJSON(t *testing.T) {
	s := New(nil, Config{AdminToken: "secret"})
	for _, route := range []struct{ method, path string }{{"POST", "/v1/experiments"}, {"GET", "/v1/experiments/test"}, {"POST", "/v1/experiments/test/cancel"}} {
		r := httptest.NewRequest(route.method, route.path, strings.NewReader(`{}`))
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatal(route, w.Code)
		}
	}
	for _, body := range []string{`{"unknown":true}`, `not-json`, `{}`} {
		r := httptest.NewRequest("POST", "/v1/experiments", strings.NewReader(body))
		r.Header.Set("X-ModelGauge-Admin-Token", "secret")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
