package cli

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nerdylucifer/modelgauge/internal/controller"
	"github.com/nerdylucifer/modelgauge/internal/server"
)

func runServer(args []string, stdout, stderr interface{ Write([]byte) (int, error) }) int {
	flags := flag.NewFlagSet("server", flag.ContinueOnError)
	setFlagOutput(flags, stderr)
	listenAddress := flags.String("listen", ":8080", "HTTP listen address")
	migrate := flags.Bool("migrate", true, "apply embedded database migrations at startup")
	if err := flags.Parse(args); err != nil {
		return flagExitCode(err)
	}

	databaseURL := server.DatabaseURLFromEnv(os.Getenv)
	if databaseURL == "" {
		fmt.Fprintln(stderr, "MODELGAUGE_DATABASE_URL or DATABASE_HOST, DATABASE_NAME, and DATABASE_USER are required")
		return 2
	}
	agentToken := server.AgentTokenFromEnv(os.Getenv)
	if agentToken == "" {
		fmt.Fprintln(stderr, "MODELGAUGE_AGENT_TOKEN is required")
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	store, err := controller.Open(ctx, databaseURL)
	if err != nil {
		fmt.Fprintf(stderr, "open controller store: %v\n", err)
		return 1
	}
	defer store.Close()
	if *migrate {
		if err := store.Migrate(ctx); err != nil {
			fmt.Fprintf(stderr, "migrate database: %v\n", err)
			return 1
		}
	}

	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	reapCtx, cancelReap := context.WithCancel(ctx)
	reapDone := make(chan struct{})
	go func() {
		defer close(reapDone)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-reapCtx.Done():
				return
			case <-ticker.C:
				if err := store.ReapExpired(reapCtx); err != nil && reapCtx.Err() == nil {
					logger.Error("expire abandoned job leases", "error", err)
				}
			}
		}
	}()
	defer func() { cancelReap(); <-reapDone }()
	api := server.New(store, server.Config{
		AgentToken: agentToken,
		AdminToken: server.AdminTokenFromEnv(os.Getenv),
		Logger:     logger,
	})
	fmt.Fprintf(stdout, "modelgauge controller listening on %s\n", *listenAddress)
	if err := server.ListenAndServe(ctx, *listenAddress, api.Handler()); err != nil {
		fmt.Fprintf(stderr, "serve controller: %v\n", err)
		return 1
	}
	return 0
}
