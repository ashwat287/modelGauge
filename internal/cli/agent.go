package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/nerdylucifer/modelgauge/internal/agent"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

func runAgent(ctx context.Context, args []string, stdout, stderr interface{ Write([]byte) (int, error) }) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: modelgauge agent <run|install|status|uninstall>")
		return 2
	}
	switch args[0] {
	case "install":
		return installAgent(ctx, args[1:], stdout, stderr)
	case "status":
		status, err := agent.StatusContext(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "agent status: %v\n", err)
			return 1
		}
		return writeJSON(stdout, status)
	case "uninstall":
		if err := agent.UninstallContext(ctx); err != nil {
			fmt.Fprintf(stderr, "uninstall agent: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "agent service removed; private configuration was retained")
		return 0
	case "run":
	default:
		fmt.Fprintln(stderr, "usage: modelgauge agent <run|install|status|uninstall>")
		return 2
	}
	flags := flag.NewFlagSet("agent run", flag.ContinueOnError)
	setFlagOutput(flags, stderr)
	once := flags.Bool("once", false, "lease and execute at most one plan")
	interval := flags.Duration("interval", 30*time.Second, "poll interval")
	if err := flags.Parse(args[1:]); err != nil {
		return flagExitCode(err)
	}
	config, err := agent.ConfigFromEnv(os.Getenv)
	if err != nil {
		fmt.Fprintf(stderr, "agent configuration: %v\n", err)
		return 2
	}
	if err := agent.EnsureResultsDir(config.ResultsDir); err != nil {
		fmt.Fprintf(stderr, "create result directory: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "modelgauge agent polling %s as %s\n", config.ControllerURL, config.NodeID)
	if err := agent.Run(ctx, config, *once, *interval); err != nil {
		fmt.Fprintf(stderr, "agent: %v\n", err)
		return 1
	}
	return 0
}

func installAgent(ctx context.Context, args []string, stdout, stderr interface{ Write([]byte) (int, error) }) int {
	flags := flag.NewFlagSet("agent install", flag.ContinueOnError)
	setFlagOutput(flags, stderr)
	controllerURL := flags.String("controller", "", "controller URL")
	token := flags.String("token", os.Getenv("MODELGAUGE_AGENT_TOKEN"), "agent token (or MODELGAUGE_AGENT_TOKEN)")
	nodeID := flags.String("node", "", "node ID")
	name := flags.String("name", "", "node display name")
	kind := flags.String("kind", "physical", "physical or virtual")
	mode := flags.String("execution-mode", "native-isolated", "native-isolated or lima-linux")
	results := flags.String("results-dir", "", "result artifact directory")
	interval := flags.Duration("interval", 30*time.Second, "controller poll interval")
	if err := flags.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if *controllerURL == "" || *token == "" || *nodeID == "" {
		fmt.Fprintln(stderr, "--controller, --token, and --node are required")
		return 2
	}
	if *name == "" {
		*name = *nodeID
	}
	if *results == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(stderr, "resolve home directory: %v\n", err)
			return 1
		}
		*results = home + "/modelgauge-results"
	}
	config := agent.Config{ControllerURL: *controllerURL, Token: *token, NodeID: *nodeID, DisplayName: *name, NodeKind: *kind, ExecutionMode: protocol.ExecutionMode(*mode), ResultsDir: *results}
	if _, err := agent.ConfigFromEnv(func(key string) string {
		values := map[string]string{"MODELGAUGE_CONTROLLER_URL": config.ControllerURL, "MODELGAUGE_AGENT_TOKEN": config.Token, "MODELGAUGE_NODE_ID": config.NodeID, "MODELGAUGE_NODE_NAME": config.DisplayName, "MODELGAUGE_NODE_KIND": config.NodeKind, "MODELGAUGE_EXECUTION_MODE": string(config.ExecutionMode), "MODELGAUGE_RESULTS_DIR": config.ResultsDir}
		return values[key]
	}); err != nil {
		fmt.Fprintf(stderr, "agent configuration: %v\n", err)
		return 2
	}
	status, err := agent.InstallContext(ctx, agent.InstallOptions{Config: config, Interval: interval.String()})
	if err != nil {
		fmt.Fprintf(stderr, "install agent: %v\n", err)
		return 1
	}
	return writeJSON(stdout, status)
}
