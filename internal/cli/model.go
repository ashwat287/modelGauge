package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/nerdylucifer/modelgauge/internal/client"
	"github.com/nerdylucifer/modelgauge/internal/modelcache"
)

func runModel(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: modelgauge model <list|install>")
		return 2
	}
	if args[0] == "install" {
		return installModel(args[1:], stdout, stderr)
	}
	if args[0] == "search" {
		query := ""
		if len(args) > 1 {
			query = args[1]
		}
		entries, err := modelcache.Search(query)
		if err != nil {
			fmt.Fprintf(stderr, "search models: %v\n", err)
			return 1
		}
		return writeJSON(stdout, entries)
	}
	if args[0] != "list" {
		fmt.Fprintln(stderr, "usage: modelgauge model <list|install>")
		return 2
	}
	f := flag.NewFlagSet("model list", flag.ContinueOnError)
	setFlagOutput(f, stderr)
	node := f.String("node", "", "node ID")
	controllerURL := f.String("controller", os.Getenv("MODELGAUGE_CONTROLLER_URL"), "controller URL")
	token := f.String("admin-token", os.Getenv("MODELGAUGE_ADMIN_TOKEN"), "admin token")
	if err := f.Parse(args[1:]); err != nil {
		return flagExitCode(err)
	}
	if *node == "" || *controllerURL == "" || *token == "" {
		fmt.Fprintln(stderr, "--node, --controller, and --admin-token are required")
		return 2
	}
	var models []catalogModel
	api := &client.Client{BaseURL: *controllerURL, AdminToken: *token}
	if _, err := api.DoJSON(context.Background(), "GET", "/v1/nodes/"+*node+"/models", "admin", nil, &models); err != nil {
		fmt.Fprintf(stderr, "list models: %v\n", err)
		return 1
	}
	for i := range models {
		models[i].Path = ""
	}
	return writeJSON(stdout, models)
}

func installModel(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "cancel" {
		return cancelJob(args[1:], stdout, stderr, true)
	}
	if len(args) > 0 && args[0] == "status" {
		return installStatus(args[1:], stdout, stderr)
	}
	positionalID := ""
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		positionalID = args[0]
		args = args[1:]
	}
	f := flag.NewFlagSet("model install", flag.ContinueOnError)
	setFlagOutput(f, stderr)
	id := f.String("id", positionalID, "stable model ID")
	sourceURL := f.String("url", "", "GGUF download URL")
	checksum := f.String("sha256", "", "expected SHA-256 (recommended)")
	node := f.String("node", "", "target node ID (queues remote installation)")
	controllerURL := f.String("controller", os.Getenv("MODELGAUGE_CONTROLLER_URL"), "controller URL")
	token := f.String("admin-token", os.Getenv("MODELGAUGE_ADMIN_TOKEN"), "admin token")
	if err := f.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if *id == "" {
		fmt.Fprintln(stderr, "a model ID is required")
		return 2
	}
	if err := modelcache.ValidateID(*id); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if *sourceURL == "" {
		entry, err := modelcache.Resolve(*id)
		if err != nil {
			fmt.Fprintf(stderr, "resolve model: %v\n", err)
			return 1
		}
		*sourceURL = entry.URL
		*checksum = entry.SHA256
	}
	if *node != "" {
		if *controllerURL == "" || *token == "" {
			fmt.Fprintln(stderr, "--controller and --admin-token are required with --node")
			return 2
		}
		api := &client.Client{BaseURL: *controllerURL, AdminToken: *token}
		var response map[string]string
		if _, err := api.DoJSON(context.Background(), "POST", "/v1/nodes/"+*node+"/model-installs", "admin", map[string]string{"model_id": *id, "source_url": *sourceURL, "sha256": *checksum}, &response); err != nil {
			fmt.Fprintf(stderr, "queue model install: %v\n", err)
			return 1
		}
		return writeJSON(stdout, response)
	}
	model, err := modelcache.Install(*id, *sourceURL, *checksum)
	if err != nil {
		fmt.Fprintf(stderr, "install model: %v\n", err)
		return 1
	}
	return writeJSON(stdout, map[string]any{"id": model.ID, "sha256": model.SHA256, "size_bytes": model.SizeBytes, "status": "installed"})
}

func installStatus(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: modelgauge model install status JOB_ID")
		return 2
	}
	id := args[0]
	f := flag.NewFlagSet("model install status", flag.ContinueOnError)
	setFlagOutput(f, stderr)
	controllerURL := f.String("controller", os.Getenv("MODELGAUGE_CONTROLLER_URL"), "controller URL")
	token := f.String("admin-token", os.Getenv("MODELGAUGE_ADMIN_TOKEN"), "admin token")
	if err := f.Parse(args[1:]); err != nil {
		return flagExitCode(err)
	}
	if *controllerURL == "" || *token == "" {
		fmt.Fprintln(stderr, "controller URL and admin token are required")
		return 2
	}
	api := &client.Client{BaseURL: *controllerURL, AdminToken: *token}
	var result any
	if _, err := api.DoJSON(context.Background(), "GET", "/v1/model-installs/"+id, "admin", nil, &result); err != nil {
		fmt.Fprintf(stderr, "installation status: %v\n", err)
		return 1
	}
	return writeJSON(stdout, result)
}
