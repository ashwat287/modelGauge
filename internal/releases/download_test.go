package releases

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nerdylucifer/modelgauge/internal/buildinfo"
)

func releaseFixture(t *testing.T) Manifest {
	t.Helper()
	version, commit, repository, channel := buildinfo.Version, buildinfo.Commit, buildinfo.Repository, buildinfo.Channel
	t.Cleanup(func() {
		buildinfo.Version, buildinfo.Commit, buildinfo.Repository, buildinfo.Channel = version, commit, repository, channel
	})
	buildinfo.Version = "v0.0.0-test.1"
	buildinfo.Commit = strings.Repeat("1", 40)
	buildinfo.Repository = "test/modelgauge"
	buildinfo.Channel = "snapshot"
	return Manifest{Schema: Schema, Version: buildinfo.Version, Commit: buildinfo.Commit, Repository: buildinfo.Repository, ControllerImage: "ghcr.io/test/modelgauge@sha256:" + strings.Repeat("0", 64)}
}

func TestArtifactIntegrityAndCache(t *testing.T) {
	m := releaseFixture(t)
	data := []byte("verified Linux executable fixture")
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	m.Assets = []Asset{{Name: "modelgauge_linux_arm64", OS: "linux", Arch: "arm64", SHA256: digest, Size: int64(len(data))}}
	var corrupt atomic.Bool
	corrupt.Store(true)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/release.json" {
			_ = json.NewEncoder(w).Encode(m)
			return
		}
		if r.URL.Path != "/modelgauge_linux_arm64" {
			t.Errorf("unexpected asset %s", r.URL.Path)
		}
		requests.Add(1)
		if corrupt.Load() {
			_, _ = w.Write([]byte("tampered"))
			return
		}
		_, _ = w.Write(data)
	}))
	defer server.Close()
	t.Setenv("MODELGAUGE_RELEASE_BASE_URL", server.URL)
	root := t.TempDir()
	if _, err := downloadAgent(context.Background(), "arm64", root); err == nil {
		t.Fatal("tampered artifact accepted")
	}
	corrupt.Store(false)
	for i := 0; i < 2; i++ {
		download, err := downloadAgent(context.Background(), "arm64", root)
		if err != nil {
			t.Fatal(err)
		}
		if string(download) != string(data) {
			t.Fatal("wrong downloaded artifact")
		}
	}
	if requests.Load() != 2 {
		t.Fatal("cache did not avoid second download")
	}
	if err := os.WriteFile(filepath.Join(root, buildinfo.Version, digest), []byte("corrupted cache"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := downloadAgent(context.Background(), "arm64", root); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 3 {
		t.Fatal("tampered cache was trusted")
	}
}
func TestReleaseIdentityAndTransport(t *testing.T) {
	m := releaseFixture(t)
	m.Version = "v9.9.9"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(m) }))
	defer server.Close()
	t.Setenv("MODELGAUGE_RELEASE_BASE_URL", server.URL)
	if _, err := Load(context.Background()); err == nil {
		t.Fatal("mismatched release version accepted")
	}
	t.Setenv("MODELGAUGE_RELEASE_BASE_URL", "http://example.invalid/releases")
	if _, err := BaseURL(); err == nil {
		t.Fatal("non-loopback plain HTTP accepted")
	}
}
