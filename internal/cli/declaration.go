package cli

import (
	"context"
	"flag"
	"fmt"
	"github.com/nerdylucifer/modelgauge/internal/benchmark"
	"github.com/nerdylucifer/modelgauge/internal/client"
	"github.com/nerdylucifer/modelgauge/internal/control"
	"github.com/nerdylucifer/modelgauge/internal/controller"
	"io"
	"net/url"
	"os"
	"strings"
	"time"
)

func runDeclaration(ctx context.Context, command string, args []string, out, stderr io.Writer) int {
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	setFlagOutput(f, stderr)
	file := ""
	if command != "history" && len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		file, args = args[0], args[1:]
	}
	preview := false
	if command == "run" {
		f.BoolVar(&preview, "dry-run", false, "preview without executing")
	}
	raw := f.Bool("json", false, "structured final result")
	endpoint := f.String("controller", os.Getenv("MODELGAUGE_CONTROLLER_URL"), "controller URL")
	token := f.String("admin-token", os.Getenv("MODELGAUGE_ADMIN_TOKEN"), "admin token")
	if err := f.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if f.NArg() != 0 {
		fmt.Fprintln(stderr, "expected a benchmark YAML file and flags")
		return 2
	}
	api := &client.Client{BaseURL: *endpoint, AdminToken: *token}
	if command == "history" {
		var entries []controller.Experiment
		if _, err := api.DoJSON(ctx, "GET", "/v1/experiments", "admin", nil, &entries); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *raw {
			return writeJSON(out, entries)
		}
		for _, e := range entries {
			if printExperiment(out, e, false) != 0 {
				return 1
			}
		}
		return 0
	}
	if file == "" {
		fmt.Fprintln(stderr, "a benchmark YAML file is required")
		return 2
	}
	handle, err := os.Open(file)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	doc, err := benchmark.ParseDocument(handle)
	handle.Close()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	c, err := control.Load()
	if err != nil || c.Model == "" {
		fmt.Fprintln(stderr, "select a model with model install MODEL or model use MODEL first")
		return 1
	}
	input := struct {
		benchmark.Document
		Model string `json:"model"`
	}{doc, c.Model}
	var result controller.Experiment
	if command == "validate" || preview {
		_, err = api.DoJSON(ctx, "POST", "/v1/experiments?dry_run=true", "admin", input, &result)
	} else {
		err = api.StreamJSON(ctx, "/v1/experiments", input, &result, stderr)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return printExperiment(out, result, *raw)
}
func printExperiment(out io.Writer, e controller.Experiment, raw bool) int {
	if raw {
		return writeJSON(out, e)
	}
	target := out
	var buffer strings.Builder
	out = &buffer
	label := e.Specification.Metadata.Name
	if label == "" {
		label = e.Name
	}
	fmt.Fprintf(out, "Experiment: %s\n", label)
	if len(e.Plans) > 0 {
		p := e.Plans[0]
		fmt.Fprintf(out, "Model:      %s\nState:      %s\n", p.Specification.Model.ID, p.Status)
	}
	if e.CreatedAt != nil {
		end := time.Now()
		if len(e.Plans) > 0 && e.Plans[0].CompletedAt != nil {
			end = *e.Plans[0].CompletedAt
		}
		fmt.Fprintf(out, "Elapsed:    %s\n", end.Sub(*e.CreatedAt).Truncate(time.Second))
	}
	if len(e.Plans) > 0 && e.Plans[0].Error != "" {
		fmt.Fprintln(out, "Reason:    ", e.Plans[0].Error)
	}
	if _, err := io.WriteString(target, buffer.String()); err != nil {
		return 1
	}
	return 0
}

func workspaceNode(ctx context.Context, api *client.Client) (controller.Node, error) {
	var nodes []controller.Node
	if _, err := api.DoJSON(ctx, "GET", "/v1/nodes", "admin", nil, &nodes); err != nil {
		return controller.Node{}, err
	}
	if len(nodes) != 1 {
		return controller.Node{}, fmt.Errorf("expected one benchmark node; run setup (found %d)", len(nodes))
	}
	return nodes[0], nil
}

func selectedResult(ctx context.Context, api *client.Client, node, name string) (controller.RunSummary, error) {
	var rows []controller.RunSummary
	if _, err := api.DoJSON(ctx, "GET", "/v1/nodes/"+url.PathEscape(node)+"/results", "admin", nil, &rows); err != nil {
		return controller.RunSummary{}, err
	}
	plan := ""
	if name != "" {
		var e controller.Experiment
		if _, err := api.DoJSON(ctx, "GET", "/v1/experiments/"+url.PathEscape(name), "admin", nil, &e); err != nil {
			return controller.RunSummary{}, err
		}
		if len(e.Plans) != 1 {
			return controller.RunSummary{}, fmt.Errorf("historical experiment has multiple plans; export its node history with --node")
		}
		plan = e.Plans[0].PlanID
	}
	for _, r := range rows {
		if (plan != "" && r.PlanID == plan) || (plan == "" && r.Status == "completed") {
			return r, nil
		}
	}
	return controller.RunSummary{}, fmt.Errorf("no completed result found; run a benchmark first")
}
