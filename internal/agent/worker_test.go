package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWorkerCancelsDownloadOnLostAuthorization(t *testing.T) {
	for _, mode := range []string{"eof", "timeout", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			started := make(chan struct{})
			stopped := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done(); close(stopped) }))
			defer server.Close()
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var out bytes.Buffer
			done := make(chan error, 1)
			go func() { done <- runWorker(ctx, reader, &out, 250*time.Millisecond) }()
			request := workerRequest{Operation: "install", ModelID: "test-model", SourceURL: server.URL, SHA256: strings.Repeat("a", 64)}
			if err := json.NewEncoder(writer).Encode(request); err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("download never started (possible nested execution lock)")
			}
			switch mode {
			case "eof":
				writer.Close()
			case "cancel":
				cancel()
			case "timeout":
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("worker did not stop")
			}
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("download continued after authorization ended")
			}
			var response workerResponse
			expected := map[string]string{"eof": "executor disconnected", "timeout": "executor authorization expired", "cancel": "context canceled"}[mode]
			if err := json.Unmarshal(out.Bytes(), &response); err != nil || !strings.Contains(response.Error, expected) {
				t.Fatal(out.String(), err)
			}
		})
	}
}
func TestWorkerKeepsAuthorizationAlive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-release:
			w.Write([]byte("GGUFtest"))
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(release)
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	var out bytes.Buffer
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stopWrite := context.AfterFunc(ctx, func() { reader.CloseWithError(ctx.Err()) })
	defer stopWrite()
	go func() {
		done <- runWorker(ctx, reader, &out, time.Second)
		reader.Close()
	}()
	_ = json.NewEncoder(writer).Encode(workerRequest{Operation: "install", ModelID: "test-model", SourceURL: server.URL})
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("download did not start:", ctx.Err())
	}
	for i := 0; i < 6; i++ {
		time.Sleep(200 * time.Millisecond)
		if _, err := io.WriteString(writer, "{}\n"); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case release <- struct{}{}:
	case <-ctx.Done():
		t.Fatal("download stopped before release:", ctx.Err())
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not finish")
	}
	var response workerResponse
	_ = json.Unmarshal(out.Bytes(), &response)
	if response.Error != "" {
		t.Fatal(response.Error)
	}
}
