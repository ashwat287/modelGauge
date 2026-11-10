package benchmark

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

func TestEngineRejectsUnsafeAndCollidingEvidenceNames(t *testing.T) {
	for _, id := range []string{"../escape", "../../.config/modelgauge/control", "/absolute", "plan", "result", "preflight", "postflight"} {
		p := protocol.ExamplePlan()
		p.Cases[0].ID = id
		if ValidatePlan(p) == nil {
			t.Fatal("accepted unsafe ID", id)
		}
	}
	p := protocol.ExamplePlan()
	p.MemoryCase.ID = p.Cases[0].ID
	if ValidatePlan(p) == nil {
		t.Fatal("memory overwrites throughput evidence")
	}
	p = protocol.ExamplePlan()
	p.Cases[1].ID = p.Cases[0].ID + "-warmup"
	if ValidatePlan(p) == nil {
		t.Fatal("case overwrites another warmup")
	}
}
func TestEngineOutputDirectoryNeverClobbersExistingEvidence(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "run")
	if err := prepareOutputDir(dir, false); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "plan.json")
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if prepareOutputDir(dir, false) == nil {
		t.Fatal("fresh execution reused a directory")
	}
	if err := prepareOutputDir(dir, true); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if prepareOutputDir(link, true) == nil {
		t.Fatal("resume followed symlink")
	}
	data, _ := os.ReadFile(marker)
	if string(data) != "keep" {
		t.Fatal("old evidence changed")
	}
}
