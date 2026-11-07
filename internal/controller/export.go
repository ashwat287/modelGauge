package controller

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
	"time"
)

// NodeExport is a read-consistent database snapshot. Operational credentials and
// model download URLs are deliberately absent; this is not a database backup.
type NodeExport struct {
	SchemaVersion   string                 `json:"schema_version"`
	CapturedAt      time.Time              `json:"captured_at"`
	Node            json.RawMessage        `json:"node"`
	Results         []RunSummary           `json:"results"`
	Plans           []json.RawMessage      `json:"plans"`
	Runs            []json.RawMessage      `json:"runs"`
	Cases           []json.RawMessage      `json:"cases"`
	Models          []json.RawMessage      `json:"models"`
	Memory          []json.RawMessage      `json:"memory"`
	Snapshots       []json.RawMessage      `json:"host_snapshots"`
	InstallJobs     []json.RawMessage      `json:"install_jobs"`
	Recommendations []json.RawMessage      `json:"stored_recommendations"`
	Artifacts       []protocol.EvidenceRef `json:"artifacts"`
}

func (s *Store) ExportNode(ctx context.Context, nodeID string) (NodeExport, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return NodeExport{}, err
	}
	defer tx.Rollback(ctx)
	output := NodeExport{SchemaVersion: "modelgauge-node-export/v1", Artifacts: []protocol.EvidenceRef{}}
	err = tx.QueryRow(ctx, `SELECT to_jsonb(n),transaction_timestamp() FROM (SELECT node_id,display_name,node_kind,execution_mode,hardware,agent_version,status,last_seen_at,created_at,updated_at FROM nodes WHERE node_id=$1) n`, nodeID).Scan(&output.Node, &output.CapturedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return output, ErrNotFound
	}
	if err != nil {
		return output, err
	}
	output.CapturedAt = output.CapturedAt.UTC()
	for _, section := range []struct {
		destination *[]json.RawMessage
		query       string
	}{
		{&output.Plans, `SELECT to_jsonb(p) FROM (SELECT plan_id,node_id,status,specification,created_at,started_at,completed_at FROM benchmark_plans WHERE node_id=$1 ORDER BY created_at,plan_id) p`},
		{&output.Runs, `SELECT to_jsonb(r) FROM (SELECT run_id,plan_id,node_id,execution_mode,status,runtime_metadata,started_at,completed_at,invalid_reason FROM benchmark_runs WHERE node_id=$1 ORDER BY started_at,run_id) r`},
		{&output.Models, `SELECT to_jsonb(m) FROM (SELECT model_id,sha256,size_bytes,discovered_at FROM node_models WHERE node_id=$1 ORDER BY model_id) m`},
		{&output.Cases, `SELECT to_jsonb(c) FROM (SELECT run_id,case_id,threads,prompt_tokens,generation_tokens,repetitions,average_tokens_per_second,stddev_tokens_per_second,raw_result FROM benchmark_cases WHERE run_id IN (SELECT run_id FROM benchmark_runs WHERE node_id=$1) ORDER BY run_id,case_id) c`},
		{&output.Memory, `SELECT to_jsonb(m) FROM (SELECT run_id,case_id,peak_rss_bytes,safe_reservation_bytes FROM memory_measurements WHERE run_id IN (SELECT run_id FROM benchmark_runs WHERE node_id=$1) ORDER BY run_id) m`},
		{&output.Snapshots, `SELECT to_jsonb(h) FROM (SELECT run_id,snapshot_phase,captured_at,metrics FROM host_snapshots WHERE run_id IN (SELECT run_id FROM benchmark_runs WHERE node_id=$1) ORDER BY captured_at,snapshot_id) h`},
		{&output.InstallJobs, `SELECT to_jsonb(j) FROM (SELECT job_id,node_id,model_id,expected_sha256,status,created_at,completed_at FROM model_install_jobs WHERE node_id=$1 ORDER BY created_at,job_id) j`},
		{&output.Recommendations, `SELECT to_jsonb(r) FROM (SELECT recommendation_id,node_id,model_id,execution_mode,context_tokens,prompt_threads,generation_threads,prompt_tokens_per_second,generation_tokens_per_second,required_memory_bytes,fit_status,source_run_ids,algorithm_version,created_at FROM recommendations WHERE node_id=$1 ORDER BY created_at,recommendation_id) r`},
	} {
		values := []json.RawMessage{}
		rows, e := tx.Query(ctx, section.query, nodeID)
		if e != nil {
			return output, e
		}
		for rows.Next() {
			var data json.RawMessage
			if e = rows.Scan(&data); e != nil {
				rows.Close()
				return output, e
			}
			values = append(values, data)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return output, e
		}
		*section.destination = values
	}
	output.Results, err = listResults(ctx, tx, nodeID)
	if err != nil {
		return output, err
	}
	rows, err := tx.Query(ctx, `SELECT a.run_id,a.artifact_name,COALESCE(a.sha256,'') FROM artifacts a JOIN benchmark_runs r USING(run_id) WHERE r.node_id=$1 AND r.status IN ('completed','invalid','failed','cancelled') ORDER BY a.run_id,a.artifact_name`, nodeID)
	if err != nil {
		return output, err
	}
	for rows.Next() {
		var ref protocol.EvidenceRef
		if err = rows.Scan(&ref.RunID, &ref.Name, &ref.SHA256); err != nil {
			rows.Close()
			return output, err
		}
		output.Artifacts = append(output.Artifacts, ref)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return output, err
	}
	if err = tx.Commit(ctx); err != nil {
		return output, err
	}
	return output, nil
}
