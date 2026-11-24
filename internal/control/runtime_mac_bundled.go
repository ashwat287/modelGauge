//go:build bundled

package control

import (
	"embed"
	"fmt"
)

//go:embed runtimes/darwin-arm64 runtimes/darwin-amd64
var macRuntimes embed.FS

func readMacRuntime(arch string) ([]byte, error) {
	if arch != "amd64" && arch != "arm64" {
		return nil, fmt.Errorf("unsupported remote Mac architecture %q", arch)
	}
	return macRuntimes.ReadFile("runtimes/darwin-" + arch)
}
