package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/nerdylucifer/modelgauge/internal/benchmark"
	"github.com/nerdylucifer/modelgauge/internal/buildinfo"
	"github.com/nerdylucifer/modelgauge/internal/client"
	"github.com/nerdylucifer/modelgauge/internal/platform"
	"github.com/nerdylucifer/modelgauge/internal/profile"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

func dispatch(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "_guest" {
		return runGuest(ctx, args[1:], stdout, stderr)
	}
	if handled, code := showRequestedHelp(args, stdout); handled {
		return code
	}
	loadControlEnvironment()
	if len(args) == 0 {
		printUsage(stdout)
		return 0
	}

	switch args[0] {
	case "export":
		return runExport(ctx, args[1:], stdout, stderr)
	case "cleanup":
		return runCleanup(ctx, args[1:], stdout, stderr)
	case "control":
		return runControl(ctx, args[1:], stdout, stderr)
	case "monitoring":
		return runMonitoring(ctx, args[1:], stdout, stderr)
	case "help", "--help", "-h":
		printUsage(stdout)
		return 0
	case "version", "--version":
		if len(args) == 2 && args[1] == "--json" {
			return writeJSON(stdout, buildinfo.Info{Version: buildinfo.Version, Commit: buildinfo.Commit, Date: buildinfo.Date, Repository: buildinfo.Repository, OS: runtime.GOOS, Architecture: runtime.GOARCH, Channel: buildinfo.Channel})
		}
		if len(args) > 1 {
			fmt.Fprintln(stderr, "usage: modelgauge version [--json]")
			return 2
		}
		fmt.Fprintln(stdout, buildinfo.Version)
		return 0
	case "plan":
		return runPlan(ctx, args[1:], stdout, stderr)
	case "node":
		return runNode(ctx, args[1:], stdout, stderr)
	case "benchmark":
		return runBenchmark(ctx, args[1:], stdout, stderr)
	case "server":
		return runServer(ctx, args[1:], stdout, stderr)
	case "agent":
		return runAgent(ctx, args[1:], stdout, stderr)
	case "results":
		return runResults(ctx, args[1:], stdout, stderr)
	case "model":
		return runModel(ctx, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func runResults(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: modelgauge results <show|recommend> ...")
		return 2
	}
	flags := flag.NewFlagSet("results", flag.ContinueOnError)
	setFlagOutput(flags, stderr)
	nodeID := flags.String("node", "", "node ID")
	controllerURL := flags.String("controller", os.Getenv("MODELGAUGE_CONTROLLER_URL"), "controller URL")
	adminToken := flags.String("admin-token", os.Getenv("MODELGAUGE_ADMIN_TOKEN"), "controller admin token")
	modelID := flags.String("model", "", "model ID")
	mode := flags.String("execution-mode", "", "execution mode")
	contextTokens := flags.Int("context", 0, "context tokens")
	memoryGiB := flags.Float64("memory-budget-gib", 0, "usable benchmark-node memory budget in GiB")
	if err := flags.Parse(args[1:]); err != nil {
		return flagExitCode(err)
	}
	if *nodeID == "" || *controllerURL == "" || *adminToken == "" {
		fmt.Fprintln(stderr, "--node, --controller, and --admin-token are required (controller and token may be environment variables)")
		return 2
	}
	api := &client.Client{BaseURL: *controllerURL, AdminToken: *adminToken}
	switch args[0] {
	case "show":
		var results any
		if _, err := api.DoJSON(ctx, "GET", "/v1/nodes/"+*nodeID+"/results", "admin", nil, &results); err != nil {
			fmt.Fprintf(stderr, "get results: %v\n", err)
			return 1
		}
		return writeJSON(stdout, results)
	case "recommend":
		if *modelID == "" || *mode == "" || *contextTokens < 1 {
			fmt.Fprintln(stderr, "--model, --execution-mode, and positive --context are required")
			return 2
		}
		budgetBytes := int64(*memoryGiB * 1024 * 1024 * 1024)
		path := fmt.Sprintf("/v1/nodes/%s/recommendation?model=%s&execution_mode=%s&context=%d&memory_budget_bytes=%d", *nodeID, *modelID, *mode, *contextTokens, budgetBytes)
		var recommendation any
		if _, err := api.DoJSON(ctx, "GET", path, "admin", nil, &recommendation); err != nil {
			fmt.Fprintf(stderr, "get recommendation: %v\n", err)
			return 1
		}
		return writeJSON(stdout, recommendation)
	default:
		fmt.Fprintf(stderr, "unknown results command %q\n", args[0])
		return 2
	}
}

func runPlan(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: modelgauge plan <example|validate>")
		return 2
	}
	switch args[0] {
	case "example":
		return writeJSON(stdout, protocol.ExamplePlan())
	case "validate":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "usage: modelgauge plan validate FILE")
			return 2
		}
		file, err := os.Open(args[1])
		if err != nil {
			fmt.Fprintf(stderr, "open plan: %v\n", err)
			return 1
		}
		defer file.Close()

		var plan protocol.Plan
		decoder := json.NewDecoder(file)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&plan); err != nil {
			fmt.Fprintf(stderr, "decode plan: %v\n", err)
			return 1
		}
		if err := benchmark.ValidatePlan(plan); err != nil {
			fmt.Fprintf(stderr, "invalid plan: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "✅ Plan is valid")
		return 0
	default:
		fmt.Fprintf(stderr, "unknown plan command %q\n", args[0])
		return 2
	}
}

func runBenchmark(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: modelgauge benchmark <profiles|plan|run|submit> ...")
		return 2
	}
	switch args[0] {
	case "serving":
		return runServing(ctx, args[1:], stdout, stderr)
	case "cancel":
		return cancelJob(ctx, args[1:], stdout, stderr, false)
	case "campaign":
		return runCampaign(ctx, args[1:], stdout, stderr)
	case "profiles":
		if len(args) != 2 || args[1] != "list" {
			fmt.Fprintln(stderr, "usage: modelgauge benchmark profiles list")
			return 2
		}
		return writeJSON(stdout, profile.Names())
	case "plan":
		return runBenchmarkPlan(ctx, args[1:], stdout, stderr)
	case "run":
		flags := flag.NewFlagSet("benchmark run", flag.ContinueOnError)
		setFlagOutput(flags, stderr)
		planPath := flags.String("plan", "", "path to a benchmark plan")
		outputPath := flags.String("output", "", "directory for result artifacts")
		if err := flags.Parse(args[1:]); err != nil {
			return flagExitCode(err)
		}
		plan, ok := loadPlan(*planPath, stderr)
		if !ok {
			return 1
		}
		result, outputDir, err := benchmark.Run(ctx, plan, *outputPath)
		if err != nil {
			fmt.Fprintf(stderr, "run benchmark: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "✅ Completed %s\n📁 Artifacts: %s\n", result.RunID, outputDir)
		return 0
	case "submit":
		flags := flag.NewFlagSet("benchmark submit", flag.ContinueOnError)
		setFlagOutput(flags, stderr)
		planPath := flags.String("plan", "", "path to a benchmark plan")
		controllerURL := flags.String("controller", os.Getenv("MODELGAUGE_CONTROLLER_URL"), "controller URL")
		adminToken := flags.String("admin-token", os.Getenv("MODELGAUGE_ADMIN_TOKEN"), "controller admin token")
		if err := flags.Parse(args[1:]); err != nil {
			return flagExitCode(err)
		}
		if *controllerURL == "" || *adminToken == "" {
			fmt.Fprintln(stderr, "--controller and --admin-token are required (or set MODELGAUGE_CONTROLLER_URL and MODELGAUGE_ADMIN_TOKEN)")
			return 2
		}
		plan, ok := loadPlan(*planPath, stderr)
		if !ok {
			return 1
		}
		api := &client.Client{BaseURL: *controllerURL, AdminToken: *adminToken}
		directory, err := submissionCacheDir()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		absolute, err := filepath.Abs(*planPath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		pending, err := reserveSubmission(directory, api.BaseURL+"\n/v1/plans\n"+absolute, plan)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		defer pending.close()
		var response client.Submission
		if _, err = api.SubmitJSON(ctx, "/v1/plans", pending.RequestID, plan, &response); err != nil {
			fmt.Fprintf(stderr, "submit plan: %v; retry the same command to check the existing submission safely\n", err)
			return 1
		}
		if _, err = fmt.Fprintf(stdout, "%s %s\n", response.Status, response.PlanID); err != nil {
			fmt.Fprintln(stderr, "write submission response:", err)
			return 1
		}
		pending.complete(stderr)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown benchmark command %q\n", args[0])
		return 2
	}
}

func runBenchmarkPlan(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "create" {
		fmt.Fprintln(stderr, "usage: modelgauge benchmark plan create --node ID --model PATH --profile standard-cpu [options]")
		return 2
	}
	flags := flag.NewFlagSet("benchmark plan create", flag.ContinueOnError)
	setFlagOutput(flags, stderr)
	nodeID := flags.String("node", "", "target node ID")
	modelPath := flags.String("model", "", "absolute model path on the target node")
	modelID := flags.String("model-id", "", "stable model ID (default: model filename)")
	profileName := flags.String("profile", profile.StandardCPU, "benchmark profile")
	binary := flags.String("binary", "", "absolute llama-bench path on the target node")
	mode := flags.String("execution-mode", "native-isolated", "native-isolated or lima-linux")
	threadsText := flags.String("threads", "1,2", "comma-separated thread counts")
	contextsText := flags.String("contexts", "512,4096,8192", "comma-separated prompt contexts")
	warmups := flags.Int("warmups", 3, "warm-up repetitions per case")
	repetitions := flags.Int("repetitions", 5, "measured repetitions per case")
	overwrite := flags.Bool("overwrite", false, "replace an existing generated plan")
	output := flags.String("output", "", "plan output path")
	if err := flags.Parse(args[1:]); err != nil {
		return flagExitCode(err)
	}
	if *profileName != profile.StandardCPU {
		fmt.Fprintf(stderr, "unknown profile %q\n", *profileName)
		return 2
	}
	if *nodeID == "" || *modelPath == "" {
		fmt.Fprintln(stderr, "--node and --model are required")
		return 2
	}
	threads, err := integerList(*threadsText)
	if err != nil {
		fmt.Fprintf(stderr, "--threads: %v\n", err)
		return 2
	}
	contexts, err := integerList(*contextsText)
	if err != nil {
		fmt.Fprintf(stderr, "--contexts: %v\n", err)
		return 2
	}
	plan, err := profile.StandardCPUPlanContext(ctx, profile.Options{NodeID: *nodeID, ExecutionMode: protocol.ExecutionMode(*mode), ModelPath: *modelPath, ModelID: *modelID, Binary: *binary, Threads: threads, Contexts: contexts, Warmups: *warmups, Repetitions: *repetitions})
	if err != nil {
		fmt.Fprintf(stderr, "create plan: %v\n", err)
		return 1
	}
	if err := benchmark.ValidatePlan(plan); err != nil {
		fmt.Fprintf(stderr, "create plan: %v\n", err)
		return 1
	}
	if *output == "" {
		*output = filepath.Base(plan.Model.ID) + "-" + *profileName + ".json"
	}
	if err := ctx.Err(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	writePlan := platform.WriteJSONNew
	if *overwrite {
		if info, e := os.Lstat(*output); e == nil && !info.Mode().IsRegular() {
			fmt.Fprintln(stderr, "refusing to replace a non-regular plan path")
			return 1
		}
		writePlan = platform.WriteJSON
	}
	if err := writePlan(*output, plan, 0600); err != nil {
		fmt.Fprintf(stderr, "create plan file: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "✅ Created %s\n", *output)
	return 0
}

func integerList(value string) ([]int, error) {
	parts := strings.Split(value, ",")
	result := make([]int, 0, len(parts))
	for _, part := range parts {
		number, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || number < 1 {
			return nil, fmt.Errorf("expected positive integers")
		}
		result = append(result, number)
	}
	return result, nil
}

func loadPlan(path string, stderr io.Writer) (protocol.Plan, bool) {
	if path == "" {
		fmt.Fprintln(stderr, "--plan is required")
		return protocol.Plan{}, false
	}
	file, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(stderr, "open plan: %v\n", err)
		return protocol.Plan{}, false
	}
	defer file.Close()
	var plan protocol.Plan
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		fmt.Fprintf(stderr, "decode plan: %v\n", err)
		return protocol.Plan{}, false
	}
	if err := benchmark.ValidatePlan(plan); err != nil {
		fmt.Fprintf(stderr, "invalid plan: %v\n", err)
		return protocol.Plan{}, false
	}
	return plan, true
}

func runNode(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] != "inspect" {
		return runNodeManagement(ctx, args, stdout, stderr)
	}
	if len(args) != 1 || args[0] != "inspect" {
		fmt.Fprintln(stderr, "usage: modelgauge node inspect")
		return 2
	}
	host, err := platform.Inspect()
	if err != nil {
		fmt.Fprintf(stderr, "inspect node: %v\n", err)
		return 1
	}
	return writeJSON(stdout, host)
}

func writeJSON(writer io.Writer, value any) int {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		if !errors.Is(err, io.ErrClosedPipe) {
			fmt.Fprintf(os.Stderr, "write JSON: %v\n", err)
		}
		return 1
	}
	return 0
}

func printUsage(writer io.Writer) {
	fmt.Fprint(writer, `📊 ModelGauge — AI model performance and memory fit

Usage: modelgauge <command> [options]

🏗️  Infrastructure
  control bootstrap   Create/reuse a Lima k3s control plane
  control status      Inspect cluster workloads and persistent storage
  cleanup             Remove verified owned resources; --dry-run previews
  cleanup --delete-vms  Also delete only VMs ModelGauge created
  node bootstrap      Create/reuse a separate Linux benchmark VM
  node list           List registered measured nodes (JSON)
  node status ID      Inspect a registered node (JSON)
  node inspect        Inspect this Linux machine (JSON)

📦 Models
  model search QUERY          Search the built-in GGUF catalog (JSON)
  model install MODEL --node ID  Queue a model download on a node (JSON)
  model install status JOB_ID Inspect a download job (JSON)
  model install cancel JOB_ID Cancel a download job (JSON)
  model list --node ID        List a node's installed models (JSON)

🧪 Experiments
  benchmark profiles list    List supported benchmark profiles (JSON)
  benchmark campaign create  Generate plans for installed model IDs
  benchmark campaign status  Read campaign progress (JSON)
  benchmark plan create      Generate one plan using a model file
  benchmark submit --plan FILE  Queue a plan (JSON)
  benchmark run --plan FILE   Execute locally on Linux
  benchmark serving plan     Plan benchmarks for an existing inference endpoint
  benchmark serving run      Measure serving latency, load, quality and resources
  benchmark serving report   Read a serving result as JSON or CSV
  benchmark cancel PLAN_ID   Cancel queued or active work (JSON)
  plan example               Print an editable plan template (JSON)
  plan validate FILE         Validate a plan without running it

📈 Results and monitoring
  export --node ID --output DIR  Save results, CSV, metrics and optional evidence
  results show --node ID     Read stored measurements (JSON)
  results recommend         Recommend threads and memory fit (JSON)
  monitoring open           Forward and open Grafana

⚙️  Services and build
  agent run|install|status|uninstall  Manage a Linux agent
  server                    Run the PostgreSQL-backed controller
  version [--json]          Print build identity

🚀 Example (development checkout; Go and Lima required)
  go build -o bin/modelgauge ./cmd/modelgauge
  bin/modelgauge control bootstrap --provider lima --source .
  bin/modelgauge node bootstrap --provider lima --name benchmark-node --source .
  bin/modelgauge model install qwen2.5-1.5b-instruct:q4_k_m --node benchmark-node

Use 'modelgauge help benchmark' for examples and '<command> --help' for flags.
Read README.md and docs/CLI.md for the complete workflow.
JSON responses and plain version output remain suitable for scripts.
`)
}

// Run is retained for embedded callers; the executable supplies signal handling.
func Run(args []string, out, stderr io.Writer) int {
	return RunContext(context.Background(), args, out, stderr)
}
func RunWithSignals(args []string, out, stderr io.Writer) int {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case s := <-signals:
			if len(args) > 0 && (args[0] == "server" || (args[0] == "agent" && len(args) > 1 && args[1] == "run")) {
				_ = json.NewEncoder(stderr).Encode(map[string]string{"level": "INFO", "msg": "shutdown requested; stopping owned work"})
			} else {
				fmt.Fprintln(stderr, "Stopping owned work; recovery may take a moment. Interrupt again to stop waiting.")
			}
			cancel(signalFailure{signal: s})
			select {
			case <-signals:
				os.Exit(130)
			case <-done:
			}
		case <-done:
		}
	}()
	return RunContext(ctx, args, out, stderr)
}
func RunContext(ctx context.Context, args []string, out, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "_guest" {
		return dispatch(ctx, args, out, stderr)
	}
	if handled, code := showRequestedHelp(args, out); handled {
		return code
	}
	loadControlEnvironment()
	errors := &redactedErrors{target: stderr, secrets: secretValues(args)}
	code := dispatch(ctx, args, out, errors)
	if ctx.Err() != nil && code != 0 {
		return interruptionExit(ctx)
	}
	return code
}

type redactedErrors struct {
	target  io.Writer
	secrets []string
}

var urlUserInfo = regexp.MustCompile(`(https?://)[^/\s@]+@`)
var urlQuery = regexp.MustCompile(`(https?://[^\s?]+)\?[^\s]+`)

func (d *redactedErrors) Write(p []byte) (int, error) {
	s := string(p)
	for _, secret := range d.secrets {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "[REDACTED]")
		}
	}
	s = urlUserInfo.ReplaceAllString(s, "$1[REDACTED]@")
	s = urlQuery.ReplaceAllString(s, "$1?[REDACTED]")
	_, err := io.WriteString(d.target, s)
	return len(p), err
}
func secretValues(args []string) []string {
	var values []string
	for _, entry := range os.Environ() {
		k, v, _ := strings.Cut(entry, "=")
		if strings.Contains(k, "TOKEN") || strings.Contains(k, "PASSWORD") {
			values = append(values, v)
		}
	}
	for i, arg := range args {
		if arg == "--token" || arg == "--admin-token" {
			if i+1 < len(args) {
				values = append(values, args[i+1])
			}
		}
		for _, prefix := range []string{"--token=", "--admin-token="} {
			if strings.HasPrefix(arg, prefix) {
				values = append(values, strings.TrimPrefix(arg, prefix))
			}
		}
	}
	return values
}

type signalFailure struct{ signal os.Signal }

func (e signalFailure) Error() string { return fmt.Sprintf("received %s", e.signal) }
func interruptionExit(ctx context.Context) int {
	if failure, ok := context.Cause(ctx).(signalFailure); ok {
		if failure.signal == syscall.SIGTERM {
			return 143
		}
		if failure.signal == syscall.SIGHUP {
			return 129
		}
	}
	if ctx.Err() == context.DeadlineExceeded {
		return 124
	}
	return 130
}
