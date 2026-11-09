package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestServiceInstallRestoresPreviousFilesAndState(t *testing.T) {
	home := t.TempDir()
	config := ConfigPath(home)
	unit := filepath.Join(home, ".config/systemd/user/modelgauge-agent.service")
	for _, p := range []string{config, unit} {
		_ = os.MkdirAll(filepath.Dir(p), 0700)
		_ = os.WriteFile(p, []byte("previous"), 0600)
	}
	failed := false
	restarted := false
	run := func(ctx context.Context, args ...string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if args[0] == "enable" && len(args) > 1 && args[1] == "--now" && !failed {
			failed = true
			return errors.New("activation failed")
		}
		if args[0] == "start" {
			restarted = true
		}
		return nil
	}
	err := installServiceTransaction(context.Background(), home, InstallOptions{}, []byte("new config"), []byte("new unit"), run)
	if err == nil || !failed {
		t.Fatal("activation failure hidden")
	}
	for _, p := range []string{config, unit} {
		data, _ := os.ReadFile(p)
		if string(data) != "previous" {
			t.Fatal("previous service file not restored")
		}
	}
	if !restarted {
		t.Fatal("previous active service not restarted")
	}
	if _, err = os.Stat(serviceJournal(home)); !os.IsNotExist(err) {
		t.Fatal("successful rollback retained transaction")
	}
}
func TestInterruptedServiceInstallUsesRecoveryContext(t *testing.T) {
	home := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	run := func(commandCtx context.Context, args ...string) error {
		calls++
		if args[0] == "enable" && len(args) > 1 && args[1] == "--now" {
			cancel()
			return ctx.Err()
		}
		return commandCtx.Err()
	}
	err := installServiceTransaction(ctx, home, InstallOptions{}, []byte("config"), []byte("unit"), run)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = os.Stat(ConfigPath(home)); !os.IsNotExist(err) {
		t.Fatal("new configuration retained after rollback")
	}
	if calls < 3 {
		t.Fatal("recovery commands were not executed")
	}
}
