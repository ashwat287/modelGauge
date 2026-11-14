package platform

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSHA256FileAndCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model")
	if err := os.WriteFile(path, []byte("abc"), 0600); err != nil {
		t.Fatal(err)
	}
	digest, err := SHA256File(context.Background(), path)
	if err != nil || digest != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatal(digest, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = SHA256File(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatal("hash ignored cancellation", err)
	}
	if _, err = SHA256File(context.Background(), path+"-missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing file accepted", err)
	}
}
