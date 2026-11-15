package control

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestForwardRejectsOccupiedHostPortAndWaitsForOldListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	if requireForwardPortFree(context.Background(), port, false) == nil {
		t.Fatal("foreign listener accepted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if requireForwardPortFree(ctx, port, true) == nil {
		t.Fatal("occupied listener accepted after timeout")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := requireForwardPortFree(context.Background(), port, true); err != nil {
		t.Fatal(err)
	}
}

func TestForwardReadinessRejectsUnrelatedHealthAndRedirects(t *testing.T) {
	for _, tc := range []struct {
		service, body string
		want          bool
	}{
		{"modelgauge-controller", `{"status":"ok","active":false}`, true},
		{"modelgauge-controller", `{"status":"ok"}`, false},
		{"modelgauge-controller", `hello`, false},
		{"grafana", `{"database":"ok","version":"test"}`, true},
		{"grafana", `{"status":"ok"}`, false},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tc.body) }))
		if got := forwardHealthy(context.Background(), server.URL, tc.service); got != tc.want {
			t.Fatal(tc, got)
		}
		server.Close()
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("health redirect followed")
		fmt.Fprint(w, `{"database":"ok"}`)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer redirect.Close()
	if forwardHealthy(context.Background(), redirect.URL, "grafana") {
		t.Fatal("redirect accepted")
	}
}

func TestForwardReuseRequiresExactOwnedTarget(t *testing.T) {
	unit := "[Service]\nExecStart=/usr/local/bin/kubectl -n monitoring port-forward svc/grafana 3000:3000\n"
	if !matchingForward(unit, "monitoring", "grafana", 3000, 3000) {
		t.Fatal("healthy legacy forward cannot be reused")
	}
	for _, changed := range []string{
		strings.Replace(unit, "svc/grafana", "svc/other", 1),
		strings.Replace(unit, "3000:3000", "3000:9090", 1),
		strings.Replace(unit, "-n monitoring", "-n other", 1),
		strings.Replace(unit, "kubectl", "other", 1),
		strings.Replace(unit, "3000:3000", "3000:3000 --address=0.0.0.0", 1),
	} {
		if matchingForward(changed, "monitoring", "grafana", 3000, 3000) {
			t.Fatal("foreign/mismatched forward reused", changed)
		}
	}
}
