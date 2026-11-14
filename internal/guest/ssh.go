package guest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nerdylucifer/modelgauge/internal/platform"
	"golang.org/x/crypto/ssh"
)

// Only the exact controller key recorded in our managed directory may be removed.
func (r Runtime) RemoveControllerKey() error {
	recorded := filepath.Join(r.Home, ".local/share/modelgauge/ssh/authorized-key")
	key, err := os.ReadFile(recorded)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return r.replaceControllerKey(key, nil)
}
func (r Runtime) InstallControllerKey(ctx context.Context, public []byte) error {
	key, _, _, _, err := ssh.ParseAuthorizedKey(public)
	if err != nil {
		return err
	}
	unlock, err := r.lock(".local/share/modelgauge/locks/execution.lock", true)
	if err != nil {
		return err
	}
	defer unlock()
	binary := filepath.Join(r.Home, ".local/bin/modelgauge")
	command := "'" + strings.ReplaceAll(binary, "'", "'\\''") + "' _guest connection"
	command = strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(command)
	line := []byte(`restrict,command="` + command + `" ` + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))) + " modelgauge-controller\n")
	recorded := filepath.Join(r.Home, ".local/share/modelgauge/ssh/authorized-key")
	previous, err := os.ReadFile(recorded)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	// Save both keys first: recovery/cleanup can remove either if the write is interrupted.
	if err = os.MkdirAll(filepath.Dir(recorded), 0700); err != nil {
		return err
	}
	if err = platform.WriteFile(recorded, append(append([]byte{}, previous...), line...), 0600); err != nil {
		return err
	}
	if err = r.replaceControllerKey(previous, append(append([]byte{}, previous...), line...)); err != nil {
		return err
	}
	return nil
}
func (r Runtime) CommitControllerKey() error {
	path := filepath.Join(r.Home, ".local/share/modelgauge/ssh/authorized-key")
	previous, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := bytes.Split(bytes.TrimSuffix(previous, []byte("\n")), []byte("\n"))
	if len(lines) == 0 {
		return errors.New("missing controller SSH key")
	}
	latest := append(append([]byte{}, lines[len(lines)-1]...), '\n')
	if err = r.replaceControllerKey(previous, latest); err != nil {
		return err
	}
	return platform.WriteFile(path, latest, 0600)
}
func (r Runtime) replaceControllerKey(previous, next []byte) error {
	directory := filepath.Join(r.Home, ".ssh")
	path := filepath.Join(directory, "authorized_keys")
	if info, err := os.Lstat(directory); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("SSH directory must be a real directory")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if info, e := os.Lstat(path); e == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return errors.New("authorized_keys must be a regular file")
	}
	remove := map[string]bool{}
	for _, line := range bytes.Split(previous, []byte("\n")) {
		if len(line) > 0 {
			if !bytes.Contains(line, []byte(" modelgauge-controller")) {
				return fmt.Errorf("invalid recorded controller SSH key")
			}
			remove[string(line)] = true
		}
	}
	var result []byte
	for _, line := range bytes.SplitAfter(data, []byte("\n")) {
		if !remove[string(bytes.TrimSuffix(line, []byte("\n")))] {
			result = append(result, line...)
		}
	}
	if len(next) > 0 && len(result) > 0 && result[len(result)-1] != '\n' {
		result = append(result, '\n')
	}
	result = append(result, next...)
	return platform.WriteFile(path, result, 0600)
}
