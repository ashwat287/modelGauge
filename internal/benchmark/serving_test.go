package benchmark

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

func servingFixture(endpoint string) protocol.Plan {
	return servingPlanWithConfig(protocol.Plan{SchemaVersion: protocol.BenchmarkPlanV1, NodeID: "node", ExecutionMode: protocol.ExecutionModeLima, Runtime: protocol.Runtime{Kind: "http"}, Model: protocol.Model{ID: "model"}}, &ServingPlan{Endpoint: endpoint, API: "openai", ServedModel: "model", RequestTimeout: 2, ReadinessTimeout: 1, SampleInterval: .1, Targets: ServingTargets{TTFTMS: 1000, TPOTMS: 1000}, Cases: []ServingCase{{ID: "load", Scenario: "load", Concurrency: 2, Requests: 3, OutputTokens: 16, WarmupRequests: 1, Prompts: []ServingPrompt{{ID: "json", Text: "Return JSON", Expect: &ServingChecks{JSON: true, JSONSubset: json.RawMessage(`{"answer":4}`)}}}}}})
}

func streamJSON(w http.ResponseWriter, value any) {
	data, _ := json.Marshal(value)
	fmt.Fprintf(w, "data: %s\n\n", data)
	w.(http.Flusher).Flush()
}

func openAIStream(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	streamJSON(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"reasoning_content": "think"}}}})
	time.Sleep(time.Millisecond)
	streamJSON(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": `{"answer":`}}}})
	time.Sleep(time.Millisecond)
	streamJSON(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": `4}`}}}})
	streamJSON(w, map[string]any{"model": "model", "choices": []any{map[string]any{"finish_reason": "stop", "delta": map[string]any{}}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 3, "prompt_tokens_details": map[string]int{"cached_tokens": 4}}})
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func TestServingRunPersistsTimingQualityAndPrivateEvidence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MODELGAUGE_INFERENCE_TOKEN", "inference-secret")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer inference-secret" {
			t.Error("missing inference authentication")
		}
		if r.URL.Path == "/v1/models" {
			fmt.Fprint(w, `{}`)
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		requests.Add(1)
		openAIStream(w)
	}))
	defer server.Close()
	plan := servingFixture(server.URL)
	config := servingConfig(plan)
	dir := filepath.Join(t.TempDir(), "run")
	result, _, err := Run(context.Background(), servingPlanWithConfig(plan, config), dir)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != protocol.RunStatusCompleted || servingResult(result) == nil {
		t.Fatal(result)
	}
	c := servingResult(result).Cases[0]
	if requests.Load() != 4 || c.Successful != 3 || c.TokenAccounted != 3 || c.InputTokens != 30 || c.OutputTokens != 9 || c.QualityPassed != 3 || c.CacheHitRequests != 3 || c.CachedTokens != 12 || c.TTFTMS == nil || c.FirstAnswerMS == nil || c.TPOTMS == nil || c.ITLMS != nil || c.ChunkGapMS == nil || c.Goodput == nil || *c.Goodput <= 0 {
		t.Fatalf("incorrect serving summary: %+v", c)
	}
	if c.FirstAnswerMS.Mean < c.TTFTMS.Mean || c.LatencyMS.Count != 3 {
		t.Fatal("reasoning/answer boundaries or warmup accounting incorrect")
	}
	for _, artifact := range result.Artifacts {
		data, err := os.ReadFile(filepath.Join(dir, artifact.Name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "inference-secret") || strings.Contains(string(data), `"output":"`) {
			t.Fatal("credential or unrequested answer persisted")
		}
	}
	if _, _, err = Run(context.Background(), servingPlanWithConfig(plan, config), dir); err == nil {
		t.Fatal("existing output overwritten")
	}
	count := requests.Load()
	resumed, _, err := RunWithOptions(context.Background(), servingPlanWithConfig(plan, config), Options{RunID: result.RunID, OutputDir: dir, Resume: true})
	if err != nil || len(servingResult(resumed).Cases) != 1 || requests.Load() != count {
		t.Fatal("completed serving case was replayed:", err)
	}
	var output bytes.Buffer
	if err = WriteServingCSV(&output, []protocol.Result{result}); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(&output).ReadAll()
	if err != nil || len(rows) != 2 || len(rows[0]) != len(rows[1]) {
		t.Fatal("invalid serving CSV:", err)
	}
}

func TestServingMissingUsageIncompleteAndNativeTokenBoundaries(t *testing.T) {
	for _, kind := range []string{"missing-usage", "incomplete", "native"} {
		t.Run(kind, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if kind == "native" {
					streamJSON(w, map[string]any{"content": "a", "tokens": []int{1}})
					time.Sleep(time.Millisecond)
					streamJSON(w, map[string]any{"content": "b", "tokens": []int{2}})
					streamJSON(w, map[string]any{"stop": true, "stop_type": "limit", "timings": map[string]int{"prompt_n": 5, "cache_n": 3, "predicted_n": 2}})
				} else {
					streamJSON(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": "ab"}}}})
					if kind != "incomplete" {
						fmt.Fprint(w, "data: [DONE]\n\n")
					}
				}
			}))
			defer server.Close()
			plan := servingFixture(server.URL)
			config := servingConfig(plan)
			if kind == "native" {
				config.API = "llama.cpp"
			}
			client := newServingClient(*config)
			defer client.close()
			r := client.complete(context.Background(), config.Cases[0], ServingPrompt{Text: "hi"}, nil, nil)
			if kind == "incomplete" {
				if r.Status != "incomplete" {
					t.Fatal(r)
				}
				return
			}
			if r.Status != "successful" {
				t.Fatal(r)
			}
			if kind == "native" {
				if r.InputTokens == nil || *r.InputTokens != 8 || *r.OutputTokens != 2 || len(r.TokenTimesMS) != 2 || r.CachedTokens != 3 {
					t.Fatal(r)
				}
			} else {
				s := summarizeServingCase(config.Cases[0], *config, []servingRequest{r}, r.StartedAt, .1, .1)
				if s.OutputTokensPerSecond != nil || s.TPOTMS != nil || s.Goodput != nil || s.ITLMS != nil {
					t.Fatal("missing token accounting replaced with invented values")
				}
			}
		})
	}
}

func TestServingArrivalRateAccountsForClientDrops(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			fmt.Fprint(w, `{}`)
			return
		}
		time.Sleep(100 * time.Millisecond)
		openAIStream(w)
	}))
	defer server.Close()
	plan := servingFixture(server.URL)
	config := servingConfig(plan)
	c := &config.Cases[0]
	c.Concurrency, c.Requests, c.RequestRate, c.WarmupRequests = 1, 10, 1000, 0
	result, _, err := Run(context.Background(), servingPlanWithConfig(plan, config), filepath.Join(t.TempDir(), "run"))
	if err != nil {
		t.Fatal(err)
	}
	s := servingResult(result).Cases[0]
	if s.Offered != 10 || s.ClientCapacityDrops == 0 || s.Dispatched+s.ClientCapacityDrops != 10 || s.Successful != s.Dispatched || s.Errors != 0 {
		t.Fatalf("open-loop load hid dropped arrivals: %+v", s)
	}
}

func TestServingCancellationPreservesEvidenceAndReleasesClient(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cancelled := make(chan struct{}, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			fmt.Fprint(w, `{}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		cancelled <- struct{}{}
	}))
	defer server.Close()
	plan := servingFixture(server.URL)
	config := servingConfig(plan)
	c := &config.Cases[0]
	c.Concurrency, c.Requests, c.WarmupRequests, c.CancelAfterMS = 1, 2, 0, 10
	result, _, err := Run(context.Background(), servingPlanWithConfig(plan, config), filepath.Join(t.TempDir(), "probe"))
	if err != nil || servingResult(result).Cases[0].Cancelled != 2 || servingResult(result).Cases[0].CancelAckMS == nil {
		t.Fatal(result, err)
	}
	c.CancelAfterMS = 0
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "interrupted")
	result, _, err = Run(ctx, servingPlanWithConfig(plan, config), dir)
	if err == nil || result.Status != protocol.RunStatusCancelled || servingResult(result) == nil {
		t.Fatal("interruption lost partial report:", result, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "result.json"))
	if err != nil || !json.Valid(data) {
		t.Fatal("partial result not saved", err)
	}
	if len(cancelled) < 2 {
		t.Fatal("client did not close cancelled requests")
	}
}

func TestServingMetricsAndQualityChecks(t *testing.T) {
	values := map[string]float64{}
	parseServingMetrics("llamacpp:requests_processing{slot=\"a}secret\"} 2\nllamacpp:requests_processing{slot=\"b\"} 3\nllamacpp:tokens_predicted_total 42\nllamacpp:bad NaN\n", values)
	if values["server:llamacpp:requests_processing"] != 5 || values["server:llamacpp:tokens_predicted_total"] != 42 {
		t.Fatal(values)
	}
	for key := range values {
		if strings.Contains(key, "secret") || strings.Contains(key, "bad") {
			t.Fatal("private labels/nonfinite value retained")
		}
	}
	check := ServingChecks{JSON: true, JSONSubset: json.RawMessage(`{"a":1}`)}
	if len(checkServingOutput(`{"a":1,"b":2}`, nil, check)) != 0 || len(checkServingOutput(`{"a":2}`, nil, check)) == 0 {
		t.Fatal("JSON quality checks incorrect")
	}
	check = ServingChecks{ToolCalls: []ServingToolCall{{Name: "lookup", Arguments: json.RawMessage(`{"id":4}`)}}}
	if len(checkServingOutput("", []ServingToolCall{{Name: "lookup", Arguments: json.RawMessage(`{"id":4}`)}}, check)) != 0 || len(checkServingOutput("", nil, check)) == 0 {
		t.Fatal("tool argument correctness not checked")
	}
	d := distribution([]float64{1, 2, 3, 4, 5})
	if d.P50 != 3 || d.P95 != 5 || d.Count != 5 || d.Mean != 3 {
		t.Fatal(d)
	}
}

func TestServingAgentInterruptionResumesUnfinishedCase(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var block atomic.Bool
	block.Store(true)
	entered := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			fmt.Fprint(w, `{}`)
			return
		}
		if block.Load() {
			w.Header().Set("Content-Type", "text/event-stream")
			w.(http.Flusher).Flush()
			entered <- struct{}{}
			<-r.Context().Done()
			return
		}
		openAIStream(w)
	}))
	defer server.Close()
	plan := servingFixture(server.URL)
	config := servingConfig(plan)
	config.Cases[0].Requests, config.Cases[0].Concurrency, config.Cases[0].WarmupRequests = 1, 1, 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { <-entered; cancel() }()
	dir := filepath.Join(t.TempDir(), "agent-run")
	options := Options{RunID: "run-agent", OutputDir: dir, Resume: true}
	if _, _, err := RunWithOptions(ctx, servingPlanWithConfig(plan, config), options); err == nil {
		t.Fatal("interrupted execution succeeded")
	}
	if _, err := os.Stat(filepath.Join(dir, "result.json")); !os.IsNotExist(err) {
		t.Fatal("agent shutdown left a final result that would prevent recovery", err)
	}
	evidence, err := filepath.Glob(filepath.Join(dir, "interrupted-result-*.json"))
	if err != nil || len(evidence) != 1 {
		t.Fatal("missing interrupted evidence", evidence, err)
	}
	block.Store(false)
	result, _, err := RunWithOptions(context.Background(), servingPlanWithConfig(plan, config), options)
	if err != nil || result.Status != protocol.RunStatusCompleted || servingResult(result).Cases[0].Successful != 1 {
		t.Fatal("unfinished case was not rerun", result, err)
	}
}

func TestServingConversationUsesAndResetsHistory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var turns atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			fmt.Fprint(w, `{}`)
			return
		}
		var request struct {
			Messages []ServingMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		turn := turns.Add(1)
		if turn%2 == 1 && len(request.Messages) != 1 || turn%2 == 0 && (len(request.Messages) != 3 || request.Messages[1].Role != "assistant" || request.Messages[1].Content != `{"answer":4}`) {
			t.Errorf("incorrect conversation history at turn %d: %+v", turn, request.Messages)
		}
		openAIStream(w)
	}))
	defer server.Close()
	plan := servingFixture(server.URL)
	config := servingConfig(plan)
	c := &config.Cases[0]
	c.Requests, c.Concurrency, c.WarmupRequests, c.ContinueConversation = 4, 1, 0, true
	c.Prompts = []ServingPrompt{{Text: "Remember four"}, {Text: "Recall it"}}
	if _, _, err := Run(context.Background(), servingPlanWithConfig(plan, config), filepath.Join(t.TempDir(), "conversation")); err != nil {
		t.Fatal(err)
	}
	if turns.Load() != 4 {
		t.Fatal("not all conversation turns were sent")
	}
}

func TestServingSyntheticContextsUseEndpointTokenizer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tokenize":
			fmt.Fprint(w, `{"tokens":[10,11,12,13,14]}`)
		case "/detokenize":
			var request struct {
				Tokens []int `json:"tokens"`
			}
			_ = json.NewDecoder(r.Body).Decode(&request)
			if len(request.Tokens) != 3 || request.Tokens[2] != 12 {
				t.Error("wrong tokenizer slice", request)
			}
			fmt.Fprint(w, `{"content":"decoded exact tokens"}`)
		default:
			t.Error("unexpected path", r.URL.Path)
		}
	}))
	defer server.Close()
	plan := servingFixture(server.URL)
	config := servingConfig(plan)
	for _, api := range []string{"openai", "llama.cpp"} {
		config.API = api
		client := newServingClient(*config)
		prompt, ids, err := client.synthetic(context.Background(), 3)
		client.close()
		if err != nil {
			t.Fatal(err)
		}
		if api == "openai" && (prompt.Text != "decoded exact tokens" || len(ids) != 0) || api == "llama.cpp" && (len(ids) != 3 || ids[2] != 12) {
			t.Fatal(api, prompt, ids)
		}
	}
}

func TestServingMetricsAuthAndRedirectsDoNotLeakInferenceToken(t *testing.T) {
	t.Setenv("MODELGAUGE_INFERENCE_TOKEN", "private-inference-token")
	t.Setenv("MODELGAUGE_INFERENCE_METRICS_TOKEN", "")
	var visits atomic.Int32
	metrics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		visits.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Error("inference token leaked to metrics origin")
		}
		fmt.Fprint(w, "llamacpp:requests_processing 2\n")
	}))
	defer metrics.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, metrics.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	plan := servingFixture(redirect.URL)
	config := servingConfig(plan)
	config.MetricsURL = metrics.URL
	client := newServingClient(*config)
	defer client.close()
	sampler, err := newServingSampler(client)
	if err != nil || sampler.sample(context.Background()).Values["server:llamacpp:requests_processing"] != 2 {
		t.Fatal(err)
	}
	_, err = client.request(context.Background(), "GET", redirect.URL, "private-inference-token", nil)
	if err == nil || visits.Load() != 1 {
		t.Fatal("redirect followed and risked exposing credentials", err, visits.Load())
	}
}

func TestServingResourceMissingSamplesDoNotProduceCounterOrEnergyTotals(t *testing.T) {
	now := time.Now()
	samples := []servingSample{
		{At: now, Values: map[string]float64{"process_rss_bytes": 100, "process_cpu_seconds": 1, "gpu_power_watts": 10}},
		{At: now.Add(time.Second), Values: map[string]float64{"process_rss_bytes": 200, "process_cpu_seconds": 2, "gpu_power_watts": 10}},
		{At: now.Add(2 * time.Second), Values: map[string]float64{}},
	}
	r := summarizeServingResources(samples, 1, 2)
	if r.CPUSeconds != nil || r.GPUEnergyJoules != nil || r.FinalRSSBytes != nil || r.SampledPeakRSSBytes == nil || *r.SampledPeakRSSBytes != 200 {
		t.Fatal("incomplete observation was presented as a complete total", r)
	}
	samples = samples[:2]
	r = summarizeServingResources(samples, 1, 2)
	if r.CPUSeconds == nil || *r.CPUSeconds != 1 || r.GPUEnergyJoules == nil || *r.GPUEnergyJoules != 10 || *r.GPUJoulesPerOutputToken != 5 {
		t.Fatal("complete observation produced incorrect totals", r)
	}
}

func TestServingTimeoutHTTPFailureAndInvalidToolArguments(t *testing.T) {
	for _, kind := range []string{"timeout", "http-error", "tool-arguments"} {
		t.Run(kind, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch kind {
				case "timeout":
					w.Header().Set("Content-Type", "text/event-stream")
					w.(http.Flusher).Flush()
					<-r.Context().Done()
				case "http-error":
					http.Error(w, "private server error body", http.StatusServiceUnavailable)
				case "tool-arguments":
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"lookup\",\"arguments\":\"broken-json\"}}]}}]}\n\ndata: [DONE]\n\n")
				}
			}))
			defer server.Close()
			plan := servingFixture(server.URL)
			config := servingConfig(plan)
			config.RequestTimeout, config.RecordOutputs = .05, true
			client := newServingClient(*config)
			defer client.close()
			prompt := ServingPrompt{Text: "Call lookup", Expect: &ServingChecks{ToolCalls: []ServingToolCall{{Name: "lookup", Arguments: json.RawMessage(`{}`)}}}}
			r := client.complete(context.Background(), config.Cases[0], prompt, nil, nil)
			data, err := json.Marshal(r)
			if err != nil || strings.Contains(string(data), "private server error body") {
				t.Fatal("unserializable or private error evidence", err, string(data))
			}
			switch kind {
			case "timeout":
				if r.Status != "timeout" {
					t.Fatal(r)
				}
			case "http-error":
				if r.HTTPStatus != 503 || r.Status != "error" {
					t.Fatal(r)
				}
			case "tool-arguments":
				if r.Status != "successful" || len(r.ToolCalls) != 1 || r.ToolCalls[0].RawArguments != "broken-json" || r.QualityPassed == nil || *r.QualityPassed {
					t.Fatal(r)
				}
			}
		})
	}
}

func servingPlanWithConfig(plan protocol.Plan, config *ServingPlan) protocol.Plan {
	if err := SetConfig(&plan, "serving", config); err != nil {
		panic(err)
	}
	return plan
}
func servingConfig(plan protocol.Plan) *ServingPlan {
	config, err := DecodeServingPlan(plan)
	if err != nil {
		panic(err)
	}
	return config
}
func servingResult(result protocol.Result) *ServingReport {
	report, err := DecodeServingReport(result)
	if err != nil {
		return nil
	}
	return report
}
