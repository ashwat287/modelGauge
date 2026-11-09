package cli

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/nerdylucifer/modelgauge/internal/platform"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/nerdylucifer/modelgauge/internal/client"
	"github.com/nerdylucifer/modelgauge/internal/modelcache"
)

func runModel(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: modelgauge model <list|install>")
		return 2
	}
	if args[0] == "install" {
		return installModel(ctx, args[1:], stdout, stderr)
	}
	if args[0] == "search" {
		query := ""
		if len(args) > 1 {
			query = args[1]
		}
		entries, err := modelcache.SearchContext(ctx, query)
		if err != nil {
			fmt.Fprintf(stderr, "search models: %v\n", err)
			return 1
		}
		return writeJSON(stdout, entries)
	}
	if args[0] != "list" {
		fmt.Fprintln(stderr, "usage: modelgauge model <list|install>")
		return 2
	}
	f := flag.NewFlagSet("model list", flag.ContinueOnError)
	setFlagOutput(f, stderr)
	node := f.String("node", "", "node ID")
	controllerURL := f.String("controller", os.Getenv("MODELGAUGE_CONTROLLER_URL"), "controller URL")
	token := f.String("admin-token", os.Getenv("MODELGAUGE_ADMIN_TOKEN"), "admin token")
	if err := f.Parse(args[1:]); err != nil {
		return flagExitCode(err)
	}
	if *node == "" || *controllerURL == "" || *token == "" {
		fmt.Fprintln(stderr, "--node, --controller, and --admin-token are required")
		return 2
	}
	var models []catalogModel
	api := &client.Client{BaseURL: *controllerURL, AdminToken: *token}
	if _, err := api.DoJSON(ctx, "GET", "/v1/nodes/"+*node+"/models", "admin", nil, &models); err != nil {
		fmt.Fprintf(stderr, "list models: %v\n", err)
		return 1
	}
	for i := range models {
		models[i].Path = ""
	}
	return writeJSON(stdout, models)
}

func installModel(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "cancel" {
		return cancelJob(ctx, args[1:], stdout, stderr, true)
	}
	if len(args) > 0 && args[0] == "status" {
		return installStatus(ctx, args[1:], stdout, stderr)
	}
	positionalID := ""
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		positionalID = args[0]
		args = args[1:]
	}
	f := flag.NewFlagSet("model install", flag.ContinueOnError)
	setFlagOutput(f, stderr)
	id := f.String("id", positionalID, "stable model ID")
	sourceURL := f.String("url", "", "GGUF download URL")
	checksum := f.String("sha256", "", "expected SHA-256 (recommended)")
	node := f.String("node", "", "target node ID (queues remote installation)")
	controllerURL := f.String("controller", os.Getenv("MODELGAUGE_CONTROLLER_URL"), "controller URL")
	token := f.String("admin-token", os.Getenv("MODELGAUGE_ADMIN_TOKEN"), "admin token")
	if err := f.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if *id == "" {
		fmt.Fprintln(stderr, "a model ID is required")
		return 2
	}
	if err := modelcache.ValidateID(*id); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if *sourceURL == "" {
		entry, err := modelcache.ResolveContext(ctx, *id)
		if err != nil {
			fmt.Fprintf(stderr, "resolve model: %v\n", err)
			return 1
		}
		*sourceURL = entry.URL
		*checksum = entry.SHA256
	}
	if *node != "" {
		if *controllerURL == "" || *token == "" {
			fmt.Fprintln(stderr, "--controller and --admin-token are required with --node")
			return 2
		}
		api := &client.Client{BaseURL: *controllerURL, AdminToken: *token}
		payload := map[string]string{"model_id": *id, "source_url": *sourceURL, "sha256": *checksum}
		directory, err := submissionCacheDir()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		endpoint := "/v1/nodes/" + *node + "/model-installs"
		pending, err := reserveSubmission(directory, api.BaseURL+"\n"+endpoint+"\n"+*id, payload)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		defer pending.close()
		var response client.Submission
		if _, err = api.SubmitJSON(ctx, endpoint, pending.RequestID, payload, &response); err != nil {
			fmt.Fprintf(stderr, "queue model install: %v; retry the same command to check the existing submission safely\n", err)
			return 1
		}
		code := writeJSON(stdout, response)
		if code == 0 {
			pending.complete(stderr)
		}
		return code
	}
	model, err := modelcache.InstallContext(ctx, *id, *sourceURL, *checksum)
	if err != nil {
		fmt.Fprintf(stderr, "install model: %v\n", err)
		return 1
	}
	return writeJSON(stdout, map[string]any{"id": model.ID, "sha256": model.SHA256, "size_bytes": model.SizeBytes, "status": "installed"})
}

func installStatus(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: modelgauge model install status JOB_ID")
		return 2
	}
	id := args[0]
	f := flag.NewFlagSet("model install status", flag.ContinueOnError)
	setFlagOutput(f, stderr)
	controllerURL := f.String("controller", os.Getenv("MODELGAUGE_CONTROLLER_URL"), "controller URL")
	token := f.String("admin-token", os.Getenv("MODELGAUGE_ADMIN_TOKEN"), "admin token")
	if err := f.Parse(args[1:]); err != nil {
		return flagExitCode(err)
	}
	if *controllerURL == "" || *token == "" {
		fmt.Fprintln(stderr, "controller URL and admin token are required")
		return 2
	}
	api := &client.Client{BaseURL: *controllerURL, AdminToken: *token}
	var result any
	if _, err := api.DoJSON(ctx, "GET", "/v1/model-installs/"+id, "admin", nil, &result); err != nil {
		fmt.Fprintf(stderr, "installation status: %v\n", err)
		return 1
	}
	return writeJSON(stdout, result)
}

// pendingSubmission belongs only to job admission. It retains a retry key while
// a response is uncertain; successful submissions remove it automatically.
type pendingSubmission struct {
	RequestID     string `json:"request_id"`
	PayloadSHA256 string `json:"payload_sha256"`
	path          string
	lock          *os.File
}

func submissionCacheDir() (string, error) {
	home, err := os.UserHomeDir()
	return filepath.Join(home, ".cache/modelgauge/submissions"), err
}
func newRequestID() (string, error) {
	value := make([]byte, 16)
	_, err := rand.Read(value)
	return hex.EncodeToString(value), err
}
func reserveSubmission(directory, key string, payload any) (pending *pendingSubmission, resultErr error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	payloadHash := fmt.Sprintf("%x", sha256.Sum256(data))
	keyHash := fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
	if err = os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("unsafe submission cache")
	}
	path := filepath.Join(directory, keyHash+".json")
	lockPath := path + ".lock"
	if info, e := os.Lstat(lockPath); e == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("unsafe submission lock")
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("the same job submission is already in progress")
	}
	entry := &pendingSubmission{path: path, lock: lock}
	defer func() {
		if resultErr != nil {
			entry.close()
		}
	}()
	if info, e := os.Lstat(path); e == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("unsafe submission cache entry")
		}
		data, e := os.ReadFile(path)
		if e != nil {
			return nil, e
		}
		if json.Unmarshal(data, entry) != nil {
			return nil, fmt.Errorf("invalid pending submission")
		}
		decoded, e := hex.DecodeString(entry.RequestID)
		if e != nil || len(decoded) != 16 {
			return nil, fmt.Errorf("invalid submission retry key")
		}
		if entry.PayloadSHA256 != payloadHash {
			return nil, fmt.Errorf("a previous submission is unresolved; retry with its original plan or model revision before changing the request")
		}
		return entry, nil
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	entry.RequestID, err = newRequestID()
	if err != nil {
		return nil, err
	}
	entry.PayloadSHA256 = payloadHash
	if err = platform.WriteJSON(path, entry, 0600); err != nil {
		return nil, err
	}
	return entry, nil
}
func (p *pendingSubmission) close() {
	if p.lock != nil {
		_ = syscall.Flock(int(p.lock.Fd()), syscall.LOCK_UN)
		_ = p.lock.Close()
		p.lock = nil
	}
}
func (p *pendingSubmission) complete(stderr io.Writer) {
	if err := os.Remove(p.path); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(stderr, "Job accepted, but its local retry record could not be cleared: %v\n", err)
	}
}
