package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
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

func TestWorkerMeasurementFramesDoNotReplaceFinalOrLeakToLogs(t *testing.T) {
	var log bytes.Buffer
	var observations []protocol.Measurement
	stream := workerStream{log: &log, observe: func(m protocol.Measurement) { observations = append(observations, m) }}
	frames := "{\"measurement\":{\"case\":\"load\",\"phase\":\"measuring\",\"values\":{\"successful\":1}}}\n{\"measurement\":{}}\n{\"host\":{\"os\":\"linux\"}}\n"
	for _, b := range []byte(frames) {
		if _, err := stream.Write([]byte{b}); err != nil {
			t.Fatal(err)
		}
	}
	response, err := decodeWorkerResponse(stream.final)
	if err != nil || len(observations) != 2 || observations[0].Values["successful"] != 1 || log.Len() != 0 || response.Host.OS != "linux" {
		t.Fatal(observations, response, err, &log)
	}
}

type heartbeatLog struct {
	bytes.Buffer
	heartbeats int
}

func (w *heartbeatLog) Write(p []byte) (int, error) {
	if len(p) == 0 {
		w.heartbeats++
	}
	return w.Buffer.Write(p)
}
func TestWorkerHeartbeatKeepsForegroundStreamAliveWithoutRepeatingLogs(t *testing.T) {
	var log heartbeatLog
	stream := workerStream{log: &log}
	if _, err := stream.Write([]byte("{\"measurement\":{}}\n")); err != nil {
		t.Fatal(err)
	}
	if log.heartbeats != 1 || log.Len() != 0 || len(stream.final) != 0 {
		t.Fatal("heartbeat was logged or treated as result", log.heartbeats, &log, stream.final)
	}
}
