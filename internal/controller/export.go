package controller

import (
	"context"
	"encoding/json"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
	"time"
)

type NodeExport struct {
	SelectedRun   string                 `json:"selected_run_id,omitempty"`
	SchemaVersion string                 `json:"schema_version"`
	CapturedAt    time.Time              `json:"captured_at"`
	Node          json.RawMessage        `json:"node"`
	Results       []RunSummary           `json:"results"`
	Plans         []json.RawMessage      `json:"plans"`
	Runs          []json.RawMessage      `json:"runs"`
	Artifacts     []protocol.EvidenceRef `json:"artifacts"`
}

func (s *Store) ExportNodeRun(ctx context.Context, node, run string) (NodeExport, error) {
	out := NodeExport{SchemaVersion: "modelgauge-node-export/v1", SelectedRun: run, CapturedAt: time.Now().UTC(), Artifacts: []protocol.EvidenceRef{}, Results: []RunSummary{}}
	nodes, err := s.ListNodes(ctx)
	if err != nil {
		return out, err
	}
	if len(nodes) != 1 || nodes[0].ID != node {
		return out, ErrNotFound
	}
	out.Node, err = json.Marshal(nodes[0])
	if err != nil {
		return out, err
	}
	records, err := s.Records()
	if err != nil {
		return out, err
	}
	for _, r := range records {
		if r.Result.NodeID != node || (run != "" && r.Result.RunID != run) {
			continue
		}
		out.Results = append(out.Results, summary(r))
		plan, err := json.Marshal(r.Plan)
		if err != nil {
			return out, err
		}
		out.Plans = append(out.Plans, plan)
		result, err := json.Marshal(r.Result)
		if err != nil {
			return out, err
		}
		out.Runs = append(out.Runs, result)
		for _, a := range r.Result.Artifacts {
			out.Artifacts = append(out.Artifacts, protocol.EvidenceRef{RunID: r.Result.RunID, Name: a.Name, SHA256: a.SHA256})
		}
	}
	if run != "" && len(out.Results) == 0 {
		return out, ErrNotFound
	}
	return out, nil
}
