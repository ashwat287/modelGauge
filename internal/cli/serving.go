package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/nerdylucifer/modelgauge/internal/benchmark"
	"github.com/nerdylucifer/modelgauge/internal/platform"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

func runServing(ctx context.Context, args []string, out, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: modelgauge benchmark serving <plan|run|report>")
		return 2
	}
	switch args[0] {
	case "plan":
		return servingPlan(args[1:], out, stderr)
	case "run":
		f := flag.NewFlagSet("benchmark serving run", flag.ContinueOnError)
		setFlagOutput(f, stderr)
		file := f.String("plan", "", "endpoint-only benchmark plan")
		output := f.String("output", "", "new result directory")
		if err := f.Parse(args[1:]); err != nil {
			return flagExitCode(err)
		}
		plan, ok := loadPlan(*file, stderr)
		if !ok {
			return 1
		}
		if benchmark.Kind(plan) != "serving" {
			fmt.Fprintln(stderr, "provide a serving plan")
			return 2
		}
		result, dir, err := benchmark.Run(ctx, plan, *output)
		if err != nil {
			fmt.Fprintf(stderr, "serving benchmark: %v; partial evidence: %s\n", err, dir)
			return 1
		}
		fmt.Fprintf(stderr, "📁 Serving evidence: %s\n", dir)
		report, err := benchmark.DecodeServingReport(result)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return writeJSON(out, report)
	case "report":
		f := flag.NewFlagSet("benchmark serving report", flag.ContinueOnError)
		setFlagOutput(f, stderr)
		file := f.String("file", "", "result.json file")
		format := f.String("format", "json", "json or csv")
		if err := f.Parse(args[1:]); err != nil {
			return flagExitCode(err)
		}
		if *format != "json" && *format != "csv" {
			fmt.Fprintln(stderr, "format must be json or csv")
			return 2
		}
		data, err := os.ReadFile(*file)
		var result protocol.Result
		if err != nil || json.Unmarshal(data, &result) != nil || result.Benchmark == nil {
			fmt.Fprintln(stderr, "cannot read serving result")
			return 1
		}
		if *format == "json" {
			report, err := benchmark.DecodeServingReport(result)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			return writeJSON(out, report)
		}
		if err = benchmark.WriteServingCSV(out, []protocol.Result{result}); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	default:
		fmt.Fprintln(stderr, "usage: modelgauge benchmark serving <plan|run|report>")
		return 2
	}
}

func servingPlan(args []string, out, stderr io.Writer) int {
	f := flag.NewFlagSet("benchmark serving plan", flag.ContinueOnError)
	setFlagOutput(f, stderr)
	endpoint := f.String("endpoint", "", "existing inference server base URL")
	api := f.String("api", "openai", "openai or native llama.cpp streaming API")
	model := f.String("model", "", "model ID used to identify benchmark results")
	served := f.String("served-model", "", "model alias requested from the endpoint (defaults to --model)")
	node := f.String("node", "endpoint-client", "benchmark client/agent node ID")
	mode := f.String("execution-mode", "native-isolated", "native-isolated or lima-linux")
	suite := f.String("suite", "basic", "basic or deployment (adds sustained, burst, quality, conversation/cache and cancellation cases)")
	contextsText := f.String("contexts", "", "synthetic content token lengths; requires endpoint /tokenize and /detokenize")
	concurrencyText := f.String("concurrency", "1,2", "comma-separated maximum in-flight requests")
	requests := f.Int("requests", 10, "measured requests per basic case (0 requires --duration)")
	warmup := f.Int("warmups", 2, "warm-up requests excluded from measurements")
	outputTokens := f.Int("output-tokens", 128, "maximum output tokens per request")
	duration := f.Duration("duration", 0, "dispatch duration limit for basic cases")
	sustain := f.Duration("sustain-duration", time.Minute, "duration of the deployment sustained-load case")
	rate := f.Float64("request-rate", 0, "fixed arrival rate; zero uses closed-loop workers")
	requestTimeout := f.Duration("request-timeout", 2*time.Minute, "deadline for each complete streamed request")
	readyTimeout := f.Duration("readiness-timeout", 30*time.Second, "endpoint readiness deadline")
	readyPath := f.String("readiness-path", "", "override readiness path, e.g. /health")
	metrics := f.String("metrics-url", "", "existing server Prometheus endpoint; never provisioned automatically")
	pid := f.Int("local-server-pid", 0, "optional server PID on the Linux benchmark client node")
	interval := f.Duration("sample-interval", time.Second, "resource sampling interval")
	dataset := f.String("dataset", "", "JSON array of prompt/messages and expected-output fixtures")
	idle := f.Duration("idle-before", 0, "add an idle-resume request after this delay")
	switchModels := f.String("switch-models", "", "comma-separated aliases for sequential model-selection probes")
	ttft := f.Float64("ttft-ms", 0, "goodput time-to-first-content target; 0 disables")
	tpot := f.Float64("tpot-ms", 0, "goodput time-per-output-token target; 0 disables")
	latency := f.Float64("latency-ms", 0, "goodput complete-response latency target; 0 disables")
	seed := f.Int("seed", 42, "request seed (endpoint support varies)")
	temperature := f.Float64("temperature", 0, "request temperature")
	record := f.Bool("record-outputs", false, "include generated answers/tool arguments in private raw evidence")
	output := f.String("output", "", "new plan JSON file; default prints JSON")
	if err := f.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if *endpoint == "" || *model == "" || f.NArg() != 0 {
		fmt.Fprintln(stderr, "--endpoint and --model are required; arguments must be flags")
		return 2
	}
	if *suite != "basic" && *suite != "deployment" {
		fmt.Fprintln(stderr, "suite must be basic or deployment")
		return 2
	}
	concurrencies, err := integerList(*concurrencyText)
	if err != nil {
		fmt.Fprintln(stderr, "invalid concurrency:", err)
		return 2
	}
	contexts := []int{0}
	if *contextsText != "" {
		contexts, err = integerList(*contextsText)
		if err != nil {
			fmt.Fprintln(stderr, "invalid contexts:", err)
			return 2
		}
	}
	var prompts []benchmark.ServingPrompt
	if *dataset != "" {
		file, err := os.Open(*dataset)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		data, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
		file.Close()
		if err != nil || len(data) > 4<<20 {
			fmt.Fprintln(stderr, "dataset exceeds 4 MiB or cannot be read")
			return 2
		}
		decoder := json.NewDecoder(strings.NewReader(string(data)))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&prompts); err != nil || len(prompts) == 0 {
			fmt.Fprintln(stderr, "dataset must be a nonempty JSON prompt array:", err)
			return 2
		}
		if decoder.Decode(new(any)) != io.EOF {
			fmt.Fprintln(stderr, "dataset must contain exactly one JSON array")
			return 2
		}
		if *contextsText != "" {
			fmt.Fprintln(stderr, "use explicit dataset inputs or synthetic --contexts, not both")
			return 2
		}
	}
	if *served == "" {
		*served = *model
	}
	s := &benchmark.ServingPlan{Endpoint: *endpoint, API: *api, ServedModel: *served, RequestTimeout: requestTimeout.Seconds(), ReadinessTimeout: readyTimeout.Seconds(), ReadinessPath: *readyPath, MetricsURL: *metrics, LocalPID: *pid, SampleInterval: interval.Seconds(), Seed: *seed, Temperature: *temperature, RecordOutputs: *record, Targets: benchmark.ServingTargets{TTFTMS: *ttft, TPOTMS: *tpot, LatencyMS: *latency}}
	for _, n := range contexts {
		for _, workers := range concurrencies {
			s.Cases = append(s.Cases, benchmark.ServingCase{ID: fmt.Sprintf("load-p%d-c%d", n, workers), Scenario: "load", Concurrency: workers, Requests: *requests, DurationSeconds: duration.Seconds(), RequestRate: *rate, WarmupRequests: *warmup, PromptTokens: n, OutputTokens: *outputTokens, Prompts: prompts})
		}
	}
	last := s.Cases[len(s.Cases)-1]
	if *suite == "deployment" {
		if *sustain <= 0 {
			fmt.Fprintln(stderr, "sustain-duration must be positive")
			return 2
		}
		steady := last
		steady.ID, steady.Scenario, steady.Requests, steady.RequestRate, steady.DurationSeconds = "sustained", "sustained", 0, 0, sustain.Seconds()
		s.Cases = append(s.Cases, steady)
		burst := last
		burst.ID, burst.Scenario, burst.Requests, burst.WarmupRequests, burst.RequestRate, burst.DurationSeconds, burst.RecoveryProbe = "burst", "burst", 10, 0, 4, 0, true
		s.Cases = append(s.Cases, burst)
		if *api == "llama.cpp" {
			for _, enabled := range []bool{false, true} {
				cache := benchmark.ServingCase{ID: fmt.Sprintf("cache-%t", enabled), Scenario: "cache", Concurrency: 1, Requests: max(2, *requests), WarmupRequests: 1, CachePrompt: enabled, OutputTokens: *outputTokens, Prompts: []benchmark.ServingPrompt{{ID: "shared-prefix", Text: strings.Repeat("The project records repeatable local inference experiments. ", 32) + "Summarize this document."}}}
				s.Cases = append(s.Cases, cache)
			}
		} else {
			s.Cases = append(s.Cases, benchmark.ServingCase{ID: "conversation", Scenario: "conversation", Concurrency: 1, Requests: 6, OutputTokens: *outputTokens, ContinueConversation: true, Prompts: []benchmark.ServingPrompt{{ID: "remember", Text: "Remember the codeword CYAN. Acknowledge briefly."}, {ID: "recall", Text: "What codeword did I ask you to remember?", Expect: &benchmark.ServingChecks{Contains: []string{"CYAN"}}}}})
		}
		s.Cases = append(s.Cases, benchmark.ServingCase{ID: "format-smoke", Scenario: "quality-smoke", Concurrency: 1, Requests: 3, OutputTokens: min(64, *outputTokens), Prompts: []benchmark.ServingPrompt{{ID: "json-arithmetic", Text: `Return only the JSON object {"result":4} with no explanation.`, Expect: &benchmark.ServingChecks{JSON: true, JSONSubset: json.RawMessage(`{"result":4}`)}}}})
		s.Cases = append(s.Cases, benchmark.ServingCase{ID: "cancel", Scenario: "cancellation", Concurrency: 1, Requests: 2, OutputTokens: max(256, *outputTokens), CancelAfterMS: 250, RecoveryProbe: true})
	}
	if *idle > 0 {
		s.Cases = append(s.Cases, benchmark.ServingCase{ID: "idle-resume", Scenario: "idle-resume", Concurrency: 1, Requests: 1, OutputTokens: *outputTokens, IdleBeforeSeconds: idle.Seconds(), Prompts: prompts})
	}
	if *switchModels != "" {
		for i, name := range strings.Split(*switchModels, ",") {
			if strings.TrimSpace(name) == "" {
				fmt.Fprintln(stderr, "switch-models contains an empty alias")
				return 2
			}
			s.Cases = append(s.Cases, benchmark.ServingCase{ID: fmt.Sprintf("model-selection-%d", i), Scenario: "model-selection", Concurrency: 1, Requests: 1, OutputTokens: min(32, *outputTokens), Model: strings.TrimSpace(name)})
		}
	}
	plan := protocol.Plan{SchemaVersion: protocol.BenchmarkPlanV1, NodeID: *node, ExecutionMode: protocol.ExecutionMode(*mode), Runtime: protocol.Runtime{Kind: "http"}, Model: protocol.Model{ID: *model}}
	if err := benchmark.SetConfig(&plan, "serving", s); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if err = benchmark.ValidatePlan(plan); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if *output == "" {
		return writeJSON(out, plan)
	}
	if err = platform.WriteJSONNew(*output, plan, 0600); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(out, "✅ Created endpoint benchmark plan: %s (%d cases)\n", *output, len(s.Cases))
	return 0
}
