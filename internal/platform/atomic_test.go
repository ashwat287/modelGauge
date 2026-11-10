package platform

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestNewJSONDoesNotReplaceConcurrentPlan(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.json")
	var group sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		group.Add(1)
		go func(v int) { defer group.Done(); results <- WriteJSONNew(path, map[string]int{"value": v}, 0600) }(i)
	}
	group.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("published %d competing files", successes)
	}
	before, _ := os.ReadFile(path)
	if err := WriteJSONNew(path, map[string]string{"replacement": "unsafe"}, 0600); err == nil {
		t.Fatal("existing plan replaced")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("committed plan changed")
	}
}

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
