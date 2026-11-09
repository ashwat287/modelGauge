package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
	"regexp"
	"time"
)

const JobTTL = 2 * time.Minute

type Renewal struct {
	ExpiresAt time.Time `json:"lease_expires_at"`
	Cancelled bool      `json:"cancelled"`
}

// Locking the node row serializes claims across BOTH job tables.
func lockNode(ctx context.Context, tx pgx.Tx, node string) error {
	var id string
	err := tx.QueryRow(ctx, `SELECT node_id FROM nodes WHERE node_id=$1 FOR UPDATE`, node).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
func expireJobs(ctx context.Context, tx pgx.Tx, node string) error {
	for _, sql := range []string{
		`UPDATE benchmark_runs r SET status=CASE WHEN p.status='cancelled' THEN 'cancelled' ELSE 'failed' END,invalid_reason='agent lease expired; interrupted execution',completed_at=now() FROM benchmark_plans p WHERE r.plan_id=p.plan_id AND p.node_id=$1 AND r.status='running' AND p.lease_expires_at<=now()`,
		`UPDATE benchmark_plans SET status='queued',lease_token=NULL,lease_expires_at=NULL WHERE node_id=$1 AND status='leased' AND lease_expires_at<=now()`,
		`UPDATE benchmark_plans SET status='failed',completed_at=now() WHERE node_id=$1 AND status='running' AND lease_expires_at<=now()`,
		`UPDATE model_install_jobs SET status='queued',lease_token=NULL,lease_expires_at=NULL,error_message='agent lease expired; download may resume' WHERE node_id=$1 AND status='running' AND (lease_expires_at IS NULL OR lease_expires_at<=now())`,
	} {
		if _, err := tx.Exec(ctx, sql, node); err != nil {
			return err
		}
	}
	return nil
}
func nodeHasJob(ctx context.Context, tx pgx.Tx, node string) (bool, error) {
	var busy bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM benchmark_plans WHERE node_id=$1 AND status IN ('leased','running')) OR EXISTS(SELECT 1 FROM benchmark_runs WHERE node_id=$1 AND status='running') OR EXISTS(SELECT 1 FROM model_install_jobs WHERE node_id=$1 AND (status='running' OR (status='cancelled' AND lease_expires_at>now())))`, node).Scan(&busy)
	return busy, err
}

func (s *Store) leaseInstall(ctx context.Context, node string) (InstallJob, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return InstallJob{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockNode(ctx, tx, node); err != nil {
		return InstallJob{}, err
	}
	if err = expireJobs(ctx, tx, node); err != nil {
		return InstallJob{}, err
	}
	busy, err := nodeHasJob(ctx, tx, node)
	if err != nil {
		return InstallJob{}, err
	}
	if busy {
		_ = tx.Commit(ctx)
		return InstallJob{}, ErrNotFound
	}
	var j InstallJob
	err = tx.QueryRow(ctx, `WITH next AS (SELECT job_id FROM model_install_jobs WHERE node_id=$1 AND status='queued' ORDER BY created_at LIMIT 1) UPDATE model_install_jobs j SET status='running',lease_token=gen_random_uuid(),lease_expires_at=now()+interval '2 minutes' FROM next WHERE j.job_id=next.job_id RETURNING j.job_id::text,j.node_id,j.model_id,j.source_url,COALESCE(j.expected_sha256,''),j.lease_token::text,j.lease_expires_at`, node).Scan(&j.ID, &j.NodeID, &j.ModelID, &j.SourceURL, &j.SHA256, &j.LeaseToken, &j.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Commit(ctx)
		return j, ErrNotFound
	}
	if err != nil {
		return j, err
	}
	return j, tx.Commit(ctx)
}

func (s *Store) Renew(ctx context.Context, node, id, token string, install bool) (Renewal, error) {
	var r Renewal
	table, key := "benchmark_plans", "plan_id"
	if install {
		table, key = "model_install_jobs", "job_id"
	}
	err := s.pool.QueryRow(ctx, `UPDATE `+table+` SET lease_expires_at=CASE WHEN status='cancelled' THEN lease_expires_at ELSE now()+interval '2 minutes' END WHERE `+key+`=$1 AND node_id=$2 AND lease_token=$3 AND status IN ('leased','running','cancelled') AND lease_expires_at>now() RETURNING lease_expires_at,status='cancelled'`, id, node, token).Scan(&r.ExpiresAt, &r.Cancelled)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrConflict
	}
	return r, err
}
func (s *Store) Cancel(ctx context.Context, id string, install bool) error {
	table, key := "benchmark_plans", "plan_id"
	if install {
		table, key = "model_install_jobs", "job_id"
	}
	command, err := s.pool.Exec(ctx, `UPDATE `+table+` SET status='cancelled',completed_at=now() WHERE `+key+`=$1 AND status IN ('queued','leased','running','cancelled')`, id)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

func (s *Store) completeInstall(ctx context.Context, node, id, token, failure string) error {
	status := "completed"
	if failure != "" {
		status = "failed"
	}
	command, err := s.pool.Exec(ctx, `UPDATE model_install_jobs SET status=CASE WHEN status='cancelled' THEN 'cancelled' ELSE $4 END,error_message=$5,completed_at=COALESCE(completed_at,now()),lease_expires_at=now() WHERE job_id=$1 AND node_id=$2 AND lease_token=$3 AND (status IN ('running','cancelled') OR status=$4)`, id, node, token, status, failure)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

var requestIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (s *Store) QueuePlanRequest(ctx context.Context, plan protocol.Plan, key string) (string, error) {
	if key == "" {
		return s.QueuePlan(ctx, plan)
	}
	if err := plan.Validate(); err != nil {
		return "", err
	}
	data, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	return s.queueRequest(ctx, key, "benchmark", data, func(tx pgx.Tx) (string, error) {
		var id string
		err := tx.QueryRow(ctx, `INSERT INTO benchmark_plans(node_id,specification) VALUES($1,$2) RETURNING plan_id::text`, plan.NodeID, data).Scan(&id)
		return id, err
	})
}
func (s *Store) QueueInstallRequest(ctx context.Context, j InstallJob, key string) (string, error) {
	if key == "" {
		return s.QueueInstall(ctx, j)
	}
	data, err := json.Marshal(struct{ Node, Model, Source, SHA string }{j.NodeID, j.ModelID, j.SourceURL, j.SHA256})
	if err != nil {
		return "", err
	}
	return s.queueRequest(ctx, key, "install", data, func(tx pgx.Tx) (string, error) {
		var id string
		err := tx.QueryRow(ctx, `INSERT INTO model_install_jobs(node_id,model_id,source_url,expected_sha256) VALUES($1,$2,$3,$4) RETURNING job_id::text`, j.NodeID, j.ModelID, j.SourceURL, j.SHA256).Scan(&id)
		return id, err
	})
}
func (s *Store) queueRequest(ctx context.Context, key, kind string, payload []byte, create func(pgx.Tx) (string, error)) (string, error) {
	if !requestIDPattern.MatchString(key) {
		return "", fmt.Errorf("invalid request ID")
	}
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, key); err != nil {
		return "", err
	}
	var previousKind, previousHash, id string
	err = tx.QueryRow(ctx, `SELECT kind,request_sha256,resource_id::text FROM submission_requests WHERE request_id=$1`, key).Scan(&previousKind, &previousHash, &id)
	if err == nil {
		if previousKind != kind || previousHash != digest {
			return "", ErrConflict
		}
		return id, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	id, err = create(tx)
	if err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO submission_requests(request_id,request_sha256,kind,resource_id) VALUES($1,$2,$3,$4)`, key, digest, kind, id); err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}
