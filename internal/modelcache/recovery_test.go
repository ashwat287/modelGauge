package modelcache

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCancelledDownloadResumesAndCacheSkipsNetwork(t *testing.T) {
	data := []byte("GGUF" + strings.Repeat("model-data", 4096))
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	var calls atomic.Int32
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
			_, _ = w.Write(data[:4096])
			w.(http.Flusher).Flush()
			close(started)
			<-r.Context().Done()
			return
		}
		var offset int
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-", &offset); err != nil || offset != 4096 {
			t.Errorf("incorrect resume offset %d: %v", offset, err)
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, len(data)-1, len(data)))
		w.WriteHeader(206)
		_, _ = w.Write(data[offset:])
	}))
	defer server.Close()
	directory := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := installAt(ctx, "test", server.URL, digest, directory); done <- err }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("download did not begin")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		info, err := os.Stat(filepath.Join(directory, "test.gguf.part"))
		if err == nil && info.Size() == 4096 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("partial file not persisted")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled download reported success")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("download ignored cancellation")
	}
	for i := 0; i < 2; i++ {
		model, err := installAt(context.Background(), "test", server.URL, digest, directory)
		if err != nil {
			t.Fatal(err)
		}
		if model.SHA256 != digest || model.SizeBytes != int64(len(data)) {
			t.Fatal("resumed model differs from original")
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("expected one interrupted transfer and one range resume; got %d requests", calls.Load())
	}
}

func TestCancelledReplacementKeepsVerifiedModel(t *testing.T) {
	directory := t.TempDir()
	original := []byte("original model")
	path := filepath.Join(directory, "test.gguf")
	if err := os.WriteFile(path, original, 0640); err != nil {
		t.Fatal(err)
	}
	replacement := []byte("replacement model data")
	expected := fmt.Sprintf("%x", sha256.Sum256(replacement))
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(replacement)))
		_, _ = w.Write(replacement[:4])
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := installAt(ctx, "test", server.URL+"?token=private", expected, directory)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("download did not start")
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("cancelled replacement succeeded")
	}
	data, _ := os.ReadFile(path)
	if string(data) != string(original) {
		t.Fatal("verified model was replaced")
	}
	metadata, err := os.ReadFile(path + ".part.meta.json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(metadata), "token=private") || strings.Contains(string(metadata), server.URL) {
		t.Fatal("source credentials stored in download identity")
	}
}
