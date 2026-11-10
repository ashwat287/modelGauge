package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nerdylucifer/modelgauge/internal/benchmark"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

func TestServingPlanGenerationAndValidation(t *testing.T) {
	for _, api := range []string{"openai", "llama.cpp"} {
		var out, stderr bytes.Buffer
		code := RunContext(context.Background(), []string{"benchmark", "serving", "plan", "--endpoint", "http://127.0.0.1:8080", "--model", "test", "--api", api, "--suite", "deployment", "--contexts", "128,512", "--concurrency", "1,2"}, &out, &stderr)
		var plan protocol.Plan
		if code != 0 || json.Unmarshal(out.Bytes(), &plan) != nil || benchmark.ValidatePlan(plan) != nil || plan.Benchmark == nil {
			t.Fatal(code, out.String(), stderr.String())
		}
		if len(servingConfig(plan).Cases) < 8 || servingConfig(plan).Cases[0].Concurrency != 1 || servingConfig(plan).Cases[0].PromptTokens != 128 {
			t.Fatal(servingConfig(plan).Cases)
		}
	}
}

func TestServingDatasetAndPlanAreNotOverwritten(t *testing.T) {
	root := t.TempDir()
	dataset := filepath.Join(root, "fixtures.json")
	file := filepath.Join(root, "plan.json")
	if err := os.WriteFile(dataset, []byte(`[{"id":"quality","text":"say yes","expect":{"exact":"yes"}}]`), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"benchmark", "serving", "plan", "--endpoint", "http://localhost:8080", "--model", "test", "--dataset", dataset, "--output", file}
	var out, stderr bytes.Buffer
	if code := Run(args, &out, &stderr); code != 0 {
		t.Fatal(code, stderr.String())
	}
	if code := Run(args, &out, &stderr); code == 0 {
		t.Fatal("plan overwritten")
	}
	data, _ := os.ReadFile(file)
	var plan protocol.Plan
	if json.Unmarshal(data, &plan) != nil || servingConfig(plan).Cases[0].Prompts[0].Expect.Exact == nil {
		t.Fatal("quality fixture lost")
	}
}

func TestServingRejectsTrailingDatasetAndSupportsTimedDeployment(t *testing.T) {
	dataset := filepath.Join(t.TempDir(), "fixtures.json")
	if err := os.WriteFile(dataset, []byte(`[{"text":"hello"}] {"ignored":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	base := []string{"benchmark", "serving", "plan", "--endpoint", "http://localhost:8080", "--model", "test"}
	if code := Run(append(base, "--dataset", dataset), &out, &stderr); code == 0 {
		t.Fatal("accepted trailing dataset content")
	}
	out.Reset()
	stderr.Reset()
	if code := Run(append(base, "--api", "llama.cpp", "--suite", "deployment", "--requests", "0", "--duration", "1m"), &out, &stderr); code != 0 {
		t.Fatal("time-based deployment suite was rejected", stderr.String())
	}
}

func servingConfig(plan protocol.Plan) *benchmark.ServingPlan {
	config, err := benchmark.DecodeServingPlan(plan)
	if err != nil {
		panic(err)
	}
	return config
}
