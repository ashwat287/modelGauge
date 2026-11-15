package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

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
		if *name == "" {
			c, err := control.Load()
			if err == nil {
				api := &client.Client{BaseURL: c.ControllerURL, AdminToken: c.AdminToken}
				node, e := workspaceNode(ctx, api)
				if e == nil {
					*name = node.ID
				} else {
					err = e
				}
			}
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
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
		fmt.Fprintf(out, "✅ Refreshed controller connection for %s; rerun the interrupted operation.\n", *name)
		return 0
	}
	if args[0] == "bootstrap" {
		return bootstrapNode(ctx, args[1:], out, stderr)
	}
	fmt.Fprintln(stderr, "usage: modelgauge node <bootstrap|reconnect|inspect>")
	return 2
}

func bootstrapNode(ctx context.Context, args []string, out, stderr io.Writer) int {
	f := flag.NewFlagSet("node bootstrap", flag.ContinueOnError)
	setFlagOutput(f, stderr)
	timeout := f.Duration("timeout", 45*time.Minute, "overall node bootstrap deadline")
	provider := f.String("provider", "lima", "node provider")
	name := f.String("name", "benchmark-node", "benchmark VM name; defaults to the existing node")
	revision := f.String("llama-revision", control.LlamaRevision, "pinned llama.cpp commit")
	cpus := f.Int("cpus", 2, "CPUs")
	memory := f.Int("memory", 3, "memory GiB")
	disk := f.Int("disk", 30, "disk GiB")
	if err := f.Parse(args); err != nil {
		return flagExitCode(err)
	}
	explicitName := false
	f.Visit(func(v *flag.Flag) {
		if v.Name == "name" {
			explicitName = true
		}
	})
	if !explicitName {
		if c, err := control.Load(); err == nil {
			api := &client.Client{BaseURL: c.ControllerURL, AdminToken: c.AdminToken}
			var nodes []struct {
				ID string `json:"node_id"`
			}
			if _, err = api.DoJSON(ctx, "GET", "/v1/nodes", "admin", nil, &nodes); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			if len(nodes) > 1 {
				fmt.Fprintln(stderr, "controller has multiple historical nodes; clean up before setup")
				return 1
			}
			if len(nodes) == 1 {
				*name = nodes[0].ID
			}
		}
	}
	if *provider != "lima" {
		fmt.Fprintln(stderr, "only the lima provider is supported")
		return 2
	}
	if *cpus < 1 || *memory < 2 || *disk < 10 {
		fmt.Fprintln(stderr, "minimum node resources: 1 CPU, 2 GiB memory, 10 GiB disk")
		return 2
	}
	c, err := control.Load()
	if err != nil {
		fmt.Fprintf(stderr, "load control connection (run control bootstrap first): %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	if err := control.BootstrapNode(ctx, c, control.NodeOptions{Name: *name, Revision: *revision, CPUs: *cpus, MemoryGiB: *memory, DiskGiB: *disk, Log: stderr}); err != nil {
		fmt.Fprintf(stderr, "node bootstrap: %v\n", err)
		return 1
	}
	return 0
}
