package benchmark

import (
	"os/exec"
	"syscall"
)

func serverParentDeath(cmd *exec.Cmd) { cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL }
