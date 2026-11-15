package benchmark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nerdylucifer/modelgauge/internal/buildinfo"
	"github.com/nerdylucifer/modelgauge/internal/platform"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

func runServingPlan(ctx context.Context, plan protocol.Plan, options Options) (result protocol.Result, outputDir string, resultErr error) {
	serving, err := decodeServingPlan(plan)
	if err != nil {
		return result, "", err
	}

	outputDir = options.OutputDir
	runID := options.RunID
	if runID == "" {
		runID = "serving-" + time.Now().UTC().Format("20060102T150405.000000000")
	}
	if outputDir == "" {
		outputDir = filepath.Join("results", runID)
	}
	if err = prepareOutputDir(outputDir); err != nil {
		return result, outputDir, err
	}
	planFile := filepath.Join(outputDir, "plan.json")
	if err = platform.WriteJSON(planFile, plan, 0600); err != nil {
		return result, outputDir, err
	}
	report := &servingReport{SchemaVersion: "modelgauge-serving/v1", ModelID: plan.Model.ID, Targets: serving.Targets, Endpoint: serving.Endpoint, API: serving.API, ServedModel: serving.ServedModel, StartedAt: time.Now().UTC(), Settings: serving.RuntimeSettings, Cases: []servingCaseResult{}, Warnings: []string{"Client latency includes queueing and local load-generator overhead. Streaming chunk boundaries are not assumed to be individual tokens."}, Unavailable: []string{"per-request backend queue time unless exposed by the runtime", "physical-host sensors hidden by a VM", "memory bandwidth and general semantic grading"}}
	if serving.Server == nil && serving.LocalPID == 0 {
		report.Unavailable = append(report.Unavailable, "local process RSS/PSS/CPU, node power and GPU sensors: no local_server_pid supplied")
	}
	if serving.Server == nil && serving.MetricsURL == "" {
		report.Unavailable = append(report.Unavailable, "server queue, KV-cache and backend metrics: no metrics_url supplied")
	}
	result = protocol.Result{SchemaVersion: resultSchemaV1, RunID: runID, PlanID: plan.PlanID, NodeID: plan.NodeID, ExecutionMode: plan.ExecutionMode, Status: protocol.RunStatusRunning, StartedAt: report.StartedAt, RuntimeMetadata: map[string]string{"benchmark_kind": "serving", "client_goos": runtime.GOOS, "client_goarch": runtime.GOARCH, "benchmark_version": buildinfo.Version, "benchmark_commit": buildinfo.Commit}}
	defer func() {
		report.CompletedAt = time.Now().UTC()
		result.CompletedAt = report.CompletedAt
		data, encodeErr := json.Marshal(report)
		if err := errors.Join(encodeErr, report.Validate()); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("serving summary: %w; detailed evidence retained locally", err))
			// Do not strand an agent retrying a result the controller cannot accept.
			result.Benchmark = nil
		} else {
			result.Benchmark = &protocol.BenchmarkReport{Kind: "serving", Data: data}
		}
		result.Status = protocol.RunStatusCompleted
		if resultErr != nil {
			result.Status = protocol.RunStatusFailed
			result.InvalidReason = resultErr.Error()
			if ctx.Err() != nil {
				result.Status = protocol.RunStatusCancelled
			}
		}
		resultErr = errors.Join(resultErr, platform.WriteJSON(filepath.Join(outputDir, "serving-summary.json"), report, 0600))
		artifacts, err := collectArtifacts(outputDir)
		if err != nil {
			resultErr = errors.Join(resultErr, err)
		} else {
			result.Artifacts = artifacts
		}
		if resultErr != nil && result.Status == protocol.RunStatusCompleted {
			result.Status = protocol.RunStatusFailed
			result.InvalidReason = resultErr.Error()
		}
		resultFile := "result.json"

		resultErr = errors.Join(resultErr, platform.WriteJSON(filepath.Join(outputDir, resultFile), result, 0600))
	}()
	stopProcess := func() {}
	var server *modelServer
	if serving.Server != nil {
		if runtime.GOOS != "linux" {
			return result, outputDir, errors.New("managed model benchmarks execute on the Linux benchmark node; submit this plan through the controller")
		}

		options.measure("(server)", "preparing", nil, nil)
		options.log("Starting temporary llama-server")
		server, err = startModelServer(ctx, plan, serving, outputDir)
		if err != nil {
			return result, outputDir, err
		}
		defer func() {
			stopped := time.Now()
			resultErr = errors.Join(resultErr, server.stop())
			report.ShutdownMS = floatPointer(milliseconds(time.Since(stopped)))
			stopProcess()
			values := map[string]float64{"process_shutdown_ms": *report.ShutdownMS, "readiness_wait_ms": report.ReadinessWaitMS}
			if report.StartupMS != nil {
				values["process_start_to_ready_ms"] = *report.StartupMS
			}
			options.measure("(server)", "final", values, report.Unavailable)
		}()
		report.Endpoint, report.Settings = serving.Endpoint, serving.RuntimeSettings
		report.Warnings = []string{"Measured on the benchmark node: the load generator shares CPU and memory with the model process.", "Stream chunks are not necessarily individual tokens. Process startup includes model loading and runtime initialization, not an isolated cold-disk load."}
		report.Unavailable = []string{"physical-host sensors and power hidden by a VM", "per-request server queue time unless exposed by the runtime", "memory bandwidth and general semantic grading"}

		result.RuntimeMetadata["model_sha256"] = plan.Model.SHA256
	}

	client := newServingClient(*serving)
	if options.Observe != nil {
		client.observe = func(m protocol.Measurement) {
			if serving.Server != nil {
				if m.Values == nil {
					m.Values = map[string]float64{}
				}
				m.Values["threads"] = float64(serving.Server.Threads)
			}
			options.Observe(m)
		}
	}
	defer client.close()
	if serving.LocalPID > 0 {
		stopProcess = observeResources(ctx, client, options, "(process)")
		if server == nil {
			defer stopProcess()
		}
	}
	readyCtx, cancelReady := context.WithCancel(ctx)
	defer cancelReady()
	if server != nil {
		go func() {
			select {
			case <-server.done:
				cancelReady()
			case <-readyCtx.Done():
			}
		}()
	}
	if report.ReadinessWaitMS, err = client.ready(readyCtx); err != nil {
		if server != nil {
			err = errors.Join(err, server.running())
		}
		return result, outputDir, err
	}
	cancelReady()
	if server != nil {
		report.StartupMS = floatPointer(milliseconds(time.Since(server.startedAt)))
		options.measure("(server)", "ready", map[string]float64{"process_start_to_ready_ms": *report.StartupMS, "readiness_wait_ms": report.ReadinessWaitMS}, report.Unavailable)
		if err = server.running(); err != nil {
			return result, outputDir, err
		}
	}
	if _, err = newServingSampler(client); err != nil {
		return result, outputDir, err
	}
	for _, spec := range serving.Cases {
		options.log("Serving benchmark: " + spec.ID)
		if ctx.Err() != nil {
			return result, outputDir, ctx.Err()
		}
		summaryPath := filepath.Join(outputDir, spec.ID+".summary.json")

		if server != nil {
			if err := server.running(); err != nil {
				return result, outputDir, err
			}
		}
		measured, err := client.runCase(ctx, spec, outputDir)
		report.Cases = append(report.Cases, measured)
		phase := "final"
		if err != nil {
			phase = "failed"
		}
		values := servingValues(measured)
		values["target_requests"], values["target_duration_seconds"] = float64(spec.Requests), spec.DurationSeconds
		if serving.Server != nil {
			values["threads"] = float64(serving.Server.Threads)
		}
		options.measure(spec.ID, phase, values, measured.Resources.Unavailable)
		if err != nil {
			return result, outputDir, fmt.Errorf("serving case %s: %w", spec.ID, err)
		}
		if server != nil {
			if err := server.running(); err != nil {
				return result, outputDir, err
			}
		}
		if err = platform.WriteJSON(summaryPath, measured, 0600); err != nil {
			return result, outputDir, err
		}
	}
	return result, outputDir, nil
}

type servingLog struct {
	file *os.File
	size int
}

func newServingLog(path string) (*servingLog, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	return &servingLog{file: f}, nil
}
func (w *servingLog) write(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if w.size+len(data)+1 > 128<<20 {
		return errors.New("serving evidence file reached 128 MiB; use shorter cases")
	}
	_, err = w.file.Write(append(data, '\n'))
	w.size += len(data) + 1
	return err
}
func (w *servingLog) close() error {
	syncErr := w.file.Sync()
	return errors.Join(syncErr, w.file.Close())
}

func (client *servingClient) runCase(parent context.Context, spec servingCase, dir string) (summary servingCaseResult, resultErr error) {
	observer := Options{Observe: client.observe}
	observer.measure(spec.ID, "warming-up", map[string]float64{"concurrency": float64(spec.Concurrency), "target_prompt_tokens": float64(spec.PromptTokens), "max_output_tokens": float64(spec.OutputTokens), "target_requests": float64(spec.Requests), "target_duration_seconds": spec.DurationSeconds}, nil)
	summary = servingCaseResult{ID: spec.ID, Scenario: spec.Scenario, Concurrency: spec.Concurrency, StartedAt: time.Now().UTC(), Warnings: []string{}}
	if err := waitServing(parent, seconds(spec.IdleBeforeSeconds)); err != nil {
		return summary, err
	}
	prompts := spec.Prompts
	var tokens []int
	if spec.PromptTokens > 0 {
		p, ids, err := client.synthetic(parent, spec.PromptTokens)
		if err != nil {
			return summary, err
		}
		prompts, tokens = []servingPrompt{p}, ids
	}
	if len(prompts) == 0 {
		prompts = []servingPrompt{{ID: "default", Text: "Explain in a few sentences how a computer processes information."}}
	}
	warmup, err := newServingLog(filepath.Join(dir, spec.ID+".warmup.jsonl"))
	if err != nil {
		return summary, err
	}
	warmSpec := spec
	warmSpec.CancelAfterMS = 0
	warmupFailures := 0
	for i := 0; i < spec.WarmupRequests; i++ {
		r := client.complete(parent, warmSpec, prompts[i%len(prompts)], tokens, nil)
		if r.Status != "successful" {
			warmupFailures++
		}
		observer.measure(spec.ID, "warming-up", map[string]float64{"warmup_completed": float64(i + 1), "warmup_total": float64(spec.WarmupRequests), "warmup_errors": float64(warmupFailures), "warmup_latency_ms": r.LatencyMS, "concurrency": float64(spec.Concurrency), "target_prompt_tokens": float64(spec.PromptTokens)}, nil)
		if err = warmup.write(r); err != nil {
			_ = warmup.close()
			return summary, err
		}
		if parent.Err() != nil {
			_ = warmup.close()
			return summary, parent.Err()
		}
	}
	if err = warmup.close(); err != nil {
		return summary, err
	}
	requests, err := newServingLog(filepath.Join(dir, spec.ID+".requests.jsonl"))
	if err != nil {
		return summary, err
	}
	defer func() { resultErr = errors.Join(resultErr, requests.close()) }()
	resources, err := newServingLog(filepath.Join(dir, spec.ID+".resources.jsonl"))
	if err != nil {
		return summary, err
	}
	defer func() { resultErr = errors.Join(resultErr, resources.close()) }()
	sampler, err := newServingSampler(client)
	if err != nil {
		return summary, err
	}
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	var samples []servingSample
	var sampleErr error
	initial := sampler.sample(ctx)
	samples = append(samples, initial)
	if err = resources.write(initial); err != nil {
		return summary, err
	}
	started := time.Now()
	startedUTC := started.UTC()
	var records []servingRequest
	var mu sync.Mutex
	var writeErr error
	var eventCount int
	lastPublished := time.Time{}
	publish := func() {
		if client.observe == nil {
			return
		}
		mu.Lock()
		partial := summarizeServingCase(spec, client.plan, records, startedUTC, time.Since(started).Seconds(), time.Since(started).Seconds())
		mu.Unlock()
		output := partial.OutputTokens
		if partial.TokenAccounted != partial.Successful {
			output = 0
		}
		partial.Resources = summarizeServingResources(samples, partial.Successful, output)
		values := servingValues(partial)
		for key, value := range samples[len(samples)-1].Values {
			values["resource_current_"+key] = value
		}
		values["target_requests"], values["target_duration_seconds"] = float64(spec.Requests), spec.DurationSeconds
		observer.measure(spec.ID, "measuring", values, partial.Resources.Unavailable)
		lastPublished = time.Now()
	}
	publish()
	stopSampling := make(chan struct{})
	samplingDone := make(chan struct{})
	go func() {
		defer close(samplingDone)
		capture := func() {
			sample := sampler.sample(ctx)
			samples = append(samples, sample)
			if e := resources.write(sample); e != nil {
				sampleErr = e
				cancel(e)
			}
			if time.Since(lastPublished) >= 5*time.Second {
				publish()
			}
		}
		ticker := time.NewTicker(seconds(client.plan.SampleInterval))
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				capture()
			case <-stopSampling:
				capture()
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	record := func(r servingRequest) {
		mu.Lock()
		defer mu.Unlock()
		eventCount += len(r.ChunkTimesMS) + len(r.TokenTimesMS)
		if eventCount > 1000000 {
			writeErr = errors.New("serving case reached one million timing observations; use shorter cases")
			cancel(writeErr)
			return
		}
		if e := requests.write(r); e != nil {
			writeErr = e
			cancel(e)
			return
		}
		r.content, r.Output, r.ToolCalls = "", "", nil
		records = append(records, r)
	}
	limit := spec.Requests
	if limit == 0 {
		limit = 100000
	}
	var next atomic.Int64
	var workers sync.WaitGroup
	var dispatchEnd time.Time
	if spec.RequestRate == 0 {
		for worker := 0; worker < spec.Concurrency; worker++ {
			workers.Add(1)
			go func(worker int) {
				defer workers.Done()
				var history []servingMessage
				turn := 0
				for {
					if ctx.Err() != nil || spec.DurationSeconds > 0 && time.Since(started).Seconds() >= spec.DurationSeconds {
						return
					}
					index := int(next.Add(1)) - 1
					if index >= limit {
						return
					}
					p := prompts[index%len(prompts)]
					if spec.ContinueConversation {
						if turn%len(prompts) == 0 {
							history = nil
						}
						p = prompts[turn%len(prompts)]
					}
					r := client.complete(ctx, spec, p, tokens, history)
					r.Index, r.Worker = index, worker
					if spec.ContinueConversation && r.Status == "successful" {
						if len(p.Messages) > 0 {
							history = append(history, p.Messages...)
						} else {
							history = append(history, servingMessage{Role: "user", Content: p.Text})
						}
						history = append(history, servingMessage{Role: "assistant", Content: r.content})
					}
					turn++
					record(r)
				}
			}(worker)
		}
		workers.Wait()
		dispatchEnd = time.Now()
	} else {
		slots := make(chan int, spec.Concurrency)
		for i := 0; i < spec.Concurrency; i++ {
			slots <- i
		}
		for index := 0; index < limit && ctx.Err() == nil; index++ {
			planned := started.Add(seconds(float64(index) / spec.RequestRate))
			if spec.DurationSeconds > 0 && planned.Sub(started).Seconds() >= spec.DurationSeconds {
				break
			}
			if err := waitServing(ctx, max(0, time.Until(planned))); err != nil {
				break
			}
			select {
			case worker := <-slots:
				workers.Add(1)
				go func(index, worker int, planned time.Time) {
					defer workers.Done()
					defer func() { slots <- worker }()
					r := client.complete(ctx, spec, prompts[index%len(prompts)], tokens, nil)
					r.Index, r.Worker = index, worker
					r.DispatchDelayMS = max(0, milliseconds(r.StartedAt.Sub(planned)))
					record(r)
				}(index, worker, planned)
			default:
				now := time.Now().UTC()
				record(servingRequest{Index: index, Status: "client_capacity", StartedAt: now, CompletedAt: now, DispatchDelayMS: max(0, milliseconds(now.Sub(planned)))})
			}
		}
		dispatchEnd = time.Now()
		workers.Wait()
	}
	if spec.RequestRate == 0 {
		dispatchEnd = started
		for _, r := range records {
			if r.StartedAt.After(dispatchEnd) {
				dispatchEnd = r.StartedAt
			}
		}
	}
	elapsed := time.Since(started).Seconds()
	close(stopSampling)
	<-samplingDone
	summary = summarizeServingCase(spec, client.plan, records, startedUTC, elapsed, dispatchEnd.Sub(started).Seconds())
	if warmupFailures > 0 {
		summary.Warnings = append(summary.Warnings, fmt.Sprintf("%d warmup requests failed; inspect warmup evidence before assuming a warmed endpoint.", warmupFailures))
	}
	accountedOutput := summary.OutputTokens
	if summary.TokenAccounted != summary.Successful {
		accountedOutput = 0
	}
	summary.Resources = summarizeServingResources(samples, summary.Successful, accountedOutput)
	if client.plan.LocalPID > 0 {
		summary.Resources.Scope += fmt.Sprintf("; operator-associated local PID %d, start ticks %s", sampler.pid, sampler.identity)
	}
	summary.Warnings = append(summary.Warnings, "Resource observation windows include collection overhead; package/GPU energy is node-wide and not idle-subtracted.")
	if spec.RecoveryProbe && parent.Err() == nil {
		probeSpec := spec
		probeSpec.CancelAfterMS, probeSpec.OutputTokens = 0, min(8, spec.OutputTokens)
		probe := client.complete(parent, probeSpec, servingPrompt{ID: "post-load-probe", Text: "Say hello."}, nil, nil)
		summary.RecoveryProbe = &servingProbe{Successful: probe.Status == "successful", LatencyMS: probe.LatencyMS, Error: probe.Error}
		if err = requests.write(map[string]any{"recovery_probe": probe}); err != nil {
			resultErr = err
		}
	}
	return summary, errors.Join(resultErr, context.Cause(ctx), writeErr, sampleErr)
}

func distribution(values []float64) *distributionStats {
	if len(values) == 0 {
		return nil
	}
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	d := &distributionStats{Count: len(values), Min: ordered[0], Max: ordered[len(ordered)-1]}
	for _, v := range values {
		d.Mean += v / float64(len(values))
	}
	if len(values) > 1 {
		for _, v := range values {
			d.Stddev += (v - d.Mean) * (v - d.Mean) / float64(len(values)-1)
		}
		d.Stddev = math.Sqrt(d.Stddev)
	}
	percentile := func(p float64) float64 { return ordered[max(0, int(math.Ceil(p*float64(len(ordered))))-1)] }
	d.P50, d.P90, d.P95, d.P99 = percentile(.5), percentile(.9), percentile(.95), percentile(.99)
	return d
}

func summarizeServingCase(spec servingCase, plan servingPlan, records []servingRequest, started time.Time, elapsed, dispatch float64) servingCaseResult {
	r := servingCaseResult{ID: spec.ID, Scenario: spec.Scenario, Concurrency: spec.Concurrency, TargetPromptTokens: spec.PromptTokens, MaxOutputTokens: spec.OutputTokens, ServedModel: spec.Model, CachePrompt: spec.CachePrompt, StartedAt: started, ElapsedSeconds: elapsed, DispatchSeconds: dispatch, Offered: len(records), Warnings: []string{}}
	if r.ServedModel == "" {
		r.ServedModel = plan.ServedModel
	}
	if plan.API == "openai" {
		r.Warnings = append(r.Warnings, "Prompt-cache policy is controlled by the endpoint; the client does not disable it.")
	}
	var latency, ttft, answer, tpot, gaps, tokenGaps, stalls, lag, scheduled, cancelAck []float64
	firstAt := time.Time{}
	windowRecords := map[int][]servingRequest{}
	models := map[string]bool{}
	windowSeconds := max(10.0, math.Ceil(elapsed/64))
	targets := plan.Targets.TTFTMS > 0 || plan.Targets.TPOTMS > 0 || plan.Targets.LatencyMS > 0
	for _, v := range records {
		lag = append(lag, v.DispatchDelayMS)
		if v.Status == "client_capacity" {
			r.ClientCapacityDrops++
			continue
		}
		r.Dispatched++
		if firstAt.IsZero() || v.StartedAt.Before(firstAt) {
			firstAt = v.StartedAt
			r.FirstRequestMS = floatPointer(v.LatencyMS)
		}
		if v.QualityPassed != nil {
			r.QualityEvaluated++
			if *v.QualityPassed {
				r.QualityPassed++
			}
		}
		switch v.Status {
		case "successful":
			r.Successful++
		case "timeout":
			r.Timeouts++
			r.Errors++
		case "cancelled":
			r.Cancelled++
		case "incomplete":
			r.Incomplete++
			r.Errors++
		default:
			r.Errors++
		}
		if v.CancelAckMS != nil {
			cancelAck = append(cancelAck, *v.CancelAckMS)
		}
		if v.Status != "successful" {
			continue
		}
		if v.Model != "" {
			models[v.Model] = true
		}
		latency = append(latency, v.LatencyMS)
		scheduled = append(scheduled, v.LatencyMS+v.DispatchDelayMS)
		if v.TTFTMS != nil {
			ttft = append(ttft, *v.TTFTMS)
		}
		if v.FirstAnswerMS != nil {
			answer = append(answer, *v.FirstAnswerMS)
		}
		if v.TPOTMS != nil {
			tpot = append(tpot, *v.TPOTMS)
		}
		longest := 0.0
		for i := 1; i < len(v.ChunkTimesMS); i++ {
			gap := v.ChunkTimesMS[i] - v.ChunkTimesMS[i-1]
			gaps = append(gaps, gap)
			longest = max(longest, gap)
		}
		if len(v.ChunkTimesMS) > 1 {
			stalls = append(stalls, longest)
		}
		for i := 1; i < len(v.TokenTimesMS); i++ {
			tokenGaps = append(tokenGaps, v.TokenTimesMS[i]-v.TokenTimesMS[i-1])
		}
		if v.InputTokens != nil && v.OutputTokens != nil {
			r.TokenAccounted++
			r.InputTokens += *v.InputTokens
			r.OutputTokens += *v.OutputTokens
		}
		if v.CacheAccounted {
			r.CacheAccounted++
		}
		r.CachedTokens += v.CachedTokens
		if v.CachedTokens > 0 {
			r.CacheHitRequests++
		}
		if v.FinishReason == "length" || v.FinishReason == "limit" {
			r.OutputLimited++
		}
		if v.Truncated {
			r.ContextTruncated++
		}
		if targets {
			known := !(plan.Targets.TTFTMS > 0 && v.TTFTMS == nil || plan.Targets.TPOTMS > 0 && v.TPOTMS == nil)
			if known {
				r.SLOEvaluated++
				if (plan.Targets.TTFTMS == 0 || *v.TTFTMS <= plan.Targets.TTFTMS) && (plan.Targets.TPOTMS == 0 || *v.TPOTMS <= plan.Targets.TPOTMS) && (plan.Targets.LatencyMS == 0 || v.LatencyMS <= plan.Targets.LatencyMS) && (v.QualityPassed == nil || *v.QualityPassed) {
					r.SLOMet++
				}
			}
		}
		bucket := max(0, int(v.CompletedAt.Sub(started).Seconds()/windowSeconds))
		windowRecords[bucket] = append(windowRecords[bucket], v)
	}
	if elapsed > 0 {
		r.RequestsPerSecond = float64(r.Successful) / elapsed
	}
	if targets && r.SLOEvaluated == r.Successful && r.Dispatched > 0 {
		r.Goodput = floatPointer(float64(r.SLOMet) / elapsed)
	}
	if r.TokenAccounted == r.Successful && r.Successful > 0 {
		r.InputTokensPerSecond = floatPointer(float64(r.InputTokens) / elapsed)
		r.OutputTokensPerSecond = floatPointer(float64(r.OutputTokens) / elapsed)
	} else {
		r.Warnings = append(r.Warnings, "Aggregate token rates unavailable unless every successful request reports token usage.")
	}
	if r.QualityEvaluated > 0 {
		r.QualityPassRate = floatPointer(float64(r.QualityPassed) / float64(r.QualityEvaluated))
	}
	r.LatencyMS, r.TTFTMS, r.FirstAnswerMS, r.TPOTMS = distribution(latency), distribution(ttft), distribution(answer), distribution(tpot)
	r.ChunkGapMS, r.ITLMS, r.LongestStallMS = distribution(gaps), distribution(tokenGaps), distribution(stalls)
	r.DispatchDelayMS, r.ScheduledLatencyMS, r.CancelAckMS = distribution(lag), distribution(scheduled), distribution(cancelAck)
	if r.Successful < 100 {
		r.Warnings = append(r.Warnings, "Fewer than 100 successful requests: tail percentiles are descriptive order statistics, not reliable population p99 estimates.")
	}
	for model := range models {
		r.ObservedModels = append(r.ObservedModels, model)
	}
	sort.Strings(r.ObservedModels)
	if len(models) == 0 {
		r.Warnings = append(r.Warnings, "Endpoint did not report a model identity; requested alias is not an attestation.")
	} else if len(models) > 1 || r.ServedModel != "" && !models[r.ServedModel] {
		r.Warnings = append(r.Warnings, "Observed model aliases differ from the requested alias; inspect raw responses before interpreting model-selection results.")
	}
	if r.ClientCapacityDrops > 0 {
		r.Warnings = append(r.Warnings, "Arrival-rate requests exceeded the client's concurrency cap; client drops are not server rejections.")
	}
	if spec.CancelAfterMS > 0 && r.Cancelled == 0 {
		r.Warnings = append(r.Warnings, "Cancellation probe did not cancel an in-flight response; it may have completed before the cancellation deadline.")
	}
	if r.ITLMS == nil {
		r.Warnings = append(r.Warnings, "Individual token boundaries unavailable; streaming chunk gaps are reported separately.")
	}
	if r.TokenAccounted != r.Successful && targets && plan.Targets.TPOTMS > 0 {
		r.Warnings = append(r.Warnings, "TPOT target cannot be evaluated without output-token accounting.")
	}
	for bucket := 0; bucket <= int(elapsed/windowSeconds); bucket++ {
		list := windowRecords[bucket]
		if len(list) == 0 {
			continue
		}
		w := servingWindow{StartSeconds: float64(bucket) * windowSeconds, EndSeconds: min(elapsed, float64(bucket+1)*windowSeconds), Successful: len(list)}
		var ttfts []float64
		count, known := 0, true
		for _, v := range list {
			if v.TTFTMS != nil {
				ttfts = append(ttfts, *v.TTFTMS)
			}
			if v.OutputTokens != nil {
				count += *v.OutputTokens
			} else {
				known = false
			}
		}
		if d := distribution(ttfts); d != nil {
			w.TTFTMS = floatPointer(d.Mean)
		}
		if known && w.EndSeconds > w.StartSeconds {
			w.OutputRate = floatPointer(float64(count) / (w.EndSeconds - w.StartSeconds))
		}
		r.Windows = append(r.Windows, w)
	}
	return r
}

// Expose the latest completion window alongside the cumulative case summary.
// Earlier windows remain in raw evidence; no per-request or per-window labels.
func servingValues(summary servingCaseResult) map[string]float64 {
	values := numericValues(summary)
	if len(summary.Windows) > 0 {
		for key, value := range numericValues(summary.Windows[len(summary.Windows)-1]) {
			values["latest_window_"+key] = value
		}
	}
	return values
}
