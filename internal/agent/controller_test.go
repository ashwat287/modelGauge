package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestWorkerStreamForwardsFramesAndOnlyRetainsFinal(t *testing.T) {
	var log bytes.Buffer
	stream := workerStream{log: &log}
	frames := `{"progress":"stage one\n"}` + "\n" + `{"host":{"os":"linux"}}` + "\n"
	for _, b := range []byte(frames) {
		if _, err := stream.Write([]byte{b}); err != nil {
			t.Fatal(err)
		}
	}
	response, err := decodeWorkerResponse(stream.final)
	if err != nil || response.Host.OS != "linux" || log.String() != "stage one\n" {
		t.Fatal(response, &log, err)
	}
	if _, err := stream.Write([]byte("not-json\n")); err == nil {
		t.Fatal("invalid frame accepted")
	}
}
func TestCleanupRefusesActiveWorkUnlessForced(t *testing.T) {
	e, err := NewControllerExecutor(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	ctx, cancel := context.WithCancel(context.Background())
	op := &operation{cancel: cancel, done: make(chan struct{})}
	e.active.Store(op)
	go func() { <-ctx.Done(); close(op.done); e.active.CompareAndSwap(op, nil) }()
	if err = e.Pause("node", false); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatal(err)
	}
	if err = e.Pause("node", true); err != nil {
		t.Fatal(err)
	}
}
func TestWorkerProgressIsAStreamFrame(t *testing.T) {
	var out bytes.Buffer
	w := workerProgress{out: &out}
	w.Write([]byte("stage\n"))
	var event workerResponse
	if err := json.NewDecoder(&out).Decode(&event); err != nil || event.Progress != "stage\n" {
		t.Fatal(event, err)
	}
}
