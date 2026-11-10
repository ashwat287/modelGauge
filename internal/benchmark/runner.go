// Package benchmark executes a versioned ModelGauge plan locally.
package benchmark

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/nerdylucifer/modelgauge/internal/modelcache"
	"github.com/nerdylucifer/modelgauge/internal/platform"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

const resultSchemaV1 = "benchmark-result/v1"

type HostSnapshot struct {
	CapturedAt time.Time     `json:"captured_at"`
	Host       platform.Host `json:"host"`
	Swap       string        `json:"swap,omitempty"`
}

type llamaBenchResult struct {
	Threads    int       `json:"n_threads"`
	Prompt     int       `json:"n_prompt"`
	Generation int       `json:"n_gen"`
	AverageTS  float64   `json:"avg_ts"`
	StddevTS   float64   `json:"stddev_ts"`
	SamplesTS  []float64 `json:"samples_ts"`
}

// Options controls local artifact naming.
type Options struct {
	Resume    bool
	RunID     string
	OutputDir string
}

// Run executes plan and stores a complete evidence bundle in outputDir.
func Run(ctx context.Context, plan protocol.Plan, outputDir string) (protocol.Result, string, error) {
	return RunWithOptions(ctx, plan, Options{OutputDir: outputDir})
}

// runEnginePlan executes the legacy llama-bench workload.
func runEnginePlan(ctx context.Context, plan protocol.Plan, options Options) (protocol.Result, string, error) {
	if runtime.GOOS != "linux" {
		return protocol.Result{}, "", fmt.Errorf("benchmark execution requires Linux; on a Mac, run the agent inside a Lima Linux VM")
	}
	unlock, err := platform.Lock("execution")
	if err != nil {
		return protocol.Result{}, "", err
	}
	defer unlock()
	if err := validateEnginePlan(plan); err != nil {
		return protocol.Result{}, "", fmt.Errorf("validate plan: %w", err)
	}

	binary, err := resolveBinary(plan.Runtime.Binary)
	if err != nil {
		return protocol.Result{}, "", err
	}
	if err := verifyModelContext(ctx, plan.Model); err != nil {
		return protocol.Result{}, "", err
	}

	if plan.PlanID == "" {
		plan.PlanID = "local-" + time.Now().UTC().Format("20060102T150405Z")
	}
	runID := options.RunID
	if runID == "" {
		runID = "run-" + plan.PlanID + "-" + time.Now().UTC().Format("20060102T150405Z")
	}
	outputDir := options.OutputDir
	if outputDir == "" {
		outputDir = filepath.Join("results", runID)
	}
	if err := prepareOutputDir(outputDir, options.Resume); err != nil {
		return protocol.Result{}, outputDir, err
	}

	planPath := filepath.Join(outputDir, "plan.json")
	recovered := false
	if options.Resume {
		if data, err := os.ReadFile(planPath); err == nil {
			recovered = true
			var previous protocol.Plan
			if err = json.Unmarshal(data, &previous); err != nil {
				return protocol.Result{}, outputDir, err
			}
			if !sameEngineResumePlan(previous, plan, binary) {
				return protocol.Result{}, outputDir, fmt.Errorf("resume plan does not match saved plan")
			}
		} else if !os.IsNotExist(err) {
			return protocol.Result{}, outputDir, err
		} else if entries, e := os.ReadDir(outputDir); e != nil || len(entries) != 0 {
			return protocol.Result{}, outputDir, fmt.Errorf("refusing to resume an unrecognized nonempty output directory")
		}
	}
	binaryHash, err := sha256FileContext(ctx, binary)
	if err != nil {
		return protocol.Result{}, outputDir, err
	}
	hashPath := filepath.Join(outputDir, "runtime.sha256")
	if options.Resume {
		if data, err := os.ReadFile(hashPath); err == nil && string(data) != binaryHash {
			return protocol.Result{}, outputDir, fmt.Errorf("runtime changed since interrupted run")
		}
	}
	if err := writeJSON(planPath, plan); err != nil {
		return protocol.Result{}, outputDir, err
	}
	if err = os.WriteFile(hashPath, []byte(binaryHash), 0640); err != nil {
		return protocol.Result{}, outputDir, err
	}
	preflight, err := snapshot()
	if err != nil {
		return protocol.Result{}, outputDir, err
	}
	if options.Resume {
		if data, err := os.ReadFile(filepath.Join(outputDir, "preflight.json")); err == nil {
			_ = json.Unmarshal(data, &preflight)
		}
	}
	if err := writeJSON(filepath.Join(outputDir, "preflight.json"), preflight); err != nil {
		return protocol.Result{}, outputDir, err
	}

	startedAt := preflight.CapturedAt
	cases := make([]protocol.CaseResult, 0, len(plan.Cases))
	for _, benchmarkCase := range plan.Cases {
		measuredPath := filepath.Join(outputDir, benchmarkCase.ID+".json")
		parsed, parseErr := parseLlamaBench(measuredPath)
		cached := options.Resume && parseErr == nil && parsed.Threads == benchmarkCase.Threads && parsed.Prompt == benchmarkCase.PromptTokens && parsed.Generation == benchmarkCase.GenerationTokens && len(parsed.SamplesTS) == plan.Defaults.MeasureRepetitions
		if !cached {
			warmupPath := filepath.Join(outputDir, benchmarkCase.ID+"-warmup.json")
			if err := runLlamaBench(ctx, binary, plan, benchmarkCase.Threads, benchmarkCase.PromptTokens, benchmarkCase.GenerationTokens, plan.Defaults.WarmupRepetitions, warmupPath); err != nil {
				return protocol.Result{}, outputDir, fmt.Errorf("warm up %s: %w", benchmarkCase.ID, err)
			}

			if err := runLlamaBench(ctx, binary, plan, benchmarkCase.Threads, benchmarkCase.PromptTokens, benchmarkCase.GenerationTokens, plan.Defaults.MeasureRepetitions, measuredPath); err != nil {
				return protocol.Result{}, outputDir, fmt.Errorf("measure %s: %w", benchmarkCase.ID, err)
			}
			parsed, err = parseLlamaBench(measuredPath)
			if err != nil {
				return protocol.Result{}, outputDir, fmt.Errorf("parse %s: %w", benchmarkCase.ID, err)
			}
		}
		cases = append(cases, protocol.CaseResult{
			CaseID:                benchmarkCase.ID,
			Threads:               parsed.Threads,
			PromptTokens:          parsed.Prompt,
			GenerationTokens:      parsed.Generation,
			Repetitions:           len(parsed.SamplesTS),
			TokensPerSecond:       parsed.AverageTS,
			TokensPerSecondStddev: parsed.StddevTS,
		})
	}

	var memory *protocol.MemoryResult
	checkpoint := filepath.Join(outputDir, "memory.checkpoint.json")
	if options.Resume {
		if data, err := os.ReadFile(checkpoint); err == nil {
			_ = json.Unmarshal(data, &memory)
		}
	}
	if memory == nil || memory.CaseID != plan.MemoryCase.ID || memory.PeakRSSBytes <= 0 {
		memory, err = runMemoryCase(ctx, binary, plan, outputDir)
		if err == nil {
			err = writeJSON(checkpoint, memory)
		}
	}
	if err != nil {
		return protocol.Result{}, outputDir, err
	}
	postflight, err := snapshot()
	if err != nil {
		return protocol.Result{}, outputDir, err
	}
	if err := writeJSON(filepath.Join(outputDir, "postflight.json"), postflight); err != nil {
		return protocol.Result{}, outputDir, err
	}

	result := protocol.Result{
		SchemaVersion: resultSchemaV1,
		RunID:         runID,
		PlanID:        plan.PlanID,
		NodeID:        plan.NodeID,
		ExecutionMode: plan.ExecutionMode,
		Status:        protocol.RunStatusCompleted,
		StartedAt:     startedAt,
		CompletedAt:   time.Now().UTC(),
		Cases:         cases,
		Memory:        memory,
		RuntimeMetadata: map[string]string{
			"recovered": fmt.Sprint(recovered),
			"binary":    binary,
			"goos":      runtime.GOOS,
			"goarch":    runtime.GOARCH,
		},
	}
	artifacts, err := collectArtifacts(outputDir)
	if err != nil {
		return protocol.Result{}, outputDir, err
	}
	result.Artifacts = artifacts
	if err := writeJSON(filepath.Join(outputDir, "result.json"), result); err != nil {
		return protocol.Result{}, outputDir, err
	}

	return result, outputDir, nil
}

func runLlamaBench(ctx context.Context, binary string, plan protocol.Plan, threads, prompt, generation, repetitions int, outputPath string) error {
	output, err := os.OpenFile(outputPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return fmt.Errorf("open output: %w", err)
	}
	defer output.Close()

	args := llamaArgs(plan, threads, prompt, generation, repetitions)
	cmd := commandContext(ctx, binary, args...)
	cmd.Stdout = output
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("llama-bench: %w: %s", errors.Join(err, ctx.Err()), strings.TrimSpace(stderr.String()))
	}
	return nil
}

func runMemoryCase(ctx context.Context, binary string, plan protocol.Plan, outputDir string) (*protocol.MemoryResult, error) {
	benchmarkCase := plan.MemoryCase
	outputPath := filepath.Join(outputDir, benchmarkCase.ID+".json")
	timePath := filepath.Join(outputDir, benchmarkCase.ID+".time.txt")
	output, err := os.OpenFile(outputPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, err
	}
	defer output.Close()

	args := llamaArgs(plan, benchmarkCase.Threads, benchmarkCase.PromptTokens, benchmarkCase.GenerationTokens, 1)
	timeArgs := append([]string{"-v", "-o", timePath, binary}, args...)
	cmd := commandContext(ctx, "/usr/bin/time", timeArgs...)
	cmd.Stdout = output

	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("memory case: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	raw, err := os.ReadFile(timePath)
	if err != nil {
		return nil, err
	}
	bytes, err := peakRSSBytes(string(raw))
	if err != nil {
		return nil, err
	}
	return &protocol.MemoryResult{
		CaseID:               benchmarkCase.ID,
		PeakRSSBytes:         bytes,
		SafeReservationBytes: ceilFraction(bytes, 125, 100),
	}, nil
}

// Kill the entire workload group, including the child of GNU time, before
// releasing execution ownership. Killing only the wrapper leaves an orphan.
func commandContext(ctx context.Context, binary string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

func llamaArgs(plan protocol.Plan, threads, prompt, generation, repetitions int) []string {
	return []string{
		"--model", plan.Model.Path,
		"--threads", fmt.Sprint(threads),
		"--n-gpu-layers", fmt.Sprint(plan.Defaults.GPULayers),
		"--repetitions", fmt.Sprint(repetitions),
		"--n-prompt", fmt.Sprint(prompt),
		"--n-gen", fmt.Sprint(generation),
		"--output", "json",
	}
}

func resolveBinary(configured string) (string, error) {
	if configured != "" {
		info, err := os.Stat(configured)
		if err != nil {
			return "", fmt.Errorf("benchmark binary: %w", err)
		}
		if info.IsDir() || info.Mode()&0o111 == 0 {
			return "", fmt.Errorf("benchmark binary %q is not executable", configured)
		}
		return configured, nil
	}
	binary, err := modelcache.DiscoverLlamaBench()
	if err != nil {
		return "", fmt.Errorf("find llama-bench: %w", err)
	}
	return binary, nil
}

func verifyModel(model protocol.Model) error { return verifyModelContext(context.Background(), model) }
func verifyModelContext(ctx context.Context, model protocol.Model) error {
	if _, err := os.Stat(model.Path); err != nil {
		return fmt.Errorf("model %q: %w", model.Path, err)
	}
	if model.SHA256 == "" {
		return nil
	}
	digest, err := sha256FileContext(ctx, model.Path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(digest, model.SHA256) {
		return fmt.Errorf("model SHA-256 mismatch: got %s", digest)
	}
	return nil
}

func parseLlamaBench(path string) (llamaBenchResult, error) {
	file, err := os.Open(path)
	if err != nil {
		return llamaBenchResult{}, err
	}
	defer file.Close()
	var rows []llamaBenchResult
	if err := json.NewDecoder(file).Decode(&rows); err != nil {
		return llamaBenchResult{}, err
	}
	if len(rows) != 1 {
		return llamaBenchResult{}, fmt.Errorf("expected one llama-bench result, got %d", len(rows))
	}
	return rows[0], nil
}

func snapshot() (HostSnapshot, error) {
	host, err := platform.Inspect()
	if err != nil {
		return HostSnapshot{}, err
	}
	return HostSnapshot{CapturedAt: time.Now().UTC(), Host: host, Swap: swapStatus()}, nil
}

func swapStatus() string {
	if runtime.GOOS == "linux" {
		output, err := exec.Command("swapon", "--show", "--noheadings").Output()
		if err == nil {
			return strings.TrimSpace(string(output))
		}
	}
	return "unavailable"
}

func peakRSSBytes(raw string) (int64, error) {
	re := regexp.MustCompile(`(?m)^\s*Maximum resident set size \(kbytes\):\s*(\d+)`)
	match := re.FindStringSubmatch(raw)
	if len(match) != 2 {
		return 0, fmt.Errorf("maximum resident set size not found")
	}
	var kib int64
	if _, err := fmt.Sscan(match[1], &kib); err != nil {
		return 0, err
	}
	return kib * 1024, nil
}

func ceilFraction(value, numerator, denominator int64) int64 {
	return (value*numerator + denominator - 1) / denominator
}

func writeJSON(path string, value any) error {
	return platform.WriteJSON(path, value, 0640)
}

func sha256File(path string) (string, error) { return sha256FileContext(context.Background(), path) }
func sha256FileContext(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, contextReader{ctx, file}); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func collectArtifacts(root string) ([]protocol.Artifact, error) {
	var artifacts []protocol.Artifact
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || entry.Name() == "result.json" {
			return nil
		}
		digest, err := sha256File(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		artifacts = append(artifacts, protocol.Artifact{Name: relative, SHA256: digest})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Name < artifacts[j].Name })
	return artifacts, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func prepareOutputDir(path string, resume bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return err
	}
	err := os.Mkdir(path, 0750)
	if err != nil && !(resume && os.IsExist(err)) {
		return fmt.Errorf("output must be a new directory unless resuming: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("output must be a real directory")
	}
	return nil
}
