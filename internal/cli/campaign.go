package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
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
	SchemaVersion string         `json:"schema_version"`
	CreatedAt     time.Time      `json:"created_at"`
	NodeID        string         `json:"node_id"`
	Profile       string         `json:"profile"`
	Plans         []campaignPlan `json:"plans"`
}
type campaignPlan struct {
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

func runCampaign(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: modelgauge benchmark campaign <create|status>")
		return 2
	}
	if args[0] == "create" {
		return createCampaign(args[1:], stdout, stderr)
	}
	if args[0] == "status" {
		return campaignStatus(args[1:], stdout, stderr)
	}
	fmt.Fprintln(stderr, "usage: modelgauge benchmark campaign <create|status>")
	return 2
}

func createCampaign(args []string, stdout, stderr io.Writer) int {
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
	if err := os.MkdirAll(*output, 0o750); err != nil {
		fmt.Fprintf(stderr, "create campaign: %v\n", err)
		return 1
	}
	manifest := campaignManifest{SchemaVersion: "benchmark-campaign/v1", CreatedAt: time.Now().UTC(), NodeID: *node, Profile: profile.StandardCPU}
	api := &client.Client{BaseURL: *controllerURL, AdminToken: *adminToken}
	var catalog []catalogModel
	if _, err := api.DoJSON(context.Background(), "GET", "/v1/nodes/"+*node+"/models", "admin", nil, &catalog); err != nil {
		fmt.Fprintf(stderr, "get node models: %v\n", err)
		return 1
	}
	byID := map[string]catalogModel{}
	for _, model := range catalog {
		byID[model.ID] = model
	}
	for _, modelID := range strings.Split(*models, ",") {
		modelID = strings.TrimSpace(modelID)
		if modelID == "" {
			continue
		}
		model, ok := byID[modelID]
		if !ok {
			fmt.Fprintf(stderr, "model %q is not available on node %s; use modelgauge model list --node %s\n", modelID, *node, *node)
			return 1
		}
		plan, err := profile.StandardCPUPlan(profile.Options{NodeID: *node, ExecutionMode: protocol.ExecutionMode(*mode), ModelPath: model.Path, ModelID: model.ID, ModelSHA256: model.SHA256, Binary: *binary, Contexts: contexts, Threads: threads})
		if err != nil {
			fmt.Fprintf(stderr, "create %s: %v\n", modelID, err)
			return 1
		}
		planFile := filepath.Join(*output, plan.Model.ID+"-standard-cpu.json")
		file, err := os.OpenFile(planFile, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
		if err != nil {
			fmt.Fprintf(stderr, "write plan: %v\n", err)
			return 1
		}
		if writeJSON(file, plan) != 0 {
			_ = file.Close()
			return 1
		}
		_ = file.Close()
		entry := campaignPlan{ModelID: plan.Model.ID, ModelPath: "managed-by-node", PlanFile: planFile, Status: "created"}
		if *submit {
			var response struct {
				PlanID string `json:"plan_id"`
				Status string `json:"status"`
			}
			if _, err := api.DoJSON(context.Background(), "POST", "/v1/plans", "admin", plan, &response); err != nil {
				fmt.Fprintf(stderr, "submit %s: %v\n", plan.Model.ID, err)
				return 1
			}
			entry.PlanID, entry.Status = response.PlanID, response.Status
		}
		manifest.Plans = append(manifest.Plans, entry)
	}
	file, err := os.OpenFile(filepath.Join(*output, "campaign.json"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		fmt.Fprintf(stderr, "write campaign: %v\n", err)
		return 1
	}
	defer file.Close()
	if writeJSON(file, manifest) != 0 {
		return 1
	}
	fmt.Fprintf(stdout, "✅ Created campaign with %d plans: %s\n", len(manifest.Plans), filepath.Join(*output, "campaign.json"))
	return 0
}

func campaignStatus(args []string, stdout, stderr io.Writer) int {
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
		if _, err := api.DoJSON(context.Background(), "GET", "/v1/plans/"+manifest.Plans[i].PlanID, "admin", nil, &response); err != nil {
			fmt.Fprintf(stderr, "get plan: %v\n", err)
			return 1
		}
		manifest.Plans[i].Status = response.Status
	}
	return writeJSON(stdout, manifest)
}
