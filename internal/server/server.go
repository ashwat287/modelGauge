// Package server executes foreground requests and serves completed measurements.
package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nerdylucifer/modelgauge/internal/agent"
	"github.com/nerdylucifer/modelgauge/internal/benchmark"
	"github.com/nerdylucifer/modelgauge/internal/buildinfo"
	"github.com/nerdylucifer/modelgauge/internal/controller"
	"github.com/nerdylucifer/modelgauge/internal/modelcache"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Executor   *agent.ControllerExecutor
	AdminToken string
}
type Server struct {
	store  *controller.Store
	config Config
	mux    *http.ServeMux
}

func New(store *controller.Store, config Config) *Server {
	s := &Server{store: store, config: config, mux: http.NewServeMux()}
	s.routes()
	return s
}
func (s *Server) Handler() http.Handler { return s.mux }
func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"status": "ok", "version": buildinfo.Version, "commit": buildinfo.Commit, "active": s.config.Executor != nil && s.config.Executor.Active()})
	})
	s.mux.HandleFunc("POST /v1/cancel", s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		nodes, err := s.store.ListNodes(r.Context())
		if err == nil && len(nodes) == 1 {
			err = s.config.Executor.Pause(nodes[0].ID, true)
		}
		s.respond(w, map[string]string{"status": "stopped"}, err)
	}))
	s.mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		text, err := s.store.Metrics(r.Context())
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprint(w, text)
		active := 0
		if s.config.Executor != nil && s.config.Executor.Active() {
			active = 1
		}
		fmt.Fprintf(w, "# TYPE modelgauge_active_operation gauge\nmodelgauge_active_operation %d\n", active)
	})
	s.mux.HandleFunc("GET /v1/nodes", s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		nodes, err := s.store.ListNodes(r.Context())
		s.respond(w, nodes, err)
	}))
	s.mux.HandleFunc("GET /v1/nodes/{nodeID}", s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		nodes, err := s.store.ListNodes(r.Context())
		if err == nil && (len(nodes) != 1 || nodes[0].ID != r.PathValue("nodeID")) {
			err = controller.ErrNotFound
		}
		if err != nil {
			s.respond(w, nil, err)
			return
		}
		s.respond(w, nodes[0], nil)
	}))
	for _, method := range []string{"GET", "PUT", "DELETE", "POST"} {
		path := method + " /v1/nodes/{nodeID}/connection"
		if method == "POST" {
			path += "/{action}"
		}
		s.mux.HandleFunc(path, s.requireAdmin(s.connection))
	}
	s.mux.HandleFunc("GET /v1/nodes/{nodeID}/models", s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		_, models, err := s.config.Executor.Inspect(r.Context(), r.PathValue("nodeID"))
		for i := range models {
			models[i].Path = ""
		}
		s.respond(w, models, err)
	}))
	s.mux.HandleFunc("POST /v1/nodes/{nodeID}/model-installs", s.requireAdmin(s.install))
	s.mux.HandleFunc("POST /v1/experiments", s.requireAdmin(s.experiment))
	s.mux.HandleFunc("GET /v1/experiments", s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		rows, err := s.store.Experiments(r.Context())
		s.respond(w, rows, err)
	}))
	s.mux.HandleFunc("GET /v1/experiments/{name}", s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		row, err := s.store.Experiment(r.Context(), r.PathValue("name"))
		s.respond(w, row, err)
	}))
	s.mux.HandleFunc("GET /v1/nodes/{nodeID}/results", s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		rows, err := s.store.ListResults(r.Context(), r.PathValue("nodeID"))
		s.respond(w, rows, err)
	}))
	s.mux.HandleFunc("GET /v1/nodes/{nodeID}/export", s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		row, err := s.store.ExportNodeRun(r.Context(), r.PathValue("nodeID"), r.URL.Query().Get("run"))
		s.respond(w, row, err)
	}))
	s.mux.HandleFunc("GET /v1/nodes/{nodeID}/recommendation", s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		tokens, _ := strconv.Atoi(q.Get("context"))
		budget, _ := strconv.ParseInt(q.Get("memory_budget_bytes"), 10, 64)
		row, err := s.store.Recommend(r.Context(), r.PathValue("nodeID"), q.Get("model"), q.Get("execution_mode"), tokens, budget)
		s.respond(w, row, err)
	}))
}
func (s *Server) connection(w http.ResponseWriter, r *http.Request) {
	node := r.PathValue("nodeID")
	var err error
	switch r.Method {
	case "PUT":
		var c agent.SSHConnection
		if !decodeJSON(w, r, &c) {
			return
		}
		if err = c.Validate(); err == nil {
			err = s.config.Executor.Set(node, c)
		}
		if err == nil {
			host, _, e := s.config.Executor.Inspect(r.Context(), node)
			err = e
			if err == nil {
				data, _ := json.Marshal(host)
				hardware := map[string]any{}
				err = json.Unmarshal(data, &hardware)
				now := time.Now().UTC()
				if err == nil {
					err = s.store.SaveNode(controller.Node{ID: node, DisplayName: node, Kind: "virtual", ExecutionMode: protocol.ExecutionModeLima, Hardware: hardware, AgentVersion: buildinfo.Version, Status: "online", LastSeenAt: &now})
				}
			}
		}
	case "DELETE":
		err = s.config.Executor.Remove(node)
	case "POST":
		switch r.PathValue("action") {
		case "pause":
			err = s.config.Executor.Pause(node, false)
		case "cleanup":
			err = s.config.Executor.Pause(node, true)
		case "resume":
		default:
			writeError(w, 404, "unknown connection action")
			return
		}
	}
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	state, err := s.config.Executor.Status(node)
	s.respond(w, state, err)
}
func (s *Server) install(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ModelID string `json:"model_id"`
		URL     string `json:"source_url"`
		SHA     string `json:"sha256"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := modelcache.ValidateID(in.ModelID); err != nil || in.URL == "" {
		writeError(w, 400, "valid model_id and source_url are required")
		return
	}
	s.stream(w, r, "Installation", func(log io.Writer) (any, error) {
		err := s.config.Executor.Install(r.Context(), r.PathValue("nodeID"), in.ModelID, in.URL, in.SHA, log)
		return map[string]string{"model_id": in.ModelID, "status": "completed"}, err
	})
}
func (s *Server) experiment(w http.ResponseWriter, r *http.Request) {
	var in struct {
		benchmark.Document
		Model string `json:"model"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := in.Document.Validate(); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	nodes, err := s.store.ListNodes(r.Context())
	if err != nil {
		s.respond(w, nil, err)
		return
	}
	if len(nodes) != 1 {
		writeError(w, 400, "run setup to register a benchmark node")
		return
	}
	_, models, err := s.config.Executor.Inspect(r.Context(), nodes[0].ID)
	if err != nil {
		s.respond(w, nil, err)
		return
	}
	var model protocol.Model
	for _, m := range models {
		if m.ID == in.Model {
			model = protocol.Model{ID: m.ID, Path: m.Path, SHA256: m.SHA256}
			break
		}
	}
	if model.Path == "" {
		writeError(w, 400, "selected model is not installed")
		return
	}
	plan, err := in.Document.Plan(r.Context(), nodes[0].ID, nodes[0].ExecutionMode, model)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if r.URL.Query().Get("dry_run") == "true" {
		writeJSON(w, 200, controller.Experiment{Specification: in.Document, Plans: []controller.ExperimentPlan{{Status: "validated", Specification: plan}}})
		return
	}
	id := make([]byte, 16)
	if _, err = rand.Read(id); err != nil {
		s.respond(w, nil, err)
		return
	}
	run := "run-" + hex.EncodeToString(id)
	plan.PlanID = run
	s.stream(w, r, "Benchmark", func(log io.Writer) (any, error) {
		result, err := s.config.Executor.Benchmark(r.Context(), nodes[0].ID, run, plan, log)
		if err != nil {
			return nil, err
		}
		if result.RunID != run || result.PlanID != plan.PlanID || result.NodeID != plan.NodeID || result.ExecutionMode != plan.ExecutionMode || result.SchemaVersion != "benchmark-result/v1" {
			return nil, fmt.Errorf("worker returned a result for another execution")
		}
		if result.Status != protocol.RunStatusCompleted {
			return nil, fmt.Errorf("benchmark ended %s: %s", result.Status, result.InvalidReason)
		}
		if err = r.Context().Err(); err != nil {
			return nil, err
		}
		record := controller.Record{Document: in.Document, Plan: plan, Result: result}
		if err = s.store.Save(record); err != nil {
			return nil, err
		}
		return record.Experiment(), nil
	})
}

type progressWriter struct {
	encoder *json.Encoder
	flush   func()
}

func (p progressWriter) Write(data []byte) (int, error) {
	if err := p.encoder.Encode(protocol.Event{Log: strings.TrimSpace(string(data))}); err != nil {
		return 0, err
	}
	p.flush()
	return len(data), nil
}
func (s *Server) stream(w http.ResponseWriter, r *http.Request, label string, work func(io.Writer) (any, error)) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	encoder := json.NewEncoder(w)
	flush := func() {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	progress := progressWriter{encoder: encoder, flush: flush}
	if _, err := io.WriteString(progress, label+": running"); err != nil {
		return
	}
	result, err := work(progress)
	if err != nil {
		encoder.Encode(protocol.Event{Error: err.Error()})
		flush()
		return
	}
	if _, err = io.WriteString(progress, label+": completed"); err != nil {
		return
	}
	data, err := json.Marshal(result)
	if err != nil {
		encoder.Encode(protocol.Event{Error: err.Error()})
	} else {
		encoder.Encode(protocol.Event{Result: data})
	}
	flush()
}
func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-ModelGauge-Admin-Token")
		if s.config.AdminToken == "" || subtle.ConstantTimeCompare([]byte(got), []byte(s.config.AdminToken)) != 1 {
			writeError(w, 401, "admin authentication required")
			return
		}
		next(w, r)
	}
}
func decodeJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		writeError(w, 400, err.Error())
		return false
	}
	if decoder.Decode(new(any)) != io.EOF {
		writeError(w, 400, "expected one JSON object")
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
func (s *Server) respond(w http.ResponseWriter, value any, err error) {
	if errors.Is(err, controller.ErrNotFound) {
		writeError(w, 404, err.Error())
		return
	}
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, value)
}
func AdminTokenFromEnv(getenv func(string) string) string { return getenv("MODELGAUGE_ADMIN_TOKEN") }
func ListenAndServe(ctx context.Context, address string, handler http.Handler) error {
	httpServer := &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	failure := make(chan error, 1)
	go func() { failure <- httpServer.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownContext)
	case err := <-failure:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
