package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/nerdylucifer/modelgauge/internal/client"
)

type campaignManifest struct {
	Settings      json.RawMessage `json:"settings,omitempty"`
	Models        []string        `json:"models,omitempty"`
	SchemaVersion string          `json:"schema_version"`
	CreatedAt     time.Time       `json:"created_at"`
	NodeID        string          `json:"node_id"`
	Profile       string          `json:"profile"`
	Plans         []campaignPlan  `json:"plans"`
}
type campaignPlan struct {
	RequestID string `json:"request_id,omitempty"`
	ModelID   string `json:"model_id"`
	ModelPath string `json:"model_path"`
	PlanFile  string `json:"plan_file"`
	PlanID    string `json:"plan_id,omitempty"`
	Status    string `json:"status"`
}

func runCampaign(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: modelgauge benchmark campaign status --file historical-campaign.json; create new experiments with apply -f benchmark.yaml")
		return 2
	}
	if args[0] == "create" {
		fmt.Fprintln(stderr, "campaign create was replaced by modelgauge apply -f benchmark.yaml")
		return 2
	}
	if args[0] == "status" {
		return campaignStatus(ctx, args[1:], stdout, stderr)
	}
	fmt.Fprintln(stderr, "usage: modelgauge benchmark campaign status --file historical-campaign.json")
	return 2
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
