// Package controller persists node state, plans, leases, and benchmark results.
package controller

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nerdylucifer/modelgauge/internal/benchmark"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

//go:embed migrations/001-init-schema.sql
var initialSchema string

//go:embed migrations/002-job-recovery.sql
var recoverySchema string

var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("state conflict")

//go:embed migrations/003-submission-requests.sql
var requestSchema string

//go:embed migrations/004-experiments.sql
var experimentSchema string

type Store struct {
	pool *pgxpool.Pool
}

type Node struct {
	ID            string                 `json:"node_id"`
	DisplayName   string                 `json:"display_name"`
	Kind          string                 `json:"node_kind"`
	ExecutionMode protocol.ExecutionMode `json:"execution_mode"`
	Hardware      map[string]any         `json:"hardware"`
	AgentVersion  string                 `json:"agent_version"`
	Status        string                 `json:"status,omitempty"`
	LastSeenAt    *time.Time             `json:"last_seen_at,omitempty"`
}

func (s *Store) ListNodes(ctx context.Context) ([]Node, error) {
	rows, err := s.pool.Query(ctx, `SELECT node_id,display_name,node_kind,execution_mode,hardware,COALESCE(agent_version,''),CASE WHEN last_seen_at IS NULL OR last_seen_at < now()-interval '90 seconds' THEN 'offline' ELSE status END,last_seen_at FROM nodes ORDER BY node_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nodes := []Node{}
	for rows.Next() {
		var node Node
		var hardware []byte
		if err := rows.Scan(&node.ID, &node.DisplayName, &node.Kind, &node.ExecutionMode, &hardware, &node.AgentVersion, &node.Status, &node.LastSeenAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(hardware, &node.Hardware); err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	return nodes, rows.Err()
}

type Lease struct {
	PlanID        string          `json:"plan_id"`
	LeaseToken    string          `json:"lease_token"`
	ExpiresAt     time.Time       `json:"lease_expires_at"`
	Specification json.RawMessage `json:"specification"`
}

type PlanStatus struct {
	PlanID string `json:"plan_id"`
	NodeID string `json:"node_id"`
	Status string `json:"status"`
}

type NodeModel struct {
	ID        string `json:"id"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
	Path      string `json:"path"`
}
type InstallJob struct {
	LeaseToken string    `json:"lease_token,omitempty"`
	ExpiresAt  time.Time `json:"lease_expires_at,omitempty"`
	ID         string    `json:"job_id"`
	NodeID     string    `json:"node_id"`
	ModelID    string    `json:"model_id"`
	SourceURL  string    `json:"source_url"`
	SHA256     string    `json:"sha256,omitempty"`
	Status     string    `json:"status"`
	Error      string    `json:"error,omitempty"`
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	config.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open database pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close()                         { s.pool.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *Store) Migrate(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(604228619)`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, initialSchema); err != nil {
		return fmt.Errorf("apply initial schema: %w", err)
	}
	if _, err = tx.Exec(ctx, recoverySchema); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, requestSchema); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, experimentSchema); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) RegisterNode(ctx context.Context, node Node) error {
	hardware, err := json.Marshal(node.Hardware)
	if err != nil {
		return fmt.Errorf("encode node hardware: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
INSERT INTO nodes (
  node_id, display_name, node_kind, execution_mode, hardware,
  agent_version, status, last_seen_at, updated_at
)
VALUES ($1, $2, $3, $4, $5, $6, 'online', now(), now())
ON CONFLICT (node_id) DO UPDATE SET
  display_name = EXCLUDED.display_name,
  node_kind = EXCLUDED.node_kind,
  execution_mode = EXCLUDED.execution_mode,
  hardware = EXCLUDED.hardware,
  agent_version = EXCLUDED.agent_version,
  status = 'online',
  last_seen_at = now(),
  updated_at = now()`, node.ID, node.DisplayName, node.Kind, node.ExecutionMode, hardware, node.AgentVersion)
	return err
}

func (s *Store) Heartbeat(ctx context.Context, nodeID, status string) error {
	command, err := s.pool.Exec(ctx, `
UPDATE nodes SET status = $1, last_seen_at = now(), updated_at = now()
WHERE node_id = $2`, status, nodeID)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ReplaceNodeModels(ctx context.Context, nodeID string, models []NodeModel) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM node_models WHERE node_id = $1`, nodeID); err != nil {
		return err
	}
	for _, model := range models {
		if _, err := tx.Exec(ctx, `INSERT INTO node_models (node_id, model_id, model_path, sha256, size_bytes) VALUES ($1,$2,$3,$4,$5)`, nodeID, model.ID, model.Path, model.SHA256, model.SizeBytes); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) ListNodeModels(ctx context.Context, nodeID string) ([]NodeModel, error) {
	rows, err := s.pool.Query(ctx, `SELECT model_id, sha256, size_bytes, model_path FROM node_models WHERE node_id = $1 ORDER BY model_id`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	models := []NodeModel{}
	for rows.Next() {
		var model NodeModel
		if err := rows.Scan(&model.ID, &model.SHA256, &model.SizeBytes, &model.Path); err != nil {
			return nil, err
		}
		models = append(models, model)
	}
	return models, rows.Err()
}

func (s *Store) QueueInstall(ctx context.Context, job InstallJob) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `INSERT INTO model_install_jobs (node_id,model_id,source_url,expected_sha256) VALUES ($1,$2,$3,$4) RETURNING job_id::text`, job.NodeID, job.ModelID, job.SourceURL, job.SHA256).Scan(&id)
	return id, err
}

func (s *Store) GetInstall(ctx context.Context, id string) (InstallJob, error) {
	var job InstallJob
	err := s.pool.QueryRow(ctx, `SELECT job_id::text,node_id,model_id,status,COALESCE(error_message,'') FROM model_install_jobs WHERE job_id=$1`, id).Scan(&job.ID, &job.NodeID, &job.ModelID, &job.Status, &job.Error)
	if errors.Is(err, pgx.ErrNoRows) {
		return job, ErrNotFound
	}
	return job, err
}
func (s *Store) QueuePlan(ctx context.Context, plan protocol.Plan) (string, error) {
	if err := benchmark.ValidatePlan(plan); err != nil {
		return "", err
	}
	specification, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	var planID string
	err = s.pool.QueryRow(ctx, `
INSERT INTO benchmark_plans (node_id, specification)
VALUES ($1, $2)
RETURNING plan_id::text`, plan.NodeID, specification).Scan(&planID)
	return planID, err
}

func (s *Store) LeasePlan(ctx context.Context, nodeID string, duration time.Duration) (*Lease, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = lockNode(ctx, tx, nodeID); err != nil {
		return nil, err
	}
	if err = expireJobs(ctx, tx, nodeID); err != nil {
		return nil, err
	}
	busy, err := nodeHasJob(ctx, tx, nodeID)
	if err != nil {
		return nil, err
	}
	if busy {
		_ = tx.Commit(ctx)
		return nil, ErrNotFound
	}

	var lease Lease
	err = tx.QueryRow(ctx, `
WITH next_plan AS (
  SELECT plan_id
  FROM benchmark_plans
  WHERE node_id = $1 AND status = 'queued'
  ORDER BY created_at
  FOR UPDATE SKIP LOCKED
  LIMIT 1
)
UPDATE benchmark_plans AS plan
SET status = 'leased', lease_token = gen_random_uuid(),
    lease_expires_at = now() + $2::interval
FROM next_plan
WHERE plan.plan_id = next_plan.plan_id
RETURNING plan.plan_id::text, plan.lease_token::text,
          plan.lease_expires_at, plan.specification`, nodeID, duration.String()).Scan(
		&lease.PlanID, &lease.LeaseToken, &lease.ExpiresAt, &lease.Specification,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Commit(ctx)
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &lease, nil
}

func (s *Store) StartRun(ctx context.Context, nodeID, runID, planID, leaseToken string, mode protocol.ExecutionMode, metadata map[string]string) error {
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockNode(ctx, tx, nodeID); err != nil {
		return err
	}

	command, err := tx.Exec(ctx, `
UPDATE benchmark_plans
SET status = 'running', started_at = COALESCE(started_at,now())
WHERE plan_id = $1 AND node_id = $2 AND status IN ('leased','running')
  AND lease_token = $3 AND lease_expires_at > now()`, planID, nodeID, leaseToken)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrConflict
	}
	_, err = tx.Exec(ctx, `
INSERT INTO benchmark_runs (
  run_id, plan_id, node_id, execution_mode, status, runtime_metadata, started_at
)
VALUES ($1, $2, $3, $4, 'running', $5, now())
ON CONFLICT (run_id) DO NOTHING`,
		runID, planID, nodeID, mode, metadataJSON)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) CompleteRun(ctx context.Context, nodeID, planID, leaseToken string, result protocol.Result) error {
	if err := benchmark.ValidateResult(result); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockNode(ctx, tx, nodeID); err != nil {
		return err
	}
	var existingStatus, existingToken string
	err = tx.QueryRow(ctx, `SELECT r.status,p.lease_token::text FROM benchmark_runs r JOIN benchmark_plans p USING(plan_id) WHERE r.run_id=$1 AND r.node_id=$2 AND p.plan_id=$3 FOR UPDATE OF r,p`, result.RunID, nodeID, planID).Scan(&existingStatus, &existingToken)
	if err != nil {
		return err
	}
	if existingToken != leaseToken {
		return ErrConflict
	}
	if existingStatus != "running" {
		if existingStatus == string(result.Status) {
			return tx.Commit(ctx)
		}
		return ErrConflict
	}

	var exists bool
	err = tx.QueryRow(ctx, `
SELECT EXISTS(
  SELECT 1 FROM benchmark_runs run
  JOIN benchmark_plans plan ON plan.plan_id = run.plan_id
  WHERE run.run_id = $1 AND run.node_id = $2 AND plan.plan_id = $3
    AND plan.lease_token = $4 AND plan.status IN ('running','cancelled')
)`, result.RunID, nodeID, planID, leaseToken).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return ErrConflict
	}

	for _, benchmarkCase := range result.Cases {
		raw, err := json.Marshal(benchmarkCase)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
INSERT INTO benchmark_cases (
  run_id, case_id, threads, prompt_tokens, generation_tokens,
  repetitions, average_tokens_per_second, stddev_tokens_per_second, raw_result
)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
ON CONFLICT (run_id, case_id) DO UPDATE SET
  threads = EXCLUDED.threads, prompt_tokens = EXCLUDED.prompt_tokens,
  generation_tokens = EXCLUDED.generation_tokens,
  repetitions = EXCLUDED.repetitions,
  average_tokens_per_second = EXCLUDED.average_tokens_per_second,
  stddev_tokens_per_second = EXCLUDED.stddev_tokens_per_second,
  raw_result = EXCLUDED.raw_result`,
			result.RunID, benchmarkCase.CaseID, benchmarkCase.Threads,
			benchmarkCase.PromptTokens, benchmarkCase.GenerationTokens,
			benchmarkCase.Repetitions, benchmarkCase.TokensPerSecond,
			benchmarkCase.TokensPerSecondStddev, raw)
		if err != nil {
			return err
		}
	}
	if result.Memory != nil {
		_, err = tx.Exec(ctx, `
INSERT INTO memory_measurements (
  run_id, case_id, peak_rss_bytes, safe_reservation_bytes, raw_time_output
)
VALUES ($1,$2,$3,$4,'')
ON CONFLICT (run_id) DO UPDATE SET
  case_id = EXCLUDED.case_id, peak_rss_bytes = EXCLUDED.peak_rss_bytes,
  safe_reservation_bytes = EXCLUDED.safe_reservation_bytes`,
			result.RunID, result.Memory.CaseID, result.Memory.PeakRSSBytes,
			result.Memory.SafeReservationBytes)
		if err != nil {
			return err
		}
	}
	for _, artifact := range result.Artifacts {
		_, err = tx.Exec(ctx, `
INSERT INTO artifacts (run_id, artifact_name, content_type, storage_uri, sha256)
VALUES ($1,$2,'application/octet-stream',$2,$3)
ON CONFLICT (run_id, artifact_name) DO UPDATE SET sha256 = EXCLUDED.sha256`,
			result.RunID, artifact.Name, artifact.SHA256)
		if err != nil {
			return err
		}
	}

	planStatus := "completed"
	var cancelled bool
	if err = tx.QueryRow(ctx, `SELECT status='cancelled' FROM benchmark_plans WHERE plan_id=$1`, planID).Scan(&cancelled); err != nil {
		return err
	}
	if cancelled {
		result.Status = protocol.RunStatusCancelled
		result.InvalidReason = "cancelled by user"
	}
	if result.Status != protocol.RunStatusCompleted {
		planStatus = "failed"
	}
	if cancelled {
		planStatus = "cancelled"
	}
	metadata := make(map[string]string, len(result.RuntimeMetadata)+1)
	for key, value := range result.RuntimeMetadata {
		metadata[key] = value
	}
	delete(metadata, "serving")
	delete(metadata, "benchmark")
	if result.Benchmark != nil {
		report, err := json.Marshal(result.Benchmark)
		if err != nil {
			return err
		}
		metadata["benchmark"] = string(report)
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
UPDATE benchmark_runs SET status = $1, completed_at = now(), invalid_reason = $2,
runtime_metadata=(CASE WHEN jsonb_typeof(runtime_metadata)='object' THEN runtime_metadata ELSE '{}'::jsonb END)||$4::jsonb
WHERE run_id = $3`, result.Status, result.InvalidReason, result.RunID, metadataJSON)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
UPDATE benchmark_plans SET status = $1, completed_at = now() WHERE plan_id = $2`, planStatus, planID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
UPDATE nodes SET status = 'online', last_seen_at = now(), updated_at = now() WHERE node_id = $1`, nodeID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) QueueDepth(ctx context.Context) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM benchmark_plans WHERE status = 'queued'`).Scan(&count)
	return count, err
}

func (s *Store) GetPlanStatus(ctx context.Context, planID string) (PlanStatus, error) {
	var status PlanStatus
	err := s.pool.QueryRow(ctx, `SELECT plan_id::text, node_id, status FROM benchmark_plans WHERE plan_id = $1`, planID).Scan(&status.PlanID, &status.NodeID, &status.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return PlanStatus{}, ErrNotFound
	}
	return status, err
}
