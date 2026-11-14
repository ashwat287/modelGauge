package evidence

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ref(data []byte) protocol.EvidenceRef {
	return protocol.EvidenceRef{RunID: "run-123", Name: "case.json", SHA256: fmt.Sprintf("%x", sha256.Sum256(data))}
}
func TestRawCopyAndArchiveValidation(t *testing.T) {
	data := []byte("{\"samples_ts\":[1,2]}\n")
	r := ref(data)
	source := t.TempDir()
	_ = os.MkdirAll(filepath.Join(source, r.RunID), 0700)
	_ = os.WriteFile(filepath.Join(source, r.RunID, r.Name), data, 0600)
	destination := t.TempDir()
	if err := CopyLocalContext(context.Background(), source, destination, []protocol.EvidenceRef{r}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(destination, r.RunID, r.Name))
	if !bytes.Equal(got, data) {
		t.Fatal("raw samples changed")
	}
	for _, scenario := range []string{"valid", "traversal", "symlink", "unexpected", "mismatch", "missing"} {
		t.Run(scenario, func(t *testing.T) {
			var buffer bytes.Buffer
			writer := tar.NewWriter(&buffer)
			name := r.RunID + "/" + r.Name
			kind := byte(tar.TypeReg)
			payload := data
			switch scenario {
			case "traversal":
				name = "../outside"
			case "symlink":
				kind = tar.TypeSymlink
				payload = nil
			case "unexpected":
				name = "run-other/case.json"
			case "mismatch":
				payload = []byte("changed")
			}
			if scenario != "missing" {
				_ = writer.WriteHeader(&tar.Header{Name: name, Typeflag: kind, Size: int64(len(payload)), Mode: 0600, Linkname: "/etc/passwd"})
				_, _ = writer.Write(payload)
			}
			_ = writer.Close()
			err := ReceiveArchiveContext(context.Background(), &buffer, t.TempDir(), []protocol.EvidenceRef{r})
			if (err == nil) != (scenario == "valid") {
				t.Fatalf("unexpected result: %v", err)
			}
		})
	}
}
func TestSymlinkAndUnsafeReferenceRefused(t *testing.T) {
	data := []byte("secret")
	r := ref(data)
	source := t.TempDir()
	_ = os.Mkdir(filepath.Join(source, r.RunID), 0700)
	outside := filepath.Join(t.TempDir(), "private")
	_ = os.WriteFile(outside, data, 0600)
	_ = os.Symlink(outside, filepath.Join(source, r.RunID, r.Name))
	if err := CopyLocalContext(context.Background(), source, t.TempDir(), []protocol.EvidenceRef{r}); err == nil {
		t.Fatal("followed a symlink")
	}
	for _, name := range []string{"../secret", "/etc/passwd", "a/../../b", "a\\b", "a/./b"} {
		r.Name = name
		if _, err := Relative(r); err == nil {
			t.Fatal("accepted", name)
		}
	}
	r = ref(data)
	r.SHA256 = strings.Repeat("x", 64)
	if _, err := Relative(r); err == nil {
		t.Fatal("accepted invalid checksum")
	}
}
