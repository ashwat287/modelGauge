package control

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nerdylucifer/modelgauge/internal/agent"
	"golang.org/x/crypto/ssh"
)

func TestReconnectUsesCurrentPortAndLeavesModelsAlone(t *testing.T) {
	m, f := fixture(t)
	prepareTestVM(t, m, f, "node", "benchmark")
	registry, _ := m.load()
	v := registry.VMs[0]
	f.guests["node"].instance.SSHLocalPort = 56487
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	keyText := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
	original := m.run
	commands := []string{}
	m.run = func(ctx context.Context, args []string, input []byte) ([]byte, error) {
		if args[0] != "shell" {
			return original(ctx, args, input)
		}
		command := strings.Join(args[5:], " ")
		commands = append(commands, command)
		switch {
		case strings.Contains(command, "getent"):
			return []byte("192.168.5.2"), nil
		case command == "id -un":
			return []byte("benchmark"), nil
		case strings.Contains(command, "ssh_host_ed25519"):
			return []byte(keyText), nil
		case args[5] == "ssh-keyscan":
			if !strings.Contains(command, "56487") {
				t.Fatal("used a stale SSH port", command)
			}
			return []byte("[host]:56487 " + keyText + "\n"), nil
		case strings.Contains(command, "controller-key"):
			return nil, nil
		default:
			return original(ctx, args, input)
		}
	}
	actions := []string{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-ModelGauge-Admin-Token") != "admin" {
			t.Error("missing admin authentication")
		}
		actions = append(actions, r.Method+" "+r.URL.Path)
		if r.Method == "PUT" {
			var c agent.SSHConnection
			if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
				t.Error(err)
			}
			if c.Address != "192.168.5.2:56487" || c.HostKey != keyText || c.Validate() != nil {
				t.Error("invalid refreshed connection", c.Address)
			}
		}
		if r.Method == "GET" {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "online", "last_seen_at": time.Now().UTC().Add(time.Second)})
			return
		}
		w.WriteHeader(200)
	}))
	defer api.Close()
	if err = m.reconnectNode(context.Background(), Config{VM: "control", ControllerURL: api.URL, AdminToken: "admin"}, "node"); err != nil {
		t.Fatal(err)
	}
	if len(actions) != 2 || actions[0] != "POST /v1/nodes/node/connection/pause" || actions[1] != "PUT /v1/nodes/node/connection" {
		t.Fatal(actions)
	}
	for _, cmd := range commands {
		if strings.Contains(cmd, "cmake") || strings.Contains(cmd, "apt-get") || strings.Contains(cmd, "model install") {
			t.Fatal("reconnect rebuilt or installed something", cmd)
		}
	}
	instances, err := m.instances(context.Background())
	if err != nil || verifyVM(v, instances["node"]) != nil {
		t.Fatal("reconnect changed VM identity", err)
	}
	// Host-key changes fail before replacing the saved executor credential.
	goodRun := m.run
	m.run = func(ctx context.Context, args []string, input []byte) ([]byte, error) {
		if len(args) > 5 && args[5] == "ssh-keyscan" {
			return []byte("invalid key"), nil
		}
		return goodRun(ctx, args, input)
	}
	if err = m.reconnectNode(context.Background(), Config{VM: "control", ControllerURL: api.URL, AdminToken: "admin"}, "node"); err == nil {
		t.Fatal("broken SSH path accepted")
	}
}

func TestNewBenchmarkVMHasPersistentSSHPort(t *testing.T) {
	m, f := fixture(t)
	ctx, finish, err := m.provision(context.Background(), "stable", "benchmark", 2, 3, 30, 0, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	var bootstrapErr error
	completeProvision(ctx, finish, &bootstrapErr)
	if bootstrapErr != nil {
		t.Fatal(bootstrapErr)
	}
	found := false
	for _, args := range f.calls {
		if args[0] == "create" {
			for i, arg := range args {
				if arg == "--set" && strings.HasPrefix(args[i+1], ".ssh.localPort = ") {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("Lima port left dynamically assigned")
	}
	f.calls = nil
	ctx, finish, err = m.provision(context.Background(), "stable", "benchmark", 2, 3, 30, 0, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	completeProvision(ctx, finish, &bootstrapErr)
	for _, args := range f.calls {
		if args[0] == "create" || args[0] == "delete" {
			t.Fatal("reuse recreated VM", args)
		}
	}
}

func TestWorkspaceRefusesASecondVMForTheSameRole(t *testing.T) {
	m, f := fixture(t)
	prepareTestVM(t, m, f, "control", "control")
	prepareTestVM(t, m, f, "node", "benchmark")
	for _, role := range []string{"control", "benchmark"} {
		if _, _, err := m.provision(context.Background(), "another-"+role, role, 2, 3, 20, 18080, io.Discard); err == nil {
			t.Fatal("created a second", role)
		}
	}
	if len(f.guests) != 2 {
		t.Fatal("changed existing workspace")
	}
}
