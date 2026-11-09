package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/nerdylucifer/modelgauge/internal/platform"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nerdylucifer/modelgauge/internal/client"
	"github.com/nerdylucifer/modelgauge/internal/profile"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

type campaignManifest struct {
	Models        []string       `json:"models,omitempty"`
	SchemaVersion string         `json:"schema_version"`
	CreatedAt     time.Time      `json:"created_at"`
	NodeID        string         `json:"node_id"`
	Profile       string         `json:"profile"`
	Plans         []campaignPlan `json:"plans"`
}
type campaignPlan struct {
	RequestID string `json:"request_id,omitempty"`
	ModelID   string `json:"model_id"`
	ModelPath string `json:"model_path"`
	PlanFile  string `json:"plan_file"`
	PlanID    string `json:"plan_id,omitempty"`
	Status    string `json:"status"`
}
type catalogModel struct {
	ID        string `json:"id"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
	Path      string `json:"path"`
}

func runCampaign(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: modelgauge benchmark campaign <create|status>")
		return 2
	}
	if args[0] == "create" {
		return createCampaign(ctx, args[1:], stdout, stderr)
	}
	if args[0] == "status" {
		return campaignStatus(ctx, args[1:], stdout, stderr)
	}
	fmt.Fprintln(stderr, "usage: modelgauge benchmark campaign <create|status>")
	return 2
}

func createCampaign(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	f := flag.NewFlagSet("benchmark campaign create", flag.ContinueOnError)
	setFlagOutput(f, stderr)
	node := f.String("node", "", "target node ID")
	models := f.String("models", "", "comma-separated installed model IDs")
	binary := f.String("binary", "", "llama-bench path")
	mode := f.String("execution-mode", "native-isolated", "execution mode")
	contextsText := f.String("contexts", "512,4096,8192", "contexts")
	threadsText := f.String("threads", "1,2", "threads")
	output := f.String("output", "", "campaign directory")
	submit := f.Bool("submit", false, "submit generated plans")
	resume := f.Bool("resume", false, "resume the same campaign without duplicating accepted jobs")
	controllerURL := f.String("controller", os.Getenv("MODELGAUGE_CONTROLLER_URL"), "controller URL")
	adminToken := f.String("admin-token", os.Getenv("MODELGAUGE_ADMIN_TOKEN"), "admin token")
	if err := f.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if *node == "" || *models == "" {
		fmt.Fprintln(stderr, "--node and --models are required")
		return 2
	}
	if *controllerURL == "" || *adminToken == "" {
		fmt.Fprintln(stderr, "--controller and --admin-token are required to resolve node model IDs")
		return 2
	}
	contexts, err := integerList(*contextsText)
	if err != nil {
		fmt.Fprintf(stderr, "--contexts: %v\n", err)
		return 2
	}
	threads, err := integerList(*threadsText)
	if err != nil {
		fmt.Fprintf(stderr, "--threads: %v\n", err)
		return 2
	}
	if *output == "" {
		*output = "campaign-" + time.Now().UTC().Format("20060102T150405Z")
	}
	absolute, err := filepath.Abs(*output)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	*output = absolute
	manifestPath := filepath.Join(*output, "campaign.json")
	manifest := campaignManifest{SchemaVersion: "benchmark-campaign/v1", CreatedAt: time.Now().UTC(), NodeID: *node, Profile: profile.StandardCPU}
	for _, value := range strings.Split(*models, ",") {
		value = strings.TrimSpace(value)
		if value != "" {
			manifest.Models = append(manifest.Models, value)
		}
	}
	if *resume {
		data, e := os.ReadFile(manifestPath)
		if e != nil {
			fmt.Fprintln(stderr, e)
			return 1
		}
		requested := strings.Join(manifest.Models, ",")
		if e = json.Unmarshal(data, &manifest); e != nil || manifest.NodeID != *node || strings.Join(manifest.Models, ",") != requested {
			fmt.Fprintln(stderr, "campaign resume requires the same node and model list")
			return 1
		}
	} else {
		if err = os.MkdirAll(filepath.Dir(*output), 0750); err == nil {
			err = os.Mkdir(*output, 0750)
		}
		if err != nil {
			fmt.Fprintln(stderr, "create new campaign directory:", err)
			return 1
		}
		if err = platform.WriteFile(filepath.Join(*output, "INCOMPLETE"), []byte("Campaign generation or submission is incomplete.\n"), 0600); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err = platform.WriteJSON(manifestPath, manifest, 0600); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}

	api := &client.Client{BaseURL: *controllerURL, AdminToken: *adminToken}
	var catalog []catalogModel
	if _, err = api.DoJSON(ctx, "GET", "/v1/nodes/"+*node+"/models", "admin", nil, &catalog); err != nil {
		fmt.Fprintln(stderr, "get node models:", err)
		return 1
	}
	byID := map[string]catalogModel{}
	for _, model := range catalog {
		byID[model.ID] = model
	}
	for _, modelID := range manifest.Models {
		model, ok := byID[modelID]
		if !ok {
			fmt.Fprintf(stderr, "model %q is not installed on node %s\n", modelID, *node)
			return 1
		}
		plan, e := profile.StandardCPUPlanContext(ctx, profile.Options{NodeID: *node, ExecutionMode: protocol.ExecutionMode(*mode), ModelPath: model.Path, ModelID: model.ID, ModelSHA256: model.SHA256, Binary: *binary, Contexts: contexts, Threads: threads})
		if e != nil {
			fmt.Fprintln(stderr, e)
			return 1
		}
		index := -1
		for i := range manifest.Plans {
			if manifest.Plans[i].ModelID == modelID {
				index = i
				break
			}
		}
		planFile := filepath.Join(*output, plan.Model.ID+"-standard-cpu.json")
		if index >= 0 {
			data, e := os.ReadFile(manifest.Plans[index].PlanFile)
			var saved protocol.Plan
			if e != nil || json.Unmarshal(data, &saved) != nil {
				fmt.Fprintln(stderr, "cannot read saved campaign plan")
				return 1
			}
			a, _ := json.Marshal(plan)
			b, _ := json.Marshal(saved)
			if !bytes.Equal(a, b) {
				fmt.Fprintln(stderr, "resume flags or model/runtime identity differ from the saved plan")
				return 1
			}
		} else {
			if e = platform.WriteJSON(planFile, plan, 0600); e != nil {
				fmt.Fprintln(stderr, e)
				return 1
			}
			requestID, e := newRequestID()
			if e != nil {
				fmt.Fprintln(stderr, e)
				return 1
			}
			manifest.Plans = append(manifest.Plans, campaignPlan{ModelID: modelID, ModelPath: "managed-by-node", PlanFile: planFile, Status: "created", RequestID: requestID})
			if e = platform.WriteJSON(manifestPath, manifest, 0600); e != nil {
				fmt.Fprintln(stderr, e)
				return 1
			}
		}
	}
	if *submit {
		for i := range manifest.Plans {
			entry := &manifest.Plans[i]
			if entry.PlanID != "" {
				continue
			}
			data, e := os.ReadFile(entry.PlanFile)
			var plan protocol.Plan
			if e != nil || json.Unmarshal(data, &plan) != nil {
				fmt.Fprintln(stderr, "cannot read saved plan")
				return 1
			}
			if entry.RequestID == "" {
				fmt.Fprintln(stderr, "legacy campaign lacks stable submission IDs; refusing automatic resubmission")
				return 1
			}
			entry.Status = "submitting"
			if e = platform.WriteJSON(manifestPath, manifest, 0600); e != nil {
				fmt.Fprintln(stderr, e)
				return 1
			}
			var response client.Submission
			if _, e = api.SubmitJSON(ctx, "/v1/plans", entry.RequestID, plan, &response); e != nil {
				fmt.Fprintf(stderr, "submit %s: %v; resume this campaign with --resume and the same flags\n", entry.ModelID, e)
				return 1
			}
			entry.PlanID = response.PlanID
			entry.Status = response.Status
			if e = platform.WriteJSON(manifestPath, manifest, 0600); e != nil {
				fmt.Fprintln(stderr, e)
				return 1
			}
		}
	}
	if err = os.Remove(filepath.Join(*output, "INCOMPLETE")); err != nil && !os.IsNotExist(err) {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "✅ Created campaign with %d plans: %s\n", len(manifest.Plans), manifestPath)
	return 0
}

func campaignStatus(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	f := flag.NewFlagSet("benchmark campaign status", flag.ContinueOnError)
	setFlagOutput(f, stderr)
	filePath := f.String("file", "", "campaign manifest")
	controllerURL := f.String("controller", os.Getenv("MODELGAUGE_CONTROLLER_URL"), "controller URL")
	token := f.String("admin-token", os.Getenv("MODELGAUGE_ADMIN_TOKEN"), "admin token")
	if err := f.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if *filePath == "" || *controllerURL == "" || *token == "" {
		fmt.Fprintln(stderr, "--file, --controller, and --admin-token are required")
		return 2
	}
	file, err := os.Open(*filePath)
	if err != nil {
		fmt.Fprintf(stderr, "open campaign: %v\n", err)
		return 1
	}
	defer file.Close()
	var manifest campaignManifest
	if err := json.NewDecoder(file).Decode(&manifest); err != nil {
		fmt.Fprintf(stderr, "read campaign: %v\n", err)
		return 1
	}
	api := &client.Client{BaseURL: *controllerURL, AdminToken: *token}
	for i := range manifest.Plans {
		if manifest.Plans[i].PlanID == "" {
			continue
		}
		var response struct {
			Status string `json:"status"`
		}
		if _, err := api.DoJSON(ctx, "GET", "/v1/plans/"+manifest.Plans[i].PlanID, "admin", nil, &response); err != nil {
			fmt.Fprintf(stderr, "get plan: %v\n", err)
			return 1
		}
		manifest.Plans[i].Status = response.Status
	}
	return writeJSON(stdout, manifest)
}
