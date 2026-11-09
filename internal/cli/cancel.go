package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
)
import "github.com/nerdylucifer/modelgauge/internal/client"

func cancelJob(ctx context.Context, args []string, out, stderr io.Writer, install bool) int {
	id := ""
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		id = args[0]
		args = args[1:]
	}
	f := flag.NewFlagSet("cancel", flag.ContinueOnError)
	setFlagOutput(f, stderr)
	plan := f.String("plan", id, "plan ID")
	controllerURL := f.String("controller", os.Getenv("MODELGAUGE_CONTROLLER_URL"), "controller URL")
	token := f.String("admin-token", os.Getenv("MODELGAUGE_ADMIN_TOKEN"), "admin token")
	if err := f.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if *plan == "" || *controllerURL == "" || *token == "" {
		fmt.Fprintln(stderr, "job ID and controller connection are required")
		return 2
	}
	path := "/v1/plans/" + *plan + "/cancel"
	if install {
		path = "/v1/model-installs/" + *plan + "/cancel"
	}
	api := &client.Client{BaseURL: *controllerURL, AdminToken: *token}
	var response any
	if _, err := api.DoJSON(ctx, "POST", path, "admin", nil, &response); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeJSON(out, response)
}
