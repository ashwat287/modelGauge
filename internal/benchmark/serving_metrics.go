package benchmark

import (
	"fmt"
	"math"
	"strings"
)

const servingMetricsHeader = `# HELP modelgauge_serving_requests Request outcomes in the latest completed serving run
# TYPE modelgauge_serving_requests gauge
# HELP modelgauge_serving_latency_seconds Client-observed latency statistics in the latest completed serving run
# TYPE modelgauge_serving_latency_seconds gauge
# TYPE modelgauge_serving_requests_per_second gauge
# TYPE modelgauge_serving_goodput_requests_per_second gauge
# TYPE modelgauge_serving_output_tokens_per_second gauge
# TYPE modelgauge_serving_input_tokens_per_second gauge
# TYPE modelgauge_serving_quality_pass_ratio gauge
# TYPE modelgauge_serving_sampled_peak_rss_bytes gauge
# TYPE modelgauge_serving_cpu_package_energy_joules gauge
`

func writeServingMetrics(out *strings.Builder, node, mode, model string, report ServingReport) {
	for _, c := range report.Cases {
		labels := fmt.Sprintf("node=%q,mode=%q,model=%q,case=%q,scenario=%q,concurrency=%q,context=%q", node, mode, model, c.ID, c.Scenario, fmt.Sprint(c.Concurrency), fmt.Sprint(c.TargetPromptTokens))
		write := func(name, extra string, value float64) {
			if !math.IsNaN(value) && !math.IsInf(value, 0) {
				fmt.Fprintf(out, "%s{%s%s} %g\n", name, labels, extra, value)
			}
		}
		for _, field := range []struct {
			name  string
			value int
		}{{"offered", c.Offered}, {"successful", c.Successful}, {"errors", c.Errors}, {"timeouts", c.Timeouts}, {"incomplete", c.Incomplete}, {"cancelled", c.Cancelled}, {"client_capacity_drops", c.ClientCapacityDrops}} {
			write("modelgauge_serving_requests", fmt.Sprintf(",outcome=%q", field.name), float64(field.value))
		}
		write("modelgauge_serving_requests_per_second", "", c.RequestsPerSecond)
		for _, field := range []struct {
			name  string
			value *float64
		}{{"goodput_requests_per_second", c.Goodput}, {"output_tokens_per_second", c.OutputTokensPerSecond}, {"input_tokens_per_second", c.InputTokensPerSecond}, {"quality_pass_ratio", c.QualityPassRate}, {"sampled_peak_rss_bytes", c.Resources.SampledPeakRSSBytes}, {"cpu_package_energy_joules", c.Resources.NodeEnergyJoules}} {
			if field.value != nil {
				write("modelgauge_serving_"+field.name, "", *field.value)
			}
		}
		for _, field := range []struct {
			name  string
			value *Distribution
		}{{"e2e", c.LatencyMS}, {"ttft", c.TTFTMS}, {"first_answer", c.FirstAnswerMS}, {"tpot", c.TPOTMS}, {"stream_chunk_gap", c.ChunkGapMS}, {"observed_token_gap", c.ITLMS}, {"longest_stream_stall", c.LongestStallMS}, {"dispatch_delay", c.DispatchDelayMS}, {"scheduled", c.ScheduledLatencyMS}, {"client_cancel_ack", c.CancelAckMS}} {
			if field.value == nil {
				continue
			}
			d := field.value
			for _, stat := range []struct {
				name  string
				value float64
			}{{"mean", d.Mean}, {"p50", d.P50}, {"p90", d.P90}, {"p95", d.P95}, {"p99", d.P99}, {"max", d.Max}} {
				write("modelgauge_serving_latency_seconds", fmt.Sprintf(",measurement=%q,statistic=%q", field.name, stat.name), stat.value/1000)
			}
		}
	}
}
