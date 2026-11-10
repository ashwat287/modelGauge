package cli

import (
	"context"
	"flag"
	"fmt"
	"github.com/nerdylucifer/modelgauge/internal/control"
	"github.com/nerdylucifer/modelgauge/internal/exporter"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
	"io"
	"os"
	"time"
)

type exportQueries []string

func (q *exportQueries) String() string         { return fmt.Sprint([]string(*q)) }
func (q *exportQueries) Set(value string) error { *q = append(*q, value); return nil }
func runExport(ctx context.Context, args []string, out, stderr io.Writer) int {
	f := flag.NewFlagSet("export", flag.ContinueOnError)
	setFlagOutput(f, stderr)
	runID := f.String("run", "", "export one run and its related plan/evidence instead of all node history")
	node := f.String("node", "", "node ID to export")
	destination := f.String("output", "", "new directory for the export bundle")
	controllerURL := f.String("controller", os.Getenv("MODELGAUGE_CONTROLLER_URL"), "controller URL")
	token := f.String("admin-token", os.Getenv("MODELGAUGE_ADMIN_TOKEN"), "controller admin token")
	includeRaw := f.Bool("include-raw", false, "retrieve checksum-verified raw files from a tracked Lima node")
	rawFrom := f.String("raw-from", "", "explicit local directory containing copied node run directories")
	prom := f.String("prometheus-url", "", "reachable Prometheus base URL for optional history")
	start := f.String("start", "", "history start, RFC3339 with timezone")
	end := f.String("end", "", "history end, RFC3339 with timezone")
	step := f.Duration("step", 30*time.Second, "history query resolution")
	var queries exportQueries
	f.Var(&queries, "query", "PromQL history expression (repeatable; defaults to node throughput, RSS, RAM and CPU)")
	if err := f.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if f.NArg() != 0 || *includeRaw && *rawFrom != "" {
		fmt.Fprintln(stderr, "export takes flags only; choose --include-raw or --raw-from")
		return 2
	}
	if *node == "" || *destination == "" || *controllerURL == "" || *token == "" {
		fmt.Fprintln(stderr, "--node, --output and a controller/admin connection are required")
		return 2
	}
	options := exporter.Options{RunID: *runID, NodeID: *node, Output: *destination, ControllerURL: *controllerURL, AdminToken: *token, PrometheusURL: *prom, PrometheusToken: os.Getenv("MODELGAUGE_PROMETHEUS_TOKEN"), Step: *step, Queries: queries}
	for _, entry := range []struct {
		value  string
		target *time.Time
	}{{*start, &options.Start}, {*end, &options.End}} {
		if entry.value != "" {
			parsed, err := time.Parse(time.RFC3339Nano, entry.value)
			if err != nil {
				fmt.Fprintln(stderr, "history times must be RFC3339 with timezone")
				return 2
			}
			*entry.target = parsed
		}
	}
	if *includeRaw {
		options.RawSource = func(ctx context.Context, refs []protocol.EvidenceRef, destination string) error {
			return control.CopyLimaEvidence(ctx, *node, refs, destination)
		}
	} else if *rawFrom != "" {
		options.RawSource = exporter.LocalEvidence(*rawFrom)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	manifest, err := exporter.Write(ctx, options)
	if err != nil {
		fmt.Fprintf(stderr, "export failed (any created output remains marked INCOMPLETE): %v\n", err)
		return 1
	}
	for _, warning := range manifest.Warnings {
		fmt.Fprintf(stderr, "ℹ️ %s\n", warning)
	}
	fmt.Fprintf(out, "✅ Exported %s to %s (%d files). Verify SHA256SUMS before cleanup.\n", manifest.NodeID, *destination, len(manifest.Files)+2)
	return 0
}
