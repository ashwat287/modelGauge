//go:build !linux

package benchmark

import "os/exec"

func serverParentDeath(cmd *exec.Cmd) {}
