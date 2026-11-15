package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nerdylucifer/modelgauge/internal/control"
)

func TestCleanupDefaultsToTextAndOffersJSON(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, raw := range []bool{false, true} {
		args := []string{"cleanup"}
		if raw {
			args = append(args, "--json")
		}
		var out, stderr bytes.Buffer
		if code := Run(args, &out, &stderr); code != 0 {
			t.Fatal(code, &stderr)
		}
		if raw {
			if !json.Valid(out.Bytes()) {
				t.Fatal("invalid JSON", &out)
			}
		} else if out.String() != "Nothing to clean.\n" {
			t.Fatal(&out)
		}
	}
}
func TestCleanupSummaryExplainsRemovedPreservedAndDryRun(t *testing.T) {
	report := control.CleanupReport{Targets: []control.CleanupTarget{{Name: "control", Status: "vm_removed"}, {Name: "node", Status: "managed_resources_removed_vm_preserved"}, {Name: "old", Status: "already_absent"}}, Untracked: []string{"personal"}}
	var out bytes.Buffer
	if code := printCleanup(&out, report); code != 0 {
		t.Fatal(code)
	}
	for _, message := range []string{"control: VM removed.", "node: Managed files and services removed; VM kept.", "old: Already absent.", "personal: Untracked VM kept.", "Cleanup completed."} {
		if !strings.Contains(out.String(), message) {
			t.Fatal(message, &out)
		}
	}
	out.Reset()
	report = control.CleanupReport{DryRun: true, Targets: []control.CleanupTarget{{Name: "node", Status: "verified", Delete: true}}}
	if printCleanup(&out, report) != 0 || !strings.Contains(out.String(), "Would remove managed resources and delete VM.") || strings.Contains(out.String(), "Cleanup completed.") {
		t.Fatal(&out)
	}
	if printCleanup(brokenCleanupOutput{}, report) == 0 {
		t.Fatal("output error ignored")
	}
}

type brokenCleanupOutput struct{}

func (brokenCleanupOutput) Write([]byte) (int, error) { return 0, bytes.ErrTooLarge }
