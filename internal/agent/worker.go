package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/nerdylucifer/modelgauge/internal/benchmark"
	"github.com/nerdylucifer/modelgauge/internal/controller"
	"github.com/nerdylucifer/modelgauge/internal/modelcache"
	"github.com/nerdylucifer/modelgauge/internal/platform"
)

func inspectLocal(ctx context.Context) (platform.Host, []controller.NodeModel, error) {
	host, err := platform.Inspect()
	if err != nil {
		return host, nil, err
	}
	models, err := modelcache.DiscoverContext(ctx)
	catalog := make([]controller.NodeModel, 0, len(models))
	for _, model := range models {
		catalog = append(catalog, controller.NodeModel{ID: model.ID, Path: model.Path, SHA256: model.SHA256, SizeBytes: model.SizeBytes})
	}
	return host, catalog, err
}

// RunWorker is the node-side stdin/stdout protocol. It never contacts the controller.
func RunWorker(ctx context.Context, in io.Reader, out io.Writer) error {
	return runWorker(ctx, in, out, 20*time.Second)
}
func runWorker(ctx context.Context, in io.Reader, out io.Writer, authorization time.Duration) error {
	ctx, stopReader := context.WithCancel(ctx)
	defer stopReader()
	frames := make(chan []byte)
	go func() {
		defer close(frames)
		scanner := bufio.NewScanner(in)
		scanner.Buffer(make([]byte, 4096), 4<<20)
		for scanner.Scan() {
			data := append([]byte(nil), scanner.Bytes()...)
			select {
			case frames <- data:
			case <-ctx.Done():
				return
			}
		}
	}()
	timer := time.NewTimer(authorization)
	defer timer.Stop()
	var request workerRequest
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("worker authorization expired before request")
	case frame, ok := <-frames:
		if !ok {
			return io.ErrUnexpectedEOF
		}
		decoder := json.NewDecoder(bytes.NewReader(frame))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			return err
		}
		if decoder.Decode(new(any)) != io.EOF {
			return errors.New("worker request must be one JSON object")
		}
	}
	workCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		defer cancel(nil)
		ticker := time.NewTimer(authorization)
		defer ticker.Stop()
		for {
			select {
			case <-workCtx.Done():
				return
			case <-ticker.C:
				cancel(errors.New("executor authorization expired"))
				return
			case frame, ok := <-frames:
				if !ok {
					cancel(errors.New("executor disconnected"))
					return
				}
				if string(frame) != "{}" {
					cancel(errors.New("invalid executor keepalive"))
					return
				}
				if !ticker.Stop() {
					select {
					case <-ticker.C:
					default:
					}
				}
				ticker.Reset(authorization)
			}
		}
	}()
	progress := &workerProgress{out: out}
	heartbeatDone := make(chan struct{})
	heartbeatCtx, stopHeartbeat := context.WithCancel(workCtx)
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				progress.observe(protocol.Measurement{})
			}
		}
	}()
	response := executeWorker(workCtx, request, progress)
	stopHeartbeat()
	<-heartbeatDone
	response.Interrupted = workCtx.Err() != nil
	if workCtx.Err() != nil && response.Error != "" && !strings.Contains(response.Error, context.Cause(workCtx).Error()) {
		response.Error = errors.Join(errors.New(response.Error), context.Cause(workCtx)).Error()
	}
	cancel(nil)
	<-stopped
	return json.NewEncoder(out).Encode(response)
}
func executeWorker(ctx context.Context, request workerRequest, progress io.Writer) (response workerResponse) {
	var err error
	defer func() {
		if err != nil {
			response.Error = err.Error()
		}
	}()
	switch request.Operation {
	case "inspect":
		response.Host, response.Models, err = inspectLocal(ctx)
	case "install":
		_, err = modelcache.InstallContext(ctx, request.ModelID, request.SourceURL, request.SHA256)
	case "observe":
		if writer, ok := progress.(*workerProgress); ok {
			if request.ObserveMillis < 1 || request.ObserveMillis > 3600000 {
				err = errors.New("observation duration must be 1ms..1h")
				return
			}
			observeCtx, stop := context.WithTimeout(ctx, time.Duration(request.ObserveMillis)*time.Millisecond)
			err = benchmark.ObserveNode(observeCtx, writer.observe)
			if observeCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
				err = nil
			}
			stop()
		} else {
			err = errors.New("observation stream required")
		}
	case "benchmark":
		if request.Plan == nil || !regexp.MustCompile(`^run-[a-zA-Z0-9_-]+$`).MatchString(request.RunID) {
			err = errors.New("worker requires a plan and safe run ID")
			return
		}
		if err = benchmark.ValidatePlan(*request.Plan); err != nil {
			return
		}
		var home string
		home, err = os.UserHomeDir()
		if err != nil {
			return
		}
		output := filepath.Join(home, ".local/share/modelgauge/results", request.RunID)
		// The benchmark prepares and validates its directory before opening the log.
		var observe func(protocol.Measurement)
		if writer, ok := progress.(*workerProgress); ok {
			observe = writer.observe
		}
		response.Result, _, err = benchmark.Run(ctx, *request.Plan, benchmark.Options{Observe: observe, Log: progress, RunID: request.RunID, OutputDir: output})
	default:
		err = fmt.Errorf("unknown worker operation %q", request.Operation)
	}
	return
}

type workerProgress struct {
	out io.Writer
	mu  sync.Mutex
}

func (w *workerProgress) observe(m protocol.Measurement) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = json.NewEncoder(w.out).Encode(workerResponse{Measurement: &m})
}

func (w *workerProgress) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := json.NewEncoder(w.out).Encode(workerResponse{Progress: string(p)}); err != nil {
		return 0, err
	}
	return len(p), nil
}
