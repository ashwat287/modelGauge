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
	"github.com/nerdylucifer/modelgauge/internal/control"
	"github.com/nerdylucifer/modelgauge/internal/platform"
	"io"
	"os"
	"os/signal"
	"regexp"
	"runtime"
	"strings"
	"syscall"
)

func dispatch(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 2 && args[0] == "_remote-host" {
		if err := control.RunRemoteHost(ctx, args[1], os.Getenv("SSH_ORIGINAL_COMMAND"), os.Stdin, stdout, stderr); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
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
	case "setup":
		return runSetup(ctx, args[1:], stdout, stderr)
	case "run", "validate", "history":
		return runDeclaration(ctx, args[0], args[1:], stdout, stderr)
	case "export":
		return runExport(ctx, args[1:], stdout, stderr)
	case "cleanup":
		return runCleanup(ctx, args[1:], stdout, stderr)
	case "control":
		return runControl(ctx, args[1:], stdout, stderr)
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
	case "node":
		return runNode(ctx, args[1:], stdout, stderr)
	case "benchmark":
		return runBenchmark(ctx, args[1:], stdout, stderr)
	case "server":
		return runServer(ctx, args[1:], stdout, stderr)
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
	if len(args) == 0 || args[0] == "" || args[0][0] == '-' {
		args = append([]string{"show"}, args...)
	}
	flags := flag.NewFlagSet("results", flag.ContinueOnError)
	setFlagOutput(flags, stderr)
	experiment := flags.String("experiment", "", "read a named experiment instead of the latest completed run")
	raw := flags.Bool("json", false, "structured result")
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
	if *controllerURL == "" || *adminToken == "" {
		fmt.Fprintln(stderr, "--node, --controller, and --admin-token are required (controller and token may be environment variables)")
		return 2
	}
	api := &client.Client{BaseURL: *controllerURL, AdminToken: *adminToken}
	target, err := workspaceNode(ctx, api)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *nodeID != "" && *nodeID != target.ID {
		fmt.Fprintln(stderr, "node differs from workspace")
		return 2
	}
	*nodeID = target.ID
	switch args[0] {
	case "show":
		result, err := selectedResult(ctx, api, *nodeID, *experiment)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *raw {
			return writeJSON(stdout, result)
		}
		fmt.Fprintf(stdout, "Model: %s\nState: %s\nPeak RSS: %.2f GiB\n", result.ModelID, result.Status, float64(result.PeakRSSBytes)/(1<<30))
		for _, c := range result.Cases {
			fmt.Fprintf(stdout, "%s: %.2f tokens/sec\n", c.CaseID, c.TokensPerSecond)
		}
		return 0
	case "recommend":
		if *modelID == "" {
			c, _ := control.Load()
			*modelID = c.Model
		}
		if *mode == "" {
			*mode = string(target.ExecutionMode)
		}
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

func runBenchmark(ctx context.Context, args []string, out, stderr io.Writer) int {
	if len(args) == 2 && args[0] == "profiles" && args[1] == "list" {
		return writeJSON(out, benchmark.Profiles())
	}
	fmt.Fprintln(stderr, "use modelgauge run benchmark.yaml")
	return 2
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
	fmt.Fprint(writer, `📊 ModelGauge — benchmark one model on your node

Usage: modelgauge <command> [options]

  setup                       Prepare the controller and a local or remote benchmark VM
  model search QUERY          Find a catalog model
  model install MODEL         Download/select a model; stream progress until complete
  model use MODEL             Select an already installed model
  model list                  List installed models
  validate benchmark.yaml     Preview the selected model and benchmark stages
  run benchmark.yaml          Run an experiment and follow live progress to completion
  results                     Show the latest completed measurements
  export --output DIR         Export the latest completed run
  history                     List recent experiments
  node reconnect              Refresh the node connection without rebuilding
  cleanup --delete-vms        Remove verified owned resources and created VMs
  version [--json]            Show build identity

Use --experiment NAME with results or export for completed history.
Use --json for structured experiment/results output, including internal IDs.
Advanced provisioning: control bootstrap, node bootstrap/reconnect/inspect.
Internal service: server. Benchmark profiles: benchmark profiles list.
Use '<command> --help' for flags. Workloads are supplied as benchmark.yaml files.
`)

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
				fmt.Fprintln(stderr, "Stopping owned work; cancellation may take a moment. Interrupt again to stop waiting.")
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
	if len(args) > 0 && (args[0] == "_guest" || args[0] == "_remote-host") {
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
var dashboardTimeURL = regexp.MustCompile(`^` + regexp.QuoteMeta(liveDashboardURL) + `\?from=[0-9]+&to=(now|[0-9]+)&refresh=(5s)?$`)

func (d *redactedErrors) Write(p []byte) (int, error) {
	s := string(p)
	for _, secret := range d.secrets {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "[REDACTED]")
		}
	}
	s = urlUserInfo.ReplaceAllString(s, "$1[REDACTED]@")
	s = urlQuery.ReplaceAllStringFunc(s, func(value string) string {
		if dashboardTimeURL.MatchString(value) {
			return value
		}
		address, _, _ := strings.Cut(value, "?")
		return address + "?[REDACTED]"
	})
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
