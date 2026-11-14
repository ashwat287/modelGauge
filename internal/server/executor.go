package server

import (
	"github.com/nerdylucifer/modelgauge/internal/agent"
	"net/http"
)

func (s *Server) executorConnection(w http.ResponseWriter, r *http.Request) {
	if s.config.Executor == nil {
		writeError(w, 503, "controller executor storage is not configured")
		return
	}
	node := r.PathValue("nodeID")
	switch r.Method {
	case "POST":
		var err error
		switch r.PathValue("action") {
		case "pause":
			err = s.config.Executor.Pause(node)
		case "resume":
			err = s.config.Executor.Resume(node)
		default:
			writeError(w, 404, "unknown connection action")
			return
		}
		if err != nil {
			writeError(w, 409, err.Error())
			return
		}

	case "PUT":
		var connection agent.SSHConnection
		if !decodeJSON(w, r, &connection) {
			return
		}
		if err := connection.Validate(); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		if err := s.config.Executor.Set(node, connection); err != nil {
			writeError(w, 409, err.Error())
			return
		}
	case "DELETE":
		if err := s.config.Executor.Remove(node); err != nil {
			writeError(w, 409, err.Error())
			return
		}
	}
	status, err := s.config.Executor.Status(node)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, status)
}
