package controller

import (
	"context"
	"fmt"
	"github.com/nerdylucifer/modelgauge/internal/benchmark"
	"strings"
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
