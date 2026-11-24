package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync/atomic"
	"time"

	"github.com/nerdylucifer/modelgauge/internal/controller"
	"github.com/nerdylucifer/modelgauge/internal/platform"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

var nodeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

type operation struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// The active pointer supports cancellation/monitoring, not admission or locking.
// shortcut: callers must submit one foreground operation at a time.
type ControllerExecutor struct {
	directory string
	live      controller.LiveMetrics
	active    atomic.Pointer[operation]
}

func NewControllerExecutor(_ context.Context, directory string) (*ControllerExecutor, error) {
	if err := os.MkdirAll(filepath.Join(directory, "connections"), 0700); err != nil {
		return nil, err
	}
	return &ControllerExecutor{directory: directory}, nil
}
func (e *ControllerExecutor) connection(node string) (SSHConnection, error) {
	var c SSHConnection
	if !nodeName.MatchString(node) {
		return c, errors.New("invalid node ID")
	}
	data, err := os.ReadFile(filepath.Join(e.directory, "connections", node+".json"))
	if err != nil {
		return c, err
	}
	if err = json.Unmarshal(data, &c); err != nil {
		return c, err
	}
	return c, c.Validate()
}
func (e *ControllerExecutor) Set(node string, c SSHConnection) error {
	if !nodeName.MatchString(node) {
		return errors.New("invalid node ID")
	}
	if err := c.Validate(); err != nil {
		return err
	}
	files, err := os.ReadDir(filepath.Join(e.directory, "connections"))
	if err != nil {
		return err
	}
	for _, f := range files {
		if filepath.Ext(f.Name()) == ".json" && f.Name() != node+".json" {
			return errors.New("workspace already has a benchmark node")
		}
	}
	return platform.WriteJSON(filepath.Join(e.directory, "connections", node+".json"), c, 0600)
}
func (e *ControllerExecutor) work(ctx context.Context, node string, request workerRequest, log io.Writer) (workerResponse, error) {
	c, err := e.connection(node)
	if err != nil {
		return workerResponse{}, err
	}
	var response workerResponse
	err = e.Manage(ctx, func(ctx context.Context) error {
		config := Config{SSH: &c, Log: log}
		if request.Operation == "benchmark" || request.Operation == "observe" {
			config.Observe = e.live.Observe
		}
		var err error
		response, err = remoteWork(ctx, config, request)
		return err
	})
	return response, err
}

// Manage gives remote provisioning the same foreground cancellation lifetime.
func (e *ControllerExecutor) Manage(ctx context.Context, work func(context.Context) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	op := &operation{cancel: cancel, done: make(chan struct{})}
	e.active.Store(op)
	defer func() { close(op.done); e.active.CompareAndSwap(op, nil) }()
	err := work(ctx)
	if ctx.Err() != nil {
		return errors.Join(err, ctx.Err())
	}
	return err
}

func (e *ControllerExecutor) Inspect(ctx context.Context, node string) (platform.Host, []controller.NodeModel, error) {
	r, err := e.work(ctx, node, workerRequest{Operation: "inspect"}, nil)
	return r.Host, r.Models, err
}
func (e *ControllerExecutor) Install(ctx context.Context, node, model, url, sha string, log io.Writer) error {
	_, err := e.work(ctx, node, workerRequest{Operation: "install", ModelID: model, SourceURL: url, SHA256: sha}, log)
	return err
}
func (e *ControllerExecutor) Benchmark(ctx context.Context, node, run string, p protocol.Plan, log io.Writer) (protocol.Result, error) {
	e.live.Start(node, p.Model.ID)
	r, err := e.work(ctx, node, workerRequest{Operation: "benchmark", RunID: run, Plan: &p}, log)
	state := "completed"
	if err != nil || r.Result.Status != protocol.RunStatusCompleted {
		state = "failed"
	}
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		state = "cancelled"
	}
	e.live.Finish(state)
	return r.Result, err
}
func (e *ControllerExecutor) Observe(ctx context.Context, node string, log io.Writer, duration time.Duration) error {
	if !e.live.Watch(true) {
		return errors.New("no finished experiment to observe")
	}
	defer e.live.Watch(false)
	_, err := e.work(ctx, node, workerRequest{Operation: "observe", ObserveMillis: duration.Milliseconds()}, log)
	return err
}

func (e *ControllerExecutor) LiveMetrics() string { return e.live.Metrics() }
func (e *ControllerExecutor) Active() bool        { return e.active.Load() != nil }
func (e *ControllerExecutor) Pause(node string, force bool) error {
	if !nodeName.MatchString(node) {
		return errors.New("invalid node ID")
	}
	op := e.active.Load()
	if op == nil {
		return nil
	}
	if !force {
		return errors.New("an operation is running; retry cleanup with --force")
	}
	op.cancel()
	select {
	case <-op.done:
		return nil
	case <-time.After(20 * time.Second):
		return errors.New("worker did not stop; cleanup refused")
	}
}
func (e *ControllerExecutor) Close() {
	if op := e.active.Load(); op != nil {
		op.cancel()
		<-op.done
	}
}
func (e *ControllerExecutor) Remove(node string) error {
	if err := e.Pause(node, false); err != nil {
		return err
	}
	err := os.Remove(filepath.Join(e.directory, "connections", node+".json"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
func (e *ControllerExecutor) Status(node string) (map[string]any, error) {
	_, err := e.connection(node)
	return map[string]any{"node_id": node, "configured": err == nil, "active": e.Active(), "location": "controller"}, nil
}
