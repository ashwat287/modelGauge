package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/nerdylucifer/modelgauge/internal/benchmark"
	"github.com/nerdylucifer/modelgauge/internal/controller"
	"github.com/nerdylucifer/modelgauge/internal/modelcache"
	"github.com/nerdylucifer/modelgauge/internal/platform"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
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
func installLocal(ctx context.Context, j *controller.InstallJob) error {
	_, err := modelcache.InstallContext(ctx, j.ModelID, j.SourceURL, j.SHA256)
	return err
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
	response := executeWorker(workCtx, request)
	response.Interrupted = workCtx.Err() != nil
	if workCtx.Err() != nil && response.Error != "" && !strings.Contains(response.Error, context.Cause(workCtx).Error()) {
		response.Error = errors.Join(errors.New(response.Error), context.Cause(workCtx)).Error()
	}
	cancel(nil)
	<-stopped
	return json.NewEncoder(out).Encode(response)
}
func executeWorker(ctx context.Context, request workerRequest) (response workerResponse) {
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
		err = installLocal(ctx, &controller.InstallJob{ModelID: request.ModelID, SourceURL: request.SourceURL, SHA256: request.SHA256})
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
		// Upload retries recover a finished run without loading the model again.
		if data, e := os.ReadFile(filepath.Join(output, "result.json")); e == nil {
			savedPlan, e := os.ReadFile(filepath.Join(output, "plan.json"))
			if e != nil {
				err = e
				return
			}
			var previous protocol.Plan
			if err = json.Unmarshal(savedPlan, &previous); err != nil {
				return
			}
			before, _ := json.Marshal(previous)
			after, _ := json.Marshal(request.Plan)
			if !bytes.Equal(before, after) {
				err = errors.New("saved result plan differs from worker request")
				return
			}
			err = json.Unmarshal(data, &response.Result)
			if err == nil && (response.Result.RunID != request.RunID || response.Result.PlanID != request.Plan.PlanID) {
				err = errors.New("saved result does not match worker request")
			}
			return
		} else if !os.IsNotExist(e) {
			err = e
			return
		}
		response.Result, _, err = benchmark.Run(ctx, *request.Plan, benchmark.Options{RunID: request.RunID, OutputDir: output, Resume: true})
	default:
		err = fmt.Errorf("unknown worker operation %q", request.Operation)
	}
	return
}
