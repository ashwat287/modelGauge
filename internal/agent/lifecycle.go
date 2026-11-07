package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

func Install(options InstallOptions) (ServiceStatus, error) {
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
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		return ServiceStatus{}, err
	}
	_ = os.Chmod(configPath, 0o600)
	if runtime.GOOS == "linux" {
		return installSystemdUser(home, configPath, options)
	}
	return ServiceStatus{}, fmt.Errorf("agent installation requires Linux")
}

func Status() (ServiceStatus, error) {
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
			status.Active = exec.Command("systemctl", "--user", "is-active", "--quiet", serviceName).Run() == nil
		}
		return status, nil
	}
	return ServiceStatus{}, fmt.Errorf("agent status requires Linux; on a Mac, run inside a Lima Linux VM")
}

func Uninstall() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if runtime.GOOS == "linux" {
		_ = exec.Command("systemctl", "--user", "disable", "--now", serviceName).Run()
		if err := os.Remove(filepath.Join(home, ".config", "systemd", "user", serviceName+".service")); err != nil && !os.IsNotExist(err) {
			return err
		}
		return exec.Command("systemctl", "--user", "daemon-reload").Run()
	}
	return fmt.Errorf("agent uninstall requires Linux")
}

func installSystemdUser(home, configPath string, options InstallOptions) (ServiceStatus, error) {
	unitPath := filepath.Join(home, ".config", "systemd", "user", serviceName+".service")
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o700); err != nil {
		return ServiceStatus{}, err
	}
	unit := fmt.Sprintf("[Unit]\nDescription=ModelGauge benchmark agent\nAfter=network-online.target\n\n[Service]\nType=simple\nEnvironmentFile=%s\nExecStart=%s agent run --interval %s\nRestart=always\nRestartSec=10\n\n[Install]\nWantedBy=default.target\n", configPath, options.Binary, options.Interval)
	if err := os.WriteFile(unitPath, []byte(unit), 0o600); err != nil {
		return ServiceStatus{}, err
	}
	if err := exec.Command("systemctl", "--user", "daemon-reload").Run(); err != nil {
		return ServiceStatus{}, fmt.Errorf("reload systemd user service: %w", err)
	}
	if err := exec.Command("systemctl", "--user", "enable", "--now", serviceName).Run(); err != nil {
		return ServiceStatus{}, fmt.Errorf("enable systemd user service: %w", err)
	}
	return Status()
}

func validateServiceValue(value string) error {
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("service configuration values cannot contain newlines")
	}
	return nil
}
