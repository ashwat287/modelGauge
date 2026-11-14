package benchmark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nerdylucifer/modelgauge/internal/platform"
	"io"
	"path/filepath"
	"strings"

	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

// servingSettings is module-owned declaration configuration.
type servingSettings struct {
	API              string          `json:"api"`
	Suite            string          `json:"suite"`
	Concurrency      []int           `json:"concurrency"`
	Requests         int             `json:"requests"`
	OutputTokens     int             `json:"output_tokens"`
	SustainSeconds   float64         `json:"sustain_seconds"`
	RequestTimeout   float64         `json:"request_timeout_seconds"`
	ReadinessTimeout float64         `json:"readiness_timeout_seconds"`
	SampleInterval   float64         `json:"sample_interval_seconds"`
	GPULayers        int             `json:"gpu_layers"`
	RecordOutputs    bool            `json:"record_outputs"`
	Targets          servingTargets  `json:"targets"`
	Prompts          []servingPrompt `json:"prompts"`
	Cases            []servingCase   `json:"cases,omitempty"`
}

func servingProfile(ctx context.Context, options planOptions, config json.RawMessage) ([]protocol.Plan, error) {
	if options.Repetitions != 0 {
		return nil, errors.New("node-serving measures config.requests; options.repetitions is for direct throughput profiles")
	}
	if options.ModelID == "" {
		options.ModelID = strings.TrimSuffix(filepath.Base(options.ModelPath), filepath.Ext(options.ModelPath))
	}
	if options.ModelSHA256 == "" {
		digest, err := platform.SHA256File(ctx, options.ModelPath)
		if err != nil {
			return nil, err
		}
		options.ModelSHA256 = digest
	}
	settings := servingSettings{API: "llama.cpp", Suite: "deployment", Concurrency: []int{1, 2}, Requests: 10, OutputTokens: 128, SustainSeconds: 60, RequestTimeout: 120, ReadinessTimeout: 300, SampleInterval: 1}
	if len(config) != 0 {
		decoder := json.NewDecoder(strings.NewReader(string(config)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&settings); err != nil {
			return nil, fmt.Errorf("node-serving config: %w", err)
		}
		if decoder.Decode(new(any)) != io.EOF {
			return nil, errors.New("node-serving config must contain exactly one JSON object")
		}
	}
	if settings.Suite != "basic" && settings.Suite != "deployment" {
		return nil, errors.New("node-serving suite must be basic or deployment")
	}
	if len(settings.Concurrency) == 0 || len(settings.Concurrency) > 16 || settings.Requests < 1 || settings.SustainSeconds <= 0 || !finite(settings.SustainSeconds) || settings.SustainSeconds > 86400 {
		return nil, errors.New("node-serving requires 1..16 concurrency settings, positive requests and sustain_seconds <= 86400")
	}
	if len(settings.Prompts) != 0 && len(options.Contexts) > 0 {
		return nil, errors.New("use prompt fixtures or synthetic contexts, not both (set matrix.contexts: [] for fixtures)")
	}
	threads := options.Threads
	if len(threads) == 0 {
		threads = []int{1, 2}
	}
	contexts := options.Contexts
	if len(contexts) == 0 {
		contexts = []int{0}
	}
	maxContext, parallel := 512, 1
	for _, n := range contexts {
		if n < 0 || n > 131072 {
			return nil, errors.New("invalid serving prompt context")
		}
		maxContext = max(maxContext, n)
	}
	for _, n := range settings.Concurrency {
		parallel = max(parallel, n)
	}
	warmups := options.Warmups
	if warmups == 0 {
		warmups = 2
	}
	plans := []protocol.Plan{}
	seen := map[int]bool{}
	for _, n := range threads {
		if seen[n] {
			continue
		}
		seen[n] = true
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s := &servingPlan{API: settings.API, ServedModel: options.ModelID, RequestTimeout: settings.RequestTimeout, ReadinessTimeout: settings.ReadinessTimeout, SampleInterval: settings.SampleInterval, Seed: 42, RecordOutputs: settings.RecordOutputs, Targets: settings.Targets, Server: &servingServer{Threads: n, ContextTokens: maxContext + max(256, settings.OutputTokens) + 256, Parallel: parallel, GPULayers: settings.GPULayers}}
		for _, tokens := range contexts {
			for _, workers := range settings.Concurrency {
				s.Cases = append(s.Cases, servingCase{ID: fmt.Sprintf("load-p%d-c%d", tokens, workers), Scenario: "load", Concurrency: workers, Requests: settings.Requests, WarmupRequests: warmups, PromptTokens: tokens, OutputTokens: settings.OutputTokens, Prompts: settings.Prompts})
			}
		}
		if settings.Suite == "deployment" {
			steady := s.Cases[len(s.Cases)-1]
			steady.ID, steady.Scenario, steady.Requests, steady.DurationSeconds = "sustained", "sustained", 0, settings.SustainSeconds
			s.Cases = append(s.Cases, steady)
			burst := s.Cases[len(s.Cases)-2]
			burst.ID, burst.Scenario, burst.Requests, burst.WarmupRequests, burst.RequestRate, burst.RecoveryProbe = "burst", "burst", 10, 0, 4, true
			s.Cases = append(s.Cases, burst)
			if settings.API == "llama.cpp" {
				for _, cache := range []bool{false, true} {
					s.Cases = append(s.Cases, servingCase{ID: fmt.Sprintf("cache-%t", cache), Scenario: "cache", Concurrency: 1, Requests: max(2, settings.Requests), WarmupRequests: 1, OutputTokens: settings.OutputTokens, CachePrompt: cache, Prompts: []servingPrompt{{Text: strings.Repeat("Record repeatable model measurements. ", 32) + "Summarize this document."}}})
				}
			} else {
				s.Cases = append(s.Cases, servingCase{ID: "conversation", Scenario: "conversation", Concurrency: 1, Requests: 4, OutputTokens: settings.OutputTokens, ContinueConversation: true, Prompts: []servingPrompt{{Text: "Remember the codeword CYAN. Acknowledge briefly."}, {Text: "What codeword did I ask you to remember?", Expect: &servingChecks{Contains: []string{"CYAN"}}}}})
			}
			s.Cases = append(s.Cases,
				servingCase{ID: "format-smoke", Scenario: "quality-smoke", Concurrency: 1, Requests: 3, OutputTokens: min(64, settings.OutputTokens), Prompts: []servingPrompt{{Text: `Return only the JSON object {"result":4}.`, Expect: &servingChecks{JSON: true, JSONSubset: json.RawMessage(`{"result":4}`)}}}},
				servingCase{ID: "cancel", Scenario: "cancellation", Concurrency: 1, Requests: 2, OutputTokens: max(256, settings.OutputTokens), CancelAfterMS: 250, RecoveryProbe: true},
				servingCase{ID: "idle-resume", Scenario: "idle-resume", Concurrency: 1, Requests: 1, OutputTokens: settings.OutputTokens, IdleBeforeSeconds: 5},
			)
		}
		if len(settings.Cases) > 0 {
			s.Cases = settings.Cases
			for _, c := range s.Cases {
				s.Server.Parallel = max(s.Server.Parallel, c.Concurrency)
				s.Server.ContextTokens = max(s.Server.ContextTokens, c.PromptTokens+c.OutputTokens+256)
			}
		}
		plan := protocol.Plan{SchemaVersion: protocol.BenchmarkPlanV1, NodeID: options.NodeID, ExecutionMode: options.ExecutionMode, Runtime: protocol.Runtime{Kind: "llama-server", Binary: options.Binary}, Model: protocol.Model{ID: options.ModelID, Path: options.ModelPath, SHA256: options.ModelSHA256}}
		if plan.ExecutionMode == "" {
			plan.ExecutionMode = protocol.ExecutionModeNative
		}
		if err := setConfig(&plan, "serving", s); err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, nil
}
