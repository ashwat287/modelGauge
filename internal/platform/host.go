package platform

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// Host describes facts used to identify a benchmark target.
type Host struct {
	Hostname     string `json:"hostname"`
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	CPUs         int    `json:"cpus"`
	MemoryBytes  uint64 `json:"memory_bytes,omitempty"`
}

// Inspect returns lightweight host facts without requiring elevated privileges.
func Inspect() (Host, error) {
	if runtime.GOOS != "linux" {
		return Host{}, fmt.Errorf("node inspection requires Linux; on a Mac, use a Lima Linux VM")
	}
	hostname, err := os.Hostname()
	if err != nil {
		return Host{}, fmt.Errorf("read hostname: %w", err)
	}

	host := Host{
		Hostname:     hostname,
		OS:           runtime.GOOS,
		Architecture: runtime.GOARCH,
		CPUs:         runtime.NumCPU(),
	}

	memory, err := totalMemoryBytes()
	if err == nil {
		host.MemoryBytes = memory
	}
	return host, nil
}

func totalMemoryBytes() (uint64, error) {
	switch runtime.GOOS {
	case "linux":
		file, err := os.Open("/proc/meminfo")
		if err != nil {
			return 0, err
		}
		defer file.Close()
		for scanner := bufio.NewScanner(file); scanner.Scan(); {
			fields := strings.Fields(scanner.Text())
			if len(fields) >= 2 && fields[0] == "MemTotal:" {
				kib, err := strconv.ParseUint(fields[1], 10, 64)
				if err != nil {
					return 0, err
				}
				return kib * 1024, nil
			}
		}
		return 0, fmt.Errorf("MemTotal not found")
	default:
		return 0, fmt.Errorf("memory inspection is unsupported on %s", runtime.GOOS)
	}
}
