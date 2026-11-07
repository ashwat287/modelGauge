package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"time"

	"github.com/nerdylucifer/modelgauge/internal/buildinfo"
	"github.com/nerdylucifer/modelgauge/internal/control"
	"github.com/nerdylucifer/modelgauge/internal/releases"
)

func runControl(args []string, out, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: modelgauge control <bootstrap|status>")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if args[0] == "status" {
		c, err := control.Load()
		if err != nil {
			fmt.Fprintf(stderr, "load control configuration: %v\n", err)
			return 1
		}
		if err = control.Status(ctx, c, out); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	if args[0] != "bootstrap" {
		fmt.Fprintln(stderr, "usage: modelgauge control <bootstrap|status>")
		return 2
	}
	f := flag.NewFlagSet("control bootstrap", flag.ContinueOnError)
	setFlagOutput(f, stderr)
	provider := f.String("provider", "lima", "control provider")
	defaultName, defaultPort := "modelgauge-control", 18080
	if saved, err := control.Load(); err == nil {
		defaultName = saved.VM
		if endpoint, err := url.Parse(saved.ControllerURL); err == nil {
			if value, err := strconv.Atoi(endpoint.Port()); err == nil {
				defaultPort = value
			}
		}
	}
	name := f.String("name", defaultName, "Lima VM name (default: active control VM, or modelgauge-control)")
	image := f.String("image", "", "published controller image")
	source := f.String("source", "", "build a controller image from this Go repository")
	version := f.String("k3s-version", "v1.37.1+k3s1", "k3s version for new installs")
	cpus := f.Int("cpus", 4, "VM CPUs")
	memory := f.Int("memory", 6, "VM memory GiB")
	port := f.Int("port", defaultPort, "local controller port (use a different port for a second control VM)")
	disk := f.Int("disk", 50, "VM disk GiB")
	benchmark := f.String("benchmark-target", "", "optional benchmark node exporter host:port")
	if err := f.Parse(args[1:]); err != nil {
		return flagExitCode(err)
	}
	if *provider != "lima" {
		fmt.Fprintln(stderr, "only the lima provider is supported")
		return 2
	}
	if *cpus < 2 || *memory < 3 || *disk < 16 || *port < 1024 || *port > 65535 {
		fmt.Fprintln(stderr, "minimum control VM: 2 CPUs, 3 GiB memory, 16 GiB disk")
		return 2
	}
	if *source == "" && *image == "" {
		if buildinfo.Released() {
			manifest, err := releases.Load(ctx)
			if err != nil {
				fmt.Fprintf(stderr, "resolve matching controller release: %v\n", err)
				return 1
			}
			*image = manifest.ControllerImage
		} else if _, err := os.Stat("go.mod"); err == nil {
			*source = "."
		} else {
			fmt.Fprintln(stderr, "provide --image IMAGE for a published controller, or --source REPO to build locally")
			return 2
		}
	}
	c, err := control.Bootstrap(ctx, control.Options{Name: *name, Image: *image, Source: *source, K3sVersion: *version, CPUs: *cpus, MemoryGiB: *memory, DiskGiB: *disk, Port: *port, BenchmarkTarget: *benchmark, Log: out})
	if err != nil {
		fmt.Fprintf(stderr, "bootstrap: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "✅ Control plane ready: %s\nConnection details saved privately. Run modelgauge control status.\n", c.ControllerURL)
	return 0
}

func runMonitoring(args []string, out, stderr io.Writer) int {
	if len(args) != 1 || args[0] != "open" {
		fmt.Fprintln(stderr, "usage: modelgauge monitoring open")
		return 2
	}
	c, err := control.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err = control.Forward(ctx, c.VM, "monitoring", "grafana", 3000, 3000, "/api/health"); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	address := "http://127.0.0.1:3000/d/modelgauge-overview"
	fmt.Fprintln(out, address)
	command := "xdg-open"
	if runtime.GOOS == "darwin" {
		command = "open"
	}
	if path, err := exec.LookPath(command); err == nil {
		_ = exec.CommandContext(ctx, path, address).Run()
	}
	return 0
}

// Explicit environment settings continue to override the saved connection.
func loadControlEnvironment() {
	c, err := control.Load()
	if err != nil {
		return
	}
	for key, value := range map[string]string{"MODELGAUGE_CONTROLLER_URL": c.ControllerURL, "MODELGAUGE_ADMIN_TOKEN": c.AdminToken} {
		if os.Getenv(key) == "" {
			_ = os.Setenv(key, value)
		}
	}
}
