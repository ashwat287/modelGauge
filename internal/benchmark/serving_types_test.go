package benchmark

import (
	"strings"
	"testing"
)

func TestServingPlanTrustBoundaries(t *testing.T) {
	for _, mutation := range []string{"valid", "credentials", "query", "bad-api", "bad-id", "unbounded", "conversation-api", "cache-control", "negative-target", "secret-setting"} {
		t.Run(mutation, func(t *testing.T) {
			s := ServingPlan{Endpoint: "http://127.0.0.1:8080", API: "openai", RequestTimeout: 10, ReadinessTimeout: 1, SampleInterval: 1, Cases: []ServingCase{{ID: "load", Concurrency: 1, Requests: 2, OutputTokens: 16}}}
			switch mutation {
			case "credentials":
				s.Endpoint = "http://user:password@localhost:8080"
			case "query":
				s.Endpoint += "?token=secret"
			case "bad-api":
				s.API = "unknown"
			case "bad-id":
				s.Cases[0].ID = "../escape"
			case "unbounded":
				s.Cases[0].Requests = 0
			case "conversation-api":
				s.API = "llama.cpp"
				s.Cases[0].ContinueConversation = true
			case "cache-control":
				s.Cases[0].CachePrompt = true
			case "negative-target":
				s.Targets.TTFTMS = -1
			case "secret-setting":
				s.RuntimeSettings = map[string]string{"api_key": "secret"}
			}
			if err := s.Validate(); (err == nil) != (mutation == "valid") {
				t.Fatal(mutation, err)
			}
		})
	}
}

func TestServingReportRejectsIncoherentAndOversizedSummaries(t *testing.T) {
	report := ServingReport{SchemaVersion: "modelgauge-serving/v1", Endpoint: "http://localhost:8080", Cases: []ServingCaseResult{{ID: "load", ElapsedSeconds: 1, Offered: 2, Dispatched: 1, Successful: 1, ClientCapacityDrops: 1}}}
	if err := report.Validate(); err != nil {
		t.Fatal(err)
	}
	report.Cases[0].Successful = 2
	if err := report.Validate(); err == nil {
		t.Fatal("incoherent request accounting was accepted")
	}
	report.Cases[0].Successful = 1
	report.Warnings = []string{strings.Repeat("x", 3<<20)}
	if err := report.Validate(); err == nil {
		t.Fatal("oversized summary was accepted")
	}
}
