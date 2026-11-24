//go:build bundled || remotehost

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

// macBinary is only used by the controller or the bundled host CLI.
func macBinary(arch string) ([]byte, error) { return readMacRuntime(arch) }
