//go:build !bundled && !remotehost

package control

import (
	"fmt"
	"os"
	"path/filepath"
)

// Controller images provide verified runtime assets in a read-only image layer.
// Standalone guest executables need no embedded payloads.
func nodeBinary(arch string) ([]byte, error) { return imageRuntime("linux-" + arch) }
func macBinary(arch string) ([]byte, error)  { return imageRuntime("darwin-" + arch) }
func imageRuntime(name string) ([]byte, error) {
	switch name {
	case "linux-arm64", "linux-amd64", "darwin-arm64", "darwin-amd64":
	default:
		return nil, fmt.Errorf("unsupported runtime %q", name)
	}
	data, err := os.ReadFile(filepath.Join("/opt/modelgauge/runtimes", name))
	if err != nil {
		return nil, fmt.Errorf("runtimes are not bundled; build the CLI with make build or make install: %w", err)
	}
	return data, nil
}
