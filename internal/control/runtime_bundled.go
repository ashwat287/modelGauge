//go:build bundled

package control

import (
	"embed"
	"fmt"
)

//go:embed runtimes/linux-arm64 runtimes/linux-amd64
var linuxRuntimes embed.FS

func nodeBinary(arch string) ([]byte, error) {
	switch arch {
	case "arm64", "amd64":
		return linuxRuntimes.ReadFile("runtimes/linux-" + arch)
	default:
		return nil, fmt.Errorf("unsupported Linux runtime architecture %q", arch)
	}
}
