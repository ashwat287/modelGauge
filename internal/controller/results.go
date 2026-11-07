package controller

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const StabilityThreshold = 0.10

type RunSummary struct {
	RunID                string       `json:"run_id"`
	PlanID               string       `json:"plan_id"`
	NodeID               string       `json:"node_id"`
	ModelID              string       `json:"model_id"`
	ExecutionMode        string       `json:"execution_mode"`
	Status               string       `json:"status"`
	InvalidReason        string       `json:"invalid_reason,omitempty"`
	Recovered            bool         `json:"recovered,omitempty"`
	CompletedAt          string       `json:"completed_at"`
	PeakRSSBytes         int64        `json:"peak_rss_bytes,omitempty"`
	SafeReservationBytes int64        `json:"safe_reservation_bytes,omitempty"`
	Cases                []StoredCase `json:"cases"`
}

type StoredCase struct {
	CaseID                string  `json:"case_id"`
	Threads               int     `json:"threads"`
	PromptTokens          int     `json:"prompt_tokens"`
	GenerationTokens      int     `json:"generation_tokens"`
	Repetitions           int     `json:"repetitions"`
	TokensPerSecond       float64 `json:"tokens_per_second"`
	TokensPerSecondStddev float64 `json:"tokens_per_second_stddev"`
}

type Recommendation struct {
	NodeID                    string  `json:"node_id"`
	ModelID                   string  `json:"model_id"`
	ExecutionMode             string  `json:"execution_mode"`
	ContextTokens             int     `json:"context_tokens"`
	PromptThreads             int     `json:"prompt_threads,omitempty"`
	PromptTokensPerSecond     float64 `json:"prompt_tokens_per_second,omitempty"`
	GenerationThreads         int     `json:"generation_threads,omitempty"`
	GenerationTokensPerSecond float64 `json:"generation_tokens_per_second,omitempty"`
	RequiredMemoryBytes       int64   `json:"required_memory_bytes,omitempty"`
	MemoryBudgetBytes         int64   `json:"memory_budget_bytes,omitempty"`
	FitStatus                 string  `json:"fit_status"`
	SourceRunID               string  `json:"source_run_id,omitempty"`
	AlgorithmVersion          string  `json:"algorithm_version"`
}

func (s *Store) ListResults(ctx context.Context, nodeID string) ([]RunSummary, error) {
	rows, err := s.pool.Query(ctx, `
SELECT run.run_id, run.plan_id::text, run.node_id,
       plan.specification #>> '{model,id}', run.execution_mode, run.status,
       COALESCE(to_char(run.completed_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), ''),
       COALESCE(memory.peak_rss_bytes, 0), COALESCE(memory.safe_reservation_bytes, 0),COALESCE(run.invalid_reason,''),COALESCE(run.runtime_metadata->>'recovered','false')='true'
FROM benchmark_runs run
JOIN benchmark_plans plan ON plan.plan_id = run.plan_id
LEFT JOIN memory_measurements memory ON memory.run_id = run.run_id
WHERE run.node_id = $1 AND run.status IN ('completed', 'invalid', 'failed','cancelled')
ORDER BY run.completed_at DESC`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := []RunSummary{}
	for rows.Next() {
		var result RunSummary
		if err := rows.Scan(&result.RunID, &result.PlanID, &result.NodeID, &result.ModelID, &result.ExecutionMode, &result.Status, &result.CompletedAt, &result.PeakRSSBytes, &result.SafeReservationBytes, &result.InvalidReason, &result.Recovered); err != nil {
			return nil, err
		}
		cases, err := s.casesForRun(ctx, result.RunID)
		if err != nil {
			return nil, err
		}
		result.Cases = cases
		results = append(results, result)
	}
	return results, rows.Err()
}

func (s *Store) casesForRun(ctx context.Context, runID string) ([]StoredCase, error) {
	rows, err := s.pool.Query(ctx, `SELECT case_id, threads, prompt_tokens, generation_tokens, repetitions, average_tokens_per_second, stddev_tokens_per_second FROM benchmark_cases WHERE run_id = $1 ORDER BY case_id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cases := []StoredCase{}
	for rows.Next() {
		var item StoredCase
		if err := rows.Scan(&item.CaseID, &item.Threads, &item.PromptTokens, &item.GenerationTokens, &item.Repetitions, &item.TokensPerSecond, &item.TokensPerSecondStddev); err != nil {
			return nil, err
		}
		cases = append(cases, item)
	}
	return cases, rows.Err()
}

func (s *Store) Recommend(ctx context.Context, nodeID, modelID, mode string, contextTokens int, memoryBudget int64) (Recommendation, error) {
	var run RunSummary
	err := s.pool.QueryRow(ctx, `
SELECT run.run_id, run.plan_id::text, run.node_id, plan.specification #>> '{model,id}', run.execution_mode, run.status,
       COALESCE(to_char(run.completed_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), ''),
       COALESCE(memory.peak_rss_bytes, 0), COALESCE(memory.safe_reservation_bytes, 0)
FROM benchmark_runs run
JOIN benchmark_plans plan ON plan.plan_id = run.plan_id
LEFT JOIN memory_measurements memory ON memory.run_id = run.run_id
WHERE run.node_id = $1 AND plan.specification #>> '{model,id}' = $2 AND run.execution_mode = $3 AND run.status = 'completed'
  AND EXISTS (SELECT 1 FROM benchmark_cases c WHERE c.run_id = run.run_id AND c.prompt_tokens = $4 AND c.generation_tokens = 0)
ORDER BY run.completed_at DESC LIMIT 1`, nodeID, modelID, mode, contextTokens).Scan(&run.RunID, &run.PlanID, &run.NodeID, &run.ModelID, &run.ExecutionMode, &run.Status, &run.CompletedAt, &run.PeakRSSBytes, &run.SafeReservationBytes)
	if err != nil {
		if err == pgx.ErrNoRows {
			return Recommendation{}, ErrNotFound
		}
		return Recommendation{}, err
	}
	run.Cases, err = s.casesForRun(ctx, run.RunID)
	if err != nil {
		return Recommendation{}, err
	}
	recommendation := Recommendation{NodeID: nodeID, ModelID: modelID, ExecutionMode: mode, ContextTokens: contextTokens, RequiredMemoryBytes: run.SafeReservationBytes, MemoryBudgetBytes: memoryBudget, SourceRunID: run.RunID, AlgorithmVersion: "deterministic-v1"}
	for _, item := range run.Cases {
		if !stable(item) {
			continue
		}
		if item.PromptTokens == contextTokens && item.GenerationTokens == 0 && item.TokensPerSecond > recommendation.PromptTokensPerSecond {
			recommendation.PromptThreads, recommendation.PromptTokensPerSecond = item.Threads, item.TokensPerSecond
		}
		if item.PromptTokens == 0 && item.GenerationTokens > 0 && item.TokensPerSecond > recommendation.GenerationTokensPerSecond {
			recommendation.GenerationThreads, recommendation.GenerationTokensPerSecond = item.Threads, item.TokensPerSecond
		}
	}
	if recommendation.PromptThreads == 0 || recommendation.GenerationThreads == 0 {
		return Recommendation{}, fmt.Errorf("no stable prompt and generation measurements in run %s", run.RunID)
	}
	switch {
	case memoryBudget == 0:
		recommendation.FitStatus = "conditional"
	case recommendation.RequiredMemoryBytes <= memoryBudget:
		recommendation.FitStatus = "fits"
	default:
		recommendation.FitStatus = "does-not-fit"
	}
	return recommendation, nil
}

func stable(item StoredCase) bool {
	return item.TokensPerSecond > 0 && item.TokensPerSecondStddev/item.TokensPerSecond <= StabilityThreshold
}
