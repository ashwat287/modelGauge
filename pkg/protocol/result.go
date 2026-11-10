package protocol

import "time"

// RunStatus is terminal when completed, invalid, failed, or cancelled.
type RunStatus string

const (
	RunStatusRunning   RunStatus = "running"
	RunStatusCompleted RunStatus = "completed"
	RunStatusInvalid   RunStatus = "invalid"
	RunStatusFailed    RunStatus = "failed"
	RunStatusCancelled RunStatus = "cancelled"
)

// Result is the normalized, durable record emitted by an agent.
type Result struct {
	SchemaVersion   string            `json:"schema_version"`
	RunID           string            `json:"run_id"`
	PlanID          string            `json:"plan_id"`
	NodeID          string            `json:"node_id"`
	ExecutionMode   ExecutionMode     `json:"execution_mode"`
	Status          RunStatus         `json:"status"`
	InvalidReason   string            `json:"invalid_reason,omitempty"`
	StartedAt       time.Time         `json:"started_at"`
	CompletedAt     time.Time         `json:"completed_at"`
	Cases           []CaseResult      `json:"cases"`
	Memory          *MemoryResult     `json:"memory,omitempty"`
	RuntimeMetadata map[string]string `json:"runtime_metadata,omitempty"`
	Artifacts       []Artifact        `json:"artifacts,omitempty"`
	Benchmark       *BenchmarkReport  `json:"benchmark,omitempty"`
}

type CaseResult struct {
	CaseID                string  `json:"case_id"`
	Threads               int     `json:"threads"`
	PromptTokens          int     `json:"prompt_tokens"`
	GenerationTokens      int     `json:"generation_tokens"`
	Repetitions           int     `json:"repetitions"`
	TokensPerSecond       float64 `json:"tokens_per_second"`
	TokensPerSecondStddev float64 `json:"tokens_per_second_stddev"`
}

type MemoryResult struct {
	CaseID               string `json:"case_id"`
	PeakRSSBytes         int64  `json:"peak_rss_bytes"`
	SafeReservationBytes int64  `json:"safe_reservation_bytes"`
}

// Artifact describes an evidence file produced by a benchmark run.
type Artifact struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}
