package control

import (
	"bytes"
	"context"
	"debug/elf"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/nerdylucifer/modelgauge/internal/buildinfo"
	"github.com/nerdylucifer/modelgauge/internal/client"
	"github.com/nerdylucifer/modelgauge/internal/releases"
)

// This is the upstream revision used by the existing validated Linux fixture.
const LlamaRevision = "eb4e244be77a906a183cd4a00fd51d9ad3200955"

type NodeOptions struct {
	Name, Source, AgentBinary, Revision string
	CPUs, MemoryGiB, DiskGiB            int
	Log                                 io.Writer
}

func BootstrapNode(ctx context.Context, c Config, o NodeOptions) (resultErr error) {
	if o.Log == nil {
		o.Log = io.Discard
	}
	if o.Source == "" && o.AgentBinary == "" && runtime.GOOS != "linux" && !buildinfo.Released() {
		return fmt.Errorf("provide --source REPO or a prebuilt Linux --agent-binary")
	}
	if o.Source != "" {
		if _, err := os.Stat(filepath.Join(o.Source, "go.mod")); err != nil {
			return fmt.Errorf("agent source repository: %w", err)
		}
	}
	if o.Name == c.VM {
		return fmt.Errorf("the benchmark VM must be separate from the control VM")
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`).MatchString(o.Name) {
		return fmt.Errorf("invalid node name")
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(o.Revision) {
		return fmt.Errorf("llama revision must be a full 40-character commit hash")
	}
	if c.AgentToken == "" {
		return fmt.Errorf("saved connection has no agent credential; rerun control bootstrap")
	}
	endpoint, err := url.Parse(c.ControllerURL)
	if err != nil {
		return err
	}
	guestURL := c.ControllerURL
	if endpoint.Hostname() == "127.0.0.1" || endpoint.Hostname() == "localhost" {
		var port int
		if _, err = fmt.Sscan(endpoint.Port(), &port); err != nil {
			return fmt.Errorf("saved controller URL needs an explicit port")
		}
		if err = Forward(ctx, c.VM, "benchmark-system", "modelgauge-controller", port, 8080, "/healthz"); err != nil {
			return err
		}
		endpoint.Host = "host.lima.internal:" + endpoint.Port()
		guestURL = endpoint.String()
	}
	ctx, finishOwnership, err := provisionVM(ctx, o.Name, "benchmark", o.CPUs, o.MemoryGiB, o.DiskGiB, 0, o.Log, o)
	if err != nil {
		return err
	}
	defer func() {
		if err := finishOwnership(); err != nil && resultErr == nil {
			resultErr = err
		}
	}()
	preflight := `set -euo pipefail
if command -v k3s >/dev/null; then echo 'Refusing a benchmark VM with k3s installed'; exit 1; fi
if pgrep -x llama-bench >/dev/null; then echo 'A benchmark is active; retry after it completes'; exit 1; fi
curl -fsS --connect-timeout 5 --max-time 15 ` + quote(guestURL+"/healthz") + ` >/dev/null
`
	if err = shell(ctx, o.Name, preflight, nil, o.Log); err != nil {
		return err
	}
	// Stop the idle agent while building so it cannot lease a job mid-upgrade.
	if err = shell(ctx, o.Name, `systemctl --user stop modelgauge-agent.service 2>/dev/null || true`, nil, o.Log); err != nil {
		return err
	}
	// Restore the prior agent after an unsuccessful upgrade when one existed.
	defer func() {
		recovery, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = shell(recovery, o.Name, `if [ -f "$HOME/.config/systemd/user/modelgauge-agent.service" ]; then systemctl --user start modelgauge-agent; fi`, nil, io.Discard)
	}()
	if err = shell(ctx, o.Name, `set -euo pipefail
sudo env DEBIAN_FRONTEND=noninteractive apt-get update
sudo env DEBIAN_FRONTEND=noninteractive apt-get install -y git cmake build-essential pkg-config time ca-certificates curl
`, nil, o.Log); err != nil {
		return err
	}
	build := `set -euo pipefail
revision=` + quote(o.Revision) + `
base="$HOME/.local/share/modelgauge/runtime"
destination="$base/$revision"
mkdir -p "$destination"
if [ ! -x "$destination/llama-bench" ]; then
 source_dir="$destination/source"
 if [ ! -d "$source_dir/.git" ]; then
  git init "$source_dir"
  git -C "$source_dir" remote add origin https://github.com/ggml-org/llama.cpp.git
 fi
 git -C "$source_dir" fetch --depth=1 origin "$revision"
 git -C "$source_dir" checkout --detach FETCH_HEAD
 test "$(git -C "$source_dir" rev-parse HEAD)" = "$revision"
 cmake -S "$source_dir" -B "$destination/build" -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF -DGGML_NATIVE=OFF -DGGML_OPENMP=OFF -DGGML_BLAS=OFF -DGGML_CUDA=OFF -DGGML_VULKAN=OFF -DLLAMA_BUILD_TESTS=OFF
 cmake --build "$destination/build" --target llama-bench -j ` + fmt.Sprint(o.CPUs) + `
 install -m 755 "$destination/build/bin/llama-bench" "$destination/llama-bench"
fi
"$destination/llama-bench" --help >/dev/null
printf '%s\n' "$revision" > "$destination/revision"
ln -sfn "$destination" "$base/current"
mkdir -p "$HOME/.local/bin" "$HOME/.local/share/modelgauge/models"
`
	if err = shell(ctx, o.Name, build, nil, o.Log); err != nil {
		return err
	}
	arch, err := capture(ctx, o.Name, "uname -m")
	if err != nil {
		return err
	}
	switch arch {
	case "aarch64":
		arch = "arm64"
	case "x86_64":
		arch = "amd64"
	default:
		return fmt.Errorf("unsupported node architecture %s", arch)
	}
	binary, err := managedBinary(ctx, o.Name, arch)
	if err != nil {
		return err
	}
	if err = shell(ctx, o.Name, `set -e; cat > "$HOME/.local/bin/modelgauge.new"; chmod 755 "$HOME/.local/bin/modelgauge.new"; mv "$HOME/.local/bin/modelgauge.new" "$HOME/.local/bin/modelgauge"`, bytes.NewReader(binary), o.Log); err != nil {
		return err
	}
	enrolledAfter := time.Now().UTC().Add(-5 * time.Second)
	install := `set -euo pipefail
IFS= read -r MODELGAUGE_AGENT_TOKEN
export MODELGAUGE_AGENT_TOKEN
"$HOME/.local/bin/modelgauge" agent install --controller ` + quote(guestURL) + ` --node ` + quote(o.Name) + ` --name ` + quote(o.Name) + ` --kind virtual --execution-mode lima-linux --results-dir "$HOME/.local/share/modelgauge/results"
sudo loginctl enable-linger "$(id -un)"
systemctl --user restart modelgauge-agent
`
	if err = shell(ctx, o.Name, install, strings.NewReader(c.AgentToken+"\n"), o.Log); err != nil {
		return err
	}
	api := &client.Client{BaseURL: c.ControllerURL, AdminToken: c.AdminToken}
	for attempt := 0; attempt < 30; attempt++ {
		var status struct {
			Status     string     `json:"status"`
			LastSeenAt *time.Time `json:"last_seen_at"`
		}
		if _, err := api.DoJSON(ctx, "GET", "/v1/nodes/"+o.Name, "admin", nil, &status); err == nil && status.Status == "online" && status.LastSeenAt != nil && status.LastSeenAt.After(enrolledAfter) {
			fmt.Fprintf(o.Log, "Benchmark node %s is online (Linux, llama.cpp %s).\n", o.Name, o.Revision)
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("agent did not register in time; inspect modelgauge node status %s and the node's systemd service", o.Name)
}

func nodeBinary(ctx context.Context, o NodeOptions, arch string) ([]byte, error) {
	if o.AgentBinary != "" {
		data, err := os.ReadFile(o.AgentBinary)
		if err != nil {
			return nil, err
		}
		binary, err := elf.NewFile(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("agent must be a Linux ELF executable: %w", err)
		}
		defer binary.Close()
		expected := elf.EM_AARCH64
		if arch == "amd64" {
			expected = elf.EM_X86_64
		}
		if binary.Machine != expected {
			return nil, fmt.Errorf("agent binary architecture does not match Linux %s", arch)
		}
		return data, nil
	}
	if o.Source == "" && buildinfo.Released() {
		data, err := releases.DownloadAgent(ctx, arch)
		if err != nil {
			return nil, err
		}
		binary, err := elf.NewFile(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("downloaded agent is not a Linux executable: %w", err)
		}
		defer binary.Close()
		expected := elf.EM_AARCH64
		if arch == "amd64" {
			expected = elf.EM_X86_64
		}
		if binary.Machine != expected {
			return nil, fmt.Errorf("downloaded agent architecture mismatch")
		}
		return data, nil
	}
	if o.Source == "" && runtime.GOOS == "linux" && runtime.GOARCH == arch {
		path, err := os.Executable()
		if err != nil {
			return nil, err
		}
		return os.ReadFile(path)
	}
	if o.Source == "" {
		return nil, fmt.Errorf("provide --source REPO to compile the Linux agent, or --agent-binary FILE for a matching Linux binary")
	}
	directory, err := os.MkdirTemp("", "modelgauge-agent-build-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(directory)
	path := filepath.Join(directory, "modelgauge")
	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", path, "./cmd/modelgauge")
	cmd.Dir = o.Source
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "GOOS=") && !strings.HasPrefix(value, "GOARCH=") && !strings.HasPrefix(value, "CGO_ENABLED=") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0")
	cmd.Stdout = o.Log
	cmd.Stderr = o.Log
	if err = cmd.Run(); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}
