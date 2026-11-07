package server

import (
	"errors"
	"github.com/nerdylucifer/modelgauge/internal/controller"
	"net/http"
)

func (s *Server) cancelPlan(w http.ResponseWriter, r *http.Request)    { s.cancelJob(w, r, false) }
func (s *Server) cancelInstall(w http.ResponseWriter, r *http.Request) { s.cancelJob(w, r, true) }
func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request, install bool) {
	id := r.PathValue("planID")
	if install {
		id = r.PathValue("jobID")
	}
	if err := s.store.Cancel(r.Context(), id, install); err != nil {
		if errors.Is(err, controller.ErrConflict) {
			writeError(w, 409, "job is missing or already terminal")
			return
		}
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "cancelled"})
}
func (s *Server) renewPlan(w http.ResponseWriter, r *http.Request)    { s.renewJob(w, r, false) }
func (s *Server) renewInstall(w http.ResponseWriter, r *http.Request) { s.renewJob(w, r, true) }
func (s *Server) renewJob(w http.ResponseWriter, r *http.Request, install bool) {
	var payload struct {
		Token string `json:"lease_token"`
	}
	if !decodeJSON(w, r, &payload) {
		return
	}
	if payload.Token == "" {
		writeError(w, 400, "lease_token is required")
		return
	}
	id := r.PathValue("planID")
	if install {
		id = r.PathValue("jobID")
	}
	renewal, err := s.store.Renew(r.Context(), r.PathValue("nodeID"), id, payload.Token, install)
	if err != nil {
		if errors.Is(err, controller.ErrConflict) {
			writeError(w, 409, "job lease expired or revoked")
			return
		}
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, 200, renewal)
}
