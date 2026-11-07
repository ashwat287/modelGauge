package controller

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
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
