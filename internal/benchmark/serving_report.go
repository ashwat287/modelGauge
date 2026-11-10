package benchmark

import (
	"encoding/csv"
	"encoding/json"
	"io"
	"strconv"
	"strings"

	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

// WriteServingCSV keeps engine-only CSV unchanged and expands serving statistics.
func WriteServingCSV(out io.Writer, results []protocol.Result) error {
	w := csv.NewWriter(out)
	header := []string{"run_id", "node_id", "model_id", "status", "case_id", "scenario", "concurrency", "target_prompt_tokens", "max_output_tokens", "elapsed_seconds", "offered", "dispatched", "successful", "errors", "timeouts", "incomplete", "cancelled", "client_capacity_drops", "successful_requests_per_second", "goodput_requests_per_second", "input_tokens_per_second", "output_tokens_per_second", "token_accounted_requests", "quality_evaluated", "quality_passed", "quality_pass_rate", "cache_hit_requests", "known_cached_tokens", "context_truncated", "output_limited"}
	for _, metric := range []string{"latency_ms", "ttft_ms", "first_answer_ms", "tpot_ms", "stream_chunk_gap_ms", "observed_token_gap_ms", "longest_stream_stall_ms", "dispatch_delay_ms", "scheduled_latency_ms", "client_cancel_ack_ms"} {
		for _, field := range []string{"count", "mean", "stddev", "min", "max", "p50", "p90", "p95", "p99"} {
			header = append(header, metric+"_"+field)
		}
	}
	header = append(header, "sampled_peak_rss_bytes", "sampled_peak_pss_bytes", "process_lifetime_peak_rss_bytes", "process_cpu_seconds", "node_cpu_package_energy_joules", "resources_json", "warnings_json")
	header = append(header, "plan_id", "execution_mode", "served_model", "cache_prompt", "cache_accounted_requests", "slo_evaluated", "slo_met", "target_ttft_ms", "target_tpot_ms", "target_latency_ms", "first_request_latency_ms", "observed_models_json", "recovery_probe_json")
	if err := w.Write(header); err != nil {
		return err
	}
	number := func(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }
	optional := func(v *float64) string {
		if v == nil {
			return ""
		}
		return number(*v)
	}
	integer := strconv.Itoa
	for _, result := range results {
		if result.Benchmark == nil || result.Benchmark.Kind != "serving" {
			continue
		}
		report, err := DecodeServingReport(result)
		if err != nil {
			return err
		}
		for _, c := range report.Cases {
			row := []string{result.RunID, result.NodeID, report.ModelID, string(result.Status), c.ID, c.Scenario, integer(c.Concurrency), integer(c.TargetPromptTokens), integer(c.MaxOutputTokens), number(c.ElapsedSeconds), integer(c.Offered), integer(c.Dispatched), integer(c.Successful), integer(c.Errors), integer(c.Timeouts), integer(c.Incomplete), integer(c.Cancelled), integer(c.ClientCapacityDrops), number(c.RequestsPerSecond), optional(c.Goodput), optional(c.InputTokensPerSecond), optional(c.OutputTokensPerSecond), integer(c.TokenAccounted), integer(c.QualityEvaluated), integer(c.QualityPassed), optional(c.QualityPassRate), integer(c.CacheHitRequests), integer(c.CachedTokens), integer(c.ContextTruncated), integer(c.OutputLimited)}
			for _, d := range []*Distribution{c.LatencyMS, c.TTFTMS, c.FirstAnswerMS, c.TPOTMS, c.ChunkGapMS, c.ITLMS, c.LongestStallMS, c.DispatchDelayMS, c.ScheduledLatencyMS, c.CancelAckMS} {
				if d == nil {
					row = append(row, "", "", "", "", "", "", "", "", "")
					continue
				}
				row = append(row, integer(d.Count), number(d.Mean), number(d.Stddev), number(d.Min), number(d.Max), number(d.P50), number(d.P90), number(d.P95), number(d.P99))
			}
			resources, err := json.Marshal(c.Resources)
			if err != nil {
				return err
			}
			warnings, err := json.Marshal(c.Warnings)
			if err != nil {
				return err
			}
			row = append(row, optional(c.Resources.SampledPeakRSSBytes), optional(c.Resources.SampledPeakPSSBytes), optional(c.Resources.LifetimePeakRSSBytes), optional(c.Resources.CPUSeconds), optional(c.Resources.NodeEnergyJoules), string(resources), string(warnings))
			models, err := json.Marshal(c.ObservedModels)
			if err != nil {
				return err
			}
			probe, err := json.Marshal(c.RecoveryProbe)
			if err != nil {
				return err
			}
			targets := report.Targets
			row = append(row, result.PlanID, string(result.ExecutionMode), c.ServedModel, strconv.FormatBool(c.CachePrompt), integer(c.CacheAccounted), integer(c.SLOEvaluated), integer(c.SLOMet), number(targets.TTFTMS), number(targets.TPOTMS), number(targets.LatencyMS), optional(c.FirstRequestMS), string(models), string(probe))
			for i, value := range row {
				trim := strings.TrimLeft(value, " \t\r\n")
				if len(trim) > 0 && strings.ContainsRune("=+-@", rune(trim[0])) {
					row[i] = "'" + value
				}
			}
			if err := w.Write(row); err != nil {
				return err
			}
		}
	}
	w.Flush()
	return w.Error()
}
