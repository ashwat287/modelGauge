package benchmark

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"

	"github.com/nerdylucifer/modelgauge/pkg/protocol"
	"go.yaml.in/yaml/v3"
)

const maxDocumentBytes = 2 << 20

// Document is the user-facing declaration. Execution still uses protocol.Plan.
type Document struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Nodes  []string `json:"nodes"`
		Models []string `json:"models"`
		Matrix struct {
			Threads  []int `json:"threads,omitempty"`
			Contexts []int `json:"contexts"`
		} `json:"matrix"`
		Stages []DocumentStage `json:"stages"`
	} `json:"spec"`
}

type DocumentStage struct {
	Profile string `json:"profile"`
	Options struct {
		Warmups     *int `json:"warmups,omitempty"`
		Repetitions *int `json:"repetitions,omitempty"`
	} `json:"options,omitempty"`
	Config json.RawMessage `json:"config,omitempty"`
}

var documentName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func ParseDocument(r io.Reader) (Document, error) {
	var d Document
	data, err := io.ReadAll(io.LimitReader(r, maxDocumentBytes+1))
	if err != nil {
		return d, err
	}
	if len(data) > maxDocumentBytes {
		return d, errors.New("benchmark YAML exceeds 2 MiB")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var root yaml.Node
	if err = decoder.Decode(&root); err != nil {
		return d, fmt.Errorf("benchmark YAML: %w", err)
	}
	if decoder.Decode(new(yaml.Node)) != io.EOF {
		return d, errors.New("expected exactly one YAML document")
	}
	if len(root.Content) != 1 {
		return d, errors.New("empty benchmark YAML")
	}
	value, err := yamlValue(root.Content[0], "$", 0)
	if err != nil {
		return d, err
	}
	data, err = json.Marshal(value)
	if err != nil {
		return d, err
	}
	strict := json.NewDecoder(bytes.NewReader(data))
	strict.DisallowUnknownFields()
	if err = strict.Decode(&d); err != nil {
		return d, fmt.Errorf("benchmark YAML: %w", err)
	}
	return d, d.Validate()
}

// Reject aliases, merges and ambiguous keys before decoding; config remains module-owned.
func yamlValue(n *yaml.Node, path string, depth int) (any, error) {
	if depth > 64 {
		return nil, fmt.Errorf("%s: YAML nesting exceeds 64 levels", path)
	}
	switch n.Kind {
	case yaml.MappingNode:
		if n.Tag != "!!map" {
			return nil, fmt.Errorf("%s: unsupported YAML tag %s", path, n.Tag)
		}
		out := map[string]any{}
		for i := 0; i < len(n.Content); i += 2 {
			key := n.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return nil, fmt.Errorf("%s line %d: keys must be strings", path, key.Line)
			}
			if _, exists := out[key.Value]; exists {
				return nil, fmt.Errorf("%s.%s line %d: duplicate key", path, key.Value, key.Line)
			}
			value, err := yamlValue(n.Content[i+1], path+"."+key.Value, depth+1)
			if err != nil {
				return nil, err
			}
			out[key.Value] = value
		}
		return out, nil
	case yaml.SequenceNode:
		if n.Tag != "!!seq" {
			return nil, fmt.Errorf("%s: unsupported YAML tag %s", path, n.Tag)
		}
		out := []any{}
		for i, child := range n.Content {
			value, err := yamlValue(child, fmt.Sprintf("%s[%d]", path, i), depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, value)
		}
		return out, nil
	case yaml.ScalarNode:
		if n.Tag != "!!str" && n.Tag != "!!int" && n.Tag != "!!float" && n.Tag != "!!bool" && n.Tag != "!!null" {
			return nil, fmt.Errorf("%s line %d: unsupported YAML tag %s", path, n.Line, n.Tag)
		}
		var value any
		err := n.Decode(&value)
		return value, err
	default:
		return nil, fmt.Errorf("%s line %d: YAML aliases are not supported", path, n.Line)
	}
}

func (d Document) Digest() string {
	data, _ := json.Marshal(d)
	// Raw module JSON must have the same identity after JSONB reorders its keys.
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var canonical any
	_ = decoder.Decode(&canonical)
	data, _ = json.Marshal(canonical)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

// Keep reference validation independent of registered modules so removed modules
// do not prevent querying or cancelling an already accepted experiment.
func (d Document) Validate() error {
	if d.APIVersion != "modelgauge.io/v1alpha1" || d.Kind != "Benchmark" {
		return errors.New("apiVersion must be modelgauge.io/v1alpha1 and kind must be Benchmark")
	}
	if !documentName.MatchString(d.Metadata.Name) {
		return errors.New("metadata.name must be 1..63 lowercase letters, digits or hyphens, starting with a letter or digit")
	}
	for name, values := range map[string][]string{"nodes": d.Spec.Nodes, "models": d.Spec.Models} {
		if len(values) < 1 || len(values) > 32 {
			return fmt.Errorf("spec.%s requires 1..32 entries", name)
		}
		seen := map[string]bool{}
		for _, v := range values {
			if v == "" || len(v) > 256 || seen[v] {
				return fmt.Errorf("spec.%s contains an empty, oversized or duplicate entry %q", name, v)
			}
			seen[v] = true
		}
	}
	if len(d.Spec.Nodes)*len(d.Spec.Models) > 128 {
		return errors.New("experiment exceeds 128 node/model pairs")
	}
	for name, values := range map[string][]int{"threads": d.Spec.Matrix.Threads, "contexts": d.Spec.Matrix.Contexts} {
		if len(values) > 16 {
			return fmt.Errorf("spec.matrix.%s exceeds 16 values", name)
		}
		seen := map[int]bool{}
		for _, v := range values {
			if v < 1 || seen[v] {
				return fmt.Errorf("spec.matrix.%s requires unique positive integers", name)
			}
			seen[v] = true
		}
	}
	if len(d.Spec.Stages) < 1 || len(d.Spec.Stages) > 32 {
		return errors.New("spec.stages requires 1..32 stages")
	}
	for i, stage := range d.Spec.Stages {
		if stage.Profile == "" || len(stage.Profile) > 128 {
			return fmt.Errorf("spec.stages[%d].profile is required and must be <=128 characters", i)
		}
		for key, n := range map[string]*int{"warmups": stage.Options.Warmups, "repetitions": stage.Options.Repetitions} {
			if n != nil && (*n < 1 || *n > 1000) {
				return fmt.Errorf("spec.stages[%d].options.%s must be 1..1000", i, key)
			}
		}
		if len(stage.Config) > 0 && (!json.Valid(stage.Config) || bytes.TrimSpace(stage.Config)[0] != '{') {
			return fmt.Errorf("spec.stages[%d].config must be an object", i)
		}
	}
	return nil
}

func (d Document) Plan(ctx context.Context, node string, mode protocol.ExecutionMode, model protocol.Model) (protocol.Plan, error) {
	threads, contexts := d.Spec.Matrix.Threads, d.Spec.Matrix.Contexts
	if len(threads) == 0 {
		threads = []int{1, 2}
	}
	if contexts == nil {
		contexts = []int{512, 4096, 8192}
	}
	var stages []protocol.Plan
	for i, stage := range d.Spec.Stages {
		o := planOptions{NodeID: node, ExecutionMode: mode, ModelID: model.ID, ModelPath: model.Path, ModelSHA256: model.SHA256, Threads: threads, Contexts: contexts}
		if stage.Options.Warmups != nil {
			o.Warmups = *stage.Options.Warmups
		}
		if stage.Options.Repetitions != nil {
			o.Repetitions = *stage.Options.Repetitions
		}
		plans, err := newPlans(ctx, stage.Profile, o, stage.Config)
		if err != nil {
			return protocol.Plan{}, fmt.Errorf("spec.stages[%d] (%s): %w", i, stage.Profile, err)
		}
		for _, p := range plans {
			if planKind(p) == "suite" {
				s, err := decodeSuite(p)
				if err != nil {
					return protocol.Plan{}, err
				}
				stages = append(stages, s.Stages...)
			} else {
				stages = append(stages, p)
			}
		}
	}
	if len(stages) == 0 {
		return protocol.Plan{}, errors.New("no benchmark stages")
	}
	p := stages[0]
	p.Runtime = protocol.Runtime{Kind: "suite"}
	p.Cases, p.MemoryCase = nil, protocol.MemoryCase{}
	if err := setConfig(&p, "suite", suitePlan{Stages: stages}); err != nil {
		return p, err
	}
	return p, ValidatePlan(p)
}
