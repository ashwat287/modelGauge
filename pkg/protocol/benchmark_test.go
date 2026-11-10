package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLegacyServingJSONUsesGenericEnvelope(t *testing.T) {
	oldPlan := `{"schema_version":"benchmark-plan/v1","node_id":"node","execution_mode":"lima-linux","runtime":{"kind":"http"},"model":{"id":"model"},"serving":{"endpoint":"http://localhost:8080","api":"openai"}}`
	var plan Plan
	if err := json.Unmarshal([]byte(oldPlan), &plan); err != nil || plan.Benchmark == nil || plan.Benchmark.Kind != "serving" {
		t.Fatal(plan, err)
	}
	data, err := json.Marshal(plan)
	if err != nil || strings.Contains(string(data), `"serving":`) || !strings.Contains(string(data), `"config":`) {
		t.Fatal(string(data), err)
	}
	var result Result
	if err = json.Unmarshal([]byte(`{"run_id":"run","serving":{"schema_version":"modelgauge-serving/v1","cases":[]}}`), &result); err != nil || result.Benchmark == nil || result.Benchmark.Kind != "serving" {
		t.Fatal(result, err)
	}
	for _, raw := range []string{
		`{"benchmark":{"kind":"serving","config":{}},"serving":{}}`,
		`{"benchmark":{"kind":"serving","config":{}},"misspelled_field":1}`,
	} {
		if json.Unmarshal([]byte(raw), &plan) == nil {
			t.Fatal("ambiguous/unknown field accepted", raw)
		}
	}
	if json.Unmarshal([]byte(`{"benchmark":{"kind":"serving","data":{}},"serving":{}}`), &result) == nil {
		t.Fatal("ambiguous result accepted")
	}
	if json.Unmarshal([]byte(`{"benchmark":{"kind":"test","data":{}},"unknown":1}`), &result) == nil {
		t.Fatal("result decoder bypassed unknown-field validation")
	}
}

func TestGenericPayloadLimitsAndHistoricalMetadata(t *testing.T) {
	for _, spec := range []BenchmarkSpec{
		{Kind: "../escape", Config: json.RawMessage(`{}`)},
		{Kind: "test", Config: json.RawMessage(`[]`)},
		{Kind: "test", Config: json.RawMessage(`null`)},
		{Kind: "test", Config: json.RawMessage(`{"x":"` + strings.Repeat("x", 3<<20) + `"}`)},
	} {
		if spec.Validate() == nil {
			t.Fatal("invalid payload accepted", spec.Kind)
		}
	}
	current := `{"kind":"removed-benchmark","data":{"schema_version":"test/v1","value":7}}`
	report, err := DecodeStoredReport(current, "")
	if err != nil || report.Kind != "removed-benchmark" {
		t.Fatal(report, err)
	}
	report, err = DecodeStoredReport("", `{"schema_version":"modelgauge-serving/v1","cases":[]}`)
	if err != nil || report.Kind != "serving" {
		t.Fatal(report, err)
	}
	if _, err = DecodeStoredReport(`{"kind":"test","data":[]}`, ""); err == nil {
		t.Fatal("invalid stored payload accepted")
	}
}
