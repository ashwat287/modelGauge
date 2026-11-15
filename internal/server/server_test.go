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
								Operation string        `json:"operation"`
								RunID     string        `json:"run_id"`
								Plan      protocol.Plan `json:"plan"`
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
							case "benchmark":
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
	httpServer := httptest.NewServer(New(store, Config{Executor: executor, AdminToken: "secret"}).Handler())
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
	done := make(chan error, 1)
	go func() { done <- api.StreamJSON(runCtx, "/v1/experiments", input, &experiment, io.Discard) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("benchmark did not start")
	}
	if err = executor.Pause("node", false); err == nil {
		t.Fatal("ordinary cleanup accepted active work")
	}
	cancel()
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("interrupted command succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker cancellation did not complete")
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
