package benchmark

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/nerdylucifer/modelgauge/internal/platform"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

// A built-in benchmark owns its workload, execution, and presentation. Adding one
// requires only its implementation/tests and an entry here, not platform branches.
type implementation struct {
	profile        string
	plan           func(context.Context, planOptions, json.RawMessage) ([]protocol.Plan, error)
	validate       func(protocol.Plan) error
	run            func(context.Context, protocol.Plan, Options) (protocol.Result, string, error)
	validateReport func(json.RawMessage) error
	csv            func(io.Writer, []protocol.Result) error
	metricsHeader  string
	metrics        func(*strings.Builder, string, string, string, json.RawMessage) error
}

var implementations = map[string]implementation{
	"llama.cpp": {profile: standardCPU, plan: engineProfile, validate: validateEnginePlan, run: runEnginePlan},
	"serving":   servingImplementation(),
}

func init() { implementations["suite"] = suiteImplementation() }

// Profiles lists the planners registered for declarations.
func Profiles() []string {
	names := []string{}
	for _, module := range implementations {
		if module.plan != nil {
			names = append(names, module.profile)
		}
	}
	sort.Strings(names)
	return names
}

func newPlans(ctx context.Context, name string, options planOptions, config json.RawMessage) ([]protocol.Plan, error) {
	for _, module := range implementations {
		if module.profile != name || module.plan == nil {
			continue
		}
		plans, err := module.plan(ctx, options, config)
		if err != nil {
			return nil, err
		}
		if len(plans) == 0 {
			return nil, fmt.Errorf("profile %q produced no plans", name)
		}
		for _, plan := range plans {
			if err := ValidatePlan(plan); err != nil {
				return nil, err
			}
		}
		return plans, nil
	}
	return nil, fmt.Errorf("unknown benchmark profile %q", name)
}

func planKind(plan protocol.Plan) string {
	if plan.Benchmark != nil {
		return plan.Benchmark.Kind
	}
	return "llama.cpp" // Existing engine plans retain their original wire format.
}

func ValidatePlan(plan protocol.Plan) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	module, ok := implementations[planKind(plan)]
	if !ok {
		return fmt.Errorf("unsupported benchmark kind %q", planKind(plan))
	}
	if module.validate != nil {
		return module.validate(plan)
	}
	return nil
}

// Run dispatches jobs without exposing benchmark details to the agent.
func Run(ctx context.Context, plan protocol.Plan, options Options) (protocol.Result, string, error) {
	if err := ValidatePlan(plan); err != nil {
		return protocol.Result{}, "", err
	}
	unlock, err := platform.Lock("execution")
	if err != nil {
		return protocol.Result{}, "", err
	}
	defer unlock()
	if err := ensureNodeIdle(ctx); err != nil {
		return protocol.Result{}, "", err
	}
	return implementations[planKind(plan)].run(ctx, plan, options)
}

func ValidateResult(result protocol.Result) error {
	if result.Benchmark == nil {
		return nil
	}
	if err := result.Benchmark.Validate(); err != nil {
		return err
	}
	module, ok := implementations[result.Benchmark.Kind]
	if !ok {
		return fmt.Errorf("unsupported benchmark kind %q", result.Benchmark.Kind)
	}
	if module.validateReport != nil {
		return module.validateReport(result.Benchmark.Data)
	}
	return nil
}

// WriteReports adds optional module CSVs; unknown historical reports remain in results.json.
func WriteReports(results []protocol.Result) (map[string][]byte, error) {
	grouped := map[string][]protocol.Result{}
	for _, result := range results {
		if result.Benchmark != nil {
			grouped[result.Benchmark.Kind] = append(grouped[result.Benchmark.Kind], result)
		}
	}
	files := map[string][]byte{}
	for kind, rows := range grouped {
		module, ok := implementations[kind]
		if !ok || module.csv == nil {
			continue
		}
		var out strings.Builder
		if err := module.csv(&out, rows); err != nil {
			return nil, err
		}
		files[kind+"-results.csv"] = []byte(out.String())
	}
	return files, nil
}

func WriteMetrics(out *strings.Builder, node, mode, model string, report *protocol.BenchmarkReport) error {
	if report == nil {
		return nil
	}
	module, ok := implementations[report.Kind]
	if !ok || module.metrics == nil {
		return nil
	}
	if module.metricsHeader != "" && !strings.Contains(out.String(), module.metricsHeader) {
		out.WriteString(module.metricsHeader)
	}
	return module.metrics(out, node, mode, model, report.Data)
}

func setConfig(plan *protocol.Plan, kind string, config any) error {
	data, err := json.Marshal(config)
	if err != nil {
		return err
	}
	plan.Benchmark = &protocol.BenchmarkSpec{Kind: kind, Config: data}
	return nil
}
