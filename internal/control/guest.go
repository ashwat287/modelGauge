package control

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nerdylucifer/modelgauge/internal/guest"
	"github.com/nerdylucifer/modelgauge/internal/platform"
)

func helperPath(v ownedVM) string  { return filepath.Join(v.Dir, ".modelgauge-helper-"+v.ID) }
func helperSum(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func (m *ownershipManager) readHelper(v ownedVM) ([]byte, error) {
	if v.HelperSHA256 == "" || v.HelperArch == "" {
		return nil, fmt.Errorf("no Go guest helper recorded; bootstrap with the current CLI first")
	}
	path := helperPath(v)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("guest helper cache is not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if helperSum(data) != v.HelperSHA256 {
		return nil, fmt.Errorf("guest helper cache checksum mismatch")
	}
	return data, nil
}
func (m *ownershipManager) prepareHelper(ctx context.Context, v *ownedVM) error {
	if m.binary == nil {
		return fmt.Errorf("provide source or a matching Linux --helper-binary for guest bootstrap")
	}
	raw, err := m.run(ctx, []string{"shell", "--workdir", "/", v.Name, "--", "uname", "-m"}, nil)
	if err != nil {
		return err
	}
	arch := strings.TrimSpace(string(raw))
	switch arch {
	case "aarch64", "arm64":
		arch = "arm64"
	case "x86_64", "amd64":
		arch = "amd64"
	default:
		return fmt.Errorf("unsupported guest architecture")
	}
	data, err := m.binary(ctx, arch)
	if err != nil {
		return err
	}
	if exists := func() bool { _, e := os.Lstat(helperPath(*v)); return e == nil }(); exists {
		if _, err = m.readHelper(*v); err != nil {
			return err
		}
	}
	file, err := os.CreateTemp(v.Dir, ".guest-upload-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	closed := file.Close()
	if err != nil {
		return err
	}
	if closed != nil {
		return closed
	}
	if err = os.Rename(file.Name(), helperPath(*v)); err != nil {
		return err
	}
	v.HelperSHA256 = helperSum(data)
	v.HelperArch = arch
	return nil
}
func (m *ownershipManager) removeHelper(v ownedVM) error {
	if _, err := os.Lstat(helperPath(v)); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if _, err := m.readHelper(v); err != nil {
		return err
	}
	return os.Remove(helperPath(v))
}

// Stage the same Linux executable into an exclusive temporary guest directory
// only when no persistent helper exists yet (prepare / previously cleaned VM).
// The fixed tar member names never come from user input. The trap removes only
// those two files and the directory this invocation created.
func helperInvocation(v ownedVM, operation string, payload, binary []byte) ([]string, []byte, error) {
	if operation != "prepare" && operation != "bootstrap-prepare" && operation != "inspect-cleanup" && v.Prepared && !v.Cleaned {
		script := `set -eu
helper="$HOME/` + guest.HelperRelative + `"
for directory in "$HOME/.local" "$HOME/.local/share" "$HOME/.local/share/modelgauge"; do
 test ! -L "$directory"
done
test ! -L "$helper"
actual=$(sha256sum "$helper")
test "${actual%% *}" = ` + quote(v.HelperSHA256) + `
exec "$helper" _guest ownership
`
		return []string{"shell", "--workdir", "/", v.Name, "--", "sh", "-c", script}, payload, nil
	}
	var envelope bytes.Buffer
	writer := tar.NewWriter(&envelope)
	for _, file := range []struct {
		name string
		data []byte
		mode int64
	}{{"modelgauge", binary, 0700}, {"request.json", payload, 0600}} {
		if err := writer.WriteHeader(&tar.Header{Name: file.name, Mode: file.mode, Size: int64(len(file.data)), Typeflag: tar.TypeReg}); err != nil {
			return nil, nil, err
		}
		if _, err := writer.Write(file.data); err != nil {
			return nil, nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, nil, err
	}
	script := `set -eu
umask 077
helper_dir=$(mktemp -d /tmp/modelgauge-guest.XXXXXX)
trap 'rm -f "$helper_dir/modelgauge" "$helper_dir/request.json"; rmdir "$helper_dir"' EXIT
trap 'exit 130' INT
trap 'exit 143' HUP TERM
tar -xf - -C "$helper_dir"
"$helper_dir/modelgauge" _guest ownership < "$helper_dir/request.json"
`
	return []string{"shell", "--workdir", "/", v.Name, "--", "sh", "-c", script}, envelope.Bytes(), nil
}
func managedBinary(ctx context.Context, name, arch string) ([]byte, error) {
	m, ok := ctx.Value(ownershipContextKey{}).(*ownershipManager)
	if !ok {
		return nil, fmt.Errorf("ownership context missing")
	}
	registry, err := m.load()
	if err != nil {
		return nil, err
	}
	for _, v := range registry.VMs {
		if v.Name == name {
			if v.HelperArch != arch {
				return nil, fmt.Errorf("guest helper architecture changed")
			}
			return m.readHelper(v)
		}
	}
	return nil, fmt.Errorf("guest helper is not registered")
}

func ownershipPayload(v ownedVM, operation string, files []string) ([]byte, error) {
	checkIdle := operation == "bootstrap-prepare"
	if operation == "inspect-cleanup" {
		operation = "inspect"
	}
	if checkIdle {
		operation = "prepare"
	}
	return json.Marshal(guest.Request{CheckIdle: checkIdle, ID: v.ID, Role: v.Role, Operation: operation, Files: files, Cleaned: v.Cleaned, Prepared: v.Prepared, Unprepared: !v.Prepared, MachineID: v.MachineID})
}

// Failed preparation must not replace a working cache when the guest still uses
// the previously verified helper. A newly installed helper remains checkpointed.
func (m *ownershipManager) restorePreviousHelper(ctx context.Context, prior ownedVM, data []byte) error {
	script := `set -eu
for path in "$HOME/.local" "$HOME/.local/share" "$HOME/.local/share/modelgauge" "$HOME/.local/share/modelgauge/guest-helper"; do test ! -L "$path"; done
sha256sum "$HOME/.local/share/modelgauge/guest-helper"
`
	raw, err := m.run(ctx, []string{"shell", "--workdir", "/", prior.Name, "--", "sh", "-c", script}, nil)
	if err != nil {
		return fmt.Errorf("could not reconcile guest helper after failed preparation: %w", err)
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return fmt.Errorf("guest helper checksum unavailable")
	}
	if fields[0] != prior.HelperSHA256 {
		return nil
	}
	if err = platform.WriteFile(helperPath(prior), data, 0600); err != nil {
		return err
	}
	registry, err := m.load()
	if err != nil {
		return err
	}
	for i := range registry.VMs {
		if registry.VMs[i].ID == prior.ID {
			registry.VMs[i].HelperSHA256 = prior.HelperSHA256
			registry.VMs[i].HelperArch = prior.HelperArch
		}
	}
	return m.save(registry)
}
