package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestHelpWorksWithoutInfrastructure(t *testing.T) {
	for _, command := range []string{
		"--help", "apply --help", "validate --help", "status --help", "cancel --help", "export --help", "help export", "cleanup --help", "help cleanup", "help benchmark", "help model install", "control --help",
		"control bootstrap --help", "control status --help", "node --help",
		"node bootstrap --help", "node reconnect --help", "node status --help", "model --help",
		"model install --help", "model install status --help", "model install cancel --help",
		"model list --help", "model search --help", "benchmark --help",
		"benchmark campaign status --help",
		"benchmark run --help", "benchmark submit --help",
		"benchmark cancel --help", "results show --help", "results recommend --help",
		"agent run --help", "agent install --help", "server --help", "monitoring --help",
	} {
		t.Run(command, func(t *testing.T) {
			var out, stderr bytes.Buffer
			if code := Run(strings.Fields(command), &out, &stderr); code != 0 {
				t.Fatalf("help returned %d: %s%s", code, &out, &stderr)
			}
			if out.Len()+stderr.Len() == 0 {
				t.Fatal("help was empty")
			}
		})
	}
}

func TestMachineReadableOutputHasNoDecoration(t *testing.T) {
	for _, command := range []string{"version --json", "plan example", "benchmark profiles list"} {
		var out, stderr bytes.Buffer
		if code := Run(strings.Fields(command), &out, &stderr); code != 0 || stderr.Len() != 0 {
			t.Fatalf("%s: unexpected failure/output: %d %s", command, code, &stderr)
		}
		if !json.Valid(out.Bytes()) {
			t.Fatalf("%s is not standalone JSON: %s", command, &out)
		}
	}
}

func TestInvalidFlagIsStillUsageError(t *testing.T) {
	var out, stderr bytes.Buffer
	if code := RunContext(context.Background(), []string{"benchmark", "run", "--unknown-option"}, &out, &stderr); code != 2 {
		t.Fatalf("invalid flag returned %d", code)
	}
}

func TestFlagHelpDoesNotExposeCredentials(t *testing.T) {
	const secret = "private-help-test-token"
	t.Setenv("MODELGAUGE_ADMIN_TOKEN", secret)
	t.Setenv("MODELGAUGE_AGENT_TOKEN", secret)
	for _, command := range []string{"results show --help", "agent install --help", "benchmark submit --help"} {
		var out, stderr bytes.Buffer
		if code := Run(strings.Fields(command), &out, &stderr); code != 0 {
			t.Fatalf("%s: help returned %d", command, code)
		}
		if strings.Contains(out.String()+stderr.String(), secret) {
			t.Fatalf("%s: help exposed a credential", command)
		}
	}
}
