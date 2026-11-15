package control

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
	"runtime"
)

// This is the upstream revision used by the existing validated Linux fixture.
const LlamaRevision = "eb4e244be77a906a183cd4a00fd51d9ad3200955"

type NodeOptions struct {
	Name, Revision           string
	CPUs, MemoryGiB, DiskGiB int
	Log                      io.Writer
}

func BootstrapNode(ctx context.Context, c Config, o NodeOptions) (resultErr error) {
	if o.Log == nil {
		o.Log = io.Discard
	}
	if _, err := nodeBinary(runtime.GOARCH); err != nil {
		return err
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
	var err error
	if err = connectionAction(ctx, c, o.Name, "pause"); err != nil {
		return fmt.Errorf("pause controller executor (update the control deployment first): %w", err)
	}

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

	if err = shell(ctx, o.Name, `sudo loginctl enable-linger "$(id -un)"`, nil, o.Log); err != nil {
		return err
	}
	if err = manager.enrollControllerExecutor(ctx, c, o.Name); err != nil {
		return err
	}
	fmt.Fprintf(o.Log, "Benchmark node %s is ready. Its executor runs inside the controller.\n", o.Name)
	return nil
}
