package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nerdylucifer/modelgauge/internal/guest"
	"github.com/nerdylucifer/modelgauge/internal/platform"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const serviceName = "modelgauge-agent"

type InstallOptions struct {
	Config   Config
	Interval string
	Binary   string
}

type ServiceStatus struct {
	Installed bool   `json:"installed"`
	Active    bool   `json:"active"`
	Manager   string `json:"manager"`
	Config    string `json:"config"`
}

func ConfigPath(home string) string {
	return filepath.Join(home, ".config", "modelgauge", "agent.env")
}

func InstallContext(ctx context.Context, options InstallOptions) (ServiceStatus, error) {
	if runtime.GOOS != "linux" {
		return ServiceStatus{}, fmt.Errorf("agent installation requires Linux; on a Mac, install inside a Lima Linux VM")
	}
	if err := validateServiceValue(options.Config.ControllerURL); err != nil {
		return ServiceStatus{}, err
	}
	if err := validateServiceValue(options.Config.Token); err != nil {
		return ServiceStatus{}, err
	}
	if options.Interval == "" {
		options.Interval = "30s"
	}
	if options.Binary == "" {
		var err error
		options.Binary, err = os.Executable()
		if err != nil {
			return ServiceStatus{}, err
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ServiceStatus{}, err
	}
	for _, value := range []string{options.Binary, options.Interval, options.Config.NodeID, options.Config.DisplayName, options.Config.NodeKind, string(options.Config.ExecutionMode), options.Config.ResultsDir} {
		if err := validateServiceValue(value); err != nil {
			return ServiceStatus{}, err
		}
	}
	configPath := ConfigPath(home)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return ServiceStatus{}, err
	}
	content := strings.Join([]string{
		"MODELGAUGE_CONTROLLER_URL=" + options.Config.ControllerURL,
		"MODELGAUGE_AGENT_TOKEN=" + options.Config.Token,
		"MODELGAUGE_NODE_ID=" + options.Config.NodeID,
		"MODELGAUGE_NODE_NAME=" + options.Config.DisplayName,
		"MODELGAUGE_NODE_KIND=" + options.Config.NodeKind,
		"MODELGAUGE_EXECUTION_MODE=" + string(options.Config.ExecutionMode),
		"MODELGAUGE_RESULTS_DIR=" + options.Config.ResultsDir,
		"",
	}, "\n")
	unit := fmt.Sprintf("[Unit]\nDescription=ModelGauge benchmark agent\nAfter=network-online.target\n[Service]\nType=simple\nEnvironmentFile=%s\nExecStart=%s agent run --interval %s\nRestart=always\nRestartSec=10\n[Install]\nWantedBy=default.target\n", configPath, options.Binary, options.Interval)
	run := func(ctx context.Context, args ...string) error {
		return platform.CommandContext(ctx, "systemctl", append([]string{"--user"}, args...)...).Run()
	}
	if err := installServiceTransaction(ctx, home, options, []byte(content), []byte(unit), run); err != nil {
		return ServiceStatus{}, err
	}
	return StatusContext(ctx)
}

func StatusContext(ctx context.Context) (ServiceStatus, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return ServiceStatus{}, err
	}
	configPath := ConfigPath(home)
	status := ServiceStatus{Config: configPath}
	if runtime.GOOS == "linux" {
		status.Manager = "systemd-user"
		unit := filepath.Join(home, ".config", "systemd", "user", serviceName+".service")
		_, statErr := os.Stat(unit)
		status.Installed = statErr == nil
		if status.Installed {
			status.Active = platform.CommandContext(ctx, "systemctl", "--user", "is-active", "--quiet", serviceName).Run() == nil
		}
		if err := ctx.Err(); err != nil {
			return ServiceStatus{}, err
		}
		return status, nil
	}
	return ServiceStatus{}, fmt.Errorf("agent status requires Linux; on a Mac, run inside a Lima Linux VM")
}

func UninstallContext(ctx context.Context) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if runtime.GOOS == "linux" {
		if _, e := os.Lstat(filepath.Join(home, ".config/systemd/user/modelgauge-agent.service")); os.IsNotExist(e) {
			return platform.CommandContext(ctx, "systemctl", "--user", "daemon-reload").Run()
		}
		if err := platform.CommandContext(ctx, "systemctl", "--user", "disable", "--now", serviceName).Run(); err != nil {
			return err
		}
		if err := removeServiceFile(ctx, home, filepath.Join(home, ".config", "systemd", "user", serviceName+".service")); err != nil && !os.IsNotExist(err) {
			return err
		}
		return platform.CommandContext(ctx, "systemctl", "--user", "daemon-reload").Run()
	}
	return fmt.Errorf("agent uninstall requires Linux")
}

func validateServiceValue(value string) error {
	if strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("service configuration values cannot contain newlines")
	}
	return nil
}

type serviceFile struct {
	Path     string `json:"path"`
	Before   []byte `json:"before,omitempty"`
	Existed  bool   `json:"existed"`
	Mode     uint32 `json:"mode"`
	Intended string `json:"intended_sha256"`
}
type serviceChange struct {
	Files   []serviceFile `json:"files"`
	Active  bool          `json:"active"`
	Enabled bool          `json:"enabled"`
}
type serviceRunner func(context.Context, ...string) error

func serviceJournal(home string) string {
	return filepath.Join(home, ".config/modelgauge/.service-change.json")
}
func serviceDigest(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }
func managedWrite(ctx context.Context, home, path string, data []byte, mode os.FileMode) error {
	relative, err := filepath.Rel(home, path)
	if err != nil {
		return err
	}
	if relative == ".config/systemd/user/modelgauge-agent.service" {
		if _, e := os.Lstat(filepath.Join(home, ".local/share/modelgauge/.ownership.json")); e == nil {
			r, e := guest.NewRuntime()
			if e != nil {
				return e
			}
			return r.WriteManaged(ctx, relative, mode, bytes.NewReader(data))
		}
	}
	if info, e := os.Lstat(path); e == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return fmt.Errorf("service file is not regular")
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return platform.WriteFile(path, data, mode)
}
func restoreService(ctx context.Context, home string, change serviceChange, run serviceRunner) error {
	// Verify every file before changing any. No recovery can adopt a replacement.
	for _, file := range change.Files {
		data, err := os.ReadFile(file.Path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if serviceDigest(data) != file.Intended && (!file.Existed || !bytes.Equal(data, file.Before)) {
			return fmt.Errorf("service recovery conflict: %s", filepath.Base(file.Path))
		}
	}
	if _, e := os.Stat(filepath.Join(home, ".config/systemd/user/modelgauge-agent.service")); e == nil {
		if err := run(ctx, "stop", serviceName); err != nil {
			return err
		}
		if err := run(ctx, "disable", serviceName); err != nil {
			return err
		}
	}
	for _, file := range change.Files {
		if file.Existed {
			if err := managedWrite(ctx, home, file.Path, file.Before, os.FileMode(file.Mode)); err != nil {
				return err
			}
		} else {
			if err := removeServiceFile(ctx, home, file.Path); err != nil {
				return err
			}
		}
	}
	if err := run(ctx, "daemon-reload"); err != nil {
		return err
	}
	if change.Enabled {
		if err := run(ctx, "enable", serviceName); err != nil {
			return err
		}
	} else if change.Files[1].Existed {
		if err := run(ctx, "disable", serviceName); err != nil {
			return err
		}
	}
	if change.Active {
		if err := run(ctx, "start", serviceName); err != nil {
			return err
		}
	}
	return os.Remove(serviceJournal(home))
}
func removeServiceFile(ctx context.Context, home, path string) error {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	}
	relative, _ := filepath.Rel(home, path)
	if relative == ".config/systemd/user/modelgauge-agent.service" {
		if _, err := os.Lstat(filepath.Join(home, ".local/share/modelgauge/.ownership.json")); err == nil {
			r, err := guest.NewRuntime()
			if err != nil {
				return err
			}
			return r.RemoveManaged(ctx, relative)
		}
	}
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("unsafe service file removal")
	}
	return os.Remove(path)
}
func installServiceTransaction(ctx context.Context, home string, options InstallOptions, config, unit []byte, run serviceRunner) (resultErr error) {
	unlock, err := platform.Lock("agent-service")
	if err != nil {
		return err
	}
	defer unlock()
	if data, err := os.ReadFile(serviceJournal(home)); err == nil {
		var previous serviceChange
		if json.Unmarshal(data, &previous) != nil {
			return fmt.Errorf("invalid service recovery checkpoint")
		}
		if err = validateServiceChange(home, previous); err != nil {
			return err
		}
		recovery, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err = restoreService(recovery, home, previous, run)
		cancel()
		if err != nil {
			return fmt.Errorf("recover previous service installation: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	change := serviceChange{}
	paths := []string{ConfigPath(home), filepath.Join(home, ".config/systemd/user/modelgauge-agent.service")}
	contents := [][]byte{config, unit}
	for i, path := range paths {
		file := serviceFile{Path: path, Mode: 0600, Intended: serviceDigest(contents[i])}
		info, e := os.Lstat(path)
		if e == nil {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("service file is not regular")
			}
			file.Existed = true
			file.Mode = uint32(info.Mode().Perm())
			file.Before, e = os.ReadFile(path)
			if e != nil {
				return e
			}
		} else if !os.IsNotExist(e) {
			return e
		}
		change.Files = append(change.Files, file)
	}
	if change.Files[1].Existed {
		if e := run(ctx, "is-active", "--quiet", serviceName); e == nil {
			change.Active = true
		} else if ctx.Err() != nil {
			return ctx.Err()
		}
		if e := run(ctx, "is-enabled", "--quiet", serviceName); e == nil {
			change.Enabled = true
		} else if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	if err = os.MkdirAll(filepath.Dir(serviceJournal(home)), 0700); err != nil {
		return err
	}
	if err = platform.WriteJSON(serviceJournal(home), change, 0600); err != nil {
		return err
	}
	defer func() {
		if resultErr != nil {
			recovery, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			resultErr = errors.Join(resultErr, restoreService(recovery, home, change, run))
		}
	}()
	if change.Files[1].Existed {
		if err = run(ctx, "stop", serviceName); err != nil {
			return err
		}
	}
	for i, path := range paths {
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = managedWrite(ctx, home, path, contents[i], 0600); err != nil {
			return err
		}
	}
	if err = run(ctx, "daemon-reload"); err != nil {
		return err
	}
	if err = run(ctx, "enable", "--now", serviceName); err != nil {
		return err
	}
	return os.Remove(serviceJournal(home))
}
func validateServiceChange(home string, c serviceChange) error {
	if len(c.Files) != 2 {
		return fmt.Errorf("invalid service checkpoint")
	}
	paths := []string{ConfigPath(home), filepath.Join(home, ".config/systemd/user/modelgauge-agent.service")}
	for i, f := range c.Files {
		if f.Path != paths[i] || len(f.Intended) != 64 {
			return fmt.Errorf("unsafe service checkpoint")
		}
	}
	return nil
}
