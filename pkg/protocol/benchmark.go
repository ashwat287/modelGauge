package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
)

// BenchmarkSpec is the stable workload envelope; its module owns Config's schema.
type BenchmarkSpec struct {
	Kind   string          `json:"kind"`
	Config json.RawMessage `json:"config"`
}

// BenchmarkReport retains a module's versioned data without platform interpretation.
type BenchmarkReport struct {
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

var benchmarkKind = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)

func (s BenchmarkSpec) Validate() error   { return validateBenchmarkPayload(s.Kind, s.Config) }
func (r BenchmarkReport) Validate() error { return validateBenchmarkPayload(r.Kind, r.Data) }
func validateBenchmarkPayload(kind string, data json.RawMessage) error {
	if !benchmarkKind.MatchString(kind) {
		return errors.New("invalid benchmark kind")
	}
	if len(data) > 5<<19 {
		return errors.New("benchmark payload exceeds 2.5 MiB")
	}
	trimmed := bytes.TrimSpace(data)
	if !json.Valid(trimmed) || len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("benchmark payload must be a JSON object")
	}
	return nil
}

// UnmarshalJSON accepts pre-envelope serving plans while new output uses benchmark.
func (p *Plan) UnmarshalJSON(data []byte) error {
	type plain Plan
	var decoded plain
	wire := struct {
		*plain
		Serving json.RawMessage `json:"serving"`
	}{plain: &decoded}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	if len(wire.Serving) > 0 && string(wire.Serving) != "null" {
		if decoded.Benchmark != nil {
			return errors.New("choose benchmark or legacy serving, not both")
		}
		decoded.Benchmark = &BenchmarkSpec{Kind: "serving", Config: wire.Serving}
	}
	*p = Plan(decoded)
	return nil
}

func (r *Result) UnmarshalJSON(data []byte) error {
	type plain Result
	var decoded plain
	wire := struct {
		*plain
		Serving json.RawMessage `json:"serving"`
	}{plain: &decoded}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	if len(wire.Serving) > 0 && string(wire.Serving) != "null" {
		if decoded.Benchmark != nil {
			return errors.New("choose benchmark or legacy serving, not both")
		}
		decoded.Benchmark = &BenchmarkReport{Kind: "serving", Data: wire.Serving}
	}
	*r = Result(decoded)
	return nil
}

// DecodeStoredReport reads both the generic metadata key and its historical form.
func DecodeStoredReport(current, legacy string) (*BenchmarkReport, error) {
	if current != "" {
		var report BenchmarkReport
		if err := json.Unmarshal([]byte(current), &report); err != nil {
			return nil, err
		}
		return &report, report.Validate()
	}
	if legacy != "" {
		report := &BenchmarkReport{Kind: "serving", Data: json.RawMessage(legacy)}
		return report, report.Validate()
	}
	return nil, nil
}
