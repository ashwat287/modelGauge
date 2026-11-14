package control

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/nerdylucifer/modelgauge/internal/evidence"
	"github.com/nerdylucifer/modelgauge/internal/guest"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
	"os"
	"path/filepath"
	"testing"
)

func TestEmbeddedEvidenceStreamRoundTrip(t *testing.T) {
	home := t.TempDir()
	data := []byte(`{"samples_ts":[25.2,25.3]}`)
	ref := protocol.EvidenceRef{RunID: "run-123", Name: "prompt.json", SHA256: fmt.Sprintf("%x", sha256.Sum256(data))}
	dir := filepath.Join(home, ".local/share/modelgauge/results", ref.RunID)
	_ = os.MkdirAll(dir, 0700)
	_ = os.WriteFile(filepath.Join(dir, ref.Name), data, 0600)
	var stream bytes.Buffer
	runtime := guest.Runtime{Home: home}
	err := runtime.WriteEvidence(context.Background(), []protocol.EvidenceRef{ref}, &stream)
	if err != nil {
		t.Fatal(err)
	}
	output := t.TempDir()
	if err = evidence.ReceiveArchiveContext(context.Background(), bytes.NewReader(stream.Bytes()), output, []protocol.EvidenceRef{ref}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(output, ref.RunID, ref.Name))
	if !bytes.Equal(got, data) {
		t.Fatal("raw samples changed")
	}
}
