package server

import (
	"errors"
	"github.com/nerdylucifer/modelgauge/internal/controller"
	"net/http"
)

func (s *Server) exportNode(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.store.ExportNodeRun(r.Context(), r.PathValue("nodeID"), r.URL.Query().Get("run"))
	if errors.Is(err, controller.ErrNotFound) {
		writeError(w, http.StatusNotFound, "node or selected run not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}
