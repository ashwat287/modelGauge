package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nerdylucifer/modelgauge/internal/agent"
	"github.com/nerdylucifer/modelgauge/internal/benchmark"
	"github.com/nerdylucifer/modelgauge/internal/client"
	"github.com/nerdylucifer/modelgauge/internal/controller"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
	"golang.org/x/crypto/ssh"
)

// A real SSH peer exercises the controller transport without a VM or model runtime.
func nodeFixture(t *testing.T, fail *atomic.Bool, block *atomic.Bool, started chan struct{}) agent.SSHConnection {
	t.Helper()
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	host, _ := ssh.NewSignerFromKey(private)
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	raw, _ := x509.MarshalPKCS8PrivateKey(key)
	clientKey, _ := ssh.NewPublicKey(key.Public())
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		if !bytes.Equal(k.Marshal(), clientKey.Marshal()) {
			return nil, io.EOF
		}
		return nil, nil
	}}
	cfg.AddHostKey(host)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				c, channels, requests, err := ssh.NewServerConn(raw, cfg)
				if err != nil {
					raw.Close()
					return
				}
				defer c.Close()
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					channel, requests, err := incoming.Accept()
					if err != nil {
						return
					}
					go func() {
						defer channel.Close()
						for request := range requests {
							if request.Type != "exec" {
								request.Reply(false, nil)
								continue
							}
							request.Reply(true, nil)
							var command struct{ Command string }
							ssh.Unmarshal(request.Payload, &command)
							if strings.HasPrefix(command.Command, "stop ") {
								channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
								return
							}
							var in struct {
								Operation     string        `json:"operation"`
								ObserveMillis int64         `json:"observe_millis"`
								RunID         string        `json:"run_id"`
								Plan          protocol.Plan `json:"plan"`
							}
							if err := json.NewDecoder(channel).Decode(&in); err != nil {
								return
							}
							encoder := json.NewEncoder(channel)
							switch in.Operation {
							case "inspect":
								encoder.Encode(map[string]any{"host": map[string]any{"os": "linux", "cpus": 2}, "models": []controller.NodeModel{{ID: "model", Path: "/models/model.gguf", SHA256: strings.Repeat("a", 64)}}})
							case "install":
								encoder.Encode(map[string]any{})
							case "observe":
								encoder.Encode(map[string]any{"measurement": protocol.Measurement{Case: "(node)", Values: map[string]float64{"resource_current_node_memory_available_bytes": 1234}}})
								time.Sleep(time.Duration(in.ObserveMillis) * time.Millisecond)
								encoder.Encode(map[string]any{"host": map[string]string{"os": "linux"}})
							case "benchmark":
								encoder.Encode(map[string]any{"measurement": protocol.Measurement{Stage: "1", Case: "live-load", Phase: "measuring", Values: map[string]float64{"successful": 2}}})
								if block.Load() {
									select {
									case started <- struct{}{}:
									default:
									}
									io.Copy(io.Discard, channel)
									return
								}
								if fail.Load() {
									encoder.Encode(map[string]string{"error": "fixture runtime failed"})
								} else {
									encoder.Encode(map[string]string{"progress": "Stage 1: engine\n"})
									encoder.Encode(map[string]string{"progress": "Stage 2: serving\n"})
									encoder.Encode(map[string]any{"result": protocol.Result{SchemaVersion: "benchmark-result/v1", RunID: in.RunID, PlanID: in.Plan.PlanID, NodeID: in.Plan.NodeID, ExecutionMode: in.Plan.ExecutionMode, Status: protocol.RunStatusCompleted, StartedAt: time.Now().UTC(), CompletedAt: time.Now().UTC()}})
								}
							}
							channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
							return
						}
					}()
				}
			}()
		}
	}()
	return agent.SSHConnection{Address: listener.Addr().String(), User: "node", HostKey: string(ssh.MarshalAuthorizedKey(host.PublicKey())), PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw}))}
}
func TestForegroundFlowFailureAndCancellation(t *testing.T) {
	ctx := context.Background()
	store, err := controller.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	executor, err := agent.NewControllerExecutor(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer executor.Close()
	var fail, block atomic.Bool
	started := make(chan struct{}, 1)
	connection := nodeFixture(t, &fail, &block, started)
	service := New(store, Config{Executor: executor, AdminToken: "secret"})
	service.cooldown = 25 * time.Millisecond
	httpServer := httptest.NewServer(service.Handler())
	defer httpServer.Close()
	api := &client.Client{BaseURL: httpServer.URL, AdminToken: "secret"}
	if _, err = api.DoJSON(ctx, "PUT", "/v1/nodes/node/connection", "admin", connection, nil); err != nil {
		t.Fatal(err)
	}
	var installed map[string]string
	var log bytes.Buffer
	if err = api.StreamJSON(ctx, "/v1/nodes/node/model-installs", map[string]string{"model_id": "model", "source_url": "https://example.test/model"}, &installed, &log); err != nil {
		t.Fatal(err)
	}
	if log.String() != "Installation: running\nInstallation: completed\n" {
		t.Fatal("state was repeated", &log)
	}
	doc, err := benchmark.ParseDocument(strings.NewReader("apiVersion: modelgauge.io/v1alpha1\nkind: Benchmark\nmetadata: {name: foreground}\nspec: {threads: [1], contexts: [32], stages: [{profile: standard-cpu}]}"))
	if err != nil {
		t.Fatal(err)
	}
	input := struct {
		benchmark.Document
		Model string `json:"model"`
	}{doc, "model"}
	var experiment controller.Experiment
	log.Reset()
	if err = api.StreamJSON(ctx, "/v1/experiments", input, &experiment, &log); err != nil {
		t.Fatal(err)
	}
	if strings.Count(log.String(), "Benchmark: running") != 1 || !strings.Contains(log.String(), "Stage 2: serving") || !strings.HasSuffix(log.String(), "Benchmark: completed\n") {
		t.Fatal(&log)
	}

	if !strings.Contains(log.String(), "Cooldown: running (25ms)") || !strings.Contains(log.String(), "Cooldown: completed") {
		t.Fatal("automatic cooldown not part of run", &log)
	}
	if !strings.Contains(executor.LiveMetrics(), "modelgauge_live_watching 0") || !strings.Contains(executor.LiveMetrics(), `measurement="resource_current_node_memory_available_bytes"} 1234`) {
		t.Fatal("cooldown did not stop with a retained final snapshot", executor.LiveMetrics())
	}

	fail.Store(true)
	if err = api.StreamJSON(ctx, "/v1/experiments", input, &experiment, io.Discard); err == nil {
		t.Fatal("runtime failure accepted")
	}
	rows, err := store.Records()
	if err != nil || len(rows) != 1 {
		t.Fatal("failed result persisted", rows, err)
	}
	fail.Store(false)
	block.Store(true)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- api.StreamJSON(runCtx, "/v1/experiments", input, &experiment, io.Discard) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("benchmark did not start")
	}
	response, metricsErr := httpServer.Client().Get(httpServer.URL + "/metrics")
	if metricsErr != nil {
		t.Fatal(metricsErr)
	}
	metrics, metricsErr := io.ReadAll(response.Body)
	response.Body.Close()
	if metricsErr != nil || !strings.Contains(string(metrics), `case="live-load",measurement="successful"} 2`) || !strings.Contains(string(metrics), `state="running"`) {
		t.Fatal("measurements not available before completion", string(metrics), metricsErr)
	}
	if err = executor.Pause("node", false); err == nil {
		t.Fatal("ordinary cleanup accepted active work")
	}
	if _, err = api.DoJSON(ctx, "POST", "/v1/cancel", "admin", nil, nil); err != nil {
		t.Fatal(err)
	}
	defer cancel()
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("interrupted command succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker cancellation did not complete")
	}
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(executor.LiveMetrics(), `state="cancelled"`) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !strings.Contains(executor.LiveMetrics(), `state="cancelled"`) {
		t.Fatal(executor.LiveMetrics())
	}
	if executor.Active() {
		t.Fatal("worker still active after cancellation")
	}
	rows, err = store.Records()
	if err != nil || len(rows) != 1 {
		t.Fatal("interrupted work retained", rows, err)
	}
	unauthorized := client.Client{BaseURL: httpServer.URL}
	if status, _ := unauthorized.DoJSON(ctx, "POST", "/v1/cancel", "", nil, nil); status != 401 {
		t.Fatal("cancellation did not require admin", status)
	}
}

func TestRemoteManagementRoutesRequireAdminAndProtectExistingNode(t *testing.T) {
	directory := t.TempDir()
	store, err := controller.Open(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := agent.NewControllerExecutor(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	defer executor.Close()
	api := New(store, Config{Executor: executor, AdminToken: "admin", DataDir: directory})
	for _, path := range []string{"/v1/remote/probe", "/v1/remote/setup", "/v1/remote/cleanup", "/v1/remote/reconnect", "/v1/remote/evidence"} {
		request := httptest.NewRequest("POST", path, strings.NewReader(`{}`))
		response := httptest.NewRecorder()
		api.Handler().ServeHTTP(response, request)
		if response.Code != 401 {
			t.Fatal(path, response.Code)
		}
	}
	if err = store.SaveNode(controller.Node{ID: "existing-local-node"}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/v1/remote/setup", strings.NewReader(`{"cpus":2,"memory_gib":3,"disk_gib":30}`))
	request.Header.Set("X-ModelGauge-Admin-Token", "admin")
	response := httptest.NewRecorder()
	api.Handler().ServeHTTP(response, request)
	if response.Code != 409 {
		t.Fatal(response.Code, response.Body.String())
	}
	// An interrupted attempt's local intent marker must not cause a missing remote
	// record to unregister an unrelated local node during cleanup.
	request = httptest.NewRequest("POST", "/v1/remote/cleanup", strings.NewReader(`{"DeleteVMs":true}`))
	request.Header.Set("X-ModelGauge-Admin-Token", "admin")
	response = httptest.NewRecorder()
	api.Handler().ServeHTTP(response, request)
	nodes, err := store.ListNodes(context.Background())
	if err != nil || len(nodes) != 1 || nodes[0].ID != "existing-local-node" {
		t.Fatal(nodes, err)
	}
}
func TestCancellationStopsForegroundProvisioningBeforeNodeRegistration(t *testing.T) {
	directory := t.TempDir()
	store, err := controller.Open(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := agent.NewControllerExecutor(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	defer executor.Close()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- executor.Manage(context.Background(), func(ctx context.Context) error { close(started); <-ctx.Done(); return ctx.Err() })
	}()
	<-started
	api := New(store, Config{Executor: executor, AdminToken: "admin", DataDir: directory})
	request := httptest.NewRequest("POST", "/v1/cancel", nil)
	request.Header.Set("X-ModelGauge-Admin-Token", "admin")
	response := httptest.NewRecorder()
	api.Handler().ServeHTTP(response, request)
	if response.Code != 200 || <-done == nil || executor.Active() {
		t.Fatal(response.Code, response.Body.String())
	}
}
