package benchmark

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

func sameEngineResumePlan(previous, current protocol.Plan, binary string) bool {
	// Older agents resolved the binary before saving the plan; discovery now belongs to the runner.
	if previous.Runtime.Binary == "" {
		previous.Runtime.Binary = binary
	}
	if current.Runtime.Binary == "" {
		current.Runtime.Binary = binary
	}
	before, beforeErr := json.Marshal(previous)
	after, afterErr := json.Marshal(current)
	return beforeErr == nil && afterErr == nil && bytes.Equal(before, after)
}

var engineCaseID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,95}$`)

func validateEnginePlan(p protocol.Plan) error {
	var problems []error
	if p.Benchmark != nil {
		problems = append(problems, errors.New("llama.cpp uses the existing engine plan fields"))
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

	files := map[string]bool{"plan.json": true, "result.json": true, "preflight.json": true, "postflight.json": true, "memory.checkpoint.json": true, "runtime.sha256": true}
	names := []string{p.MemoryCase.ID + ".json", p.MemoryCase.ID + ".time.txt"}
	for _, c := range p.Cases {
		names = append(names, c.ID+".json", c.ID+"-warmup.json")
	}
	for _, name := range names {
		if files[name] {
			problems = append(problems, fmt.Errorf("evidence filename collision: %s", name))
		}
		files[name] = true
	}

	return errors.Join(problems...)
}

func validateCase(id string, threads, prompt, generation int) error {
	switch {
	case !engineCaseID.MatchString(id):
		return errors.New("id must be a simple filename of at most 96 characters")
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
