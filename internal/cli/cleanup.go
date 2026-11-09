package cli

import (
	"context"
	"flag"
	"fmt"
	"github.com/nerdylucifer/modelgauge/internal/control"
	"io"
	"time"
)

func runCleanup(ctx context.Context, args []string, out, stderr io.Writer) int {
	f := flag.NewFlagSet("cleanup", flag.ContinueOnError)
	setFlagOutput(f, stderr)
	dryRun := f.Bool("dry-run", false, "preview ownership-verified cleanup without changing resources or starting VMs")
	deleteVMs := f.Bool("delete-vms", false, "also delete VMs created by ModelGauge, including everything on their disks")
	if err := f.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if f.NArg() != 0 {
		fmt.Fprintln(stderr, "cleanup takes flags only")
		return 2
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if !*dryRun {

	}
	report, err := control.Cleanup(ctx, control.CleanupOptions{DryRun: *dryRun, DeleteVMs: *deleteVMs})
	if err != nil {
		fmt.Fprintf(stderr, "cleanup refused or interrupted (ownership records retained for retry): %v\n", err)
		return 1
	}
	return writeJSON(out, report)
}
