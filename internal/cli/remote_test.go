package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nerdylucifer/modelgauge/internal/benchmark"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

func TestParseRemoteMacDestination(t *testing.T) {
	for _, value := range []string{"user@192.168.1.50", "user@mac.local:2222", "user@[::1]:22"} {
		user, address, err := parseRemoteHost(value)
		if err != nil || user != "user" || !strings.Contains(address, ":") {
			t.Fatal(user, address, err)
		}
	}
	for _, value := range []string{"mac.local", "@host", "user@", "user@host\ncommand", "user@::1"} {
		if _, _, err := parseRemoteHost(value); err == nil {
			t.Fatal("invalid destination accepted", value)
		}
	}
}
func TestRemoteSetupRejectsInvalidFlagsBeforeProvisioning(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, args := range [][]string{{"--remote-host", "missing-user"}, {"--identity", "key"}, {"--host-key", "SHA256:key"}, {"--remote-host", "user@mac", "--node-cpus", "8"}} {
		var out bytes.Buffer
		if code := runSetup(context.Background(), args, &out, &out); code != 2 {
			t.Fatal(args, code, out.String())
		}
	}
}
func TestTinyRemoteExamplePlansBothStages(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "examples", "remote-smoke.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	document, err := benchmark.ParseDocument(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := document.Plan(context.Background(), "benchmark-node", protocol.ExecutionModeLima, protocol.Model{ID: "smollm2-135m-instruct:q4_k_m", Path: "/models/tiny.gguf", SHA256: strings.Repeat("a", 64)})
	if err != nil || plan.Benchmark == nil || plan.Benchmark.Kind != "suite" {
		t.Fatal(plan, err)
	}
}
