package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRetiredCreationCommandsExplainYAMLMigration(t *testing.T) {
	for _, command := range []string{"benchmark campaign create", "benchmark plan create"} {
		var out, stderr bytes.Buffer
		if code := Run(strings.Fields(command), &out, &stderr); code != 2 || !strings.Contains(stderr.String(), "apply -f benchmark.yaml") {
			t.Fatal(command, code, stderr.String())
		}
	}
}

func TestRemovedExternalCommands(t *testing.T) {
	for _, command := range []string{"executor run", "benchmark serving plan"} {
		var out, stderr bytes.Buffer
		if code := Run(strings.Fields(command), &out, &stderr); code != 2 {
			t.Fatal(command, code, stderr.String())
		}
	}
}
