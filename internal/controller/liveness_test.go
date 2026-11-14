package controller

import (
	"context"
	"testing"

	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

func TestNodeStatusExpiresStaleHeartbeat(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.RegisterNode(ctx, Node{ID: "node", DisplayName: "node", Kind: "virtual", ExecutionMode: protocol.ExecutionModeLima}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE nodes SET last_seen_at=now()-interval '2 minutes' WHERE node_id='node'`); err != nil {
		t.Fatal(err)
	}
	nodes, err := s.ListNodes(ctx)
	if err != nil || len(nodes) != 1 || nodes[0].Status != "offline" {
		t.Fatal(nodes, err)
	}
	if err = s.Heartbeat(ctx, "node", "busy"); err != nil {
		t.Fatal(err)
	}
	nodes, err = s.ListNodes(ctx)
	if err != nil || nodes[0].Status != "busy" {
		t.Fatal(nodes, err)
	}
}
