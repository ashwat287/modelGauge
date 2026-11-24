package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/nerdylucifer/modelgauge/internal/buildinfo"
	"github.com/nerdylucifer/modelgauge/internal/control"
	"github.com/nerdylucifer/modelgauge/internal/controller"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

func (s *Server) remoteRoutes() {
	s.mux.HandleFunc("GET /v1/remote", s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]bool{"configured": s.config.DataDir != "" && control.RemoteConfigured(s.config.DataDir)})
	}))
	s.mux.HandleFunc("POST /v1/remote/probe", s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Address string `json:"address"`
			User    string `json:"user"`
		}
		if !decodeJSON(w, r, &in) {
			return
		}
		key, err := control.ProbeRemoteHost(r.Context(), in.Address, in.User)
		s.respond(w, map[string]string{"host_key": key}, err)
	}))
	s.mux.HandleFunc("POST /v1/remote/setup", s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		var in control.RemoteOptions
		if !decodeJSON(w, r, &in) {
			return
		}
		if s.config.DataDir == "" {
			writeError(w, 500, "controller data directory is unavailable")
			return
		}
		if !control.RemoteConfigured(s.config.DataDir) {
			nodes, err := s.store.ListNodes(r.Context())
			if err != nil {
				s.respond(w, nil, err)
				return
			}
			if len(nodes) > 0 {
				writeError(w, 409, "workspace already has a node; clean up before changing its location")
				return
			}
		}
		s.stream(w, r, "Remote setup", func(log io.Writer) (any, error) {
			err := s.config.Executor.Manage(r.Context(), func(ctx context.Context) error {
				c, err := control.PrepareRemote(ctx, s.config.DataDir, in, log)
				if err != nil {
					return err
				}
				return s.config.Executor.Set("benchmark-node", c)
			})
			// Inspect after management returns: both use the executor's active lifetime.
			if err == nil {
				err = s.saveRemoteNode(r.Context())
			}
			return map[string]string{"status": "completed", "node": "benchmark-node"}, err
		})
	}))
	s.mux.HandleFunc("POST /v1/remote/reconnect", s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		if !control.RemoteConfigured(s.config.DataDir) {
			writeError(w, 404, "no remote host configured")
			return
		}
		err := s.saveRemoteNode(r.Context())
		s.respond(w, map[string]string{"status": "connected"}, err)
	}))
	s.mux.HandleFunc("POST /v1/remote/cleanup", s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		var options control.CleanupOptions
		if !decodeJSON(w, r, &options) {
			return
		}
		if s.config.DataDir == "" {
			writeError(w, 500, "controller data directory is unavailable")
			return
		}
		if !control.RemoteConfigured(s.config.DataDir) {
			s.stream(w, r, "Remote cleanup", func(io.Writer) (any, error) {
				return control.CleanupReport{DryRun: options.DryRun, Targets: []control.CleanupTarget{}, Untracked: []string{}}, nil
			})
			return
		}
		if !options.DryRun {
			if err := s.config.Executor.Pause("benchmark-node", options.Force); err != nil {
				writeError(w, 409, err.Error())
				return
			}
		}
		s.stream(w, r, "Remote cleanup", func(log io.Writer) (any, error) {
			report, err := control.CleanupRemote(r.Context(), s.config.DataDir, options, log)
			if err == nil && !options.DryRun {
				err = s.config.Executor.Remove("benchmark-node")
				if err == nil {
					err = os.Remove(filepath.Join(s.config.DataDir, "node.json"))
					if os.IsNotExist(err) {
						err = nil
					}
				}
			}
			return report, err
		})
	}))
	s.mux.HandleFunc("POST /v1/remote/evidence", s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		var refs []protocol.EvidenceRef
		if !decodeJSON(w, r, &refs) {
			return
		}
		if s.config.DataDir == "" {
			writeError(w, 500, "controller data directory is unavailable")
			return
		}
		w.Header().Set("Content-Type", "application/x-tar")
		if err := control.RemoteEvidence(r.Context(), s.config.DataDir, refs, w); err != nil {
			// A partial archive is rejected by the receiving checksum verifier.
			fmt.Fprintln(os.Stderr, "remote evidence transfer:", err)
		}
	}))
}
func (s *Server) saveRemoteNode(ctx context.Context) error {
	host, _, err := s.config.Executor.Inspect(ctx, "benchmark-node")
	if err != nil {
		return err
	}
	if host.OS != "linux" {
		return errors.New("benchmark worker must run inside the Linux VM")
	}
	data, err := json.Marshal(host)
	if err != nil {
		return err
	}
	hardware := map[string]any{}
	if err = json.Unmarshal(data, &hardware); err != nil {
		return err
	}
	now := time.Now().UTC()
	return s.store.SaveNode(controller.Node{ID: "benchmark-node", DisplayName: "benchmark-node", Kind: "virtual", ExecutionMode: protocol.ExecutionModeLima, Hardware: hardware, AgentVersion: buildinfo.Version, Status: "online", LastSeenAt: &now})
}
