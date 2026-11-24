package cli

import (
	"context"
	"flag"
	"fmt"
	"github.com/nerdylucifer/modelgauge/internal/client"
	"io"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"time"

	"github.com/nerdylucifer/modelgauge/internal/control"
)

func runControl(ctx context.Context, args []string, out, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: modelgauge control bootstrap")
		return 2
	}
	if args[0] != "bootstrap" {
		fmt.Fprintln(stderr, "usage: modelgauge control bootstrap")
		return 2
	}
	f := flag.NewFlagSet("control bootstrap", flag.ContinueOnError)
	setFlagOutput(f, stderr)
	timeout := f.Duration("timeout", 60*time.Minute, "overall bootstrap deadline")
	readiness := f.Duration("readiness-timeout", 10*time.Minute, "per-deployment readiness deadline")
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
	version := f.String("k3s-version", "v1.37.1+k3s1", "k3s version for new installs")
	cpus := f.Int("cpus", 4, "VM CPUs")
	memory := f.Int("memory", 6, "VM memory GiB")
	port := f.Int("port", defaultPort, "local controller port")
	disk := f.Int("disk", 50, "VM disk GiB")
	benchmark := f.String("benchmark-target", "", "optional benchmark node exporter host:port")
	if err := f.Parse(args[1:]); err != nil {
		return flagExitCode(err)
	}
	if *timeout <= 0 || *readiness < time.Second {
		fmt.Fprintln(stderr, "timeouts must be positive (readiness at least one second)")
		return 2
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	if *provider != "lima" {
		fmt.Fprintln(stderr, "only the lima provider is supported")
		return 2
	}
	if *cpus < 2 || *memory < 3 || *disk < 16 || *port < 1024 || *port > 65535 {
		fmt.Fprintln(stderr, "minimum control VM: 2 CPUs, 3 GiB memory, 16 GiB disk")
		return 2
	}
	c, err := control.Bootstrap(ctx, control.Options{ReadinessTimeout: *readiness, Name: *name, K3sVersion: *version, CPUs: *cpus, MemoryGiB: *memory, DiskGiB: *disk, Port: *port, BenchmarkTarget: *benchmark, Log: stderr})
	if err != nil {
		fmt.Fprintf(stderr, "bootstrap: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "✅ Control plane ready: %s\nConnection details saved privately.\n", c.ControllerURL)
	return 0
}

const liveDashboardURL = "http://127.0.0.1:3000/d/modelgauge-live"

func prepareDashboard(ctx context.Context, c control.Config, out io.Writer, browser bool, since time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err := control.Forward(ctx, c.VM, "monitoring", "grafana", 3000, 3000, "/api/health"); err != nil {
		return err
	}
	address := fmt.Sprintf("%s?from=%d&to=now&refresh=5s", liveDashboardURL, since.UnixMilli())
	fmt.Fprintln(out, "Dashboard: "+address)
	if browser {
		openDashboard(ctx, address, out)
	}
	return nil
}

func openDashboard(ctx context.Context, address string, out io.Writer) {
	command := "xdg-open"
	if runtime.GOOS == "darwin" {
		command = "open"
	}
	if path, err := exec.LookPath(command); err == nil {
		if err := exec.CommandContext(ctx, path, address).Run(); err == nil {
			return
		}
	}
	fmt.Fprintln(out, "Could not open browser; use the dashboard URL above.")
}

func interactiveOutput(out io.Writer) bool {
	file, ok := out.(*os.File)
	if !ok {
		return false
	}
	stat, err := file.Stat()
	if err != nil || stat.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	input, err := os.Stdin.Stat()
	return err == nil && input.Mode()&os.ModeCharDevice != 0 && os.Getenv("CI") == ""
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

func runSetup(ctx context.Context, args []string, out, stderr io.Writer) int {
	f := flag.NewFlagSet("setup", flag.ContinueOnError)
	setFlagOutput(f, stderr)
	controlMemory := f.Int("control-memory", 6, "control VM RAM in GiB")
	nodeMemory := f.Int("node-memory", 3, "benchmark VM RAM in GiB")
	nodeCPUs := f.Int("node-cpus", 2, "benchmark VM CPUs")
	nodeDisk := f.Int("node-disk", 30, "benchmark VM disk GiB")
	remoteHost := f.String("remote-host", "", "Mac SSH destination USER@HOST[:PORT]; create its benchmark VM remotely")
	identity := f.String("identity", "", "bootstrap SSH private key file; otherwise prompt for password")
	hostKey := f.String("host-key", "", "verified remote Mac SHA256 host-key fingerprint; otherwise prompt")
	if err := f.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if f.NArg() != 0 {
		fmt.Fprintln(stderr, "setup accepts flags only")
		return 2
	}
	if *nodeCPUs < 1 || *nodeMemory < 2 || *nodeDisk < 10 || (*remoteHost != "" && *nodeCPUs > 4) {
		fmt.Fprintln(stderr, "invalid benchmark VM resources")
		return 2
	}
	if *remoteHost != "" {
		if _, _, err := parseRemoteHost(*remoteHost); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
	}
	if *remoteHost == "" && (*identity != "" || *hostKey != "") {
		fmt.Fprintln(stderr, "--identity and --host-key require --remote-host")
		return 2
	}
	controlArgs := []string{"bootstrap", "--memory", strconv.Itoa(*controlMemory)}
	nodeArgs := []string{"--memory", strconv.Itoa(*nodeMemory), "--cpus", strconv.Itoa(*nodeCPUs), "--disk", strconv.Itoa(*nodeDisk)}
	if code := runControl(ctx, controlArgs, out, stderr); code != 0 {
		return code
	}
	loadControlEnvironment()
	c, err := control.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	// Use the freshly bootstrapped controller, not an inherited stale environment.
	if *remoteHost != "" || control.RemoteTargetTracked() {
		return setupRemote(ctx, c, *remoteHost, *identity, *hostKey, *nodeCPUs, *nodeMemory, *nodeDisk, out, stderr)
	}
	api := &client.Client{BaseURL: c.ControllerURL, AdminToken: c.AdminToken}
	var state struct {
		Configured bool `json:"configured"`
	}
	if _, err = api.DoJSON(ctx, "GET", "/v1/remote", "admin", nil, &state); err == nil && state.Configured {
		return setupRemote(ctx, c, "", "", "", *nodeCPUs, *nodeMemory, *nodeDisk, out, stderr)
	}
	return bootstrapNode(ctx, nodeArgs, out, stderr)
}
