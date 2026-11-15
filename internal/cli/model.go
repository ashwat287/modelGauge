package cli

import (
	"context"
	"flag"
	"fmt"
	"github.com/nerdylucifer/modelgauge/internal/client"
	"github.com/nerdylucifer/modelgauge/internal/control"
	"github.com/nerdylucifer/modelgauge/internal/modelcache"
	"io"
	"net/url"
	"os"
)

type catalogModel struct {
	ID        string `json:"id"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
	Path      string `json:"path"`
}

func runModel(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: modelgauge model <list|install>")
		return 2
	}
	if args[0] == "install" {
		return installModel(ctx, args[1:], stdout, stderr)
	}
	if args[0] == "use" {
		if len(args) != 2 {
			fmt.Fprintln(stderr, "usage: modelgauge model use MODEL")
			return 2
		}
		c, err := control.Load()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		api := &client.Client{BaseURL: c.ControllerURL, AdminToken: c.AdminToken}
		node, err := workspaceNode(ctx, api)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		var models []catalogModel
		if _, err = api.DoJSON(ctx, "GET", "/v1/nodes/"+node.ID+"/models", "admin", nil, &models); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		for _, m := range models {
			if m.ID == args[1] {
				c.Model = m.ID
				if err = control.Save(c); err != nil {
					fmt.Fprintln(stderr, err)
					return 1
				}
				fmt.Fprintln(stdout, "Selected model:", m.ID)
				return 0
			}
		}
		fmt.Fprintln(stderr, "model is not installed; run model install MODEL")
		return 1
	}
	if args[0] == "search" {
		query := ""
		if len(args) > 1 {
			query = args[1]
		}
		entries, err := modelcache.SearchContext(ctx, query)
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
	if *controllerURL == "" || *token == "" {
		fmt.Fprintln(stderr, "--node, --controller, and --admin-token are required")
		return 2
	}
	var models []catalogModel
	api := &client.Client{BaseURL: *controllerURL, AdminToken: *token}
	target, err := workspaceNode(ctx, api)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *node != "" && *node != target.ID {
		fmt.Fprintln(stderr, "node differs from the workspace node")
		return 2
	}
	*node = target.ID
	if _, err := api.DoJSON(ctx, "GET", "/v1/nodes/"+*node+"/models", "admin", nil, &models); err != nil {
		fmt.Fprintf(stderr, "list models: %v\n", err)
		return 1
	}
	for i := range models {
		models[i].Path = ""
	}
	return writeJSON(stdout, models)
}

func installModel(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	positional := ""
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		positional, args = args[0], args[1:]
	}
	f := flag.NewFlagSet("model install", flag.ContinueOnError)
	setFlagOutput(f, stderr)
	raw := f.Bool("json", false, "structured final result")
	id := f.String("id", positional, "model ID")
	source := f.String("url", "", "GGUF URL")
	sha := f.String("sha256", "", "expected SHA-256")
	endpoint := f.String("controller", os.Getenv("MODELGAUGE_CONTROLLER_URL"), "controller URL")
	token := f.String("admin-token", os.Getenv("MODELGAUGE_ADMIN_TOKEN"), "admin token")
	if err := f.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if f.NArg() != 0 || *id == "" {
		fmt.Fprintln(stderr, "a model ID is required")
		return 2
	}
	if err := modelcache.ValidateID(*id); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	c, err := control.Load()
	if err != nil {
		fmt.Fprintln(stderr, "run setup first:", err)
		return 1
	}
	api := &client.Client{BaseURL: *endpoint, AdminToken: *token}
	target, err := workspaceNode(ctx, api)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *source == "" {
		entry, e := modelcache.ResolveContext(ctx, *id)
		if e != nil {
			fmt.Fprintln(stderr, e)
			return 1
		}
		*source, *sha = entry.URL, entry.SHA256
	}
	input := map[string]string{"model_id": *id, "source_url": *source, "sha256": *sha}
	var result map[string]string
	if err = api.StreamJSON(ctx, "/v1/nodes/"+url.PathEscape(target.ID)+"/model-installs", input, &result, stderr); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	c.Model = *id
	if err = control.Save(c); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *raw {
		return writeJSON(stdout, result)
	}
	fmt.Fprintln(stdout, "Installed model:", *id)
	return 0
}
