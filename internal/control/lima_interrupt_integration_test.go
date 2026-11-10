package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Use a cold, isolated Lima home and a local failing/blocking download proxy.
// This exercises real Lima creation without downloading an image or booting a VM.
func TestLimaImageDownloadFailureRollback(t *testing.T) {
	executable := os.Getenv("MODELGAUGE_TEST_LIMA_CLI")
	if executable == "" {
		t.Skip("requires explicit disposable-VM validation with a built host CLI")
	}
	for _, interrupted := range []bool{false, true} {
		t.Run(fmt.Sprintf("interrupted=%t", interrupted), func(t *testing.T) {
			root, err := os.MkdirTemp("/tmp", "mg-create-check-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			home := filepath.Join(root, "home")
			if err = os.Mkdir(home, 0700); err != nil {
				t.Fatal(err)
			}
			download := make(chan struct{}, 1)
			release := make(chan struct{})
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case download <- struct{}{}:
				default:
				}
				if interrupted {
					select {
					case <-release:
					case <-r.Context().Done():
					}
				}
				http.Error(w, "test image download failure", http.StatusBadGateway)
			}))
			defer proxy.Close()
			defer close(release)
			env := []string{}
			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				switch strings.ToUpper(key) {
				case "HOME", "LIMA_HOME", "XDG_CACHE_HOME", "HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "NO_PROXY":
					continue
				}
				env = append(env, entry)
			}
			env = append(env, "HOME="+home, "LIMA_HOME="+filepath.Join(root, "lima"), "XDG_CACHE_HOME="+filepath.Join(root, "cache"), "HTTPS_PROXY="+proxy.URL, "HTTP_PROXY="+proxy.URL, "NO_PROXY=")
			source, err := filepath.Abs("../..")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "control", "bootstrap", "--provider", "lima", "--name", "mg-create-check", "--source", source, "--cpus", "2", "--memory", "3", "--disk", "16", "--port", "18089")
			output := &bootstrapProgress{ready: make(chan struct{}, 1)}
			cmd.Env, cmd.Stdout, cmd.Stderr = env, io.Discard, output
			cmd.WaitDelay = 5 * time.Second
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			stopped := false
			defer func() {
				if !stopped {
					_ = cmd.Process.Kill()
					<-done
				}
			}()
			select {
			case <-download:
				if interrupted {
					if err = cmd.Process.Signal(os.Interrupt); err != nil {
						t.Fatal(err)
					}
				}
			case <-done:
				stopped = true
				t.Fatalf("exited before image download: %s", output)
			case <-ctx.Done():
				t.Fatalf("never attempted image download: %s", output)
			}
			select {
			case <-done:
				stopped = true
			case <-ctx.Done():
				t.Fatalf("creation rollback timed out: %s", output)
			}
			want := 1
			if interrupted {
				want = 130
			}
			if cmd.ProcessState.ExitCode() != want || !strings.Contains(output.String(), "rollback completed:") {
				t.Fatalf("exit=%d: %s", cmd.ProcessState.ExitCode(), output)
			}
			if _, err = os.Lstat(filepath.Join(root, "lima", "mg-create-check")); !os.IsNotExist(err) {
				t.Fatalf("partial instance directory remains: %v", err)
			}
			data, err := os.ReadFile(filepath.Join(home, ".config", "modelgauge", "ownership.json"))
			var registry ownershipRegistry
			if err != nil || json.Unmarshal(data, &registry) != nil || len(registry.VMs) != 0 {
				t.Fatalf("creation ownership was not retired: %s, %v", data, err)
			}
			t.Logf("image-download failure exit %d removed its partial VM", want)
		})
	}
}

// Opt-in host test: creates its own disposable VM and interrupts before k3s.
// Observe normal bootstrap progress and the actual VM, not a recovery journal.
func TestLimaFreshBootstrapInterruptRollback(t *testing.T) {
	executable := os.Getenv("MODELGAUGE_TEST_LIMA_CLI")
	if executable == "" {
		t.Skip("requires explicit disposable-VM validation with a built host CLI")
	}
	id, err := newScopeID()
	if err != nil {
		t.Fatal(err)
	}
	name := "mg-interrupt-check-" + id[:12]
	source, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "control", "bootstrap", "--provider", "lima", "--name", name, "--source", source, "--cpus", "2", "--memory", "3", "--disk", "16", "--port", "18089", "--timeout", "4m")
	cmd.Stdout = io.Discard
	output := &bootstrapProgress{ready: make(chan struct{}, 1)}
	cmd.Stderr = output
	cmd.WaitDelay = 5 * time.Second
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	stopped := false
	defer func() {
		if !stopped {
			_ = cmd.Process.Kill()
			<-done
		}
	}()
	select {
	case <-output.ready:
		if err = cmd.Process.Signal(os.Interrupt); err != nil {
			t.Fatal(err)
		}
	case <-done:
		stopped = true
		t.Fatalf("bootstrap exited before interruption: %s", output)
	case <-ctx.Done():
		t.Fatal("bootstrap did not prepare the VM")
	}
	select {
	case <-done:
		stopped = true
	case <-ctx.Done():
		t.Fatal("rollback did not finish")
	}
	if cmd.ProcessState.ExitCode() != 130 || !strings.Contains(output.String(), "rollback completed:") {
		t.Fatalf("exit=%d: %s", cmd.ProcessState.ExitCode(), output)
	}
	data, err := exec.Command("limactl", "list", "--json").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var instance limaInstance
		if json.Unmarshal([]byte(line), &instance) == nil && instance.Name == name {
			t.Fatalf("new VM remains after rollback: %s", name)
		}
	}
	t.Logf("interrupted bootstrap removed only its new VM %s", name)
}

type bootstrapProgress struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	ready  chan struct{}
}

func (p *bootstrapProgress) Write(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n, err := p.buffer.Write(data)
	if strings.Contains(p.buffer.String(), "Installing k3s...") {
		select {
		case p.ready <- struct{}{}:
		default:
		}
	}
	return n, err
}
func (p *bootstrapProgress) String() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.buffer.String()
}
