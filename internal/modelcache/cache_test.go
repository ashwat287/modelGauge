package modelcache

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

func TestInterruptedDownloadIsDiscardedAndRerunStartsFresh(t *testing.T) {
	directory := t.TempDir()
	var first atomic.Bool
	first.Store(true)
	started := make(chan struct{}, 1)
	data := []byte("complete-model")
	sum := fmt.Sprintf("%x", sha256.Sum256(data))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			t.Error("download attempted resume")
		}
		if first.Swap(false) {
			io.WriteString(w, "partial")
			w.(http.Flusher).Flush()
			started <- struct{}{}
			<-r.Context().Done()
			return
		}
		w.Write(data)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := installAt(ctx, "model", server.URL, sum, directory); done <- err }()
	<-started
	cancel()
	if err := <-done; err == nil {
		t.Fatal("cancelled download succeeded")
	}
	files, err := os.ReadDir(directory)
	if err != nil || len(files) != 0 {
		t.Fatal("partial download retained", files, err)
	}
	model, err := installAt(context.Background(), "model", server.URL, sum, directory)
	if err != nil || model.SHA256 != sum {
		t.Fatal(model, err)
	}
	files, _ = os.ReadDir(directory)
	for _, f := range files {
		if strings.HasPrefix(f.Name(), ".download-") {
			t.Fatal("temporary file retained")
		}
	}
}
