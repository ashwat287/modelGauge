package benchmark

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nerdylucifer/modelgauge/internal/platform"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

// suitePlan contains ordered module-owned plans for one model on one node.
type suitePlan struct {
	Stages []protocol.Plan `json:"stages"`
}
type suiteReport struct {
	Stages []protocol.Result `json:"stages"`
}

func suiteImplementation() implementation {
	return implementation{profile: "deployment", plan: deploymentProfile, validate: validateSuite, run: runSuite,
		validateReport: func(raw json.RawMessage) error {
			var report suiteReport
			if err := json.Unmarshal(raw, &report); err != nil {
				return err
			}
			if len(report.Stages) > 32 {
				return errors.New("too many stage results")
			}
			for _, r := range report.Stages {
				if r.Benchmark != nil && r.Benchmark.Kind == "suite" {
					return errors.New("nested suite result")
				}
				if err := ValidateResult(r); err != nil {
					return err
				}
			}
			return nil
		},
		csv: func(w io.Writer, rows []protocol.Result) error {
			writer := csv.NewWriter(w)
			_ = writer.Write([]string{"run_id", "stage", "kind", "status", "result_json"})
			for _, r := range rows {
				var report suiteReport
				if err := json.Unmarshal(r.Benchmark.Data, &report); err != nil {
					return err
				}
				for i, s := range report.Stages {
					data, err := json.Marshal(s)
					if err != nil {
						return err
					}
					if err = writer.Write([]string{r.RunID, fmt.Sprint(i + 1), resultKind(s), string(s.Status), string(data)}); err != nil {
						return err
					}
				}
			}
			writer.Flush()
			return writer.Error()
		},
		metrics: func(out *strings.Builder, node, mode, model string, raw json.RawMessage) error {
			var report suiteReport
			if err := json.Unmarshal(raw, &report); err != nil {
				return err
			}
			for i, s := range report.Stages {
				var stage strings.Builder
				if err := WriteMetrics(&stage, node, mode, model, s.Benchmark); err != nil {
					return err
				}
				for _, line := range strings.Split(stage.String(), "\n") {
					if line == "" {
						continue
					}
					if strings.HasPrefix(line, "#") {
						if !strings.Contains(out.String(), line+"\n") {
							out.WriteString(line + "\n")
						}
						continue
					}
					line = strings.Replace(line, "{", fmt.Sprintf("{stage=%q,", fmt.Sprint(i+1)), 1)
					out.WriteString(line + "\n")
				}
			}
			return nil
		}}
}
func resultKind(r protocol.Result) string {
	if r.Benchmark != nil {
		return r.Benchmark.Kind
	}
	return "llama.cpp"
}
func deploymentProfile(ctx context.Context, o planOptions, config json.RawMessage) ([]protocol.Plan, error) {
	engine, err := standardCPUPlan(ctx, o)
	if err != nil {
		return nil, err
	}
	// The serving module owns its configuration; the suite passes it through.
	o.ModelID, o.ModelSHA256 = engine.Model.ID, engine.Model.SHA256
	o.Binary = ""     // --binary selects the direct runtime; the serving module discovers its sibling.
	o.Repetitions = 0 // Serving request counts belong to its configuration.
	serving, err := servingProfile(ctx, o, config)
	if err != nil {
		return nil, err
	}
	outer := engine
	outer.Runtime = protocol.Runtime{Kind: "suite"}
	outer.Cases = nil
	outer.MemoryCase = protocol.MemoryCase{}
	err = setConfig(&outer, "suite", suitePlan{Stages: append([]protocol.Plan{engine}, serving...)})
	return []protocol.Plan{outer}, err
}
func decodeSuite(p protocol.Plan) (suitePlan, error) {
	var s suitePlan
	d := json.NewDecoder(bytes.NewReader(p.Benchmark.Config))
	d.DisallowUnknownFields()
	err := d.Decode(&s)
	if err == nil && d.Decode(new(any)) != io.EOF {
		err = errors.New("invalid suite JSON")
	}
	return s, err
}
func validateSuite(p protocol.Plan) error {
	s, err := decodeSuite(p)
	if err != nil {
		return err
	}
	if p.Runtime.Kind != "suite" || len(s.Stages) < 1 || len(s.Stages) > 32 {
		return errors.New("suite requires 1..32 stages and suite runtime")
	}
	for _, stage := range s.Stages {
		if planKind(stage) == "suite" {
			return errors.New("nested suites are not supported")
		}
		if stage.NodeID != p.NodeID || stage.ExecutionMode != p.ExecutionMode || stage.Model != p.Model {
			return errors.New("suite stages must use the same node, model, checksum and execution mode")
		}
		if err := ValidatePlan(stage); err != nil {
			return err
		}
	}
	return nil
}
func ensureNodeIdle(ctx context.Context) error {
	// Never kill an unowned model process to make room for a benchmark.
	cmd := exec.CommandContext(ctx, "pgrep", "-x", "llama-bench|llama-server")
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check benchmark node activity: %w", err)
	}
	return errors.New("benchmark node is busy: llama-bench or llama-server is already running")
}
func runSuite(ctx context.Context, p protocol.Plan, o Options) (result protocol.Result, dir string, runErr error) {
	suite, err := decodeSuite(p)
	if err != nil {
		return result, "", err
	}
	if o.RunID == "" {
		o.RunID = "run-" + time.Now().UTC().Format("20060102T150405.000000000")
	}
	dir = o.OutputDir
	if dir == "" {
		dir = filepath.Join("results", o.RunID)
	}
	if err = prepareOutputDir(dir, o.Resume); err != nil {
		return result, dir, err
	}
	planFile := filepath.Join(dir, "plan.json")
	if data, e := os.ReadFile(planFile); e == nil {
		var previous protocol.Plan
		if json.Unmarshal(data, &previous) != nil {
			return result, dir, errors.New("invalid saved suite plan")
		}
		before, _ := json.Marshal(previous)
		after, _ := json.Marshal(p)
		if !bytes.Equal(before, after) {
			return result, dir, errors.New("suite resume plan differs")
		}
	} else if !os.IsNotExist(e) {
		return result, dir, e
	} else if entries, e := os.ReadDir(dir); e != nil || len(entries) != 0 {
		return result, dir, errors.New("unrecognized suite output directory")
	}
	if err = platform.WriteJSON(planFile, p, 0600); err != nil {
		return result, dir, err
	}
	result = protocol.Result{SchemaVersion: resultSchemaV1, RunID: o.RunID, PlanID: p.PlanID, NodeID: p.NodeID, ExecutionMode: p.ExecutionMode, StartedAt: time.Now().UTC(), Status: protocol.RunStatusCompleted}
	report := suiteReport{Stages: []protocol.Result{}}
	defer func() {
		result.CompletedAt = time.Now().UTC()
		if runErr != nil {
			result.Status = protocol.RunStatusFailed
			result.InvalidReason = runErr.Error()
			if ctx.Err() != nil {
				result.Status = protocol.RunStatusCancelled
			}
		}
		data, e := json.Marshal(report)
		runErr = errors.Join(runErr, e)
		result.Benchmark = &protocol.BenchmarkReport{Kind: "suite", Data: data}
		result.Artifacts, e = collectArtifacts(dir)
		runErr = errors.Join(runErr, e)
		if runErr != nil && result.Status == protocol.RunStatusCompleted {
			result.Status = protocol.RunStatusFailed
			result.InvalidReason = runErr.Error()
		}
		name := "result.json"
		if ctx.Err() != nil {
			name = "interrupted-result.json"
		}
		runErr = errors.Join(runErr, platform.WriteJSON(filepath.Join(dir, name), result, 0600))
	}()
	for i, stage := range suite.Stages {
		if err = ctx.Err(); err != nil {
			return result, dir, err
		}
		if err = ensureNodeIdle(ctx); err != nil {
			return result, dir, err
		}
		stage.PlanID = p.PlanID
		stageOptions := Options{RunID: fmt.Sprintf("%s-stage-%d", o.RunID, i+1), OutputDir: filepath.Join(dir, fmt.Sprintf("stage-%02d", i+1)), Resume: o.Resume}
		var measured protocol.Result
		if o.Resume {
			if data, e := os.ReadFile(filepath.Join(stageOptions.OutputDir, "result.json")); e == nil {
				if e = json.Unmarshal(data, &measured); e != nil {
					return result, dir, e
				}
				if measured.RunID != stageOptions.RunID || measured.PlanID != stage.PlanID || measured.NodeID != p.NodeID || measured.ExecutionMode != p.ExecutionMode {
					return result, dir, errors.New("saved stage belongs to another run")
				}
			} else if !os.IsNotExist(e) {
				return result, dir, e
			}
		}
		if measured.SchemaVersion == "" {
			measured, _, err = implementations[planKind(stage)].run(ctx, stage, stageOptions)
		}
		report.Stages = append(report.Stages, measured)
		if err != nil {
			return result, dir, fmt.Errorf("stage %d: %w", i+1, err)
		}
		if measured.Status != protocol.RunStatusCompleted {
			return result, dir, fmt.Errorf("stage %d ended %s: %s", i+1, measured.Status, measured.InvalidReason)
		}
		for _, c := range measured.Cases {
			c.CaseID = fmt.Sprintf("stage-%d-%s", i+1, c.CaseID)
			result.Cases = append(result.Cases, c)
		}
		if measured.Memory != nil && (result.Memory == nil || measured.Memory.SafeReservationBytes > result.Memory.SafeReservationBytes) {
			result.Memory = measured.Memory
		}
		// Synchronous module return includes process reaping. Fail closed on leftovers.
		if err = ensureNodeIdle(ctx); err != nil {
			return result, dir, err
		}
	}
	return result, dir, nil
}
