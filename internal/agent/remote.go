package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/nerdylucifer/modelgauge/internal/benchmark"
	"github.com/nerdylucifer/modelgauge/internal/controller"
	"github.com/nerdylucifer/modelgauge/internal/platform"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

// workerRequest contains no controller credentials or controller-specific lease data.
type workerRequest struct {
	Operation string         `json:"operation"`
	RunID     string         `json:"run_id,omitempty"`
	Plan      *protocol.Plan `json:"plan,omitempty"`
	ModelID   string         `json:"model_id,omitempty"`
	SourceURL string         `json:"source_url,omitempty"`
	SHA256    string         `json:"sha256,omitempty"`
}
type workerResponse struct {
	Host        platform.Host          `json:"host,omitempty"`
	Models      []controller.NodeModel `json:"models,omitempty"`
	Result      protocol.Result        `json:"result,omitempty"`
	Error       string                 `json:"error,omitempty"`
	Interrupted bool                   `json:"interrupted,omitempty"`
}

var errWorkerTransport = errors.New("node connection interrupted")

// remoteWork uses an owned transient systemd unit. EOF or missing keepalives
// cancels the node worker even if this executor is killed or loses its connection.
func remoteWork(ctx context.Context, c Config, request workerRequest) (workerResponse, error) {
	var response workerResponse
	if c.SSH == nil && !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`).MatchString(c.TargetVM) {
		return response, errors.New("invalid target VM")
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return response, err
	}
	unit := "modelgauge-worker-" + hex.EncodeToString(random)
	if c.SSH != nil {
		return c.SSH.work(ctx, request, unit)
	}
	script := `exec systemd-run --user --quiet --pipe --wait --collect --unit=` + unit + ` --property=KillMode=control-group --property=TimeoutStopSec=5s --property=RuntimeMaxSec=24h -- "$HOME/.local/bin/modelgauge" _guest worker`
	command := platform.CommandContext(ctx, "limactl", "shell", "--workdir", "/", c.TargetVM, "--", "sh", "-c", script)
	writer, pipeErr := command.StdinPipe()
	if pipeErr != nil {
		return response, pipeErr
	}
	defer writer.Close()
	var stdout, stderr limitedBuffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		writer.Close()
		return response, fmt.Errorf("%w: %v", errWorkerTransport, err)
	}
	err := exchangeWorker(ctx, request, writer, command.Wait)
	if err != nil {
		// Stop only this invocation's unit; systemd waits for its entire cgroup.
		stopCtx, stop := context.WithTimeout(context.Background(), 15*time.Second)
		cleanup := platform.CommandContext(stopCtx, "limactl", "shell", "--workdir", "/", c.TargetVM, "--", "systemctl", "--user", "stop", unit+".service")
		cleanupErr := cleanup.Run()
		stop()
		return response, errors.Join(fmt.Errorf("%w: node worker: %v: %s", errWorkerTransport, err, stderr.String()), cleanupErr)
	}
	return decodeWorkerResponse(stdout.Bytes())
}
func decodeWorkerResponse(data []byte) (workerResponse, error) {
	var response workerResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return response, fmt.Errorf("%w: decode node worker response: %v", errWorkerTransport, err)
	}
	if response.Interrupted {
		return response, fmt.Errorf("%w: %s", errWorkerTransport, response.Error)
	}
	if response.Error != "" {
		return response, errors.New(response.Error)
	}
	return response, nil
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 16<<20 {
		return 0, errors.New("worker output exceeds 16 MiB")
	}
	return b.Buffer.Write(p)
}

func inspectTarget(ctx context.Context, c Config) (platform.Host, []controller.NodeModel, error) {
	if c.TargetVM != "" || c.SSH != nil {
		r, err := remoteWork(ctx, c, workerRequest{Operation: "inspect"})
		return r.Host, r.Models, err
	}
	return inspectLocal(ctx)
}
func installTarget(ctx context.Context, c Config, j *controller.InstallJob) error {
	if c.TargetVM == "" && c.SSH == nil {
		return installLocal(ctx, j)
	}
	_, err := remoteWork(ctx, c, workerRequest{Operation: "install", ModelID: j.ModelID, SourceURL: j.SourceURL, SHA256: j.SHA256})
	return err
}
func benchmarkTarget(ctx context.Context, c Config, p protocol.Plan, o benchmark.Options) (protocol.Result, string, error) {
	if c.TargetVM == "" && c.SSH == nil {
		return benchmark.Run(ctx, p, o)
	}
	r, err := remoteWork(ctx, c, workerRequest{Operation: "benchmark", RunID: o.RunID, Plan: &p})
	if ctx.Err() == nil && !errors.Is(err, errWorkerTransport) && r.Result.SchemaVersion != "" {
		if e := os.MkdirAll(o.OutputDir, 0700); e != nil {
			return r.Result, o.OutputDir, e
		}
		if e := platform.WriteJSON(filepath.Join(o.OutputDir, "result.json"), r.Result, 0600); e != nil {
			return r.Result, o.OutputDir, e
		}
	}
	return r.Result, o.OutputDir, err
}

func exchangeWorker(ctx context.Context, request workerRequest, writer io.WriteCloser, wait func() error) error {
	feedCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer writer.Close()
		encoder := json.NewEncoder(writer)
		if encoder.Encode(request) != nil {
			return
		}
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-feedCtx.Done():
				return
			case <-ticker.C:
				if encoder.Encode(struct{}{}) != nil {
					return
				}
			}
		}
	}()
	err := wait()
	cancel()
	<-done
	return err
}
