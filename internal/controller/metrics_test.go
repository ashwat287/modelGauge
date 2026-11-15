package controller

import (
	"fmt"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLiveSnapshotsResetAndStaySeparateFromResults(t *testing.T) {
	var live LiveMetrics
	if !strings.Contains(live.Metrics(), "modelgauge_live_present 0") {
		t.Fatal(live.Metrics())
	}
	live.Start("node", "first")
	live.Observe(protocol.Measurement{Stage: "1", Case: "stage", Phase: "preparing"})
	values := map[string]float64{"ttft_ms_p95": 42, "invalid": math.NaN()}
	live.Observe(protocol.Measurement{Stage: "1", Case: "load", Phase: "measuring", Values: values, Unavailable: []string{"sensor unavailable"}})
	values["ttft_ms_p95"] = 999
	text := live.Metrics()
	for _, part := range []string{`model="first"`, `measurement="ttft_ms_p95"} 42`, `reason="sensor unavailable"`, `state="running"`, `modelgauge_live_started_seconds`} {
		if !strings.Contains(text, part) {
			t.Fatal(part, text)
		}
	}
	if strings.Contains(text, `measurement="invalid"`) {
		t.Fatal(text)
	}
	live.Observe(protocol.Measurement{Stage: "1", Case: "load", Phase: "warming-up"})
	if strings.Contains(live.Metrics(), `measurement="ttft_ms_p95"`) {
		t.Fatal("old same-case measurement was not cleared")
	}
	live.contact = time.Now().Add(-time.Minute)
	if !strings.Contains(live.Metrics(), "modelgauge_live_stale 1") {
		t.Fatal("missing stale indicator")
	}
	live.Observe(protocol.Measurement{})
	if !strings.Contains(live.Metrics(), "modelgauge_live_stale 0") {
		t.Fatal("heartbeat did not refresh contact")
	}
	live.Finish("cancelled")
	if strings.Contains(live.Metrics(), `phase="preparing"`) {
		t.Fatal("terminal operation left stage running", live.Metrics())
	}
	live.Observe(protocol.Measurement{Case: "late", Phase: "final"})
	if text = live.Metrics(); !strings.Contains(text, `state="cancelled"`) || strings.Contains(text, `case="late"`) {
		t.Fatal(text)
	}
	live.Start("node", "second")
	if text = live.Metrics(); strings.Contains(text, "load") || strings.Contains(text, "first") || strings.Contains(text, "sensor unavailable") {
		t.Fatal("stale experiment data", text)
	}
}
func TestLiveMetricsConcurrentReadersAndBoundedObservations(t *testing.T) {
	var live LiveMetrics
	live.Start("node", "model")
	var group sync.WaitGroup
	for i := 0; i < 4; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for n := 0; n < 300; n++ {
				live.Observe(protocol.Measurement{Case: fmt.Sprint(n), Phase: "final", Values: map[string]float64{"n": float64(n)}})
				_ = live.Metrics()
			}
		}()
	}
	group.Wait()
	if len(live.cases) != 256 {
		t.Fatal(len(live.cases))
	}
	tooMany := map[string]float64{}
	for i := 0; i < 2049; i++ {
		tooMany[fmt.Sprint(i)] = 1
	}
	live.Start("node", "model")
	live.Observe(protocol.Measurement{Case: "large", Phase: "measuring", Values: tooMany})
	if len(live.cases) != 0 {
		t.Fatal("unbounded observation retained")
	}
	// Updating an older case must retain it when the numeric budget evicts others.
	live.Start("node", "model")
	values := map[string]float64{}
	for i := 0; i < 2048; i++ {
		values[fmt.Sprint(i)] = 1
	}
	for i := 0; i < 8; i++ {
		live.Observe(protocol.Measurement{Case: fmt.Sprint(i), Phase: "measuring", Values: values})
	}
	live.Observe(protocol.Measurement{Case: "0", Phase: "measuring", Values: values})
	live.Observe(protocol.Measurement{Case: "new", Phase: "measuring", Values: values})
	if _, ok := live.cases["/0"]; !ok || live.valueCount > 16384 {
		t.Fatal("current/recent observations evicted", live.valueCount)
	}
	live.Finish("failed")
}

func TestContinuousObservationDoesNotAdvanceCasesOrCancelCompletedResults(t *testing.T) {
	var live LiveMetrics
	live.Start("node", "model")
	live.Observe(protocol.Measurement{Stage: "1", Case: "generation", Phase: "warming-up"})
	live.Observe(protocol.Measurement{Case: "(node)", Values: map[string]float64{"resource_current_node_memory_available_bytes": 100}})
	if live.current != "generation" || live.stage != "1" {
		t.Fatal("resource sample replaced active case")
	}
	live.Observe(protocol.Measurement{Stage: "1", Case: "generation", Phase: "final", Values: map[string]float64{"tokens_per_second": 8}})
	live.Finish("completed")
	if !live.Watch(true) {
		t.Fatal("cannot observe completed benchmark")
	}
	live.Observe(protocol.Measurement{Case: "(node)", Values: map[string]float64{"resource_current_node_memory_available_bytes": 200}})
	text := live.Metrics()
	for _, want := range []string{`state="completed"`, `modelgauge_live_watching 1`, `measurement="tokens_per_second"} 8`, `measurement="resource_current_node_memory_available_bytes"} 200`} {
		if !strings.Contains(text, want) {
			t.Fatal(want, text)
		}
	}
	live.Watch(false)
	if live.observationEnded.IsZero() {
		t.Fatal("cooldown end not recorded")
	}
	text = live.Metrics()
	if !strings.Contains(text, `measurement="resource_current_node_memory_available_bytes"} 200`) || !strings.Contains(text, `state="completed"`) || !strings.Contains(text, "modelgauge_live_watching 0") {
		t.Fatal("cooldown stop lost the final snapshot or corrupted results", text)
	}
}
