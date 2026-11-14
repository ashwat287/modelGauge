package control

import (
	"bytes"
	"context"
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/nerdylucifer/modelgauge/internal/agent"
	"github.com/nerdylucifer/modelgauge/internal/buildinfo"
	"github.com/nerdylucifer/modelgauge/internal/platform"
	"github.com/nerdylucifer/modelgauge/internal/releases"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

// This is the upstream revision used by the existing validated Linux fixture.
const LlamaRevision = "eb4e244be77a906a183cd4a00fd51d9ad3200955"

type NodeOptions struct {
	queuedJobsPaused                    bool
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
	unlock, err := platform.Lock("agent-" + o.Name)
	if err != nil {
		return fmt.Errorf("stop the external executor before upgrading: %w", err)
	}
	defer unlock()

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if _, err = os.Lstat(filepath.Join(home, ".local/share/modelgauge/executors", o.Name, ".pending.json")); err == nil {
		return fmt.Errorf("external executor has pending work; finish recovery before upgrading")
	} else if !os.IsNotExist(err) {
		return err
	}

	if err = connectionAction(ctx, c, o.Name, "pause"); err != nil {
		return fmt.Errorf("pause controller executor (update the control deployment first): %w", err)
	}
	defer func() {
		if resultErr != nil {
			restore, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			resultErr = errors.Join(resultErr, connectionAction(restore, c, o.Name, "resume"))
		}
	}()

	ctx, finishOwnership, err := provisionVM(ctx, o.Name, "benchmark", o.CPUs, o.MemoryGiB, o.DiskGiB, 0, o.Log, o)
	if err != nil {
		return err
	}
	defer completeProvision(ctx, finishOwnership, &resultErr)
	manager, _ := ctx.Value(ownershipContextKey{}).(*ownershipManager)
	if manager == nil {
		return fmt.Errorf("benchmark provisioning ownership context is missing")
	}
	if err = manager.checkNodeForward(ctx, c, o.Name); err != nil {
		return err
	}

	preflight := `set -euo pipefail
if command -v k3s >/dev/null; then echo 'Refusing a benchmark VM with k3s installed'; exit 1; fi
if pgrep -x 'llama-bench|llama-server' >/dev/null; then echo 'A benchmark is active; retry after it completes'; exit 1; fi

`
	if err = shell(ctx, o.Name, preflight, nil, o.Log); err != nil {
		return err
	}
	// Preserve prior service state and never interrupt a pending job to upgrade.
	previousAgent, err := capture(ctx, o.Name, `if systemctl --user is-active --quiet modelgauge-agent.service; then printf active; else printf inactive; fi`)
	if err != nil {
		return err
	}
	if err = shell(ctx, o.Name, `set -euo pipefail
if [ -f "$HOME/.local/share/modelgauge/results/.pending.json" ]; then echo 'Pending agent work must finish or recover before upgrade' >&2; exit 1; fi
state=$(systemctl --user show modelgauge-agent.service -p LoadState --value 2>/dev/null || true)
case "$state" in
 loaded) systemctl --user stop modelgauge-agent.service ;;
 not-found) ;;
 *) echo 'Cannot verify prior agent state' >&2; exit 1 ;;
esac
`, nil, o.Log); err != nil {
		return err
	}
	defer func() {
		if resultErr == nil || previousAgent != "active" {
			return
		}
		session, _ := ctx.Value(bootstrapKey{}).(*bootstrapSession)
		if session != nil && session.newVM {
			return
		}
		recovery, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		resultErr = errors.Join(resultErr, rawShell(recovery, o.Name, `systemctl --user start modelgauge-agent.service`, nil, io.Discard))
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
if [ ! -x "$destination/llama-bench" ] || [ ! -x "$destination/llama-server" ]; then
 source_dir="$destination/source"
 if [ ! -d "$source_dir/.git" ]; then
  git init "$source_dir"
  git -C "$source_dir" remote add origin https://github.com/ggml-org/llama.cpp.git
 fi
 git -C "$source_dir" fetch --depth=1 origin "$revision"
 git -C "$source_dir" checkout --detach FETCH_HEAD
 test "$(git -C "$source_dir" rev-parse HEAD)" = "$revision"
 cmake -S "$source_dir" -B "$destination/build" -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF -DGGML_NATIVE=OFF -DGGML_OPENMP=OFF -DGGML_BLAS=OFF -DGGML_CUDA=OFF -DGGML_VULKAN=OFF -DLLAMA_BUILD_TESTS=OFF -DLLAMA_BUILD_SERVER=ON -DLLAMA_BUILD_UI=OFF -DLLAMA_USE_PREBUILT_UI=OFF
 cmake --build "$destination/build" --target llama-bench llama-server -j ` + fmt.Sprint(o.CPUs) + `
 install -m 755 "$destination/build/bin/llama-bench" "$destination/llama-bench"
 install -m 755 "$destination/build/bin/llama-server" "$destination/llama-server"
fi
"$destination/llama-bench" --help >/dev/null
"$destination/llama-server" --help >/dev/null
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
	if err = shell(ctx, o.Name, `set -e; "$HOME/.local/share/modelgauge/guest-helper" _guest write-file .local/bin/modelgauge 755`, bytes.NewReader(binary), o.Log); err != nil {
		return err
	}

	if err = agent.SyncNode(ctx, agent.Config{ControllerURL: c.ControllerURL, Token: c.AgentToken, TargetVM: o.Name, NodeID: o.Name, DisplayName: o.Name, NodeKind: "virtual", ExecutionMode: protocol.ExecutionModeLima}); err != nil {
		return err
	}
	// The old node service is retired only after its replacement binary is installed.
	if err = shell(ctx, o.Name, `set -euo pipefail
state=$(systemctl --user show modelgauge-agent.service -p LoadState --value 2>/dev/null || true)
if [ "$state" = loaded ]; then "$HOME/.local/bin/modelgauge" agent uninstall; fi
rm -f "$HOME/.config/modelgauge/agent.env"
sudo loginctl enable-linger "$(id -un)"
`, nil, o.Log); err != nil {
		return err
	}
	if err = manager.enrollControllerExecutor(ctx, c, o.Name); err != nil {
		return err
	}
	fmt.Fprintf(o.Log, "Benchmark node %s is ready. Its executor runs inside the controller.\n", o.Name)
	return nil
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
	cmd := platform.CommandContext(ctx, "go", "build", "-trimpath", "-o", path, "./cmd/modelgauge")
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
