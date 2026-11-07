package benchmark

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCancellationKillsWrapperChildren(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux process-group semantics")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := commandContext(ctx, "/bin/sh", "-c", "sleep 60 & echo $!; wait")
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var pid int
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fmt.Sscan(line, &pid); err != nil {
		t.Fatal(err)
	}
	cancel()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("wrapper did not stop")
	}
	// A killed grandchild can briefly remain a zombie until init reaps it.
	deadline := time.Now().Add(time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if err == syscall.ESRCH {
			return
		}
		stat, readErr := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if os.IsNotExist(readErr) {
			return
		}
		if len(strings.Fields(string(stat))) > 2 && strings.Fields(string(stat))[2] == "Z" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("wrapper child %d is still executing", pid)
		}
		time.Sleep(time.Millisecond)
	}
}
