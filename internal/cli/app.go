package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/nerdylucifer/modelgauge/internal/benchmark"
	"github.com/nerdylucifer/modelgauge/internal/buildinfo"
	"github.com/nerdylucifer/modelgauge/internal/client"
	"github.com/nerdylucifer/modelgauge/internal/platform"
	"github.com/nerdylucifer/modelgauge/internal/profile"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

func Run(args []string, stdout, stderr io.Writer) int {
	if handled, code := showRequestedHelp(args, stdout); handled {
		return code
	}
	loadControlEnvironment()
	if len(args) == 0 {
		printUsage(stdout)
		return 0
	}

	switch args[0] {
	case "control":
		return runControl(args[1:], stdout, stderr)
	case "monitoring":
		return runMonitoring(args[1:], stdout, stderr)
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
		return runPlan(args[1:], stdout, stderr)
	case "node":
		return runNode(args[1:], stdout, stderr)
	case "benchmark":
		return runBenchmark(args[1:], stdout, stderr)
	case "server":
		return runServer(args[1:], stdout, stderr)
	case "agent":
		return runAgent(args[1:], stdout, stderr)
	case "results":
		return runResults(args[1:], stdout, stderr)
	case "model":
		return runModel(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func runResults(args []string, stdout, stderr io.Writer) int {
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
		if _, err := api.DoJSON(context.Background(), "GET", "/v1/nodes/"+*nodeID+"/results", "admin", nil, &results); err != nil {
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
		if _, err := api.DoJSON(context.Background(), "GET", path, "admin", nil, &recommendation); err != nil {
			fmt.Fprintf(stderr, "get recommendation: %v\n", err)
			return 1
		}
		return writeJSON(stdout, recommendation)
	default:
		fmt.Fprintf(stderr, "unknown results command %q\n", args[0])
		return 2
	}
}

func runPlan(args []string, stdout, stderr io.Writer) int {
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
		if err := plan.Validate(); err != nil {
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

func runBenchmark(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: modelgauge benchmark <profiles|plan|run|submit> ...")
		return 2
	}
	switch args[0] {
	case "cancel":
		return cancelJob(args[1:], stdout, stderr, false)
	case "campaign":
		return runCampaign(args[1:], stdout, stderr)
	case "profiles":
		if len(args) != 2 || args[1] != "list" {
			fmt.Fprintln(stderr, "usage: modelgauge benchmark profiles list")
			return 2
		}
		return writeJSON(stdout, profile.Names())
	case "plan":
		return runBenchmarkPlan(args[1:], stdout, stderr)
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
		result, outputDir, err := benchmark.Run(context.Background(), plan, *outputPath)
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
		var response struct {
			PlanID string `json:"plan_id"`
			Status string `json:"status"`
		}
		_, err := api.DoJSON(context.Background(), "POST", "/v1/plans", "admin", plan, &response)
		if err != nil {
			fmt.Fprintf(stderr, "submit plan: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "queued %s\n", response.PlanID)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown benchmark command %q\n", args[0])
		return 2
	}
}

func runBenchmarkPlan(args []string, stdout, stderr io.Writer) int {
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
	plan, err := profile.StandardCPUPlan(profile.Options{NodeID: *nodeID, ExecutionMode: protocol.ExecutionMode(*mode), ModelPath: *modelPath, ModelID: *modelID, Binary: *binary, Threads: threads, Contexts: contexts, Warmups: *warmups, Repetitions: *repetitions})
	if err != nil {
		fmt.Fprintf(stderr, "create plan: %v\n", err)
		return 1
	}
	if err := plan.Validate(); err != nil {
		fmt.Fprintf(stderr, "create plan: %v\n", err)
		return 1
	}
	if *output == "" {
		*output = filepath.Base(plan.Model.ID) + "-" + *profileName + ".json"
	}
	file, err := os.OpenFile(*output, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		fmt.Fprintf(stderr, "create plan file: %v\n", err)
		return 1
	}
	defer file.Close()
	if code := writeJSON(file, plan); code != 0 {
		return code
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
	if err := plan.Validate(); err != nil {
		fmt.Fprintf(stderr, "invalid plan: %v\n", err)
		return protocol.Plan{}, false
	}
	return plan, true
}

func runNode(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] != "inspect" {
		return runNodeManagement(args, stdout, stderr)
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
  benchmark cancel PLAN_ID   Cancel queued or active work (JSON)
  plan example               Print an editable plan template (JSON)
  plan validate FILE         Validate a plan without running it

📈 Results and monitoring
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
