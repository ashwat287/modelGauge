// Package server exposes the controller HTTP API used by the CLI and agents.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nerdylucifer/modelgauge/internal/agent"
	"github.com/nerdylucifer/modelgauge/internal/benchmark"
	"github.com/nerdylucifer/modelgauge/internal/buildinfo"
	"github.com/nerdylucifer/modelgauge/internal/controller"
	"github.com/nerdylucifer/modelgauge/internal/modelcache"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

type Config struct {
	Executor   *agent.ControllerExecutor
	AgentToken string
	AdminToken string
	LeaseTTL   time.Duration
	Logger     *slog.Logger
}

type Server struct {
	store  *controller.Store
	config Config
	mux    *http.ServeMux
}

func New(store *controller.Store, config Config) *Server {
	if config.LeaseTTL == 0 {
		config.LeaseTTL = controller.JobTTL
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	server := &Server{store: store, config: config, mux: http.NewServeMux()}
	server.routes()
	return server
}

func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	s.mux.HandleFunc("POST /v1/experiments", s.requireAdmin(s.applyExperiment))
	s.mux.HandleFunc("GET /v1/experiments/{name}", s.requireAdmin(s.experiment))
	s.mux.HandleFunc("POST /v1/experiments/{name}/cancel", s.requireAdmin(s.experiment))
	s.mux.HandleFunc("POST /v1/nodes/{nodeID}/connection/{action}", s.requireAdmin(s.executorConnection))
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		s.mux.HandleFunc(method+" /v1/nodes/{nodeID}/connection", s.requireAdmin(s.executorConnection))
	}

	s.mux.HandleFunc("GET /healthz", s.healthz)
	s.mux.HandleFunc("GET /metrics", s.metrics)
	s.mux.HandleFunc("POST /v1/plans", s.requireAdmin(s.queuePlan))
	s.mux.HandleFunc("POST /v1/plans/{planID}/cancel", s.requireAdmin(s.cancelPlan))
	s.mux.HandleFunc("POST /v1/model-installs/{jobID}/cancel", s.requireAdmin(s.cancelInstall))
	s.mux.HandleFunc("POST /v1/nodes/{nodeID}/plans/{planID}/renew", s.requireAgent(s.renewPlan))
	s.mux.HandleFunc("POST /v1/nodes/{nodeID}/model-installs/{jobID}/renew", s.requireAgent(s.renewInstall))
	s.mux.HandleFunc("GET /v1/model-installs/{jobID}", s.requireAdmin(s.installStatus))
	s.mux.HandleFunc("GET /v1/plans/{planID}", s.requireAdmin(s.planStatus))
	s.mux.HandleFunc("GET /v1/nodes/{nodeID}/export", s.requireAdmin(s.exportNode))
	s.mux.HandleFunc("GET /v1/nodes/{nodeID}/results", s.requireAdmin(s.listResults))
	s.mux.HandleFunc("GET /v1/nodes/{nodeID}/recommendation", s.requireAdmin(s.recommend))
	s.mux.HandleFunc("POST /v1/nodes/register", s.requireAgent(s.registerNode))
	s.mux.HandleFunc("GET /v1/nodes", s.requireAdmin(s.listNodes))
	s.mux.HandleFunc("GET /v1/nodes/{nodeID}", s.requireAdmin(s.nodeStatus))
	s.mux.HandleFunc("POST /v1/nodes/{nodeID}/models", s.requireAgent(s.syncModels))
	s.mux.HandleFunc("POST /v1/nodes/{nodeID}/model-installs", s.requireAdmin(s.queueInstall))
	s.mux.HandleFunc("POST /v1/nodes/{nodeID}/model-installs/lease", s.requireAgent(s.leaseInstall))
	s.mux.HandleFunc("POST /v1/nodes/{nodeID}/model-installs/{jobID}/complete", s.requireAgent(s.completeInstall))
	s.mux.HandleFunc("GET /v1/nodes/{nodeID}/models", s.requireAdmin(s.listModels))
	s.mux.HandleFunc("POST /v1/nodes/{nodeID}/heartbeat", s.requireAgent(s.heartbeat))
	s.mux.HandleFunc("POST /v1/nodes/{nodeID}/lease", s.requireAgent(s.leasePlan))
	s.mux.HandleFunc("POST /v1/nodes/{nodeID}/runs/{runID}/start", s.requireAgent(s.startRun))
	s.mux.HandleFunc("POST /v1/nodes/{nodeID}/runs/{runID}/complete", s.requireAgent(s.completeRun))
}

func (s *Server) listNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.store.ListNodes(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, 200, nodes)
}
func (s *Server) nodeStatus(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.store.ListNodes(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	for _, node := range nodes {
		if node.ID == r.PathValue("nodeID") {
			var executor map[string]any
			if s.config.Executor != nil {
				executor, err = s.config.Executor.Status(node.ID)
				if err != nil {
					s.internalError(w, r, err)
					return
				}
			}
			writeJSON(w, 200, struct {
				controller.Node
				Executor map[string]any `json:"executor,omitempty"`
			}{node, executor})
			return
		}
	}
	writeError(w, 404, "node not found")
}

func (s *Server) installStatus(w http.ResponseWriter, r *http.Request) {
	job, err := s.store.GetInstall(r.Context(), r.PathValue("jobID"))
	if errors.Is(err, controller.ErrNotFound) {
		writeError(w, 404, "installation job not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]string{"job_id": job.ID, "node_id": job.NodeID, "model_id": job.ModelID, "status": job.Status, "error": job.Error})
}

func (s *Server) queueInstall(w http.ResponseWriter, r *http.Request) {
	var j controller.InstallJob
	if !decodeJSON(w, r, &j) {
		return
	}
	j.NodeID = r.PathValue("nodeID")
	if err := modelcache.ValidateID(j.ModelID); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if j.ModelID == "" || j.SourceURL == "" {
		writeError(w, 400, "model_id and source_url are required")
		return
	}
	id, err := s.store.QueueInstallRequest(r.Context(), j, r.Header.Get("X-ModelGauge-Request-ID"))
	if err != nil {
		if errors.Is(err, controller.ErrConflict) {
			writeError(w, 409, "request ID already used for different content")
			return
		}
		s.internalError(w, r, err)
		return
	}
	job, err := s.store.GetInstall(r.Context(), id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, 201, map[string]string{"job_id": id, "status": job.Status})
}
func (s *Server) leaseInstall(w http.ResponseWriter, r *http.Request) {
	j, err := s.store.LeaseInstall(r.Context(), r.PathValue("nodeID"))
	if errors.Is(err, controller.ErrNotFound) {
		w.WriteHeader(204)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, 200, j)
}
func (s *Server) completeInstall(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Error      string `json:"error"`
		LeaseToken string `json:"lease_token"`
	}
	if !decodeJSON(w, r, &p) {
		return
	}
	if p.LeaseToken == "" {
		writeError(w, 400, "lease_token is required")
		return
	}
	if err := s.store.CompleteInstall(r.Context(), r.PathValue("nodeID"), r.PathValue("jobID"), p.LeaseToken, p.Error); err != nil {
		if errors.Is(err, controller.ErrConflict) {
			writeError(w, 409, "installation lease no longer owned")
			return
		}
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "recorded"})
}

func (s *Server) syncModels(writer http.ResponseWriter, request *http.Request) {
	var models []controller.NodeModel
	if !decodeJSON(writer, request, &models) {
		return
	}
	if err := s.store.ReplaceNodeModels(request.Context(), request.PathValue("nodeID"), models); err != nil {
		s.internalError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]int{"count": len(models)})
}

func (s *Server) listModels(writer http.ResponseWriter, request *http.Request) {
	models, err := s.store.ListNodeModels(request.Context(), request.PathValue("nodeID"))
	if err != nil {
		s.internalError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, models)
}

func (s *Server) planStatus(writer http.ResponseWriter, request *http.Request) {
	status, err := s.store.GetPlanStatus(request.Context(), request.PathValue("planID"))
	if errors.Is(err, controller.ErrNotFound) {
		writeError(writer, http.StatusNotFound, "plan not found")
		return
	}
	if err != nil {
		s.internalError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, status)
}

func (s *Server) listResults(writer http.ResponseWriter, request *http.Request) {
	results, err := s.store.ListResults(request.Context(), request.PathValue("nodeID"))
	if err != nil {
		s.internalError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, results)
}

func (s *Server) recommend(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	modelID, mode, contextText := query.Get("model"), query.Get("execution_mode"), query.Get("context")
	if modelID == "" || mode == "" || contextText == "" {
		writeError(writer, http.StatusBadRequest, "model, execution_mode, and context are required")
		return
	}
	var contextTokens int
	var memoryBudget int64
	if _, err := fmt.Sscan(contextText, &contextTokens); err != nil || contextTokens < 1 {
		writeError(writer, http.StatusBadRequest, "context must be a positive integer")
		return
	}
	if budget := query.Get("memory_budget_bytes"); budget != "" {
		if _, err := fmt.Sscan(budget, &memoryBudget); err != nil || memoryBudget < 0 {
			writeError(writer, http.StatusBadRequest, "memory_budget_bytes must be a non-negative integer")
			return
		}
	}
	recommendation, err := s.store.Recommend(request.Context(), request.PathValue("nodeID"), modelID, mode, contextTokens, memoryBudget)
	if errors.Is(err, controller.ErrNotFound) {
		writeError(writer, http.StatusNotFound, "no completed run matches this node, model, mode, and context")
		return
	}
	if err != nil {
		writeError(writer, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(writer, http.StatusOK, recommendation)
}

func (s *Server) healthz(writer http.ResponseWriter, request *http.Request) {
	if err := s.store.Ping(request.Context()); err != nil {
		writeError(writer, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ok", "database": "reachable", "version": buildinfo.Version, "commit": buildinfo.Commit, "submission_protocol": "idempotent-v1"})
}

func (s *Server) metrics(writer http.ResponseWriter, request *http.Request) {
	metrics, err := s.store.Metrics(request.Context())
	if err != nil {
		writeError(writer, http.StatusServiceUnavailable, "metrics database unavailable")
		return
	}
	depth, err := s.store.QueueDepth(request.Context())
	if err != nil {
		writeError(writer, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writer.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintln(writer, "# HELP modelgauge_controller_up Controller availability")
	fmt.Fprintln(writer, "# TYPE modelgauge_controller_up gauge")
	fmt.Fprintln(writer, "modelgauge_controller_up 1")
	fmt.Fprintln(writer, "# HELP modelgauge_controller_queue_depth Queued benchmark plans")
	fmt.Fprintln(writer, "# TYPE modelgauge_controller_queue_depth gauge")
	fmt.Fprintf(writer, "modelgauge_controller_queue_depth %d\n", depth)
	fmt.Fprint(writer, metrics)
}

type registerRequest struct {
	NodeID        string                 `json:"node_id"`
	DisplayName   string                 `json:"display_name"`
	NodeKind      string                 `json:"node_kind"`
	ExecutionMode protocol.ExecutionMode `json:"execution_mode"`
	Hardware      map[string]any         `json:"hardware"`
	AgentVersion  string                 `json:"agent_version"`
}

func (s *Server) registerNode(writer http.ResponseWriter, request *http.Request) {
	var payload registerRequest
	if !decodeJSON(writer, request, &payload) {
		return
	}
	if payload.NodeID == "" || payload.DisplayName == "" || (payload.NodeKind != "physical" && payload.NodeKind != "virtual") {
		writeError(writer, http.StatusBadRequest, "node_id, display_name, and valid node_kind are required")
		return
	}
	if payload.ExecutionMode != protocol.ExecutionModeNative && payload.ExecutionMode != protocol.ExecutionModeLima {
		writeError(writer, http.StatusBadRequest, "invalid execution_mode")
		return
	}
	if err := s.store.RegisterNode(request.Context(), controller.Node{
		ID: payload.NodeID, DisplayName: payload.DisplayName, Kind: payload.NodeKind,
		ExecutionMode: payload.ExecutionMode, Hardware: payload.Hardware, AgentVersion: payload.AgentVersion,
	}); err != nil {
		s.internalError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"node_id": payload.NodeID, "status": "registered"})
}

type heartbeatRequest struct {
	Status string `json:"status"`
}

func (s *Server) heartbeat(writer http.ResponseWriter, request *http.Request) {
	var payload heartbeatRequest
	if !decodeJSON(writer, request, &payload) {
		return
	}
	if payload.Status != "online" && payload.Status != "busy" && payload.Status != "offline" {
		writeError(writer, http.StatusBadRequest, "invalid status")
		return
	}
	err := s.store.Heartbeat(request.Context(), request.PathValue("nodeID"), payload.Status)
	if errors.Is(err, controller.ErrNotFound) {
		writeError(writer, http.StatusNotFound, "node is not registered")
		return
	}
	if err != nil {
		s.internalError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"status": payload.Status})
}

func (s *Server) queuePlan(writer http.ResponseWriter, request *http.Request) {
	var plan protocol.Plan
	if !decodeJSON(writer, request, &plan) {
		return
	}
	planID, err := s.store.QueuePlanRequest(request.Context(), plan, request.Header.Get("X-ModelGauge-Request-ID"))
	if err != nil {
		if errors.Is(err, controller.ErrConflict) {
			writeError(writer, 409, "request ID already used for different content")
			return
		}
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	state, err := s.store.GetPlanStatus(request.Context(), planID)
	if err != nil {
		s.internalError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]string{"plan_id": planID, "status": state.Status})
}

func (s *Server) leasePlan(writer http.ResponseWriter, request *http.Request) {
	lease, err := s.store.LeasePlan(request.Context(), request.PathValue("nodeID"), s.config.LeaseTTL)
	if errors.Is(err, controller.ErrNotFound) {
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		s.internalError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, lease)
}

type startRunRequest struct {
	PlanID          string                 `json:"plan_id"`
	LeaseToken      string                 `json:"lease_token"`
	ExecutionMode   protocol.ExecutionMode `json:"execution_mode"`
	RuntimeMetadata map[string]string      `json:"runtime_metadata"`
}

func (s *Server) startRun(writer http.ResponseWriter, request *http.Request) {
	var payload startRunRequest
	if !decodeJSON(writer, request, &payload) {
		return
	}
	if payload.PlanID == "" || payload.LeaseToken == "" {
		writeError(writer, http.StatusBadRequest, "plan_id and lease_token are required")
		return
	}
	err := s.store.StartRun(request.Context(), request.PathValue("nodeID"), request.PathValue("runID"), payload.PlanID, payload.LeaseToken, payload.ExecutionMode, payload.RuntimeMetadata)
	if errors.Is(err, controller.ErrConflict) {
		writeError(writer, http.StatusConflict, "plan is not leased by this node or lease has expired")
		return
	}
	if err != nil {
		s.internalError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"run_id": request.PathValue("runID"), "status": "running"})
}

type completeRunRequest struct {
	PlanID     string          `json:"plan_id"`
	LeaseToken string          `json:"lease_token"`
	Result     protocol.Result `json:"result"`
}

func (s *Server) completeRun(writer http.ResponseWriter, request *http.Request) {
	var payload completeRunRequest
	if !decodeJSON(writer, request, &payload) {
		return
	}
	if payload.PlanID == "" || payload.LeaseToken == "" || payload.Result.RunID != request.PathValue("runID") {
		writeError(writer, http.StatusBadRequest, "invalid completion payload")
		return
	}
	if payload.Result.Status != protocol.RunStatusCompleted && payload.Result.Status != protocol.RunStatusFailed && payload.Result.Status != protocol.RunStatusInvalid && payload.Result.Status != protocol.RunStatusCancelled {
		writeError(writer, 400, "result must have a terminal status")
		return
	}
	if err := benchmark.ValidateResult(payload.Result); err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	err := s.store.CompleteRun(request.Context(), request.PathValue("nodeID"), payload.PlanID, payload.LeaseToken, payload.Result)
	if errors.Is(err, controller.ErrConflict) {
		writeError(writer, http.StatusConflict, "run is not active for this lease")
		return
	}
	if err != nil {
		s.internalError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"run_id": payload.Result.RunID, "status": string(payload.Result.Status)})
}

func (s *Server) requireAgent(next http.HandlerFunc) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if s.config.AgentToken == "" || !secureEqual(request.Header.Get("X-ModelGauge-Token"), s.config.AgentToken) {
			writeError(writer, http.StatusUnauthorized, "invalid agent token")
			return
		}
		next(writer, request)
	}
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if s.config.AdminToken == "" || !secureEqual(request.Header.Get("X-ModelGauge-Admin-Token"), s.config.AdminToken) {
			writeError(writer, http.StatusUnauthorized, "invalid admin token")
			return
		}
		next(writer, request)
	}
}

func (s *Server) internalError(writer http.ResponseWriter, request *http.Request, err error) {
	s.config.Logger.Error("controller request failed", "path", request.URL.Path, "error", err)
	writeError(writer, http.StatusInternalServerError, "internal server error")
}

func decodeJSON(writer http.ResponseWriter, request *http.Request, destination any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 4<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid JSON payload")
		return false
	}
	return true
}

func writeJSON(writer http.ResponseWriter, status int, payload any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(payload)
}

func writeError(writer http.ResponseWriter, status int, message string) {
	writeJSON(writer, status, map[string]string{"error": message})
}

func secureEqual(got, expected string) bool {
	if len(got) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
}

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

func DatabaseURLFromEnv(getenv func(string) string) string {
	if databaseURL := getenv("MODELGAUGE_DATABASE_URL"); databaseURL != "" {
		return databaseURL
	}
	user := getenv("DATABASE_USER")
	password := getenv("DATABASE_PASSWORD")
	host := getenv("DATABASE_HOST")
	port := getenv("DATABASE_PORT")
	name := getenv("DATABASE_NAME")
	if port == "" {
		port = "5432"
	}
	if host == "" || user == "" || name == "" {
		return ""
	}
	return (&url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, password),
		Host:     net.JoinHostPort(host, port),
		Path:     name,
		RawQuery: "sslmode=disable",
	}).String()
}

func AgentTokenFromEnv(getenv func(string) string) string {
	return strings.TrimSpace(getenv("MODELGAUGE_AGENT_TOKEN"))
}
func AdminTokenFromEnv(getenv func(string) string) string {
	return strings.TrimSpace(getenv("MODELGAUGE_ADMIN_TOKEN"))
}
