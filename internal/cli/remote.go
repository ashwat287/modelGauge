package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/nerdylucifer/modelgauge/internal/client"
	"github.com/nerdylucifer/modelgauge/internal/control"
	"golang.org/x/crypto/ssh"
)

func parseRemoteHost(value string) (user, address string, err error) {
	user, host, ok := strings.Cut(value, "@")
	if !ok || user == "" || host == "" || strings.ContainsAny(user+host, "\n\r\x00") {
		return "", "", errors.New("--remote-host must be USER@HOST or USER@HOST:PORT")
	}
	hostname, port, e := net.SplitHostPort(host)
	if e != nil {
		if strings.Contains(host, ":") {
			return "", "", errors.New("use [IPv6]:PORT for an IPv6 remote address")
		}
		hostname, port = host, "22"
	}
	if value, err := strconv.Atoi(port); err != nil || value < 1 || value > 65535 {
		return "", "", errors.New("remote SSH port must be between 1 and 65535")
	}
	if hostname == "" {
		return "", "", errors.New("remote hostname is required")
	}
	return user, net.JoinHostPort(hostname, port), nil
}

func setupRemote(ctx context.Context, c control.Config, target, identity, fingerprint string, cpus, memory, disk int, out, log io.Writer) int {
	options := control.RemoteOptions{CPUs: cpus, MemoryGiB: memory, DiskGiB: disk}
	api := &client.Client{BaseURL: c.ControllerURL, AdminToken: c.AdminToken}
	if target != "" {
		var err error
		options.User, options.Address, err = parseRemoteHost(target)
		if err != nil {
			fmt.Fprintln(log, err)
			return 2
		}
		var probe struct {
			HostKey string `json:"host_key"`
		}
		if _, err = api.DoJSON(ctx, "POST", "/v1/remote/probe", "admin", map[string]string{"address": options.Address, "user": options.User}, &probe); err != nil {
			fmt.Fprintln(log, err)
			return 1
		}
		key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(probe.HostKey))
		if err != nil {
			fmt.Fprintln(log, "Invalid remote host key:", err)
			return 1
		}
		actual := ssh.FingerprintSHA256(key)
		if fingerprint != "" && fingerprint != actual {
			fmt.Fprintln(log, "Remote host key mismatch; expected", fingerprint, "received", actual)
			return 1
		}
		if fingerprint == "" {
			answer, err := terminalPrompt(ctx, fmt.Sprintf("Remote Mac %s host key: %s\nVerify this fingerprint on the Mac. Trust it? Type yes: ", options.Address, actual), false, log)
			if err != nil || answer != "yes" {
				fmt.Fprintln(log, "Remote host key was not trusted; setup stopped.")
				return 1
			}
		}
		options.HostKey = probe.HostKey
		if identity != "" {
			data, err := os.ReadFile(identity)
			if err != nil {
				fmt.Fprintln(log, err)
				return 1
			}
			if _, err = ssh.ParsePrivateKey(data); err != nil {
				fmt.Fprintln(log, "Use an unencrypted bootstrap SSH identity:", err)
				return 1
			}
			options.PrivateKey = string(data)
		} else {
			password, err := terminalPrompt(ctx, "Remote Mac SSH password: ", true, log)
			if err != nil || password == "" {
				fmt.Fprintln(log, "Could not read the SSH password; use --identity for key authentication.")
				return 1
			}
			options.Password = password
		}
	}
	// Non-secret local intent prevents cleanup from deleting the controller before
	// its remote resources, including after an interrupted first setup.
	if err := control.TrackRemoteTarget(); err != nil {
		fmt.Fprintln(log, err)
		return 1
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Minute)
	defer cancel()
	var result map[string]string
	if err := api.StreamJSON(ctx, "/v1/remote/setup", options, &result, log); err != nil {
		fmt.Fprintln(log, "remote setup:", err)
		return 1
	}
	fmt.Fprintln(out, "✅ Remote benchmark VM ready. Install a model, then run your benchmark YAML.")
	return 0
}

// Read credentials from the controlling terminal, never from flags or shell
// history. Restore echo even when the caller cancels the prompt.
func terminalPrompt(ctx context.Context, message string, secret bool, out io.Writer) (string, error) {
	terminal, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", err
	}
	defer terminal.Close()
	if secret {
		cmd := exec.Command("/bin/stty", "-echo")
		cmd.Stdin = terminal
		if err = cmd.Run(); err != nil {
			return "", err
		}
		defer func() {
			restoreTerminal, e := os.OpenFile("/dev/tty", os.O_RDWR, 0)
			if e == nil {
				defer restoreTerminal.Close()
				restore := exec.Command("/bin/stty", "echo")
				restore.Stdin = restoreTerminal
				_ = restore.Run()
			}
			fmt.Fprintln(out)
		}()
	}
	fmt.Fprint(out, message)
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			terminal.Close()
		case <-done:
		}
	}()
	defer close(done)
	line, err := bufio.NewReader(terminal).ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), nil
}
