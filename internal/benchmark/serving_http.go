package benchmark

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var errProbeCancelled = errors.New("benchmark cancellation probe")

type servingClient struct {
	plan ServingPlan
	http *http.Client
}

type servingRequest struct {
	CacheAccounted  bool              `json:"cache_accounted"`
	Index           int               `json:"index"`
	Worker          int               `json:"worker"`
	PromptID        string            `json:"prompt_id,omitempty"`
	StartedAt       time.Time         `json:"started_at"`
	CompletedAt     time.Time         `json:"completed_at"`
	Status          string            `json:"status"`
	HTTPStatus      int               `json:"http_status,omitempty"`
	Error           string            `json:"error,omitempty"`
	Model           string            `json:"served_model,omitempty"`
	LatencyMS       float64           `json:"latency_ms"`
	DispatchDelayMS float64           `json:"dispatch_delay_ms"`
	TTFTMS          *float64          `json:"ttft_ms"`
	FirstAnswerMS   *float64          `json:"first_answer_ms"`
	TPOTMS          *float64          `json:"tpot_ms"`
	CancelAckMS     *float64          `json:"client_cancel_ack_ms"`
	InputTokens     *int              `json:"input_tokens"`
	OutputTokens    *int              `json:"output_tokens"`
	CachedTokens    int               `json:"cached_tokens"`
	ChunkTimesMS    []float64         `json:"chunk_times_ms,omitempty"`
	TokenTimesMS    []float64         `json:"token_times_ms,omitempty"`
	FinishReason    string            `json:"finish_reason,omitempty"`
	Truncated       bool              `json:"context_truncated"`
	QualityPassed   *bool             `json:"quality_passed"`
	CheckFailures   []string          `json:"check_failures,omitempty"`
	Output          string            `json:"output,omitempty"`
	ToolCalls       []ServingToolCall `json:"tool_calls,omitempty"`
	content         string
}

func newServingClient(plan ServingPlan) *servingClient {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost, transport.MaxIdleConns = 256, 256
	return &servingClient{plan: plan, http: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("inference redirects are refused to protect credentials")
	}}}
}

func (c *servingClient) close() { c.http.CloseIdleConnections() }

func (c *servingClient) address(path string) string {
	return strings.TrimSuffix(strings.TrimRight(c.plan.Endpoint, "/"), "/v1") + path
}

func (c *servingClient) request(ctx context.Context, method, address, token string, body any) (*http.Response, error) {
	var input io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		input = bytes.NewReader(data)
	}
	r, err := http.NewRequestWithContext(ctx, method, address, input)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return c.http.Do(r)
}

func (c *servingClient) json(ctx context.Context, path string, body, result any) error {
	ctx, stop := context.WithTimeout(ctx, seconds(c.plan.RequestTimeout))
	defer stop()
	r, err := c.request(ctx, "POST", c.address(path), os.Getenv("MODELGAUGE_INFERENCE_TOKEN"), body)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("endpoint request failed")
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return fmt.Errorf("endpoint %s returned HTTP %d", path, r.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, (8<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 8<<20 {
		return errors.New("endpoint response exceeds 8 MiB")
	}
	return json.Unmarshal(data, result)
}

func (c *servingClient) ready(ctx context.Context) (float64, error) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, seconds(c.plan.ReadinessTimeout))
	defer cancel()
	path := c.plan.ReadinessPath
	if path == "" {
		path = "/v1/models"
		if c.plan.API == "llama.cpp" {
			path = "/health"
		}
	}
	for {
		probe, stop := context.WithTimeout(ctx, 2*time.Second)
		r, err := c.request(probe, "GET", c.address(path), os.Getenv("MODELGAUGE_INFERENCE_TOKEN"), nil)
		ok := err == nil && r.StatusCode >= 200 && r.StatusCode < 300
		if r != nil {
			r.Body.Close()
		}
		stop()
		if ok {
			return milliseconds(time.Since(started)), nil
		}
		if err = waitServing(ctx, 200*time.Millisecond); err != nil {
			return 0, fmt.Errorf("endpoint readiness: %w", err)
		}
	}
}

// Exact synthetic content needs the endpoint's own tokenizer, never word counts.
func (c *servingClient) synthetic(ctx context.Context, n int) (ServingPrompt, []int, error) {
	text := "Continue this document with a short summary. The sky is blue and the grass is green. "
	var tokens []int
	for attempt := 0; attempt < 16; attempt++ {
		var response struct {
			Tokens []int `json:"tokens"`
		}
		if err := c.json(ctx, "/tokenize", map[string]any{"content": text, "add_special": false}, &response); err != nil {
			return ServingPrompt{}, nil, fmt.Errorf("token-length inputs require llama.cpp /tokenize and /detokenize; provide explicit prompt fixtures for other endpoints: %w", err)
		}
		tokens = response.Tokens
		if len(tokens) >= n {
			break
		}
		text += text
	}
	if len(tokens) < n {
		return ServingPrompt{}, nil, errors.New("tokenizer did not produce enough input tokens")
	}
	tokens = tokens[:n]
	if c.plan.API == "llama.cpp" {
		return ServingPrompt{ID: fmt.Sprintf("synthetic-%d", n)}, tokens, nil
	}
	var decoded struct {
		Content string `json:"content"`
	}
	if err := c.json(ctx, "/detokenize", map[string]any{"tokens": tokens}, &decoded); err != nil {
		return ServingPrompt{}, nil, err
	}
	return ServingPrompt{ID: fmt.Sprintf("synthetic-%d", n), Text: decoded.Content}, nil, nil
}

func (c *servingClient) complete(ctx context.Context, spec ServingCase, prompt ServingPrompt, tokens []int, history []ServingMessage) servingRequest {
	result := servingRequest{StartedAt: time.Now().UTC(), PromptID: prompt.ID, Status: "error"}
	started := time.Now()
	requestCtx, cancel := context.WithTimeout(ctx, seconds(c.plan.RequestTimeout))
	defer cancel()
	if spec.CancelAfterMS > 0 {
		var stop context.CancelFunc
		requestCtx, stop = context.WithTimeoutCause(requestCtx, time.Duration(spec.CancelAfterMS*float64(time.Millisecond)), errProbeCancelled)
		defer stop()
	}
	model := spec.Model
	if model == "" {
		model = c.plan.ServedModel
	}
	body := map[string]any{"stream": true, "seed": c.plan.Seed, "temperature": c.plan.Temperature}
	path := "/v1/chat/completions"
	if model != "" {
		body["model"] = model
	}
	if c.plan.API == "llama.cpp" {
		path = "/completion"
		body["prompt"], body["n_predict"] = prompt.Text, spec.OutputTokens
		if len(tokens) > 0 {
			body["prompt"] = tokens
		}
		body["cache_prompt"], body["ignore_eos"], body["return_tokens"] = spec.CachePrompt, spec.IgnoreEOS, true
	} else {
		messages := append([]ServingMessage(nil), history...)
		if len(prompt.Messages) > 0 {
			messages = append(messages, prompt.Messages...)
		} else {
			messages = append(messages, ServingMessage{Role: "user", Content: prompt.Text})
		}
		body["messages"], body["max_tokens"] = messages, spec.OutputTokens
		body["stream_options"] = map[string]bool{"include_usage": true}
		if len(prompt.Tools) > 0 {
			body["tools"] = prompt.Tools
		}
	}
	r, err := c.request(requestCtx, "POST", c.address(path), os.Getenv("MODELGAUGE_INFERENCE_TOKEN"), body)
	terminal := false
	var content strings.Builder
	toolParts := map[int]*ServingToolCall{}
	toolBytes := 0
	tokenBoundaries := c.plan.API == "llama.cpp"
	if err == nil {
		result.HTTPStatus = r.StatusCode
		if r.StatusCode < 200 || r.StatusCode >= 300 {
			result.Error = "http_error"
		} else if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "text/event-stream") {
			result.Error = "expected_event_stream"
		} else {
			err = readServingEvents(r.Body, func(data []byte) error {
				if string(data) == "[DONE]" {
					terminal = true
					return io.EOF
				}
				var event struct {
					Content   string          `json:"content"`
					Model     string          `json:"model"`
					Stop      bool            `json:"stop"`
					StopType  string          `json:"stop_type"`
					Truncated bool            `json:"truncated"`
					Tokens    []int           `json:"tokens"`
					Predicted *int            `json:"tokens_predicted"`
					Evaluated *int            `json:"tokens_evaluated"`
					Cached    *int            `json:"tokens_cached"`
					Error     json.RawMessage `json:"error"`
					Timings   struct {
						PromptN    *int `json:"prompt_n"`
						CacheN     *int `json:"cache_n"`
						PredictedN *int `json:"predicted_n"`
					} `json:"timings"`
					Usage *struct {
						Prompt     *int `json:"prompt_tokens"`
						Completion *int `json:"completion_tokens"`
						Details    struct {
							Cached *int `json:"cached_tokens"`
						} `json:"prompt_tokens_details"`
					} `json:"usage"`
					Choices []struct {
						Finish *string `json:"finish_reason"`
						Delta  struct {
							Content   string `json:"content"`
							Reasoning string `json:"reasoning_content"`
							ToolCalls []struct {
								Index    int `json:"index"`
								Function struct {
									Name      string `json:"name"`
									Arguments string `json:"arguments"`
								} `json:"function"`
							} `json:"tool_calls"`
						} `json:"delta"`
					} `json:"choices"`
				}
				if err := json.Unmarshal(data, &event); err != nil {
					return errors.New("malformed_stream")
				}
				if len(event.Error) > 0 && string(event.Error) != "null" {
					return errors.New("stream_error")
				}
				stamp := milliseconds(time.Since(started))
				text, visible := event.Content, event.Content != ""
				productive := visible || len(event.Tokens) > 0 && !event.Stop
				for _, choice := range event.Choices {
					if choice.Finish != nil {
						terminal = true
						result.FinishReason = *choice.Finish
					}
					text += choice.Delta.Content
					visible = visible || choice.Delta.Content != ""
					productive = productive || choice.Delta.Content != "" || choice.Delta.Reasoning != ""
					for _, call := range choice.Delta.ToolCalls {
						toolBytes += len(call.Function.Name) + len(call.Function.Arguments)
						if toolBytes > 1<<20 {
							return errors.New("tool_output_limit_exceeded")
						}
						if call.Index < 0 || call.Index >= 64 {
							return errors.New("invalid_tool_call_index")
						}
						part := toolParts[call.Index]
						if part == nil {
							part = &ServingToolCall{}
							toolParts[call.Index] = part
						}
						part.Name += call.Function.Name
						part.Arguments = append(part.Arguments, call.Function.Arguments...)
						productive = productive || call.Function.Name != "" || call.Function.Arguments != ""
					}
				}
				if productive {
					if result.TTFTMS == nil {
						result.TTFTMS = floatPointer(stamp)
					}
					result.ChunkTimesMS = append(result.ChunkTimesMS, stamp)
					if c.plan.API == "llama.cpp" && len(event.Tokens) == 1 {
						result.TokenTimesMS = append(result.TokenTimesMS, stamp)
					} else {
						tokenBoundaries = false
					}
				}
				if visible && result.FirstAnswerMS == nil {
					result.FirstAnswerMS = floatPointer(stamp)
				}
				content.WriteString(text)
				if content.Len() > 1<<20 || len(result.ChunkTimesMS) > 131072 || len(toolParts) > 64 {
					return errors.New("response_limit_exceeded")
				}
				if len(event.Model) > 512 {
					return errors.New("model_alias_exceeds_limit")
				}
				if event.Model != "" {
					result.Model = event.Model
				}
				if event.Stop {
					terminal = true
					result.FinishReason = event.StopType
				}
				result.Truncated = result.Truncated || event.Truncated
				if event.Predicted != nil {
					result.OutputTokens = event.Predicted
				}
				if event.Evaluated != nil {
					result.InputTokens = event.Evaluated
				}
				if event.Cached != nil {
					result.CachedTokens = *event.Cached
					result.CacheAccounted = true
				}
				if event.Timings.PromptN != nil {
					n := *event.Timings.PromptN
					if event.Timings.CacheN != nil {
						n += *event.Timings.CacheN
					}
					result.InputTokens = &n
				}
				if event.Timings.CacheN != nil {
					result.CachedTokens = *event.Timings.CacheN
					result.CacheAccounted = true
				}
				if event.Timings.PredictedN != nil {
					result.OutputTokens = event.Timings.PredictedN
				}
				if event.Usage != nil {
					if event.Usage.Prompt != nil {
						result.InputTokens = event.Usage.Prompt
					}
					if event.Usage.Completion != nil {
						result.OutputTokens = event.Usage.Completion
					}
					if event.Usage.Details.Cached != nil {
						result.CachedTokens = *event.Usage.Details.Cached
						result.CacheAccounted = true
					}
				}
				if event.Stop {
					return io.EOF
				}
				return nil
			})
		}
		r.Body.Close()
	}
	result.CompletedAt, result.LatencyMS = time.Now().UTC(), milliseconds(time.Since(started))
	result.content = content.String()
	if result.InputTokens != nil && (*result.InputTokens < 0 || *result.InputTokens > 1000000000) || result.OutputTokens != nil && (*result.OutputTokens < 0 || *result.OutputTokens > 1000000000) || result.CachedTokens < 0 {
		result.InputTokens, result.OutputTokens = nil, nil
		result.Error = "invalid_token_accounting"
	}
	if !tokenBoundaries || result.OutputTokens == nil || len(result.TokenTimesMS) != *result.OutputTokens {
		result.TokenTimesMS = nil
	}
	indices := make([]int, 0, len(toolParts))
	for index := range toolParts {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		part := toolParts[index]
		if !json.Valid(part.Arguments) {
			part.RawArguments = string(part.Arguments)
			part.Arguments = nil
		}
		result.ToolCalls = append(result.ToolCalls, *part)
	}
	if result.Error != "" { /* HTTP shape errors are already classified. */
	} else if errors.Is(context.Cause(requestCtx), errProbeCancelled) && !terminal {
		result.Status, result.Error = "cancelled", "client_cancellation"
		result.CancelAckMS = floatPointer(max(0, result.LatencyMS-spec.CancelAfterMS))
	} else if requestCtx.Err() != nil && !(err == nil && terminal) {
		result.Status, result.Error = "timeout", "request_timeout"
		if ctx.Err() != nil {
			result.Status, result.Error = "cancelled", "benchmark_interrupted"
		}
	} else if err != nil {
		result.Error = "transport_or_stream_error"
	} else if !terminal {
		result.Status, result.Error = "incomplete", "missing_terminal_event"
	} else {
		result.Status = "successful"
		if result.OutputTokens != nil && *result.OutputTokens > 1 && result.TTFTMS != nil {
			last := result.LatencyMS
			if n := len(result.ChunkTimesMS); n > 0 {
				last = result.ChunkTimesMS[n-1]
			}
			result.TPOTMS = floatPointer(max(0, last-*result.TTFTMS) / float64(*result.OutputTokens-1))
		}
	}
	if prompt.Expect != nil {
		result.CheckFailures = checkServingOutput(result.content, result.ToolCalls, *prompt.Expect)
		passed := result.Status == "successful" && len(result.CheckFailures) == 0
		result.QualityPassed = &passed
	}
	if c.plan.RecordOutputs {
		result.Output = result.content
	} else {
		result.ToolCalls = nil
	}
	return result
}

func readServingEvents(input io.Reader, consume func([]byte) error) error {
	limited := &io.LimitedReader{R: input, N: (16 << 20) + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var event []byte
	flush := func() error {
		if len(event) == 0 {
			return nil
		}
		err := consume(event)
		event = nil
		return err
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if len(event) > 0 {
				event = append(event, '\n')
			}
			event = append(event, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")...)
			if len(event) > 1<<20 {
				return errors.New("SSE event exceeds 1 MiB")
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if limited.N == 0 {
		return errors.New("stream response exceeds 16 MiB")
	}
	err := flush()
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func checkServingOutput(text string, calls []ServingToolCall, check ServingChecks) []string {
	var failures []string
	if check.Exact != nil && text != *check.Exact {
		failures = append(failures, "exact")
	}
	for _, value := range check.Contains {
		if !strings.Contains(text, value) {
			failures = append(failures, "contains")
		}
	}
	for _, value := range check.NotContains {
		if strings.Contains(text, value) {
			failures = append(failures, "not_contains")
		}
	}
	if check.Regex != "" {
		if ok, _ := regexp.MatchString(check.Regex, text); !ok {
			failures = append(failures, "regex")
		}
	}
	var decoded any
	if check.JSON || len(check.JSONSubset) > 0 {
		if json.Unmarshal([]byte(text), &decoded) != nil {
			failures = append(failures, "json")
		}
	}
	if len(check.JSONSubset) > 0 {
		var expected any
		_ = json.Unmarshal(check.JSONSubset, &expected)
		if !jsonContains(decoded, expected) {
			failures = append(failures, "json_subset")
		}
	}
	used := map[int]bool{}
	if len(check.ToolCalls) > 0 && len(check.ToolCalls) != len(calls) {
		failures = append(failures, "tool_call_count")
	}
	for _, wanted := range check.ToolCalls {
		found := false
		for index, actual := range calls {
			if used[index] {
				continue
			}
			var a, b any
			if actual.Name == wanted.Name && json.Unmarshal(actual.Arguments, &a) == nil && json.Unmarshal(wanted.Arguments, &b) == nil && reflect.DeepEqual(a, b) {
				found = true
				used[index] = true
				break
			}
		}
		if !found {
			failures = append(failures, "tool_call")
		}
	}
	return failures
}

func jsonContains(actual, expected any) bool {
	if fields, ok := expected.(map[string]any); ok {
		object, ok := actual.(map[string]any)
		if !ok {
			return false
		}
		for k, v := range fields {
			a, exists := object[k]
			if !exists || !jsonContains(a, v) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(actual, expected)
}

func milliseconds(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
func seconds(v float64) time.Duration      { return time.Duration(v * float64(time.Second)) }
func floatPointer(v float64) *float64      { return &v }
func waitServing(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Keep HTTP errors out of artifacts: they can contain credentials or response text.
func parseServingNumber(value string) (float64, bool) {
	n, err := strconv.ParseFloat(value, 64)
	return n, err == nil && !math.IsNaN(n) && !math.IsInf(n, 0)
}
