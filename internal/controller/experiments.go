package controller

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nerdylucifer/modelgauge/internal/benchmark"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

type Experiment struct {
	Name          string             `json:"name"`
	Digest        string             `json:"spec_sha256"`
	Specification benchmark.Document `json:"specification"`
	CreatedAt     *time.Time         `json:"created_at,omitempty"`
	Plans         []ExperimentPlan   `json:"plans"`
}

type ExperimentPlan struct {
	PlanID        string        `json:"plan_id,omitempty"`
	Status        string        `json:"status"`
	Specification protocol.Plan `json:"specification"`
}

var ErrInvalidExperiment = errors.New("invalid experiment")

func invalidExperiment(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidExperiment, fmt.Sprintf(format, args...))
}

// ApplyExperiment freezes inventory and queues every job in one transaction.
// A retry reads the frozen snapshot before consulting potentially changed inventory.
func (s *Store) ApplyExperiment(ctx context.Context, d benchmark.Document, dryRun bool) (Experiment, error) {
	var e Experiment
	if err := d.Validate(); err != nil {
		return e, invalidExperiment("%v", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return e, err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "experiment/"+d.Metadata.Name); err != nil {
		return e, err
	}
	e, err = readExperiment(ctx, tx, d.Metadata.Name)
	if err == nil {
		if e.Digest != d.Digest() {
			return e, fmt.Errorf("%w: experiment name already has a different specification; use a new metadata.name", ErrConflict)
		}
		return e, tx.Commit(ctx)
	}
	if !errors.Is(err, ErrNotFound) {
		return e, err
	}
	e = Experiment{Name: d.Metadata.Name, Digest: d.Digest(), Specification: d, Plans: []ExperimentPlan{}}
	resolvedBytes := 0
	for _, node := range d.Spec.Nodes {
		var mode protocol.ExecutionMode
		err = tx.QueryRow(ctx, `SELECT execution_mode FROM nodes WHERE node_id=$1 FOR SHARE`, node).Scan(&mode)
		if errors.Is(err, pgx.ErrNoRows) {
			return e, invalidExperiment("spec.nodes: node %q is not registered", node)
		}
		if err != nil {
			return e, err
		}
		for _, modelID := range d.Spec.Models {
			model := protocol.Model{ID: modelID}
			err = tx.QueryRow(ctx, `SELECT model_path,sha256 FROM node_models WHERE node_id=$1 AND model_id=$2 FOR SHARE`, node, modelID).Scan(&model.Path, &model.SHA256)
			if errors.Is(err, pgx.ErrNoRows) {
				return e, invalidExperiment("model %q is not installed on node %q; install it before applying", modelID, node)
			}
			if err != nil {
				return e, err
			}
			if digest, err := hex.DecodeString(model.SHA256); err != nil || len(digest) != 32 {
				return e, invalidExperiment("model %q on node %q has no valid SHA-256; refresh node inventory", modelID, node)
			}
			p, err := d.Plan(ctx, node, mode, model)
			if err != nil {
				return e, invalidExperiment("node %s model %s: %v", node, modelID, err)
			}
			data, err := json.Marshal(p)
			if err != nil {
				return e, err
			}
			resolvedBytes += len(data)
			if resolvedBytes > 16<<20 {
				return e, invalidExperiment("resolved experiment exceeds 16 MiB; split the experiment into smaller declarations")
			}
			e.Plans = append(e.Plans, ExperimentPlan{Status: "resolved", Specification: p})
		}
	}
	if dryRun {
		return e, tx.Commit(ctx)
	}
	for i := range e.Plans {
		p := &e.Plans[i]
		data, err := json.Marshal(p.Specification)
		if err != nil {
			return e, err
		}
		err = tx.QueryRow(ctx, `INSERT INTO benchmark_plans(node_id,specification) VALUES($1,$2) RETURNING plan_id::text`, p.Specification.NodeID, data).Scan(&p.PlanID)
		if err != nil {
			return e, err
		}
		p.Status = "queued"
	}
	spec, err := json.Marshal(d)
	if err != nil {
		return e, err
	}
	resolved, err := json.Marshal(e.Plans)
	if err != nil {
		return e, err
	}
	var created time.Time
	err = tx.QueryRow(ctx, `INSERT INTO benchmark_experiments(name,spec_sha256,specification,resolved) VALUES($1,$2,$3,$4) RETURNING created_at`, e.Name, e.Digest, spec, resolved).Scan(&created)
	if err != nil {
		return e, err
	}
	e.CreatedAt = &created
	return e, tx.Commit(ctx)
}

func readExperiment(ctx context.Context, tx pgx.Tx, name string) (Experiment, error) {
	var e Experiment
	var spec, resolved []byte
	var created time.Time
	err := tx.QueryRow(ctx, `SELECT name,spec_sha256,specification,resolved,created_at FROM benchmark_experiments WHERE name=$1`, name).Scan(&e.Name, &e.Digest, &spec, &resolved, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, ErrNotFound
	}
	if err != nil {
		return e, err
	}
	e.CreatedAt = &created
	if err = json.Unmarshal(spec, &e.Specification); err != nil {
		return e, err
	}
	if err = json.Unmarshal(resolved, &e.Plans); err != nil {
		return e, err
	}
	for i := range e.Plans {
		if err = tx.QueryRow(ctx, `SELECT status FROM benchmark_plans WHERE plan_id=$1`, e.Plans[i].PlanID).Scan(&e.Plans[i].Status); err != nil {
			return e, err
		}
	}
	return e, nil
}

func (s *Store) Experiment(ctx context.Context, name, digest string, cancel bool) (Experiment, error) {
	var e Experiment
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return e, err
	}
	defer tx.Rollback(context.Background())
	e, err = readExperiment(ctx, tx, name)
	if err != nil {
		return e, err
	}
	if digest != "" && digest != e.Digest {
		return e, fmt.Errorf("%w: file differs from saved experiment", ErrConflict)
	}
	if cancel {
		ids := make([]string, len(e.Plans))
		for i, p := range e.Plans {
			ids[i] = p.PlanID
		}
		_, err = tx.Exec(ctx, `UPDATE benchmark_plans SET status='cancelled',completed_at=now() WHERE plan_id::text=ANY($1::text[]) AND status IN ('queued','leased','running')`, ids)
		if err != nil {
			return e, err
		}
		e, err = readExperiment(ctx, tx, name)
		if err != nil {
			return e, err
		}
	}
	return e, tx.Commit(ctx)
}
