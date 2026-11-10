package modelcache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInventoryDeduplicatesByIDAndUsesManagedPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, dir := range []string{ManagedDir(home), filepath.Join(home, "models"), filepath.Join(home, "benchmark-work", "models")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "same.gguf"), []byte(dir), 0600); err != nil {
			t.Fatal(err)
		}
	}
	models, err := DiscoverContext(context.Background())
	if err != nil || len(models) != 1 || models[0].Path != filepath.Join(ManagedDir(home), "same.gguf") {
		t.Fatal(models, err)
	}
}
func TestInventoryReusesFingerprintAndRehashesChangedModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := ManagedDir(home)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "model.gguf")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := DiscoverContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(home, ".local/share/modelgauge/model-inventory.json")
	data, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	var cached map[string]inventoryEntry
	if err := json.Unmarshal(data, &cached); err != nil {
		t.Fatal(err)
	}
	entry := cached[path]
	entry.SHA256 = strings.Repeat("a", 64)
	cached[path] = entry
	data, _ = json.Marshal(cached)
	if err := os.WriteFile(cachePath, data, 0600); err != nil {
		t.Fatal(err)
	}
	// A matching fingerprint uses its cached inventory checksum; run-time verification is separate.
	models, err := DiscoverContext(context.Background())
	if err != nil || models[0].SHA256 != entry.SHA256 {
		t.Fatal(models, err)
	}
	if err := os.WriteFile(path, []byte("changed contents"), 0600); err != nil {
		t.Fatal(err)
	}
	models, err = DiscoverContext(context.Background())
	sum := sha256.Sum256([]byte("changed contents"))
	if err != nil || models[0].SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal(models, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DiscoverContext(ctx); err != context.Canceled {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	models, err = DiscoverContext(context.Background())
	if err != nil || len(models) != 0 {
		t.Fatal(models, err)
	}
	data, _ = os.ReadFile(cachePath)
	if string(data) != "{}" {
		t.Fatal("removed model stayed cached", string(data))
	}
}
