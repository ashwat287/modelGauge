package guest

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestControllerKeyRotationAndCleanupPreserveUserKeys(t *testing.T) {
	home := t.TempDir()
	r := Runtime{Home: home}
	path := filepath.Join(home, ".ssh/authorized_keys")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte("# user keys\n\nssh-ed25519 user-owned-key keep-me\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		public, _, _ := ed25519.GenerateKey(rand.Reader)
		key, _ := ssh.NewPublicKey(public)
		if err := r.InstallControllerKey(context.Background(), ssh.MarshalAuthorizedKey(key)); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(path)
		if !strings.HasPrefix(string(data), string(original)) || !strings.Contains(string(data), `restrict,command=`) || !strings.Contains(string(data), "_guest connection") {
			t.Fatal(string(data))
		}
	}
	data, _ := os.ReadFile(path)
	if strings.Count(string(data), "modelgauge-controller") != 2 {
		t.Fatal("old key removed before enrollment succeeded", string(data))
	}
	if err := r.CommitControllerKey(); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if strings.Count(string(data), "modelgauge-controller") != 1 {
		t.Fatal("old key not retired", string(data))
	}
	if err := r.RemoveControllerKey(); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if string(data) != string(original) {
		t.Fatal("user keys changed", string(data))
	}
}
