package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPendingSubmissionKeepsRetryIdentityAndReleasesLock(t *testing.T) {
	directory := t.TempDir()
	payload := map[string]string{"model": "test"}
	first, err := reserveSubmission(directory, "same job", payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reserveSubmission(directory, "same job", payload); err == nil {
		t.Fatal("concurrent admission accepted")
	}
	id := first.RequestID
	first.close()
	retry, err := reserveSubmission(directory, "same job", payload)
	if err != nil {
		t.Fatal(err)
	}
	if retry.RequestID != id {
		t.Fatal("uncertain submission was assigned a new identity")
	}
	retry.complete(os.Stderr)
	retry.close()
	next, err := reserveSubmission(directory, "same job", payload)
	if err != nil {
		t.Fatal(err)
	}
	defer next.close()
	if next.RequestID == id {
		t.Fatal("a new benchmark was deduplicated with a completed submission")
	}
}
func TestPendingSubmissionRejectsSymlinkAndChangedContent(t *testing.T) {
	directory := t.TempDir()
	first, err := reserveSubmission(directory, "job", map[string]string{"model": "original"})
	if err != nil {
		t.Fatal(err)
	}
	path := first.path
	first.close()
	if _, err = reserveSubmission(directory, "job", map[string]string{"model": "changed"}); err == nil {
		t.Fatal("changed content replaced unresolved admission")
	}
	outside := filepath.Join(t.TempDir(), "outside")
	_ = os.WriteFile(outside, []byte("keep"), 0600)
	_ = os.Remove(path)
	_ = os.Symlink(outside, path)
	if _, err = reserveSubmission(directory, "job", map[string]string{"model": "original"}); err == nil {
		t.Fatal("followed a symlink")
	}
	data, _ := os.ReadFile(outside)
	if string(data) != "keep" {
		t.Fatal("foreign content overwritten")
	}
}
func TestPendingSubmissionDoesNotStoreCredentials(t *testing.T) {
	entry, err := reserveSubmission(t.TempDir(), "https://controller/?token=secret", map[string]string{"source_url": "https://model/?token=private"})
	if err != nil {
		t.Fatal(err)
	}
	defer entry.close()
	data, err := os.ReadFile(entry.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret") || strings.Contains(string(data), "private") || strings.Contains(string(data), "https://") {
		t.Fatal("request details stored in retry cache")
	}
}
