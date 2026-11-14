package server

import (
	"errors"
	"net/http"

	"github.com/nerdylucifer/modelgauge/internal/benchmark"
	"github.com/nerdylucifer/modelgauge/internal/controller"
)

func (s *Server) applyExperiment(w http.ResponseWriter, r *http.Request) {
	if values, present := r.URL.Query()["dry_run"]; present && (len(values) != 1 || (values[0] != "true" && values[0] != "false")) {
		writeError(w, 400, "dry_run must be true or false")
		return
	}
	var document benchmark.Document
	if !decodeJSON(w, r, &document) {
		return
	}
	e, err := s.store.ApplyExperiment(r.Context(), document, r.URL.Query().Get("dry_run") == "true")
	s.experimentResponse(w, r, e, err)
}

func (s *Server) experiment(w http.ResponseWriter, r *http.Request) {
	e, err := s.store.Experiment(r.Context(), r.PathValue("name"), r.URL.Query().Get("spec_sha256"), r.Method == http.MethodPost)
	s.experimentResponse(w, r, e, err)
}

func (s *Server) experimentResponse(w http.ResponseWriter, r *http.Request, e controller.Experiment, err error) {
	if errors.Is(err, controller.ErrConflict) {
		writeError(w, 409, err.Error())
		return
	}
	if errors.Is(err, controller.ErrNotFound) {
		writeError(w, 404, "experiment not found")
		return
	}
	if err != nil {
		if errors.Is(err, controller.ErrInvalidExperiment) {
			writeError(w, 400, err.Error())
		} else {
			s.internalError(w, r, err)
		}
		return
	}
	writeJSON(w, 200, e)
}
