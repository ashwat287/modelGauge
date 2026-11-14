package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"time"

	"github.com/nerdylucifer/modelgauge/internal/buildinfo"
	"github.com/nerdylucifer/modelgauge/internal/client"
	"github.com/nerdylucifer/modelgauge/internal/control"
)

func runNodeManagement(ctx context.Context, args []string, out, stderr io.Writer) int {
	if args[0] == "reconnect" {
		f := flag.NewFlagSet("node reconnect", flag.ContinueOnError)
		setFlagOutput(f, stderr)
		name := f.String("name", "", "managed benchmark VM name")
		if err := f.Parse(args[1:]); err != nil {
			return flagExitCode(err)
		}
		if *name == "" || f.NArg() != 0 {
			fmt.Fprintln(stderr, "usage: modelgauge node reconnect --name NODE_ID")
			return 2
		}
		c, err := control.Load()
		if err == nil {
			ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			err = control.ReconnectNode(ctx, c, *name)
		}
		if err != nil {
			fmt.Fprintln(stderr, "node reconnect:", err)
			return 1
		}
		fmt.Fprintf(out, "✅ Refreshed controller connection for %s; queued work can resume.\n", *name)
		return 0
	}
	if args[0] == "bootstrap" {
		return bootstrapNode(ctx, args[1:], out, stderr)
	}
	api := &client.Client{BaseURL: os.Getenv("MODELGAUGE_CONTROLLER_URL"), AdminToken: os.Getenv("MODELGAUGE_ADMIN_TOKEN")}
	if api.BaseURL == "" || api.AdminToken == "" {
		fmt.Fprintln(stderr, "run modelgauge control bootstrap first or configure the controller connection")
		return 2
	}
	path := "/v1/nodes"
	if args[0] == "status" {
		if len(args) != 2 {
			fmt.Fprintln(stderr, "usage: modelgauge node status NODE_ID")
			return 2
		}
		path += "/" + url.PathEscape(args[1])
	} else if args[0] != "list" || len(args) != 1 {
		fmt.Fprintln(stderr, "usage: modelgauge node <bootstrap|reconnect|list|status|inspect>")
		return 2
	}
	var response any
	if _, err := api.DoJSON(ctx, "GET", path, "admin", nil, &response); err != nil {
		fmt.Fprintf(stderr, "node status: %v\n", err)
		return 1
	}
	return writeJSON(out, response)
}

func bootstrapNode(ctx context.Context, args []string, out, stderr io.Writer) int {
	f := flag.NewFlagSet("node bootstrap", flag.ContinueOnError)
	setFlagOutput(f, stderr)
	timeout := f.Duration("timeout", 45*time.Minute, "overall node bootstrap deadline")
	provider := f.String("provider", "lima", "node provider")
	name := f.String("name", "benchmark-node", "Lima VM and node ID")
	source := f.String("source", "", "Go repository to build the Linux agent")
	binary := f.String("agent-binary", "", "prebuilt Linux agent for the VM architecture")
	revision := f.String("llama-revision", control.LlamaRevision, "pinned llama.cpp commit")
	cpus := f.Int("cpus", 2, "CPUs")
	memory := f.Int("memory", 3, "memory GiB")
	disk := f.Int("disk", 30, "disk GiB")
	if err := f.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if *provider != "lima" {
		fmt.Fprintln(stderr, "only the lima provider is supported")
		return 2
	}
	if *cpus < 1 || *memory < 2 || *disk < 10 {
		fmt.Fprintln(stderr, "minimum node resources: 1 CPU, 2 GiB memory, 10 GiB disk")
		return 2
	}
	if *source == "" && *binary == "" && !buildinfo.Released() {
		if _, err := os.Stat("go.mod"); err == nil {
			*source = "."
		}
	}
	c, err := control.Load()
	if err != nil {
		fmt.Fprintf(stderr, "load control connection (run control bootstrap first): %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	if err := control.BootstrapNode(ctx, c, control.NodeOptions{Name: *name, Source: *source, AgentBinary: *binary, Revision: *revision, CPUs: *cpus, MemoryGiB: *memory, DiskGiB: *disk, Log: stderr}); err != nil {
		fmt.Fprintf(stderr, "node bootstrap: %v\n", err)
		return 1
	}
	return 0
}
