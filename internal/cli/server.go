package cli

import (
	"context"
	"flag"
	"fmt"
	"github.com/nerdylucifer/modelgauge/internal/agent"
	"github.com/nerdylucifer/modelgauge/internal/controller"
	"github.com/nerdylucifer/modelgauge/internal/server"
	"io"
	"os"
)

func runServer(ctx context.Context, args []string, out, stderr io.Writer) int {
	f := flag.NewFlagSet("server", flag.ContinueOnError)
	setFlagOutput(f, stderr)
	listen := f.String("listen", ":8080", "HTTP listen address")
	directory := f.String("data-dir", os.Getenv("MODELGAUGE_DATA_DIR"), "configuration and completed-result directory")
	if err := f.Parse(args); err != nil {
		return flagExitCode(err)
	}
	token := os.Getenv("MODELGAUGE_ADMIN_TOKEN")
	if token == "" || *directory == "" {
		fmt.Fprintln(stderr, "MODELGAUGE_ADMIN_TOKEN and --data-dir (or MODELGAUGE_DATA_DIR) are required")
		return 2
	}
	store, err := controller.Open(ctx, *directory)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	executor, err := agent.NewControllerExecutor(ctx, *directory)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer executor.Close()
	api := server.New(store, server.Config{Executor: executor, AdminToken: token, DataDir: *directory})
	fmt.Fprintln(out, "ModelGauge controller listening on", *listen)
	if err = server.ListenAndServe(ctx, *listen, api.Handler()); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
