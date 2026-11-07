package controller

import (
	"context"
	"fmt"
	"strings"
)

// Metrics exports database-backed gauges. Run IDs and artifact names never
// become labels; benchmark series retain only the latest matching result.
func (s *Store) Metrics(ctx context.Context) (string, error) {
	var out strings.Builder
	for _, family := range []struct{ name, query string }{
		{"modelgauge_plans", `SELECT status,count(*) FROM benchmark_plans GROUP BY status`},
		{"modelgauge_install_jobs", `SELECT status,count(*) FROM model_install_jobs GROUP BY status`},
		{"modelgauge_runs", `SELECT status,count(*) FROM benchmark_runs GROUP BY status`},
	} {
		fmt.Fprintf(&out, "# HELP %s Persisted records by current state\n# TYPE %s gauge\n", family.name, family.name)
		rows, err := s.pool.Query(ctx, family.query)
		if err != nil {
			return "", err
		}
		counts := map[string]int64{}
		for rows.Next() {
			var status string
			var count int64
			if err := rows.Scan(&status, &count); err != nil {
				rows.Close()
				return "", err
			}
			counts[status] = count
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return "", err
		}
		for _, status := range []string{"queued", "leased", "running", "completed", "invalid", "failed", "cancelled"} {
			fmt.Fprintf(&out, "%s{status=%q} %d\n", family.name, status, counts[status])
		}
	}
	fmt.Fprintln(&out, "# HELP modelgauge_agent_last_seen_seconds Last agent contact as Unix timestamp\n# TYPE modelgauge_agent_last_seen_seconds gauge")
	rows, err := s.pool.Query(ctx, `SELECT node_id,extract(epoch FROM last_seen_at)::double precision FROM nodes WHERE last_seen_at IS NOT NULL`)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var node string
		var stamp float64
		if err := rows.Scan(&node, &stamp); err != nil {
			rows.Close()
			return "", err
		}
		fmt.Fprintf(&out, "modelgauge_agent_last_seen_seconds{node=%q} %g\n", node, stamp)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return "", err
	}
	fmt.Fprintln(&out, "# HELP modelgauge_benchmark_tokens_per_second Latest completed throughput\n# TYPE modelgauge_benchmark_tokens_per_second gauge\n# HELP modelgauge_benchmark_relative_stddev Standard deviation divided by mean throughput\n# TYPE modelgauge_benchmark_relative_stddev gauge")
	rows, err = s.pool.Query(ctx, `SELECT DISTINCT ON (r.node_id,r.execution_mode,p.specification #>> '{model,id}',c.threads,c.prompt_tokens,c.generation_tokens) r.node_id,r.execution_mode,p.specification #>> '{model,id}',c.threads,c.prompt_tokens,c.generation_tokens,c.average_tokens_per_second,c.stddev_tokens_per_second FROM benchmark_runs r JOIN benchmark_plans p USING(plan_id) JOIN benchmark_cases c USING(run_id) WHERE r.status='completed' ORDER BY r.node_id,r.execution_mode,p.specification #>> '{model,id}',c.threads,c.prompt_tokens,c.generation_tokens,r.completed_at DESC`)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var node, mode, model string
		var threads, prompt, gen int
		var avg, dev float64
		if err := rows.Scan(&node, &mode, &model, &threads, &prompt, &gen, &avg, &dev); err != nil {
			rows.Close()
			return "", err
		}
		labels := fmt.Sprintf("node=%q,mode=%q,model=%q,threads=%q,prompt=%q,generation=%q", node, mode, model, fmt.Sprint(threads), fmt.Sprint(prompt), fmt.Sprint(gen))
		fmt.Fprintf(&out, "modelgauge_benchmark_tokens_per_second{%s} %g\n", labels, avg)
		if avg > 0 {
			fmt.Fprintf(&out, "modelgauge_benchmark_relative_stddev{%s} %g\n", labels, dev/avg)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return "", err
	}
	fmt.Fprintln(&out, "# HELP modelgauge_peak_rss_bytes Latest measured process peak RSS\n# TYPE modelgauge_peak_rss_bytes gauge\n# HELP modelgauge_memory_reservation_bytes Memory reservation from measured RSS plus margin\n# TYPE modelgauge_memory_reservation_bytes gauge")
	rows, err = s.pool.Query(ctx, `SELECT DISTINCT ON (r.node_id,r.execution_mode,p.specification #>> '{model,id}',m.case_id) r.node_id,r.execution_mode,p.specification #>> '{model,id}',m.case_id,m.peak_rss_bytes,m.safe_reservation_bytes FROM benchmark_runs r JOIN benchmark_plans p USING(plan_id) JOIN memory_measurements m USING(run_id) WHERE r.status='completed' ORDER BY r.node_id,r.execution_mode,p.specification #>> '{model,id}',m.case_id,r.completed_at DESC`)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var node, mode, model, workload string
		var rss, reservation int64
		if err := rows.Scan(&node, &mode, &model, &workload, &rss, &reservation); err != nil {
			rows.Close()
			return "", err
		}
		labels := fmt.Sprintf("node=%q,mode=%q,model=%q,workload=%q", node, mode, model, workload)
		fmt.Fprintf(&out, "modelgauge_peak_rss_bytes{%s} %d\nmodelgauge_memory_reservation_bytes{%s} %d\n", labels, rss, labels, reservation)
	}
	err = rows.Err()
	rows.Close()
	return out.String(), err
}
