package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/nerdylucifer/modelgauge/internal/evidence"
	"github.com/nerdylucifer/modelgauge/internal/guest"
	"github.com/nerdylucifer/modelgauge/internal/platform"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
	"io"
	"net/http"
	"strings"
)

// CopyLimaEvidence reads only manifest-listed files. It holds the ownership lock
// through transfer so cleanup cannot remove a node while evidence is copied.
func CopyLimaEvidence(ctx context.Context, node string, refs []protocol.EvidenceRef, destination string) error {
	if RemoteTargetTracked() {
		c, err := Load()
		if err != nil {
			return err
		}
		return CopyRemoteEvidence(ctx, c, refs, destination)
	}
	if len(refs) > 10000 {
		return fmt.Errorf("too many raw evidence files")
	}
	for _, ref := range refs {
		if _, err := evidence.Relative(ref); err != nil {
			return err
		}
	}
	m, err := newOwnershipManager()
	if err != nil {
		return err
	}
	registry, err := m.load()
	if err != nil {
		return err
	}
	var owned *ownedVM
	for i := range registry.VMs {
		if registry.VMs[i].Name == node {
			owned = &registry.VMs[i]
			break
		}
	}
	if owned == nil || owned.Role != "benchmark" || owned.Cleaned {
		return fmt.Errorf("raw export needs a tracked, running Lima benchmark node; use --raw-from for copied/manual evidence")
	}
	err = m.prepareDirectory()
	if err != nil {
		return err
	}
	instances, err := m.instances(ctx)
	if err != nil {
		return err
	}
	instance, present := instances[node]
	if !present || instance.Status != "Running" {
		return fmt.Errorf("raw evidence VM must already be running")
	}
	if err = verifyVM(*owned, instance); err != nil {
		return err
	}
	if _, err = m.guest(ctx, *owned, "identity", nil); err != nil {
		return err
	}
	data, err := json.Marshal(refs)
	if err != nil {
		return err
	}
	script := `set -eu
helper="$HOME/` + guest.HelperRelative + `"
for directory in "$HOME/.local" "$HOME/.local/share" "$HOME/.local/share/modelgauge"; do test ! -L "$directory"; done
test ! -L "$helper"
actual=$(sha256sum "$helper")
test "${actual%% *}" = ` + quote(owned.HelperSHA256) + `
exec "$helper" _guest evidence
`
	command := platform.CommandContext(ctx, "limactl", "shell", "--workdir", "/", node, "--", "sh", "-c", script)
	command.Stdin = bytes.NewReader(data)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	if err = command.Start(); err != nil {
		return err
	}
	receiveErr := evidence.ReceiveArchiveContext(ctx, output, destination, refs)
	_ = output.Close()
	if receiveErr != nil {
		_ = command.Process.Kill()
	}
	waitErr := command.Wait()
	if receiveErr != nil {
		return receiveErr
	}
	if waitErr != nil {
		return fmt.Errorf("raw evidence transfer failed: %w: %s", waitErr, stderr.String())
	}
	return nil
}

// RemoteEvidence keeps raw transfer behind the same controller-owned SSH route.
func RemoteEvidence(ctx context.Context, directory string, refs []protocol.EvidenceRef, out io.Writer) error {
	if len(refs) > 10000 {
		return fmt.Errorf("too many raw evidence files")
	}
	for _, ref := range refs {
		if _, err := evidence.Relative(ref); err != nil {
			return err
		}
	}
	host, err := loadRemote(directory)
	if err != nil {
		return err
	}
	return host.Host.Command(ctx, "manage", remoteHostRequest{Operation: "evidence", Evidence: refs}, out, io.Discard)
}
func CopyRemoteEvidence(ctx context.Context, c Config, refs []protocol.EvidenceRef, destination string) error {
	data, err := json.Marshal(refs)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.ControllerURL, "/")+"/v1/remote/evidence", bytes.NewReader(data))
	if err != nil {
		return err
	}
	request.Header.Set("X-ModelGauge-Admin-Token", c.AdminToken)
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("remote evidence: %s", response.Status)
	}
	return evidence.ReceiveArchiveContext(ctx, response.Body, destination, refs)
}
