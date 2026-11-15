package control

import (
	"archive/tar"
	"bytes"
	"context"
	"debug/elf"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestLinuxRuntimeSelection(t *testing.T) {
	for arch, machine := range map[string]elf.Machine{"arm64": elf.EM_AARCH64, "amd64": elf.EM_X86_64} {
		data, err := nodeBinary(arch)
		if err != nil {
			if !strings.Contains(err.Error(), "make build or make install") {
				t.Fatal(err)
			}
			// Plain Go test builds intentionally omit payloads; make test checks both.
			if _, err = Bootstrap(context.Background(), Options{}); err == nil || !strings.Contains(err.Error(), "not bundled") {
				t.Fatal("unbundled CLI attempted provisioning", err)
			}
			continue
		}
		binary, err := elf.NewFile(bytes.NewReader(data))
		if err != nil {
			t.Fatal(arch, err)
		}
		if binary.Machine != machine || binary.Entry == 0 {
			t.Fatal("wrong embedded Linux executable", arch, binary.FileHeader)
		}
		binary.Close()
	}
	if _, err := nodeBinary("unsupported"); err == nil {
		t.Fatal("unsupported architecture accepted")
	}
}

func TestInstalledCLISetupWithoutCheckoutOrGo(t *testing.T) {
	built := os.Getenv("MODELGAUGE_TEST_INSTALLED_CLI")
	if built == "" {
		t.Skip("make test supplies the bundled host CLI")
	}
	root := t.TempDir()
	directory := filepath.Join(root, "bin")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(built)
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(directory, "modelgauge")
	if err = os.WriteFile(executable, data, 0755); err != nil {
		t.Fatal(err)
	}
	// Stop at VM creation: prove setup reached Lima without touching real VMs.
	fakeLima := "#!/bin/sh\ncase \"$1\" in\nlist) exit 0 ;;\ncreate) printf '%s' 'creation reached without source or Go' >&2; exit 1 ;;\n*) exit 99 ;;\nesac\n"
	if err = os.WriteFile(filepath.Join(directory, "limactl"), []byte(fakeLima), 0755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "setup")
	command.Dir = root
	command.Env = append(os.Environ(), "HOME="+root, "LIMA_HOME="+filepath.Join(root, "lima"), "PATH="+directory)
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "creation reached without source or Go") || !strings.Contains(string(output), "rollback completed") {
		t.Fatalf("standalone setup did not reach Lima and recover: %v\n%s", err, output)
	}
}
