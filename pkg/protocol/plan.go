// Package protocol contains versioned messages shared by the CLI, controller,
// and node agent. These types are the compatibility boundary of ModelGauge.
package protocol

import (
	"errors"
	"fmt"
	"strings"
)

const BenchmarkPlanV1 = "benchmark-plan/v1"

// ExecutionMode identifies the environment that produced a measurement.
type ExecutionMode string

const (
	ExecutionModeNative ExecutionMode = "native-isolated"
	ExecutionModeLima   ExecutionMode = "lima-linux"
)

// Plan describes one reproducible benchmark job.
type Plan struct {
	SchemaVersion string        `json:"schema_version"`
	PlanID        string        `json:"plan_id,omitempty"`
	NodeID        string        `json:"node_id"`
	ExecutionMode ExecutionMode `json:"execution_mode"`
	Runtime       Runtime       `json:"runtime"`
	Model         Model         `json:"model"`
	Defaults      Defaults      `json:"defaults"`
	Cases         []Case        `json:"cases"`
	MemoryCase    MemoryCase    `json:"memory_case"`
}

type Runtime struct {
	Kind          string `json:"kind"`
	Binary        string `json:"binary,omitempty"`
	BinaryVersion string `json:"binary_version,omitempty"`
}

type Model struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256,omitempty"`
}

type Defaults struct {
	WarmupRepetitions  int `json:"warmup_repetitions"`
	MeasureRepetitions int `json:"measure_repetitions"`
	GPULayers          int `json:"gpu_layers"`
}

type Case struct {
	ID               string `json:"id"`
	Threads          int    `json:"threads"`
	PromptTokens     int    `json:"n_prompt"`
	GenerationTokens int    `json:"n_gen"`
}

type MemoryCase struct {
	ID               string `json:"id"`
	Threads          int    `json:"threads"`
	PromptTokens     int    `json:"n_prompt"`
	GenerationTokens int    `json:"n_gen"`
}

// Validate checks the invariants required before a node accepts a plan.
func (p Plan) Validate() error {
	var problems []error

	if p.SchemaVersion != BenchmarkPlanV1 {
		problems = append(problems, fmt.Errorf("schema_version must be %q", BenchmarkPlanV1))
	}
	if strings.TrimSpace(p.NodeID) == "" {
		problems = append(problems, errors.New("node_id is required"))
	}
	if p.ExecutionMode != ExecutionModeNative && p.ExecutionMode != ExecutionModeLima {
		problems = append(problems, errors.New("execution_mode must be native-isolated or lima-linux"))
	}
	if p.Runtime.Kind != "llama.cpp" {
		problems = append(problems, errors.New("runtime.kind must be llama.cpp"))
	}
	if strings.TrimSpace(p.Model.ID) == "" {
		problems = append(problems, errors.New("model.id is required"))
	}
	if strings.TrimSpace(p.Model.Path) == "" {
		problems = append(problems, errors.New("model.path is required"))
	}
	if p.Defaults.WarmupRepetitions < 0 {
		problems = append(problems, errors.New("warmup_repetitions cannot be negative"))
	}
	if p.Defaults.MeasureRepetitions < 1 {
		problems = append(problems, errors.New("measure_repetitions must be at least one"))
	}
	if len(p.Cases) == 0 {
		problems = append(problems, errors.New("at least one benchmark case is required"))
	}

	seen := make(map[string]struct{}, len(p.Cases))
	for _, c := range p.Cases {
		if err := validateCase(c.ID, c.Threads, c.PromptTokens, c.GenerationTokens); err != nil {
			problems = append(problems, fmt.Errorf("case %q: %w", c.ID, err))
		}
		if _, exists := seen[c.ID]; exists {
			problems = append(problems, fmt.Errorf("case %q is duplicated", c.ID))
		}
		seen[c.ID] = struct{}{}
	}
	if err := validateCase(p.MemoryCase.ID, p.MemoryCase.Threads, p.MemoryCase.PromptTokens, p.MemoryCase.GenerationTokens); err != nil {
		problems = append(problems, fmt.Errorf("memory_case: %w", err))
	}

	return errors.Join(problems...)
}

func validateCase(id string, threads, prompt, generation int) error {
	switch {
	case strings.TrimSpace(id) == "":
		return errors.New("id is required")
	case threads < 1:
		return errors.New("threads must be at least one")
	case prompt < 0 || generation < 0:
		return errors.New("token counts cannot be negative")
	case prompt == 0 && generation == 0:
		return errors.New("prompt and generation cannot both be zero")
	default:
		return nil
	}
}

// ExamplePlan returns the smallest useful CPU-only llama.cpp plan.
func ExamplePlan() Plan {
	return Plan{
		SchemaVersion: BenchmarkPlanV1,
		NodeID:        "replace-with-node-id",
		ExecutionMode: ExecutionModeNative,
		Runtime: Runtime{
			Kind: "llama.cpp",
		},
		Model: Model{
			ID:   "replace-with-model-id",
			Path: "/absolute/path/to/model.gguf",
		},
		Defaults: Defaults{
			WarmupRepetitions:  3,
			MeasureRepetitions: 5,
			GPULayers:          0,
		},
		Cases: []Case{
			{ID: "prompt-512-t1", Threads: 1, PromptTokens: 512},
			{ID: "prompt-512-t2", Threads: 2, PromptTokens: 512},
			{ID: "generation-128-t1", Threads: 1, GenerationTokens: 128},
			{ID: "generation-128-t2", Threads: 2, GenerationTokens: 128},
		},
		MemoryCase: MemoryCase{
			ID: "memory-p512-g128-t2", Threads: 2, PromptTokens: 512, GenerationTokens: 128,
		},
	}
}
