package platform

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
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

// CommandContext owns a separate process group. Never release a workload lock
// with its wrapper killed but descendants still executing.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 5 * time.Second
	return cmd
}
