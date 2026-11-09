package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFinalizeRejectsInterruptedBuild(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "INCOMPLETE"), []byte("interrupted"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := finish(directory, "ghcr.io/test/modelgauge@sha256:"+strings.Repeat("a", 64)); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatal("interrupted build finalized:", err)
	}
}
