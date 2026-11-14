package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/nerdylucifer/modelgauge/internal/platform"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

var nodeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

type controllerWorker struct {
	connection               SSHConnection
	cancel                   context.CancelFunc
	done                     chan struct{}
	healthMu                 sync.Mutex
	lastError                string
	lastAttempt, lastSuccess time.Time
}

// ControllerExecutor embeds the existing durable lease loop in the controller.
// Its configuration and upload journals share the controller's persistent volume.
type ControllerExecutor struct {
	mu                    sync.Mutex
	ctx                   context.Context
	directory, url, token string
	workers               map[string]*controllerWorker
	closed                bool
}

func NewControllerExecutor(ctx context.Context, directory, url, token string) (*ControllerExecutor, error) {
	e := &ControllerExecutor{ctx: ctx, directory: directory, url: url, token: token, workers: map[string]*controllerWorker{}}
	if err := os.MkdirAll(filepath.Join(directory, "connections"), 0700); err != nil {
		return nil, err
	}
	files, err := os.ReadDir(filepath.Join(directory, "connections"))
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if f.IsDir() || filepath.Ext(f.Name()) != ".json" {
			continue
		}
		node := f.Name()[:len(f.Name())-5]
		if _, err := os.Stat(filepath.Join(directory, "connections", node+".paused")); err == nil {
			continue
		}
		if !nodeName.MatchString(node) {
			e.Close()
			return nil, errors.New("invalid executor node configuration")
		}
		data, err := os.ReadFile(filepath.Join(directory, "connections", f.Name()))
		var connection SSHConnection
		if err == nil {
			err = json.Unmarshal(data, &connection)
		}
		if err == nil {
			err = connection.Validate()
		}
		if err != nil {
			e.Close()
			return nil, fmt.Errorf("load executor connection for %s: %w", node, err)
		}
		e.start(node, connection)
	}
	return e, nil
}
func (e *ControllerExecutor) state(node string) string {
	return filepath.Join(e.directory, "runs", node)
}
func (e *ControllerExecutor) start(node string, connection SSHConnection) {
	ctx, cancel := context.WithCancel(e.ctx)
	w := &controllerWorker{connection: connection, cancel: cancel, done: make(chan struct{})}
	e.workers[node] = w
	go func() {
		defer close(w.done)
		for {
			config := Config{SSH: &connection, NodeID: node, DisplayName: node, NodeKind: "virtual", ExecutionMode: protocol.ExecutionModeLima, ControllerURL: e.url, Token: e.token, ResultsDir: e.state(node)}
			config.report = func(err error) {
				w.healthMu.Lock()
				defer w.healthMu.Unlock()
				w.lastAttempt = time.Now().UTC()
				w.lastError = ""
				if err == nil {
					w.lastSuccess = w.lastAttempt
					return
				}
				w.lastError = err.Error()
				if e.token != "" {
					w.lastError = strings.ReplaceAll(w.lastError, e.token, "[REDACTED]")
				}
				if len(w.lastError) > 4096 {
					w.lastError = w.lastError[:4096] + "..."
				}
			}
			err := EnsureResultsDir(config.ResultsDir)
			if err == nil {
				err = Run(ctx, config, false, 5*time.Second)
			}
			if err != nil {
				config.report(err)
			}
			if err != nil && ctx.Err() == nil {
				slog.Error("controller executor stopped; retrying", "node", node, "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}()
}
func (e *ControllerExecutor) pending(node string) (bool, error) {
	_, err := os.Lstat(filepath.Join(e.state(node), ".pending.json"))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}
func (e *ControllerExecutor) stopIdle(node string) error {
	if busy, err := e.pending(node); err != nil {
		return err
	} else if busy {
		return errors.New("executor has pending work; finish or cancel it before changing the connection")
	}
	if w := e.workers[node]; w != nil {
		w.cancel()
		<-w.done
		delete(e.workers, node)
		// A claim racing the first check must retain its connection and recovery state.
		if busy, err := e.pending(node); err != nil || busy {
			e.start(node, w.connection)
			return errors.New("executor claimed work during connection change; retry after recovery")
		}
	}
	return nil
}
func (e *ControllerExecutor) Set(node string, connection SSHConnection) error {
	if !nodeName.MatchString(node) {
		return errors.New("invalid executor node ID")
	}
	if err := connection.Validate(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.setLocked(node, connection)
}
func (e *ControllerExecutor) setLocked(node string, connection SSHConnection) error {
	if e.closed {
		return errors.New("controller executor is closed")
	}
	if e.ctx.Err() != nil {
		return e.ctx.Err()
	}
	if w := e.workers[node]; w != nil && w.connection == connection {
		return nil
	}
	previous := e.workers[node]
	if err := e.stopIdle(node); err != nil {
		return err
	}
	if err := platform.WriteJSON(filepath.Join(e.directory, "connections", node+".json"), connection, 0600); err != nil {
		if previous != nil {
			e.start(node, previous.connection)
		}
		return err
	}
	if err := os.Remove(filepath.Join(e.directory, "connections", node+".paused")); err != nil && !os.IsNotExist(err) {
		return err
	}
	e.start(node, connection)
	return nil
}
func (e *ControllerExecutor) Remove(node string) error {
	if !nodeName.MatchString(node) {
		return errors.New("invalid executor node ID")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.stopIdle(node); err != nil {
		return err
	}
	err := os.Remove(filepath.Join(e.directory, "connections", node+".json"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
func (e *ControllerExecutor) Status(node string) (map[string]any, error) {
	if !nodeName.MatchString(node) {
		return nil, errors.New("invalid executor node ID")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	pending, err := e.pending(node)
	_, configured := e.workers[node]
	status := map[string]any{"node_id": node, "configured": configured, "pending": pending, "location": "controller"}
	_, markerErr := os.Stat(filepath.Join(e.directory, "connections", node+".paused"))
	status["paused"] = markerErr == nil
	if markerErr != nil && !os.IsNotExist(markerErr) {
		return nil, markerErr
	}
	if w := e.workers[node]; w != nil {
		w.healthMu.Lock()
		defer w.healthMu.Unlock()
		status["address"], status["last_error"] = w.connection.Address, w.lastError
		if !w.lastAttempt.IsZero() {
			status["last_attempt_at"] = w.lastAttempt
		}
		if !w.lastSuccess.IsZero() {
			status["last_success_at"] = w.lastSuccess
		}
	}
	return status, err
}
func (e *ControllerExecutor) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	for _, w := range e.workers {
		w.cancel()
	}
	for node, w := range e.workers {
		<-w.done
		delete(e.workers, node)
	}
}

func (e *ControllerExecutor) Pause(node string) error {
	if !nodeName.MatchString(node) {
		return errors.New("invalid node ID")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	marker := filepath.Join(e.directory, "connections", node+".paused")
	if err := platform.WriteFile(marker, []byte("paused\n"), 0600); err != nil {
		return err
	}
	if err := e.stopIdle(node); err != nil {
		_ = os.Remove(marker)
		return err
	}
	return nil
}
func (e *ControllerExecutor) Resume(node string) error {
	if !nodeName.MatchString(node) {
		return errors.New("invalid node ID")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	data, err := os.ReadFile(filepath.Join(e.directory, "connections", node+".json"))
	if os.IsNotExist(err) {
		_ = os.Remove(filepath.Join(e.directory, "connections", node+".paused"))
		return nil
	}
	if err != nil {
		return err
	}
	var connection SSHConnection
	if err = json.Unmarshal(data, &connection); err != nil {
		return err
	}
	if err = connection.Validate(); err != nil {
		return err
	}
	return e.setLocked(node, connection)
}
