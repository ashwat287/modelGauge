package controller

import (
	"context"
	"fmt"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

const StabilityThreshold = 0.10

type RunSummary struct {
	RunID                string                    `json:"run_id"`
	PlanID               string                    `json:"plan_id"`
	NodeID               string                    `json:"node_id"`
	ModelID              string                    `json:"model_id"`
	ExecutionMode        string                    `json:"execution_mode"`
	Status               string                    `json:"status"`
	InvalidReason        string                    `json:"invalid_reason,omitempty"`
	CompletedAt          string                    `json:"completed_at"`
	PeakRSSBytes         int64                     `json:"peak_rss_bytes,omitempty"`
	SafeReservationBytes int64                     `json:"safe_reservation_bytes,omitempty"`
	Cases                []StoredCase              `json:"cases"`
	Benchmark            *protocol.BenchmarkReport `json:"benchmark,omitempty"`
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

func summary(r Record) RunSummary {
	result := r.Result
	out := RunSummary{RunID: result.RunID, PlanID: result.PlanID, NodeID: result.NodeID, ModelID: r.Plan.Model.ID, ExecutionMode: string(result.ExecutionMode), Status: string(result.Status), CompletedAt: result.CompletedAt.Format("2006-01-02T15:04:05Z"), Cases: []StoredCase{}, Benchmark: result.Benchmark}
	for _, c := range result.Cases {
		out.Cases = append(out.Cases, StoredCase(c))
	}
	if result.Memory != nil {
		out.PeakRSSBytes = result.Memory.PeakRSSBytes
		out.SafeReservationBytes = result.Memory.SafeReservationBytes
	}
	return out
}
func (s *Store) ListResults(_ context.Context, node string) ([]RunSummary, error) {
	records, err := s.Records()
	if err != nil {
		return nil, err
	}
	out := []RunSummary{}
	for _, r := range records {
		if r.Result.NodeID == node {
			out = append(out, summary(r))
		}
	}
	return out, nil
}
func (s *Store) Recommend(ctx context.Context, node, model, mode string, contextTokens int, budget int64) (Recommendation, error) {
	rows, err := s.ListResults(ctx, node)
	if err != nil {
		return Recommendation{}, err
	}
	for _, run := range rows {
		if run.ModelID != model || run.ExecutionMode != mode {
			continue
		}
		out := Recommendation{NodeID: node, ModelID: model, ExecutionMode: mode, ContextTokens: contextTokens, RequiredMemoryBytes: run.SafeReservationBytes, MemoryBudgetBytes: budget, SourceRunID: run.RunID, AlgorithmVersion: "deterministic-v1"}
		for _, c := range run.Cases {
			if c.TokensPerSecond <= 0 || c.TokensPerSecondStddev/c.TokensPerSecond > StabilityThreshold {
				continue
			}
			if c.PromptTokens == contextTokens && c.GenerationTokens == 0 && c.TokensPerSecond > out.PromptTokensPerSecond {
				out.PromptThreads = c.Threads
				out.PromptTokensPerSecond = c.TokensPerSecond
			}
			if c.PromptTokens == 0 && c.GenerationTokens > 0 && c.TokensPerSecond > out.GenerationTokensPerSecond {
				out.GenerationThreads = c.Threads
				out.GenerationTokensPerSecond = c.TokensPerSecond
			}
		}
		if out.PromptThreads == 0 || out.GenerationThreads == 0 {
			continue
		}
		switch {
		case budget == 0:
			out.FitStatus = "conditional"
		case out.RequiredMemoryBytes <= budget:
			out.FitStatus = "fits"
		default:
			out.FitStatus = "does-not-fit"
		}
		return out, nil
	}
	return Recommendation{}, fmt.Errorf("no stable completed measurements for model %s and context %d", model, contextTokens)
}
