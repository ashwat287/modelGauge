package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/nerdylucifer/modelgauge/internal/benchmark"
	"github.com/nerdylucifer/modelgauge/internal/buildinfo"
	"github.com/nerdylucifer/modelgauge/internal/client"
	"github.com/nerdylucifer/modelgauge/internal/controller"
	"github.com/nerdylucifer/modelgauge/internal/modelcache"
	"github.com/nerdylucifer/modelgauge/internal/platform"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

type pendingJob struct {
	Kind        string                 `json:"kind"`
	Plan        *lease                 `json:"plan,omitempty"`
	Install     *controller.InstallJob `json:"install,omitempty"`
	Result      *protocol.Result       `json:"result,omitempty"`
	InstallDone bool                   `json:"install_done,omitempty"`
	Failure     string                 `json:"failure,omitempty"`
}

func pendingJobForPlan(l lease) pendingJob { return pendingJob{Kind: "benchmark", Plan: &l} }
func pendingJobForInstall(j controller.InstallJob) pendingJob {
	return pendingJob{Kind: "install", Install: &j}
}
func pendingPath(c Config) string               { return filepath.Join(c.ResultsDir, ".pending.json") }
func savePending(c Config, p *pendingJob) error { return platform.WriteJSON(pendingPath(c), p, 0600) }
func loadPending(c Config) (*pendingJob, error) {
	data, err := os.ReadFile(pendingPath(c))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p pendingJob
	if err = json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("read recovery journal: %w", err)
	}
	if (p.Kind != "benchmark" || p.Plan == nil) && (p.Kind != "install" || p.Install == nil) {
		return nil, fmt.Errorf("invalid recovery journal")
	}
	return &p, nil
}

var errCancelled = errors.New("cancelled by user")
var errRejected = errors.New("controller rejected saved work")

func processPending(ctx context.Context, api *client.Client, c Config, p *pendingJob) error {
	install := p.Kind == "install"
	if install && p.Install.NodeID != c.NodeID {
		return rejectPending(c, "installation journal belongs to a different node")
	}
	if !install && p.Plan.Specification.NodeID != c.NodeID {
		return rejectPending(c, "benchmark journal belongs to a different node")
	}
	id, token := "", ""
	var expires time.Time
	if install {
		id, token, expires = p.Install.ID, p.Install.LeaseToken, p.Install.ExpiresAt
	} else {
		id, token, expires = p.Plan.PlanID, p.Plan.LeaseToken, p.Plan.ExpiresAt
	}
	root := "/v1/nodes/" + c.NodeID
	renewPath := root + "/plans/" + id + "/renew"
	if install {
		renewPath = root + "/model-installs/" + id + "/renew"
	}
	output := filepath.Join(c.ResultsDir, "run-"+id)
	// A crash after result.json is synced but before the journal update must
	// never cause the expensive measurements to execute again.
	if !install && p.Result == nil {
		if data, err := os.ReadFile(filepath.Join(output, "result.json")); err == nil {
			var result protocol.Result
			if json.Unmarshal(data, &result) == nil && result.RunID == "run-"+id {
				p.Result = &result
				if err = savePending(c, p); err != nil {
					return err
				}
			}
		}
	}
	if p.Result == nil && !p.InstallDone {
		var renewed controller.Renewal
		status, err := api.DoJSON(ctx, "POST", renewPath, "agent", map[string]string{"lease_token": token}, &renewed)
		if err != nil {
			if status == 409 || status == 404 {
				return rejectPending(c, "job lease expired or was reassigned")
			}
			return err
		}
		expires = renewed.ExpiresAt
		if renewed.Cancelled {
			if !install {
				_ = os.Remove(pendingPath(c))
				return nil
			}
			p.InstallDone = true
			p.Failure = errCancelled.Error()
			if err = savePending(c, p); err != nil {
				return err
			}
		} else {
			workCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			stop, reason := watchLease(workCtx, api, renewPath, token, expires, cancel)
			defer stop()
			if install {
				_, err = modelcache.InstallContext(workCtx, p.Install.ModelID, p.Install.SourceURL, p.Install.SHA256)
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if err != nil {
					p.Failure = err.Error()
					select {
					case cause := <-reason:
						p.Failure = cause.Error()
					default:
					}
				}
				p.InstallDone = true
			} else {
				plan := p.Plan.Specification
				plan.PlanID = id
				plan.NodeID = c.NodeID
				if plan.ExecutionMode != c.ExecutionMode {
					return rejectPending(c, "plan execution mode differs from node mode")
				}
				start := startRequest{PlanID: id, LeaseToken: token, ExecutionMode: plan.ExecutionMode, RuntimeMetadata: map[string]string{"agent_version": buildinfo.Version, "agent_commit": buildinfo.Commit}}
				if err = retryJSON(workCtx, api, root+"/runs/run-"+id+"/start", start); err != nil {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					return err
				}
				_, _ = api.DoJSON(workCtx, "POST", root+"/heartbeat", "agent", heartbeatRequest{Status: "busy"}, nil)
				result, _, err := benchmark.RunWithOptions(workCtx, plan, benchmark.Options{RunID: "run-" + id, OutputDir: output, Resume: true})
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if err != nil {
					failure := err.Error()
					state := protocol.RunStatusFailed
					select {
					case cause := <-reason:
						failure = cause.Error()
						if errors.Is(cause, errCancelled) {
							state = protocol.RunStatusCancelled
						}
					default:
					}
					if result.SchemaVersion != "" {
						result.Status, result.InvalidReason = state, failure
					} else {
						result = protocol.Result{SchemaVersion: "benchmark-result/v1", RunID: "run-" + id, PlanID: id, NodeID: c.NodeID, ExecutionMode: plan.ExecutionMode, Status: state, InvalidReason: failure, StartedAt: time.Now().UTC(), CompletedAt: time.Now().UTC()}
					}
					if err = os.MkdirAll(output, 0750); err != nil {
						return err
					}
					if err = platform.WriteJSON(filepath.Join(output, "result.json"), result, 0640); err != nil {
						return err
					}
				}
				p.Result = &result
			}
			if err = savePending(c, p); err != nil {
				return err
			}
		}
	}
	var err error
	if install {
		err = retryJSON(ctx, api, root+"/model-installs/"+id+"/complete", map[string]string{"lease_token": token, "error": p.Failure})
	} else {
		err = retryJSON(ctx, api, root+"/runs/run-"+id+"/complete", completeRequest{PlanID: id, LeaseToken: token, Result: *p.Result})
	}
	if err != nil {
		if errors.Is(err, errRejected) {
			return rejectPending(c, err.Error())
		}
		return err
	}
	if err = os.Remove(pendingPath(c)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func rejectPending(c Config, reason string) error {
	rejected := filepath.Join(c.ResultsDir, ".rejected-"+time.Now().UTC().Format("20060102T150405.000000000")+".json")
	if err := os.Rename(pendingPath(c), rejected); err != nil {
		return err
	}
	return fmt.Errorf("%s; recovery evidence retained at %s", reason, rejected)
}

func retryJSON(ctx context.Context, api *client.Client, path string, payload any) error {
	delay := time.Second
	for {
		status, err := api.DoJSON(ctx, "POST", path, "agent", payload, nil)
		if err == nil {
			return nil
		}
		if status == 409 || status == 400 || status == 404 || status == 413 || status == 422 {
			return fmt.Errorf("%w: %v", errRejected, err)
		}
		if status >= 400 && status < 500 && status != 408 && status != 429 {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		slog.Warn("controller request failed; retrying saved work", "path", path, "error", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		if delay < 5*time.Second {
			delay *= 2
			if delay > 5*time.Second {
				delay = 5 * time.Second
			}
		}
	}
}

func watchLease(ctx context.Context, api *client.Client, path, token string, expires time.Time, cancelWork context.CancelFunc) (func(), <-chan error) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	reason := make(chan error, 1)
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				requestCtx, stop := context.WithTimeout(ctx, 5*time.Second)
				var r controller.Renewal
				status, err := api.DoJSON(requestCtx, "POST", path, "agent", map[string]string{"lease_token": token}, &r)
				stop()
				if ctx.Err() != nil {
					return
				}
				if err == nil {
					expires = r.ExpiresAt
					if r.Cancelled {
						reason <- errCancelled
						cancelWork()
						return
					}
					continue
				}
				if status == 409 || status == 401 || status == 403 || !time.Now().Before(expires) {
					reason <- fmt.Errorf("lease ownership lost: %w", err)
					cancelWork()
					return
				}
			}
		}
	}()
	return func() { cancel(); <-done }, reason
}
