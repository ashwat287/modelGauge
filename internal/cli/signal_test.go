package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestSignalHelper(t *testing.T) {
	if os.Getenv("MODELGAUGE_TEST_SIGNAL_HELPER") != "1" {
		return
	}
	os.Exit(RunWithSignals([]string{"model", "list", "--node", "test", "--controller", os.Getenv("MODELGAUGE_TEST_SIGNAL_URL"), "--admin-token", "test"}, os.Stdout, os.Stderr))
}
func TestExecutableSignalHandling(t *testing.T) {
	for _, test := range []struct {
		signal os.Signal
		code   int
	}{{os.Interrupt, 130}, {syscall.SIGTERM, 143}, {syscall.SIGHUP, 129}} {
		t.Run(test.signal.String(), func(t *testing.T) {
			started := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
			defer server.Close()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(executable, "-test.run=^TestSignalHelper$")
			cmd.Env = append(os.Environ(), "MODELGAUGE_TEST_SIGNAL_HELPER=1", "MODELGAUGE_TEST_SIGNAL_URL="+server.URL)
			var output bytes.Buffer
			cmd.Stdout = &output
			cmd.Stderr = &output
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if cmd.ProcessState == nil {
					_ = cmd.Process.Kill()
				}
			})
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatalf("helper did not request controller: %s", &output)
			}
			if err = cmd.Process.Signal(test.signal); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err = <-done:
				if err == nil || cmd.ProcessState.ExitCode() != test.code {
					t.Fatalf("exit=%d: %s", cmd.ProcessState.ExitCode(), &output)
				}
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
				<-done
				t.Fatal("signal did not cancel in-flight HTTP")
			}
		})
	}
}
