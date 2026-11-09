package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

func flagExitCode(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	return 2
}

// Keep environment-backed credentials out of help while retaining their values
// for execution. Only the displayed defaults change during PrintDefaults.
func setFlagOutput(f *flag.FlagSet, out io.Writer) {
	f.SetOutput(out)
	f.Usage = func() {
		fmt.Fprintf(out, "📋 Usage: modelgauge %s [options]\n", f.Name())
		saved := map[*flag.Flag]string{}
		f.VisitAll(func(value *flag.Flag) {
			if value.Name == "token" || value.Name == "admin-token" {
				saved[value] = value.DefValue
				value.DefValue = ""
			}
		})
		f.PrintDefaults()
		for value, original := range saved {
			value.DefValue = original
		}
	}
}

// Group help works without credentials, network access, or a running VM.
func showRequestedHelp(args []string, out io.Writer) (bool, int) {
	if len(args) == 0 {
		return false, 0
	}
	var path []string
	if args[0] == "help" {
		path = args[1:]
	} else if last := args[len(args)-1]; last == "--help" || last == "-h" {
		path = args[:len(args)-1]
	} else {
		return false, 0
	}
	if len(path) == 0 {
		printUsage(out)
		return true, 0
	}
	if (strings.Join(path, " ") == "model install" || strings.Join(path, " ") == "cleanup" || strings.Join(path, " ") == "export") && args[0] != "help" {
		return false, 0 // Render the install flag set, including its defaults.
	}
	text, ok := commandHelp[strings.Join(path, " ")]
	if !ok {
		if args[0] == "help" {
			fmt.Fprintln(out, "Unknown help topic. Use modelgauge --help or a leaf command followed by --help.")
			return true, 2
		}
		return false, 0 // Leaf flag sets render their own exact defaults.
	}
	fmt.Fprintln(out, text)
	return true, 0
}

var commandHelp = map[string]string{
	"export": `📦 Export results and metrics before cleanup
  modelgauge export --node benchmark-node --output exports/experiment
  modelgauge export --node benchmark-node --output exports/with-raw --include-raw
  modelgauge export --node benchmark-node --output exports/history \
    --prometheus-url http://127.0.0.1:9090 \
    --start 2026-11-07T10:00:00Z --end 2026-11-07T11:00:00Z
Exports JSON/CSV results, metadata, current controller metrics, manifest and checksums.
Raw files and Prometheus history are optional; missing requested data fails explicitly.
Use a new output directory and verify SHA256SUMS before cleanup.
See docs/operations/EXPORT.md. Use 'export --help' for all flags.`,
	"cleanup": `🧹 Cleanup — remove only resources with verified ModelGauge ownership
  modelgauge cleanup --dry-run --delete-vms
  modelgauge cleanup
  modelgauge cleanup --delete-vms
Default cleanup removes tracked guest directories/files and owned user services;
VMs are kept. --delete-vms also removes only VMs ModelGauge originally created,
including everything subsequently added to those VM disks. Reused VMs stay.
Unknown legacy installs are never adopted. Pending/active work blocks cleanup.
Stopped VMs are not started by a dry run; actual cleanup starts them to verify.
See docs/operations/CLEANUP.md. Use 'cleanup --help' for flags.`,
	"model install status": "📦 modelgauge model install status JOB_ID\nQueries one installation job as JSON. Use the returned job_id, not a model ID.\nOptional connection flags: --controller URL and --admin-token TOKEN.",
	"control": `🏗️ Control plane — k3s, PostgreSQL, controller, Prometheus, Grafana
  modelgauge control bootstrap --provider lima [--source REPO | --image IMAGE]
  modelgauge control status
Development: pass --source . from the repository. Released builds resolve their image.
Defaults for a new VM: 4 CPUs, 6 GiB RAM, 50 GiB disk; API localhost:18080.
See docs/deployment/BOOTSTRAP.md. Use 'control bootstrap --help' for all flags.`,
	"node": `🖥️ Nodes — a measured node runs the benchmark, separately from k3s
  modelgauge node bootstrap --provider lima --name benchmark-node [--source REPO]
  modelgauge node list
  modelgauge node status benchmark-node
  modelgauge node inspect
Bootstrap defaults: 2 CPUs, 3 GiB RAM, 30 GiB disk. Inspection requires Linux.
See docs/deployment/NODE-BOOTSTRAP.md. Node list/status return JSON.`,
	"model": `📦 Models — managed GGUF downloads and installed inventory
  modelgauge model search qwen2.5
  modelgauge model install qwen2.5-1.5b-instruct:q4_k_m --node benchmark-node
  modelgauge model install status JOB_ID
  modelgauge model install cancel JOB_ID
  modelgauge model list --node benchmark-node
Wait for completion and inventory registration before creating a campaign.
Omitting --node installs on this machine. See docs/GETTING-STARTED.md.`,
	"model install": `📦 Install a model
  modelgauge model install MODEL_ID --node NODE_ID
  modelgauge model install --id custom-model --url HTTPS_URL --sha256 HASH --node NODE_ID
  modelgauge model install status JOB_ID
  modelgauge model install cancel JOB_ID
Use --node for remote work. The response contains the job_id; poll status until completed.
Use 'model install --help' for the flag defaults.`,
	"benchmark": `🧪 Benchmarks — one plan per model; campaigns group plans
  modelgauge benchmark profiles list
  modelgauge benchmark campaign create --node benchmark-node --execution-mode lima-linux \
    --models qwen2.5-1.5b-instruct:q4_k_m --contexts 512 --threads 1,2 --output campaign --submit
  modelgauge benchmark campaign status --file campaign/campaign.json
  modelgauge benchmark plan create --node NODE_ID --model /target/model.gguf --output plan.json
  modelgauge benchmark submit --plan plan.json
  modelgauge benchmark run --plan plan.json --output results/local
  modelgauge benchmark cancel PLAN_ID
Campaigns resolve installed model IDs. Plan creation hashes a file visible to this CLI.
Always set --execution-mode lima-linux for Lima targets. See docs/BENCHMARKING.md.`,
	"benchmark campaign": `🧪 Campaigns — generate and optionally submit plans for installed model IDs
  modelgauge benchmark campaign create --node benchmark-node --execution-mode lima-linux \
    --models qwen2.5-1.5b-instruct:q4_k_m,qwen2.5-1.5b-instruct:q5_k_m \
    --contexts 512,4096 --threads 1,2 --output comparison --submit
  modelgauge benchmark campaign status --file comparison/campaign.json
Use a fresh output directory per experiment. Resume interrupted generation/submission with --resume and identical flags. Accepted jobs are preserved.`,
	"benchmark plan": `📋 Plans — generate a reproducible standard-cpu matrix
  modelgauge benchmark plan create --node linux-node --model /absolute/model.gguf \
    --model-id custom-model --contexts 512,4096 --threads 1,2 --output plan.json
  modelgauge plan validate plan.json
Run creation where the model file is readable; paths in the plan must be valid on the execution node.`,
	"benchmark profiles":      "🧪 Profiles\n  modelgauge benchmark profiles list\nCurrently available: standard-cpu (CPU-only prompt, generation, and peak RSS).",
	"benchmark profiles list": "🧪 Profiles\n  modelgauge benchmark profiles list\nReturns a JSON array of available profile names.",
	"plan":                    "📋 Plan templates\n  modelgauge plan example > plan.json\n  modelgauge plan validate plan.json\nReplace template node ID, model ID and model path before execution.",
	"plan example":            "📋 modelgauge plan example\nWrites a JSON template to stdout. Redirect it to a file and edit before running.",
	"plan validate":           "✅ modelgauge plan validate FILE\nValidates schema and case structure without running a model or proving file availability.",
	"results": `📈 Results — stored evidence and deterministic recommendations
  modelgauge results show --node benchmark-node
  modelgauge results recommend --node benchmark-node --model qwen2.5-1.5b-instruct:q4_k_m \
    --execution-mode lima-linux --context 512 --memory-budget-gib 2
Both commands return JSON. Recommendations require completed stable measurements
for the requested context. A memory budget is usable workload RAM, not host total RAM.`,
	"monitoring":      "📈 Monitoring\n  modelgauge monitoring open\nForwards Grafana to localhost:3000 and opens the ModelGauge overview.\nUsername: admin. Read the generated password using docs/operations/MONITORING.md.",
	"monitoring open": "📈 modelgauge monitoring open\nUses the saved control connection to forward Grafana and open its overview dashboard.",
	"agent": `⚙️ Linux agent — install and supervise on the measured node
  modelgauge agent install --controller URL --node NODE_ID --execution-mode native-isolated
  modelgauge agent status
  modelgauge agent run [--once] [--interval 30s]
  modelgauge agent uninstall
Install reads MODELGAUGE_AGENT_TOKEN and writes a private environment file.
Run reads configuration from environment; a systemd service loads the installed file.
See docs/CONFIGURATION.md. Use 'agent install --help' or 'agent run --help' for flags.`,
	"agent status":    "⚙️ modelgauge agent status\nPrints Linux systemd user service status as JSON without exposing credentials.",
	"agent uninstall": "⚙️ modelgauge agent uninstall\nStops and removes the Linux user service; retains configuration and benchmark evidence.",
	"node list":       "🖥️ modelgauge node list\nLists registered nodes as JSON using the saved or environment controller connection.",
	"control status":  "🏗️ modelgauge control status\nReports cluster nodes, deployments and persistent storage using the saved control VM.",
	"node status":     "🖥️ modelgauge node status NODE_ID\nPrints controller-observed node state, last contact and hardware as JSON.",
	"node inspect":    "🖥️ modelgauge node inspect\nPrints this Linux machine's hostname, architecture, CPU count and memory as JSON.",
	"model search":    "📦 modelgauge model search [QUERY]\nFetches metadata for matching entries in the built-in Hugging Face GGUF catalog. Requires network access.",
	"version":         "ℹ️ modelgauge version [--json]\nPlain output is the version string; --json adds commit, repository, build date, OS and architecture.",
}
