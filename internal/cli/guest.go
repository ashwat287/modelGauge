package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/nerdylucifer/modelgauge/internal/guest"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
	"io"
	"os"
	"strconv"
)

// Internal Linux protocol. JSON and tar stdout never contain human banners.
func runGuest(ctx context.Context, args []string, out, stderr io.Writer) int {
	if len(args) == 2 && args[0] == "remove-file" {
		runtime, err := guest.NewRuntime()
		if err == nil {
			err = runtime.RemoveManaged(ctx, args[1])
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	if len(args) == 3 && args[0] == "write-file" {
		runtime, err := guest.NewRuntime()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		mode, err := strconv.ParseUint(args[2], 8, 32)
		if err == nil {
			err = runtime.WriteManaged(ctx, args[1], os.FileMode(mode), os.Stdin)
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	if len(args) != 1 {
		fmt.Fprintln(stderr, "internal guest operation required")
		return 2
	}
	runtime, err := guest.NewRuntime()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	decoder := json.NewDecoder(io.LimitReader(os.Stdin, 4<<20))
	decoder.DisallowUnknownFields()
	switch args[0] {
	case "ownership":
		var request guest.Request
		if err = decoder.Decode(&request); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		state, e := runtime.Execute(ctx, request)
		if e != nil {
			fmt.Fprintln(stderr, e)
			return 1
		}
		return writeJSON(out, state)
	case "evidence":
		var refs []protocol.EvidenceRef
		if err = decoder.Decode(&refs); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err = runtime.WriteEvidence(ctx, refs, out); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	default:
		fmt.Fprintln(stderr, "unknown internal guest operation")
		return 2
	}
}
