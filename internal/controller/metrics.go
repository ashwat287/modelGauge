package controller

import (
	"context"
	"fmt"
	"github.com/nerdylucifer/modelgauge/internal/benchmark"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

func (s *Store) Metrics(ctx context.Context) (string, error) {
	// Read all completed records; no job state or heartbeat is persisted.
	records, err := s.Records()
	if err != nil {
		return "", err
	}
	rows := []RunSummary{}
	for _, r := range records {
		rows = append(rows, summary(r))
	}
	var out strings.Builder
	fmt.Fprintf(&out, "# TYPE modelgauge_runs gauge\nmodelgauge_runs{status=\"completed\"} %d\n# TYPE modelgauge_benchmark_tokens_per_second gauge\n# TYPE modelgauge_benchmark_relative_stddev gauge\n# TYPE modelgauge_peak_rss_bytes gauge\n# TYPE modelgauge_memory_reservation_bytes gauge\n", len(rows))
	seen := map[string]bool{}
	for _, r := range rows {
		labels := fmt.Sprintf("node=%q,mode=%q,model=%q", r.NodeID, r.ExecutionMode, r.ModelID)
		key := labels
		if !seen[key] {
			seen[key] = true
			fmt.Fprintf(&out, "modelgauge_peak_rss_bytes{%s} %d\nmodelgauge_memory_reservation_bytes{%s} %d\n", labels, r.PeakRSSBytes, labels, r.SafeReservationBytes)
			if err := benchmark.WriteMetrics(&out, r.NodeID, r.ExecutionMode, r.ModelID, r.Benchmark); err != nil {
				return "", err
			}
		}
		for _, c := range r.Cases {
			labels := fmt.Sprintf("%s,threads=%q,prompt=%q,generation=%q", labels, fmt.Sprint(c.Threads), fmt.Sprint(c.PromptTokens), fmt.Sprint(c.GenerationTokens))
			if seen[labels] {
				continue
			}
			seen[labels] = true
			fmt.Fprintf(&out, "modelgauge_benchmark_tokens_per_second{%s} %g\n", labels, c.TokensPerSecond)
			if c.TokensPerSecond > 0 {
				fmt.Fprintf(&out, "modelgauge_benchmark_relative_stddev{%s} %g\n", labels, c.TokensPerSecondStddev/c.TokensPerSecond)
			}
		}
	}
	return out.String(), nil
}

// LiveMetrics is only the current experiment's bounded, replaceable view.
// It is observation state, never a queue, checkpoint or execution lock.
type LiveMetrics struct {
	watching                           bool
	order                              []string
	valueCount                         int
	mu                                 sync.Mutex
	node, model, state, stage, current string
	started, ended, contact, measured  time.Time
	observationEnded                   time.Time
	cases                              map[string]protocol.Measurement
}

func (l *LiveMetrics) Start(node, model string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.node, l.model, l.state = node, model, "running"
	l.watching = false
	l.observationEnded = time.Time{}
	l.stage, l.current = "", ""
	l.started, l.contact = time.Now(), time.Now()
	l.ended, l.measured = time.Time{}, time.Time{}
	l.cases = map[string]protocol.Measurement{}
	l.order = nil
	l.valueCount = 0
}
func (l *LiveMetrics) Observe(m protocol.Measurement) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state != "running" && !l.watching {
		return
	}
	l.contact = time.Now()
	if m.Case == "" {
		return
	} // Heartbeat.
	if len(m.Stage) > 96 || len(m.Case) > 96 || len(m.Phase) > 32 || len(m.Values) > 2048 {
		return
	}
	key := m.Stage + "/" + m.Case
	if old, exists := l.cases[key]; exists {
		l.valueCount -= len(old.Values)
		for i, existing := range l.order {
			if existing == key {
				l.order = append(l.order[:i], l.order[i+1:]...)
				break
			}
		}
	}
	l.order = append(l.order, key)
	copy := m
	if m.Phase == "" {
		copy.Phase = "observing"
	}
	copy.Values = map[string]float64{}
	copy.Unavailable = nil
	for k, v := range m.Values {
		if len(k) <= 192 && !math.IsNaN(v) && !math.IsInf(v, 0) {
			copy.Values[k] = v
		}
	}
	for _, reason := range m.Unavailable {
		if len(copy.Unavailable) < 32 && len(reason) <= 256 {
			copy.Unavailable = append(copy.Unavailable, reason)
		}
	}
	l.cases[key] = copy
	l.valueCount += len(copy.Values)
	for len(l.cases) > 256 || l.valueCount > 16384 {
		oldest := l.order[0]
		l.order = l.order[1:]
		l.valueCount -= len(l.cases[oldest].Values)
		delete(l.cases, oldest)
	}
	if m.Phase != "" {
		l.stage, l.current = m.Stage, m.Case
	}
	l.measured = time.Now()
}
func (l *LiveMetrics) Finish(state string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.state, l.ended = state, time.Now()
	if state != "completed" {
		l.observationEnded = l.ended
	}
	for key, m := range l.cases {
		if m.Phase == "observing" {
			l.valueCount -= len(m.Values)
			delete(l.cases, key)
			continue
		}
		if m.Phase != "final" && m.Phase != "ready" {
			m.Phase = state
			l.cases[key] = m
		}
	}
}
func (l *LiveMetrics) Watch(enabled bool) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state == "" || l.state == "running" {
		return false
	}
	l.watching = enabled
	if enabled {
		l.contact = time.Now()
		l.observationEnded = time.Time{}
	} else {
		l.observationEnded = time.Now()
	}
	for key, m := range l.cases {
		if m.Phase == "observing" {
			m.Phase = "final"
			l.cases[key] = m
		}
	}
	order := l.order[:0]
	for _, key := range l.order {
		if _, ok := l.cases[key]; ok {
			order = append(order, key)
		}
	}
	l.order = order
	return true
}

func (l *LiveMetrics) Metrics() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out strings.Builder
	if l.state == "" {
		return "# TYPE modelgauge_live_present gauge\nmodelgauge_live_present 0\n"
	}
	fmt.Fprintln(&out, "# TYPE modelgauge_live_present gauge\nmodelgauge_live_present 1\n# TYPE modelgauge_live_value gauge\n# HELP modelgauge_live_value Latest case observations; units follow the measurement field name.")
	labels := fmt.Sprintf("node=%q,model=%q", l.node, l.model)
	watching := 0
	if l.watching {
		watching = 1
	}
	fmt.Fprintf(&out, "modelgauge_live_watching %d\n", watching)
	windowEnd := "+Inf"
	if !l.observationEnded.IsZero() {
		windowEnd = fmt.Sprint(float64(l.observationEnded.UnixMilli()) / 1000)
	}
	fmt.Fprintf(&out, "# HELP modelgauge_live_window_end_seconds End of collected history; +Inf while collection is active.\n# TYPE modelgauge_live_window_end_seconds gauge\nmodelgauge_live_window_end_seconds %s\n", windowEnd)
	fmt.Fprintf(&out, "modelgauge_live_info{%s,state=%q,stage=%q,case=%q} 1\n", labels, l.state, l.stage, l.current)
	end := l.ended
	if end.IsZero() {
		end = time.Now()
	}
	stale := 0
	if (l.state == "running" || l.watching) && time.Since(l.contact) > 15*time.Second {
		stale = 1
	}
	fmt.Fprintf(&out, "modelgauge_live_started_seconds %g\nmodelgauge_live_elapsed_seconds %g\nmodelgauge_live_last_contact_seconds %g\nmodelgauge_live_stale %d\n", float64(l.started.UnixMilli())/1000, end.Sub(l.started).Seconds(), float64(l.contact.UnixMilli())/1000, stale)
	if !l.measured.IsZero() {
		fmt.Fprintf(&out, "modelgauge_live_last_measurement_seconds %g\n", float64(l.measured.UnixMilli())/1000)
	}
	keys := make([]string, 0, len(l.cases))
	for key := range l.cases {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		m := l.cases[key]
		base := fmt.Sprintf("%s,stage=%q,case=%q", labels, m.Stage, m.Case)
		fmt.Fprintf(&out, "modelgauge_live_case_info{%s,phase=%q} 1\n", base, m.Phase)
		names := make([]string, 0, len(m.Values))
		for name := range m.Values {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Fprintf(&out, "modelgauge_live_value{%s,measurement=%q} %g\n", base, name, m.Values[name])
		}
		for _, reason := range m.Unavailable {
			fmt.Fprintf(&out, "modelgauge_live_unavailable{%s,reason=%q} 1\n", base, reason)
		}
	}
	return out.String()
}
