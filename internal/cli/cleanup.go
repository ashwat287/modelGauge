package cli

import (
	"context"
	"flag"
	"fmt"
	"github.com/nerdylucifer/modelgauge/internal/control"
	"io"
	"strings"
	"time"
)

func runCleanup(ctx context.Context, args []string, out, stderr io.Writer) int {
	f := flag.NewFlagSet("cleanup", flag.ContinueOnError)
	setFlagOutput(f, stderr)
	raw := f.Bool("json", false, "structured cleanup report")
	dryRun := f.Bool("dry-run", false, "preview ownership-verified cleanup without changing resources or starting VMs")
	force := f.Bool("force", false, "cancel managed jobs and stop the controller executor before cleanup (dry run makes no changes)")
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
	report, err := control.Cleanup(ctx, control.CleanupOptions{DryRun: *dryRun, DeleteVMs: *deleteVMs, Force: *force})
	if err != nil {
		fmt.Fprintf(stderr, "cleanup refused or interrupted (ownership records retained for retry): %v\n", err)
		if !*force {
			command := "modelgauge cleanup --force"
			if *deleteVMs {
				command += " --delete-vms"
			}
			fmt.Fprintf(stderr, "To cancel managed work before cleanup, retry with: %s\nOwnership checks still apply.\n", command)
		}
		return 1
	}
	if *raw {
		return writeJSON(out, report)
	}
	return printCleanup(out, report)
}

func printCleanup(out io.Writer, report control.CleanupReport) int {
	var text strings.Builder
	if report.DryRun {
		text.WriteString("Dry run: no changes made.\n")
	}
	if len(report.Targets) == 0 {
		text.WriteString("Nothing to clean.\n")
	}
	for _, target := range report.Targets {
		message := ""
		switch target.Status {
		case "already_absent":
			message = "Already absent."
		case "vm_removed":
			message = "VM removed."
		case "managed_resources_removed_vm_preserved":
			message = "Managed files and services removed; VM kept."
		default:
			if report.DryRun {
				message = "Would remove managed files and services; keep VM."
				if target.Delete {
					message = "Would remove managed resources and delete VM."
				}
				if target.Status != "verified" {
					message = "Guest verification required before cleanup."
				}
			} else {
				message = "VM kept."
			}
		}
		fmt.Fprintf(&text, "%s: %s\n", target.Name, message)
		if target.Note != "" {
			fmt.Fprintln(&text, target.Note)
		}
	}
	for _, name := range report.Untracked {
		fmt.Fprintf(&text, "%s: Untracked VM kept.\n", name)
	}
	if !report.DryRun && len(report.Targets) > 0 {
		text.WriteString("Cleanup completed.\n")
	}
	if _, err := io.WriteString(out, text.String()); err != nil {
		return 1
	}
	return 0
}
