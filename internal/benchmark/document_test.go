package benchmark

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

const documentExample = `apiVersion: modelgauge.io/v1alpha1
kind: Benchmark
metadata:
  name: test-run
spec:
  nodes: [node]
  models: [model]
  matrix:
    threads: [1, 2]
    contexts: [512]
  stages:
    - profile: standard-cpu
      options: {warmups: 1, repetitions: 2}
    - profile: node-serving
      config: {suite: basic, requests: 2, concurrency: [1], output_tokens: 16}
`

func TestDeclarationCompilesOrderedModulesAndFreezesSettings(t *testing.T) {
	d, err := ParseDocument(strings.NewReader(documentExample))
	if err != nil {
		t.Fatal(err)
	}
	p, err := d.Plan(context.Background(), "real-node", protocol.ExecutionModeLima, protocol.Model{ID: "model", Path: "/remote/model.gguf", SHA256: strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	s, err := decodeSuite(p)
	if err != nil || len(s.Stages) != 3 {
		t.Fatal(s, err)
	}
	if planKind(s.Stages[0]) != "llama.cpp" || planKind(s.Stages[1]) != "serving" || planKind(s.Stages[2]) != "serving" {
		t.Fatal(s)
	}
	if s.Stages[0].Defaults.MeasureRepetitions != 2 || s.Stages[0].Defaults.WarmupRepetitions != 1 {
		t.Fatal("options lost")
	}
	for _, stage := range s.Stages {
		if stage.NodeID != "real-node" || stage.Model != p.Model || stage.ExecutionMode != protocol.ExecutionModeLima {
			t.Fatal("target identity lost", stage)
		}
	}
	serving, _ := decodeServingPlan(s.Stages[2])
	if serving.Server.Threads != 2 || serving.Cases[0].Requests != 2 {
		t.Fatal(serving)
	}
	equivalent, err := ParseDocument(strings.NewReader("# comment\n" + documentExample))
	if err != nil || equivalent.Digest() != d.Digest() {
		t.Fatal("comments changed identity", err)
	}
}

func TestDeclarationRejectsAmbiguityUnknownSettingsAndBadTypes(t *testing.T) {
	for name, input := range map[string]string{
		"unknown root":       documentExample + "typo: true\n",
		"duplicate":          strings.Replace(documentExample, "name: test-run", "name: test-run\n  name: other", 1),
		"nested duplicate":   strings.Replace(documentExample, "suite: basic", "suite: basic, suite: deployment", 1),
		"module typo":        strings.Replace(documentExample, "suite: basic", "suiet: basic", 1),
		"unknown stage":      strings.Replace(documentExample, "standard-cpu", "missing-module", 1),
		"bad type":           strings.Replace(documentExample, "threads: [1, 2]", "threads: ['1', 2]", 1),
		"duplicate threads":  strings.Replace(documentExample, "threads: [1, 2]", "threads: [1, 1]", 1),
		"invalid option":     strings.Replace(documentExample, "warmups: 1", "warmups: 0", 1),
		"null config":        strings.Replace(documentExample, "config: {suite: basic, requests: 2, concurrency: [1], output_tokens: 16}", "config: null", 1),
		"alias":              strings.Replace(documentExample, "models: [model]", "models: &models [model]\n  nodes: *models", 1),
		"multiple documents": documentExample + "---\n{}",
		"invalid model list": strings.Replace(documentExample, "models: [model]", "models: [model, model]", 1),
		"custom mapping tag": strings.Replace(documentExample, "metadata:", "metadata: !custom", 1),
		"deep nesting":       "[" + strings.Repeat("[", 70) + "0" + strings.Repeat("]", 71),
		"empty":              "",
		"oversized":          strings.Repeat("x", maxDocumentBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			d, err := ParseDocument(strings.NewReader(input))
			if err == nil {
				_, err = d.Plan(context.Background(), "validation", protocol.ExecutionModeNative, protocol.Model{ID: "validation", Path: "/model.gguf", SHA256: strings.Repeat("a", 64)})
			}
			if err == nil {
				t.Fatal("invalid declaration accepted")
			}
		})
	}
}

func TestDeclarationUsesRegistryWithoutProfileBranches(t *testing.T) {
	const kind = "yaml-test"
	module := implementation{profile: kind}
	module.plan = func(ctx context.Context, o planOptions, c json.RawMessage) ([]protocol.Plan, error) {
		p := protocol.Plan{SchemaVersion: protocol.BenchmarkPlanV1, NodeID: o.NodeID, ExecutionMode: o.ExecutionMode, Model: protocol.Model{ID: o.ModelID, Path: o.ModelPath, SHA256: o.ModelSHA256}}
		err := setConfig(&p, kind, map[string]string{})
		return []protocol.Plan{p}, err
	}
	implementations[kind] = module
	defer delete(implementations, kind)
	input := strings.Replace(documentExample, "standard-cpu", kind, 1)
	d, err := ParseDocument(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	p, err := d.Plan(context.Background(), "node", protocol.ExecutionModeNative, protocol.Model{ID: "model", Path: "/remote/model", SHA256: strings.Repeat("b", 64)})
	if err != nil {
		t.Fatal(err)
	}
	s, _ := decodeSuite(p)
	if planKind(s.Stages[0]) != kind {
		t.Fatal("new module was not dispatched")
	}
}

func TestDeclarationIdentitySurvivesJSONBKeyOrdering(t *testing.T) {
	d, err := ParseDocument(strings.NewReader(documentExample))
	if err != nil {
		t.Fatal(err)
	}
	before := d.Digest()
	d.Spec.Stages[1].Config = json.RawMessage(`{"requests":2,"output_tokens":16,"suite":"basic","concurrency":[1]}`)
	if d.Digest() != before {
		t.Fatal("module key order changed experiment identity")
	}
	d.Spec.Stages[1].Config = json.RawMessage(`{"requests":3,"output_tokens":16,"suite":"basic","concurrency":[1]}`)
	if d.Digest() == before {
		t.Fatal("changed workload has same identity")
	}
}

func TestDeclarationExampleAndPromptFixtures(t *testing.T) {
	f, err := os.Open("../../examples/benchmark.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	d, err := ParseDocument(f)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Plan(context.Background(), "node", protocol.ExecutionModeLima, protocol.Model{ID: "qwen", Path: "/node/qwen.gguf", SHA256: strings.Repeat("a", 64)}); err != nil {
		t.Fatal(err)
	}
	d.Spec.Matrix.Contexts = []int{}
	d.Spec.Stages = d.Spec.Stages[1:]
	d.Spec.Stages[0].Config = json.RawMessage(`{"suite":"basic","prompts":[{"text":"Say hello"}]}`)
	if _, err = d.Plan(context.Background(), "node", protocol.ExecutionModeLima, protocol.Model{ID: "qwen", Path: "/node/qwen.gguf", SHA256: strings.Repeat("a", 64)}); err != nil {
		t.Fatal("explicit empty contexts must permit fixtures", err)
	}
}
