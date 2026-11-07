package control

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nerdylucifer/modelgauge/internal/guest"
)

func TestGoHelperTransport(t *testing.T) {
	vm := ownedVM{Name: "node", ID: strings.Repeat("a", 32), HelperSHA256: strings.Repeat("b", 64)}
	payload := []byte(`{"operation":"prepare"}`)
	args, input, err := helperInvocation(vm, "prepare", payload, []byte("linux executable"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(args[len(args)-1], "mktemp -d") {
		t.Fatal("initial helper is not staged privately")
	}
	reader := tar.NewReader(bytes.NewReader(input))
	for _, want := range []struct {
		name, body string
		mode       int64
	}{{"modelgauge", "linux executable", 0700}, {"request.json", string(payload), 0600}} {
		header, err := reader.Next()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if header.Name != want.name || header.Mode != want.mode || string(data) != want.body {
			t.Fatalf("unexpected helper envelope: %+v", header)
		}
	}
	if _, err := reader.Next(); err != io.EOF {
		t.Fatal("unexpected envelope member")
	}
	vm.Prepared = true
	args, input, err = helperInvocation(vm, "inspect", payload, nil)
	if err != nil {
		t.Fatal(err)
	}
	script := args[len(args)-1]
	if !bytes.Equal(input, payload) || strings.Contains(script, "mktemp") || !strings.Contains(script, vm.HelperSHA256) || !strings.Contains(script, "_guest ownership") {
		t.Fatal("inspection must use verified installed Go helper without staging")
	}
}

func TestCleanupRefusesChangedGoHelpers(t *testing.T) {
	for _, location := range []string{"host", "guest"} {
		t.Run(location, func(t *testing.T) {
			m, f := fixture(t)
			g := prepareTestVM(t, m, f, "owned", "benchmark")
			registry, err := m.load()
			if err != nil {
				t.Fatal(err)
			}
			path := helperPath(registry.VMs[0])
			if location == "guest" {
				path = filepath.Join(g.home, guest.HelperRelative)
			}
			if err = os.WriteFile(path, []byte("replacement"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = m.cleanup(context.Background(), CleanupOptions{DeleteVMs: true}); err == nil {
				t.Fatal("changed helper accepted")
			}
			if f.guests["owned"] == nil || !exists(filepath.Join(g.home, ".config/modelgauge")) {
				t.Fatal("cleanup removed resources after failed verification")
			}
		})
	}
}

func TestRebootstrapUpdatesGoHelper(t *testing.T) {
	m, f := fixture(t)
	g := prepareTestVM(t, m, f, "owned", "benchmark")
	m.binary = func(context.Context, string) ([]byte, error) { return []byte("new Go executable"), nil }
	prepareTestVM(t, m, f, "owned", "benchmark")
	var state guest.State
	data, err := os.ReadFile(filepath.Join(g.home, ".local/share/modelgauge/.ownership.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if state.HelperSHA256 != helperSum([]byte("new Go executable")) {
		t.Fatal("guest helper did not upgrade")
	}
	if _, err = m.cleanup(context.Background(), CleanupOptions{}); err != nil {
		t.Fatal(err)
	}
}
