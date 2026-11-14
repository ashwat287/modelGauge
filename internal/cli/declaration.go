package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"

	"github.com/nerdylucifer/modelgauge/internal/benchmark"
	"github.com/nerdylucifer/modelgauge/internal/client"
	"github.com/nerdylucifer/modelgauge/internal/controller"
)

func runDeclaration(ctx context.Context, command string, args []string, stdout, stderr io.Writer) int {
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	setFlagOutput(f, stderr)
	var file string
	f.StringVar(&file, "f", "", "benchmark YAML file")
	f.StringVar(&file, "file", "", "benchmark YAML file")
	dryRun := f.Bool("dry-run", false, "resolve and validate without queuing jobs (apply only)")
	endpoint := f.String("controller", os.Getenv("MODELGAUGE_CONTROLLER_URL"), "controller URL")
	token := f.String("admin-token", os.Getenv("MODELGAUGE_ADMIN_TOKEN"), "controller admin token")
	if err := f.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if file == "" || f.NArg() != 0 || (*dryRun && command != "apply") {
		fmt.Fprintln(stderr, "use modelgauge "+command+" -f benchmark.yaml (only apply accepts --dry-run)")
		return 2
	}
	handle, err := os.Open(file)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	document, err := benchmark.ParseDocument(handle)
	_ = handle.Close()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if *endpoint == "" || *token == "" {
		fmt.Fprintln(stderr, "controller URL and admin token are required; use the saved control configuration, environment variables, or connection flags")
		return 2
	}
	api := &client.Client{BaseURL: *endpoint, AdminToken: *token}
	method, path := "POST", "/v1/experiments"
	var input any = document
	switch command {
	case "validate":
		path += "?dry_run=true"
	case "apply":
		if *dryRun {
			path += "?dry_run=true"
		}
	case "status", "cancel":
		input = nil
		method, path = "GET", "/v1/experiments/"+url.PathEscape(document.Metadata.Name)
		if command == "cancel" {
			method, path = "POST", path+"/cancel"
		}
		path += "?spec_sha256=" + document.Digest()
	}
	var receipt controller.Experiment
	if _, err = api.DoJSON(ctx, method, path, "admin", input, &receipt); err != nil {
		fmt.Fprintln(stderr, err)
		if command == "apply" && !*dryRun {
			fmt.Fprintln(stderr, "Retry the same file: accepted experiments are returned without queuing duplicate jobs.")
		}
		return 1
	}
	return writeJSON(stdout, receipt)
}
