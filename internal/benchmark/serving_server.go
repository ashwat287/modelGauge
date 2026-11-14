package benchmark

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nerdylucifer/modelgauge/internal/modelcache"
	"github.com/nerdylucifer/modelgauge/internal/platform"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

type modelServer struct {
	cmd       *exec.Cmd
	done      chan struct{}
	err       error
	log       *os.File
	startedAt time.Time
	stopOnce  sync.Once
	stopErr   error
}

func serverBinary(configured string) (string, error) {
	if configured != "" {
		return exec.LookPath(configured)
	}
	if bench, err := modelcache.DiscoverLlamaBench(); err == nil {
		candidate := filepath.Join(filepath.Dir(bench), "llama-server")
		if binary, err := exec.LookPath(candidate); err == nil {
			return binary, nil
		}
	}
	if binary, err := exec.LookPath("llama-server"); err == nil {
		return binary, nil
	}
	return "", errors.New("llama-server is missing on the benchmark node; rerun node bootstrap to install the model runtime")
}

// Only the process group started here is stopped; existing model services are untouched.
func startModelServer(ctx context.Context, plan protocol.Plan, serving *servingPlan, dir string) (*modelServer, error) {
	if err := verifyModelContext(ctx, plan.Model); err != nil {
		return nil, err
	}
	binary, err := serverBinary(plan.Runtime.Binary)
	if err != nil {
		return nil, err
	}
	digest, err := platform.SHA256File(ctx, binary)
	if err != nil {
		return nil, err
	}
	if err := platform.WriteFile(filepath.Join(dir, "runtime.sha256"), []byte(digest+"\n"), 0600); err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return nil, err
	}
	settings := serving.Server
	args := []string{"--model", plan.Model.Path, "--alias", plan.Model.ID, "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--threads", strconv.Itoa(settings.Threads), "--threads-batch", strconv.Itoa(settings.Threads), "--ctx-size", strconv.Itoa(settings.ContextTokens * settings.Parallel), "--parallel", strconv.Itoa(settings.Parallel), "--n-gpu-layers", strconv.Itoa(settings.GPULayers), "--cache-ram", "0", "--fit", "off", "--metrics"}
	log, err := os.OpenFile(filepath.Join(dir, "model-server.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	cmd := platform.CommandContext(ctx, binary, args...)
	// Keep node credentials and unrelated llama settings out of the model process.
	cmd.Env = []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "MODELGAUGE_") && !strings.HasPrefix(entry, "LLAMA_ARG_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	serverParentDeath(cmd)
	writer := &modelServerLog{file: log}
	cmd.Stdout, cmd.Stderr = writer, writer
	server := &modelServer{cmd: cmd, done: make(chan struct{}), log: log}
	started := make(chan error, 1)
	go func() {
		// Linux parent-death signaling follows the creating OS thread.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		server.startedAt = time.Now()
		err := cmd.Start()
		started <- err
		if err == nil {
			err = cmd.Wait()
		}
		server.err = err
		close(server.done)
	}()
	if err := <-started; err != nil {
		return nil, errors.Join(err, log.Close())
	}
	serving.Endpoint = "http://127.0.0.1:" + strconv.Itoa(port)
	serving.MetricsURL = serving.Endpoint + "/metrics"
	serving.LocalPID = cmd.Process.Pid
	serving.RuntimeSettings = map[string]string{"threads": strconv.Itoa(settings.Threads), "threads_batch": strconv.Itoa(settings.Threads), "context_capacity": strconv.Itoa(settings.ContextTokens), "parallel_slots": strconv.Itoa(settings.Parallel), "gpu_layers": strconv.Itoa(settings.GPULayers), "model_sha256": plan.Model.SHA256}
	return server, nil
}

func (s *modelServer) running() error {
	select {
	case <-s.done:
		return fmt.Errorf("model process exited unexpectedly: %v; inspect model-server.log", s.err)
	default:
		return nil
	}
}

func (s *modelServer) stop() error {
	s.stopOnce.Do(func() {
		select {
		case <-s.done:
		default:
			_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGTERM)
			timer := time.NewTimer(3 * time.Second)
			defer timer.Stop()
			select {
			case <-s.done:
			case <-timer.C:
				_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
				<-s.done
			}
		}
		s.stopErr = errors.Join(s.log.Sync(), s.log.Close())
	})
	return s.stopErr
}

type modelServerLog struct {
	mu      sync.Mutex
	file    *os.File
	written int
}

func (w *modelServerLog) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(data)
	if available := (8 << 20) - w.written; available > 0 {
		if len(data) > available {
			data = data[:available]
		}
		written, err := w.file.Write(data)
		w.written += written
		if err != nil {
			return written, err
		}
	}
	return n, nil
}
