package cli

import (
	"context"
	"io"
)

// Run supplies a context for CLI tests; production callers use RunContext or RunWithSignals.
func Run(args []string, out, stderr io.Writer) int {
	return RunContext(context.Background(), args, out, stderr)
}
