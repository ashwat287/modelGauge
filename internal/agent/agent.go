// Package agent runs on the measured node and executes controller-issued plans.
package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nerdylucifer/modelgauge/internal/buildinfo"
	"github.com/nerdylucifer/modelgauge/internal/client"
	"github.com/nerdylucifer/modelgauge/internal/controller"
	"github.com/nerdylucifer/modelgauge/internal/modelcache"
	"github.com/nerdylucifer/modelgauge/internal/platform"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

type Config struct {
	ControllerURL string
	Token         string
	NodeID        string
	DisplayName   string
	NodeKind      string
	ExecutionMode protocol.ExecutionMode
	ResultsDir    string
}

type lease struct {
	PlanID        string        `json:"plan_id"`
	LeaseToken    string        `json:"lease_token"`
	ExpiresAt     time.Time     `json:"lease_expires_at"`
	Specification protocol.Plan `json:"specification"`
}

type startRequest struct {
	PlanID          string                 `json:"plan_id"`
	LeaseToken      string                 `json:"lease_token"`
	ExecutionMode   protocol.ExecutionMode `json:"execution_mode"`
	RuntimeMetadata map[string]string      `json:"runtime_metadata"`
}

type completeRequest struct {
	PlanID     string          `json:"plan_id"`
	LeaseToken string          `json:"lease_token"`
	Result     protocol.Result `json:"result"`
}

type registerRequest struct {
	NodeID        string                 `json:"node_id"`
	DisplayName   string                 `json:"display_name"`
	NodeKind      string                 `json:"node_kind"`
	ExecutionMode protocol.ExecutionMode `json:"execution_mode"`
	Hardware      map[string]any         `json:"hardware"`
	AgentVersion  string                 `json:"agent_version"`
}

type heartbeatRequest struct {
	Status string `json:"status"`
}

func ConfigFromEnv(getenv func(string) string) (Config, error) {
	mode := protocol.ExecutionMode(getenv("MODELGAUGE_EXECUTION_MODE"))
	if mode == "" {
		mode = protocol.ExecutionModeNative
	}
	kind := getenv("MODELGAUGE_NODE_KIND")
	if kind == "" {
		kind = "physical"
	}
	name := getenv("MODELGAUGE_NODE_NAME")
	if name == "" {
		name = getenv("MODELGAUGE_NODE_ID")
	}
	results := getenv("MODELGAUGE_RESULTS_DIR")
	if results == "" {
		results = "results"
	}
	config := Config{
		ControllerURL: strings.TrimRight(getenv("MODELGAUGE_CONTROLLER_URL"), "/"),
		Token:         getenv("MODELGAUGE_AGENT_TOKEN"), NodeID: getenv("MODELGAUGE_NODE_ID"),
		DisplayName: name, NodeKind: kind, ExecutionMode: mode, ResultsDir: results,
	}
	if config.ControllerURL == "" || config.Token == "" || config.NodeID == "" {
		return Config{}, fmt.Errorf("MODELGAUGE_CONTROLLER_URL, MODELGAUGE_AGENT_TOKEN, and MODELGAUGE_NODE_ID are required")
	}
	if config.NodeKind != "physical" && config.NodeKind != "virtual" {
		return Config{}, fmt.Errorf("MODELGAUGE_NODE_KIND must be physical or virtual")
	}
	if config.ExecutionMode != protocol.ExecutionModeNative && config.ExecutionMode != protocol.ExecutionModeLima {
		return Config{}, fmt.Errorf("invalid MODELGAUGE_EXECUTION_MODE")
	}
	return config, nil
}

// RunOnce registers the node, leases at most one plan, and uploads the result.
// It returns true when a plan was executed.
func RunOnce(ctx context.Context, config Config) (bool, error) {
	api := &client.Client{BaseURL: config.ControllerURL, AgentToken: config.Token}
	host, err := platform.Inspect()
	if err != nil {
		return false, err
	}
	_, err = api.DoJSON(ctx, "POST", "/v1/nodes/register", "agent", registerRequest{
		NodeID: config.NodeID, DisplayName: config.DisplayName, NodeKind: config.NodeKind,
		ExecutionMode: config.ExecutionMode, AgentVersion: buildinfo.Version,
		Hardware: map[string]any{"hostname": host.Hostname, "os": host.OS, "architecture": host.Architecture, "cpus": host.CPUs, "memory_bytes": host.MemoryBytes},
	}, nil)
	if err != nil {
		return false, fmt.Errorf("register node: %w", err)
	}
	var busy atomic.Bool
	stopHeartbeat := keepHeartbeat(ctx, api, config.NodeID, &busy, 30*time.Second)
	defer func() {
		stopHeartbeat()
		finalCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = api.DoJSON(finalCtx, "POST", "/v1/nodes/"+config.NodeID+"/heartbeat", "agent", heartbeatRequest{Status: "online"}, nil)
	}()
	pending, err := loadPending(config)
	if err != nil {
		return false, err
	}
	if pending != nil {
		busy.Store(true)
		return true, processPending(ctx, api, config, pending)
	}
	models, err := modelcache.Discover()
	if err != nil {
		return false, fmt.Errorf("discover models: %w", err)
	}
	catalog := make([]controller.NodeModel, 0, len(models))
	for _, model := range models {
		catalog = append(catalog, controller.NodeModel{ID: model.ID, SHA256: model.SHA256, SizeBytes: model.SizeBytes, Path: model.Path})
	}
	if _, err := api.DoJSON(ctx, "POST", "/v1/nodes/"+config.NodeID+"/models", "agent", catalog, nil); err != nil {
		return false, fmt.Errorf("sync models: %w", err)
	}
	var install controller.InstallJob
	status, err := api.DoJSON(ctx, "POST", "/v1/nodes/"+config.NodeID+"/model-installs/lease", "agent", nil, &install)
	if err != nil {
		return false, fmt.Errorf("lease model install: %w", err)
	}
	var pendingJob pendingJob
	if status != 204 {
		pendingJob = pendingJobForInstall(install)
	} else {
		var leased lease
		status, err = api.DoJSON(ctx, "POST", "/v1/nodes/"+config.NodeID+"/lease", "agent", nil, &leased)
		if err != nil {
			return false, fmt.Errorf("lease plan: %w", err)
		}
		if status == 204 {
			return false, nil
		}
		pendingJob = pendingJobForPlan(leased)
	}
	if err = savePending(config, &pendingJob); err != nil {
		return true, err
	}
	busy.Store(true)
	return true, processPending(ctx, api, config, &pendingJob)
}

func Run(ctx context.Context, config Config, once bool, interval time.Duration) error {
	unlock, err := platform.Lock("agent")
	if err != nil {
		return err
	}
	defer unlock()
	if interval <= 0 {
		return fmt.Errorf("poll interval must be positive")
	}
	for {
		_, err := RunOnce(ctx, config)
		if once {
			return err
		}
		if err != nil && ctx.Err() == nil {
			slog.Error("agent iteration failed; durable work will retry", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

func EnsureResultsDir(path string) error { return os.MkdirAll(path, 0o750) }

// keepHeartbeat continues during hashing, downloads, and synchronous benchmarks.
// The returned stop function joins the worker before the final online update,
// so a delayed busy heartbeat cannot overwrite the idle state.
func keepHeartbeat(ctx context.Context, api *client.Client, nodeID string, busy *atomic.Bool, interval time.Duration) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				status := "online"
				if busy.Load() {
					status = "busy"
				}
				requestCtx, requestCancel := context.WithTimeout(ctx, 5*time.Second)
				_, err := api.DoJSON(requestCtx, "POST", "/v1/nodes/"+nodeID+"/heartbeat", "agent", heartbeatRequest{Status: status}, nil)
				requestCancel()
				if err != nil && ctx.Err() == nil {
					slog.Warn("agent heartbeat failed; will retry", "error", err)
				}
			}
		}
	}()
	return func() { cancel(); <-done }
}
