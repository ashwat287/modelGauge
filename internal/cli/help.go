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
	"model":                   "📦 Models\n  modelgauge model search QUERY\n  modelgauge model install MODEL\n  modelgauge model use MODEL\n  modelgauge model list\nDownloads happen on the workspace node. Install selects the model; use selects an installed model.",
	"model install":           "📦 modelgauge model install MODEL [--url HTTPS_URL --sha256 HASH]\nDownloads and selects a model, streams progress, and waits for completion. Ctrl+C requests cancellation; no job ID is needed.",
	"node":                    "🖥️ One benchmark node\n  modelgauge node reconnect\n  modelgauge node bootstrap\n  modelgauge node inspect\nThe workspace owns one benchmark node, separate from k3s.",
	"node inspect":            "🖥️ modelgauge node inspect\nInspect this Linux machine as JSON.",
	"control":                 "🏗️ One control plane\n  modelgauge setup\n  modelgauge control bootstrap\nExisting VM resources are reused, not resized.",
	"benchmark":               "🧪 modelgauge run benchmark.yaml\nOne selected model, one node, ordered benchmark stages. Progress is shown until completion; Ctrl+C stops the operation.\nAvailable profiles: modelgauge benchmark profiles list.",
	"benchmark profiles list": "🧪 modelgauge benchmark profiles list\nList registered profiles as JSON.",
	"results":                 "📈 modelgauge results [--experiment NAME] [--json]\nShow the latest completed run by default.\nmodelgauge results recommend --context 512 [--memory-budget-gib 2]\nRecommendations use the selected model and measured node.",
	"export":                  "📦 modelgauge export --output DIR [--include-raw] [--experiment NAME]\nExport the latest completed run, with checksums. --node NODE retains the advanced whole-history export.\nOptional Prometheus history: --prometheus-url URL --start RFC3339 --end RFC3339.",
	"cleanup":                 "🧹 modelgauge cleanup [--dry-run] [--force] [--delete-vms]\nRemove only verified owned files/services. --delete-vms also removes VMs created by ModelGauge.\nActive work blocks cleanup; --force cancels managed work first. Export evidence first.",
	"version":                 "ℹ️ modelgauge version [--json]\nShow version and optional build identity.",
}
