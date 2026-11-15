package benchmark

import (
	"bufio"
	"context"
	"fmt"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
	"os"
	"path/filepath"
	"reflect"
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

func TestDirectObservationsUseMeasuredEvidenceAndWaitForRSS(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("GNU time and Linux node inspection")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "llama-bench")
	model := filepath.Join(dir, "model.gguf")
	script := `#!/bin/sh
threads=1; prompt=0; generation=0
while [ "$#" -gt 0 ]; do
 case "$1" in --threads) threads="$2";; --n-prompt) prompt="$2";; --n-gen) generation="$2";; esac
 shift 2
done
printf '[{"n_threads":%s,"n_prompt":%s,"n_gen":%s,"avg_ts":8,"stddev_ts":1,"samples_ts":[7,9]}]\n' "$threads" "$prompt" "$generation"
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(model, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := standardCPUPlan(context.Background(), planOptions{NodeID: "node", ModelPath: model, Threads: []int{1}, Contexts: []int{32}, Warmups: 1, Repetitions: 2})
	if err != nil {
		t.Fatal(err)
	}
	p.Runtime.Binary = binary
	var events []protocol.Measurement
	result, _, err := runEnginePlan(context.Background(), p, Options{OutputDir: filepath.Join(dir, "results"), Observe: func(m protocol.Measurement) { events = append(events, m) }})
	if err != nil {
		t.Fatal(err)
	}
	final := map[string]protocol.Measurement{}
	for _, m := range events {
		if m.Phase == "final" {
			final[m.Case] = m
		} else if _, exists := m.Values["tokens_per_second"]; exists {
			t.Fatal("invented in-flight throughput", m)
		}
	}
	for _, c := range result.Cases {
		if !reflect.DeepEqual(final[c.CaseID].Values, numericValues(c)) {
			t.Fatal("direct snapshot differs from result", c, final)
		}
	}
	if result.Memory == nil || final[p.MemoryCase.ID].Values["peak_rss_bytes"] != float64(result.Memory.PeakRSSBytes) {
		t.Fatal("RSS not measured at process exit", final, result.Memory)
	}
}
