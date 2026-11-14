package platform

import (
	"os"
	"path/filepath"
	"testing"
)

func TestJSONReplacementPreservesOldFileOnMarshalFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := WriteJSON(path, map[string]string{"keep": "value"}, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(path, func() {}, 0600); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatal("previous configuration lost", err)
	}
	if err := WriteJSON(path, map[string]int{"new": 1}, 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
}
