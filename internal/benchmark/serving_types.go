package benchmark

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
	"math"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// ServingPlan observes an existing endpoint; it never starts or reconfigures it.
type ServingPlan struct {
	Endpoint         string            `json:"endpoint"`
	API              string            `json:"api"`
	ServedModel      string            `json:"served_model,omitempty"`
	RequestTimeout   float64           `json:"request_timeout_seconds"`
	ReadinessTimeout float64           `json:"readiness_timeout_seconds"`
	ReadinessPath    string            `json:"readiness_path,omitempty"`
	MetricsURL       string            `json:"metrics_url,omitempty"`
	SampleInterval   float64           `json:"sample_interval_seconds"`
	LocalPID         int               `json:"local_server_pid,omitempty"`
	RecordOutputs    bool              `json:"record_outputs,omitempty"`
	Seed             int               `json:"seed"`
	Temperature      float64           `json:"temperature"`
	RuntimeSettings  map[string]string `json:"operator_runtime_settings,omitempty"`
	Targets          ServingTargets    `json:"targets"`
	Cases            []ServingCase     `json:"cases"`
}

type ServingTargets struct {
	TTFTMS    float64 `json:"ttft_ms,omitempty"`
	TPOTMS    float64 `json:"tpot_ms,omitempty"`
	LatencyMS float64 `json:"latency_ms,omitempty"`
}

type ServingCase struct {
	ID                   string          `json:"id"`
	Scenario             string          `json:"scenario"`
	Concurrency          int             `json:"concurrency"`
	Requests             int             `json:"requests"`
	DurationSeconds      float64         `json:"duration_seconds,omitempty"`
	RequestRate          float64         `json:"request_rate,omitempty"`
	WarmupRequests       int             `json:"warmup_requests"`
	PromptTokens         int             `json:"target_prompt_tokens,omitempty"`
	OutputTokens         int             `json:"max_output_tokens"`
	CachePrompt          bool            `json:"cache_prompt"`
	IgnoreEOS            bool            `json:"ignore_eos,omitempty"`
	ContinueConversation bool            `json:"continue_conversation,omitempty"`
	CancelAfterMS        float64         `json:"cancel_after_ms,omitempty"`
	RecoveryProbe        bool            `json:"recovery_probe,omitempty"`
	IdleBeforeSeconds    float64         `json:"idle_before_seconds,omitempty"`
	Model                string          `json:"served_model,omitempty"`
	Prompts              []ServingPrompt `json:"prompts,omitempty"`
}

type ServingMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ServingPrompt struct {
	ID       string           `json:"id"`
	Text     string           `json:"text,omitempty"`
	Messages []ServingMessage `json:"messages,omitempty"`
	Tools    json.RawMessage  `json:"tools,omitempty"`
	Expect   *ServingChecks   `json:"expect,omitempty"`
}

type ServingChecks struct {
	Exact       *string           `json:"exact,omitempty"`
	Contains    []string          `json:"contains,omitempty"`
	NotContains []string          `json:"not_contains,omitempty"`
	Regex       string            `json:"regex,omitempty"`
	JSON        bool              `json:"json,omitempty"`
	JSONSubset  json.RawMessage   `json:"json_subset,omitempty"`
	ToolCalls   []ServingToolCall `json:"tool_calls,omitempty"`
}

type ServingToolCall struct {
	RawArguments string          `json:"raw_arguments,omitempty"`
	Name         string          `json:"name"`
	Arguments    json.RawMessage `json:"arguments"`
}

func ServingURL(value string) (*url.URL, error) {
	if len(value) > 4096 {
		return nil, errors.New("endpoint URL exceeds 4096 bytes")
	}
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(value, "\r\n") {
		return nil, errors.New("endpoint must be an HTTP(S) URL without credentials, query or fragment")
	}
	return u, nil
}

func (s ServingPlan) Validate() error {
	var problems []error
	if len(s.ServedModel) > 512 {
		problems = append(problems, errors.New("served model alias exceeds 512 bytes"))
	}
	if _, err := ServingURL(s.Endpoint); err != nil {
		problems = append(problems, err)
	}
	if s.MetricsURL != "" {
		if _, err := ServingURL(s.MetricsURL); err != nil {
			problems = append(problems, fmt.Errorf("metrics: %w", err))
		}
	}
	if s.API != "openai" && s.API != "llama.cpp" {
		problems = append(problems, errors.New("serving.api must be openai or llama.cpp"))
	}
	if s.RequestTimeout <= 0 || s.RequestTimeout > 86400 || !finite(s.RequestTimeout) || s.ReadinessTimeout <= 0 || s.ReadinessTimeout > 3600 || !finite(s.ReadinessTimeout) {
		problems = append(problems, errors.New("serving timeouts must be positive (request <= 86400s, readiness <= 3600s)"))
	}
	if s.SampleInterval < .1 || s.SampleInterval > 60 || !finite(s.SampleInterval) || s.LocalPID < 0 {
		problems = append(problems, errors.New("sample interval must be 0.1..60 seconds and local_server_pid nonnegative"))
	}
	if s.ReadinessPath != "" && (!strings.HasPrefix(s.ReadinessPath, "/") || strings.HasPrefix(s.ReadinessPath, "//") || strings.ContainsAny(s.ReadinessPath, "?#\r\n")) {
		problems = append(problems, errors.New("readiness_path must be an absolute URL path"))
	}
	for _, v := range []float64{s.Temperature, s.Targets.TTFTMS, s.Targets.TPOTMS, s.Targets.LatencyMS} {
		if v < 0 || !finite(v) {
			problems = append(problems, errors.New("temperature and latency targets must be finite and nonnegative"))
		}
	}
	allowed := map[string]bool{}
	for _, key := range strings.Fields("threads threads_batch context_capacity parallel_slots batch_size ubatch_size gpu_layers cache_type_k cache_type_v flash_attention runtime_revision model_sha256 quantization power_mode background_workload filesystem_cache_state thermal_condition") {
		allowed[key] = true
	}
	for key, value := range s.RuntimeSettings {
		if !allowed[key] || len(value) > 256 {
			problems = append(problems, fmt.Errorf("unsupported or oversized runtime setting %q", key))
		}
	}
	if len(s.Cases) == 0 || len(s.Cases) > 128 {
		problems = append(problems, errors.New("serving requires 1..128 cases"))
	}
	ids := map[string]bool{}
	for _, c := range s.Cases {
		if len(c.Model) > 512 {
			problems = append(problems, errors.New("case model alias exceeds 512 bytes"))
		}
		if c.IdleBeforeSeconds > 0 && c.WarmupRequests > 0 {
			problems = append(problems, errors.New("idle-resume cases must have zero warmups"))
		}

		if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,95}$`).MatchString(c.ID) || ids[c.ID] {
			problems = append(problems, fmt.Errorf("invalid or duplicate serving case ID %q", c.ID))
		}
		ids[c.ID] = true
		if c.Concurrency < 1 || c.Concurrency > 256 || c.Requests < 0 || c.Requests > 100000 || (c.Requests == 0 && c.DurationSeconds <= 0) || c.WarmupRequests < 0 || c.WarmupRequests > 100 {
			problems = append(problems, fmt.Errorf("case %s: invalid concurrency/request/warmup limits", c.ID))
		}
		for _, v := range []float64{c.DurationSeconds, c.RequestRate, c.CancelAfterMS, c.IdleBeforeSeconds} {
			if !finite(v) || v < 0 {
				problems = append(problems, fmt.Errorf("case %s: load and timing parameters must be finite and nonnegative", c.ID))
			}
		}
		if c.DurationSeconds > 86400 || c.RequestRate > 10000 || c.IdleBeforeSeconds > 3600 || c.CancelAfterMS >= s.RequestTimeout*1000 {
			problems = append(problems, fmt.Errorf("case %s: timing/rate limit exceeded", c.ID))
		}
		if c.OutputTokens < 1 || c.OutputTokens > 32768 || c.PromptTokens < 0 || c.PromptTokens > 131072 {
			problems = append(problems, fmt.Errorf("case %s: invalid token limits", c.ID))
		}
		if c.ContinueConversation && (s.API != "openai" || c.RequestRate != 0 || c.PromptTokens != 0) {
			problems = append(problems, fmt.Errorf("case %s: conversations require openai, closed-loop load and explicit prompts", c.ID))
		}
		if s.API == "openai" && c.IgnoreEOS {
			problems = append(problems, fmt.Errorf("case %s: ignore_eos is only supported by llama.cpp completion", c.ID))
		}
		if s.API == "openai" && c.CachePrompt {
			problems = append(problems, fmt.Errorf("case %s: explicit cache_prompt control requires the native llama.cpp API; OpenAI caching is server-controlled", c.ID))
		}
		if c.PromptTokens > 0 && len(c.Prompts) > 0 {
			problems = append(problems, fmt.Errorf("case %s: choose generated token-length input or explicit prompts", c.ID))
		}
		if len(c.Prompts) > 128 {
			problems = append(problems, fmt.Errorf("case %s: at most 128 prompt fixtures", c.ID))
		}
		for _, p := range c.Prompts {
			if len(p.Text) > 1<<20 || len(p.Messages) > 128 || (p.Text != "" && len(p.Messages) != 0) || (s.API == "llama.cpp" && (len(p.Messages) > 0 || len(p.Tools) > 0)) {
				problems = append(problems, fmt.Errorf("case %s: invalid prompt shape or unsupported API feature", c.ID))
			}
			for _, m := range p.Messages {
				if (m.Role != "system" && m.Role != "user" && m.Role != "assistant") || len(m.Content) > 1<<20 {
					problems = append(problems, fmt.Errorf("case %s: invalid conversation message", c.ID))
				}
			}
			if len(p.Tools) > 0 && (!json.Valid(p.Tools) || !strings.HasPrefix(strings.TrimSpace(string(p.Tools)), "[") || len(p.Tools) > 64<<10) {
				problems = append(problems, fmt.Errorf("case %s: tools must be a JSON array <=64 KiB", c.ID))
			}
			if e := p.Expect; e != nil {
				if e.Exact == nil && len(e.Contains) == 0 && len(e.NotContains) == 0 && e.Regex == "" && !e.JSON && len(e.JSONSubset) == 0 && len(e.ToolCalls) == 0 {
					problems = append(problems, fmt.Errorf("case %s: empty quality checks", c.ID))
				}
				if _, err := regexp.Compile(e.Regex); err != nil {
					problems = append(problems, fmt.Errorf("case %s: invalid quality regex", c.ID))
				}
				if len(e.JSONSubset) > 0 && !json.Valid(e.JSONSubset) {
					problems = append(problems, fmt.Errorf("case %s: invalid expected JSON", c.ID))
				}
				for _, call := range e.ToolCalls {
					if call.Name == "" || !json.Valid(call.Arguments) {
						problems = append(problems, fmt.Errorf("case %s: invalid expected tool call", c.ID))
					}
				}
			}
		}
	}
	return errors.Join(problems...)
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

type Distribution struct {
	Count  int     `json:"count"`
	Mean   float64 `json:"mean"`
	Stddev float64 `json:"stddev"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
	P50    float64 `json:"p50"`
	P90    float64 `json:"p90"`
	P95    float64 `json:"p95"`
	P99    float64 `json:"p99"`
}

type ServingReport struct {
	ModelID         string              `json:"model_id"`
	Targets         ServingTargets      `json:"targets"`
	SchemaVersion   string              `json:"schema_version"`
	Endpoint        string              `json:"endpoint"`
	API             string              `json:"api"`
	ServedModel     string              `json:"served_model,omitempty"`
	StartedAt       time.Time           `json:"started_at"`
	CompletedAt     time.Time           `json:"completed_at"`
	ReadinessWaitMS float64             `json:"readiness_wait_ms"`
	Settings        map[string]string   `json:"operator_runtime_settings,omitempty"`
	Cases           []ServingCaseResult `json:"cases"`
	Warnings        []string            `json:"warnings"`
	Unavailable     []string            `json:"unavailable"`
}

type ServingCaseResult struct {
	ObservedModels        []string         `json:"observed_models,omitempty"`
	CacheAccounted        int              `json:"cache_accounted_requests"`
	ID                    string           `json:"id"`
	Scenario              string           `json:"scenario"`
	Concurrency           int              `json:"concurrency"`
	TargetPromptTokens    int              `json:"target_prompt_tokens"`
	MaxOutputTokens       int              `json:"max_output_tokens"`
	ServedModel           string           `json:"served_model,omitempty"`
	CachePrompt           bool             `json:"cache_prompt"`
	StartedAt             time.Time        `json:"started_at"`
	ElapsedSeconds        float64          `json:"elapsed_seconds"`
	DispatchSeconds       float64          `json:"dispatch_seconds"`
	Offered               int              `json:"offered"`
	Dispatched            int              `json:"dispatched"`
	Successful            int              `json:"successful"`
	Errors                int              `json:"errors"`
	Timeouts              int              `json:"timeouts"`
	Incomplete            int              `json:"incomplete"`
	Cancelled             int              `json:"cancelled"`
	ClientCapacityDrops   int              `json:"client_capacity_drops"`
	OutputLimited         int              `json:"output_limited"`
	ContextTruncated      int              `json:"context_truncated"`
	SLOEvaluated          int              `json:"slo_evaluated"`
	SLOMet                int              `json:"slo_met"`
	Goodput               *float64         `json:"goodput_requests_per_second"`
	RequestsPerSecond     float64          `json:"successful_requests_per_second"`
	InputTokens           int              `json:"known_input_tokens"`
	OutputTokens          int              `json:"known_output_tokens"`
	TokenAccounted        int              `json:"token_accounted_requests"`
	InputTokensPerSecond  *float64         `json:"input_tokens_per_second"`
	OutputTokensPerSecond *float64         `json:"output_tokens_per_second"`
	CachedTokens          int              `json:"known_cached_tokens"`
	CacheHitRequests      int              `json:"cache_hit_requests"`
	QualityEvaluated      int              `json:"quality_evaluated"`
	QualityPassed         int              `json:"quality_passed"`
	QualityPassRate       *float64         `json:"quality_pass_rate"`
	LatencyMS             *Distribution    `json:"latency_ms"`
	TTFTMS                *Distribution    `json:"ttft_ms"`
	FirstAnswerMS         *Distribution    `json:"first_answer_ms"`
	TPOTMS                *Distribution    `json:"tpot_ms"`
	ChunkGapMS            *Distribution    `json:"stream_chunk_gap_ms"`
	ITLMS                 *Distribution    `json:"observed_token_gap_ms"`
	LongestStallMS        *Distribution    `json:"longest_stream_stall_ms"`
	DispatchDelayMS       *Distribution    `json:"dispatch_delay_ms"`
	ScheduledLatencyMS    *Distribution    `json:"scheduled_latency_ms"`
	CancelAckMS           *Distribution    `json:"client_cancel_ack_ms"`
	FirstRequestMS        *float64         `json:"first_request_latency_ms"`
	RecoveryProbe         *ServingProbe    `json:"recovery_probe,omitempty"`
	Resources             ServingResources `json:"resources"`
	Windows               []ServingWindow  `json:"windows"`
	Warnings              []string         `json:"warnings"`
}

type ServingProbe struct {
	Successful bool    `json:"successful"`
	LatencyMS  float64 `json:"latency_ms"`
	Error      string  `json:"error,omitempty"`
}

type ServingWindow struct {
	StartSeconds float64  `json:"start_seconds"`
	EndSeconds   float64  `json:"end_seconds"`
	Successful   int      `json:"successful"`
	TTFTMS       *float64 `json:"mean_ttft_ms"`
	OutputRate   *float64 `json:"output_tokens_per_second"`
}

type ServingResources struct {
	ObservationSeconds      float64                 `json:"observation_seconds"`
	AverageCPUPercent       *float64                `json:"process_average_cpu_percent"`
	GPUEnergyJoules         *float64                `json:"node_gpu_energy_joules"`
	GPUJoulesPerOutputToken *float64                `json:"node_gpu_joules_per_output_token"`
	Scope                   string                  `json:"scope"`
	Samples                 int                     `json:"samples"`
	SampledPeakRSSBytes     *float64                `json:"sampled_peak_rss_bytes"`
	SampledPeakPSSBytes     *float64                `json:"sampled_peak_pss_bytes"`
	LifetimePeakRSSBytes    *float64                `json:"process_lifetime_peak_rss_bytes"`
	InitialRSSBytes         *float64                `json:"initial_rss_bytes"`
	FinalRSSBytes           *float64                `json:"final_rss_bytes"`
	CPUSeconds              *float64                `json:"process_cpu_seconds"`
	CPUSecondsPerRequest    *float64                `json:"process_cpu_seconds_per_successful_request"`
	ReadBytes               *float64                `json:"process_read_bytes"`
	WriteBytes              *float64                `json:"process_write_bytes"`
	MajorFaults             *float64                `json:"process_major_faults"`
	MinorFaults             *float64                `json:"process_minor_faults"`
	ContextSwitches         *float64                `json:"process_context_switches"`
	NodeEnergyJoules        *float64                `json:"node_cpu_package_energy_joules"`
	JoulesPerOutputToken    *float64                `json:"node_cpu_package_joules_per_output_token"`
	Observed                map[string]Distribution `json:"observed"`
	Unavailable             []string                `json:"unavailable"`
}

func (r ServingReport) Validate() error {
	if r.SchemaVersion != "modelgauge-serving/v1" || len(r.Cases) > 128 {
		return errors.New("invalid serving report schema or case count")
	}
	if _, err := ServingURL(r.Endpoint); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, c := range r.Cases {
		if c.ID == "" || seen[c.ID] || c.ElapsedSeconds < 0 || !finite(c.ElapsedSeconds) || c.Offered < 0 || c.Dispatched < 0 || c.Successful < 0 || c.Errors < 0 || c.Cancelled < 0 || c.ClientCapacityDrops < 0 || c.Successful+c.Errors+c.Cancelled != c.Dispatched || c.Dispatched+c.ClientCapacityDrops != c.Offered {
			return errors.New("invalid serving case accounting")
		}
		seen[c.ID] = true
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if len(data) > 5<<19 {
		return errors.New("serving summary exceeds 2.5 MiB; use smaller plans (raw evidence remains separate)")
	}
	return nil
}

func DecodeServingPlan(plan protocol.Plan) (*ServingPlan, error) {
	if plan.Benchmark == nil || plan.Benchmark.Kind != "serving" {
		return nil, errors.New("provide a serving benchmark plan")
	}
	var serving ServingPlan
	decoder := json.NewDecoder(strings.NewReader(string(plan.Benchmark.Config)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&serving); err != nil {
		return nil, err
	}
	return &serving, nil
}

func DecodeServingReport(result protocol.Result) (*ServingReport, error) {
	if result.Benchmark == nil || result.Benchmark.Kind != "serving" {
		return nil, errors.New("provide a serving benchmark result")
	}
	var serving ServingReport
	if err := json.Unmarshal(result.Benchmark.Data, &serving); err != nil {
		return nil, err
	}
	return &serving, nil
}

func servingImplementation() implementation {
	return implementation{
		validate: func(plan protocol.Plan) error {
			if plan.Runtime.Kind != "http" || len(plan.Cases) != 0 {
				return errors.New("serving requires runtime.kind=http and benchmark.config.cases")
			}
			serving, err := DecodeServingPlan(plan)
			if err != nil {
				return err
			}
			return serving.Validate()
		},
		run: runServingPlan,
		validateReport: func(data json.RawMessage) error {
			var report ServingReport
			decoder := json.NewDecoder(strings.NewReader(string(data)))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&report); err != nil {
				return err
			}
			return report.Validate()
		},
		csv:           WriteServingCSV,
		metricsHeader: servingMetricsHeader,
		metrics: func(out *strings.Builder, node, mode, model string, data json.RawMessage) error {
			var report ServingReport
			if err := json.Unmarshal(data, &report); err != nil {
				return err
			}
			writeServingMetrics(out, node, mode, model, report)
			return nil
		},
	}
}
