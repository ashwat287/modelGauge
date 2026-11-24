package control

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nerdylucifer/modelgauge/internal/agent"
	"github.com/nerdylucifer/modelgauge/internal/platform"
	"golang.org/x/crypto/ssh"
)

// RemoteOptions contains one-time bootstrap credentials. Never persist this type.
type RemoteOptions struct {
	Address    string `json:"address,omitempty"`
	User       string `json:"user,omitempty"`
	Password   string `json:"password,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`
	HostKey    string `json:"host_key,omitempty"`
	CPUs       int    `json:"cpus"`
	MemoryGiB  int    `json:"memory_gib"`
	DiskGiB    int    `json:"disk_gib"`
}
type RemoteHost struct {
	Owner        string              `json:"owner"`
	Host         agent.SSHConnection `json:"host"`
	Node         agent.SSHConnection `json:"node"`
	HelperSHA256 string              `json:"helper_sha256"`
}

func ProbeRemoteHost(ctx context.Context, address, user string) (string, error) {
	if _, _, err := net.SplitHostPort(address); err != nil {
		return "", err
	}
	var key string
	client, err := agent.DialSSH(ctx, address, &ssh.ClientConfig{User: user, HostKeyAlgorithms: []string{ssh.KeyAlgoED25519}, HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
		key = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
		return errors.New("host key captured; authentication not attempted")
	}})
	if client != nil {
		client.Close()
	}
	if key != "" {
		return key, nil
	}
	return "", fmt.Errorf("read remote SSH host key: %w", err)
}
func loadRemote(directory string) (RemoteHost, error) {
	var host RemoteHost
	path := filepath.Join(directory, "remote-host.json")
	info, err := os.Lstat(path)
	if err != nil {
		return host, err
	}
	if !info.Mode().IsRegular() {
		return host, errors.New("remote ownership file is not regular")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return host, err
	}
	if err = json.Unmarshal(data, &host); err != nil {
		return host, err
	}
	if !validOwner.MatchString(host.Owner) || len(host.HelperSHA256) != 64 {
		return host, errors.New("invalid remote ownership record")
	}
	if err = host.Host.Validate(); err != nil {
		return host, err
	}
	if host.Host.Jump != nil {
		return host, errors.New("remote Mac cannot have another jump host")
	}
	return host, nil
}
func (h RemoteHost) save(directory string) error {
	return platform.WriteJSON(filepath.Join(directory, "remote-host.json"), h, 0600)
}
func newSSHKey() (string, string, error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	data, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return "", "", err
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: data})), nil
}

// PrepareRemote runs inside the controller. Only the one-time login uses the
// supplied credentials; subsequent management uses the scoped controller key.
func PrepareRemote(ctx context.Context, directory string, o RemoteOptions, log io.Writer) (connection agent.SSHConnection, resultErr error) {
	if o.CPUs < 1 || o.CPUs > 4 || o.MemoryGiB < 2 || o.DiskGiB < 10 {
		return connection, errors.New("remote node requires 1..4 CPUs, at least 2 GiB memory, 10 GiB disk")
	}
	if log == nil {
		log = io.Discard
	}
	host, err := loadRemote(directory)
	fresh := os.IsNotExist(err)
	if err != nil && !fresh {
		return connection, err
	}
	if !fresh && o.Address != "" && (o.User != host.Host.User || o.HostKey != host.Host.HostKey) {
		return connection, errors.New("workspace owns a different remote host; clean up before changing it")
	}
	if !fresh && o.Address != "" {
		host.Host.Address = o.Address
	}
	if fresh && (o.Address == "" || o.User == "" || o.HostKey == "" || o.Password == "" && o.PrivateKey == "") {
		return connection, errors.New("remote host address, user, verified host key and credentials are required")
	}
	if fresh {
		_, private, err := newSSHKey()
		if err != nil {
			return connection, err
		}
		owner, err := newScopeID()
		if err != nil {
			return connection, err
		}
		host = RemoteHost{Owner: owner, Host: agent.SSHConnection{Address: o.Address, User: o.User, HostKey: o.HostKey, PrivateKey: private}}
	}
	if fresh || o.Password != "" || o.PrivateKey != "" {
		signer, err := ssh.ParsePrivateKey([]byte(host.Host.PrivateKey))
		if err != nil {
			return connection, err
		}
		key := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
		var auth []ssh.AuthMethod
		if o.PrivateKey != "" {
			signer, err := ssh.ParsePrivateKey([]byte(o.PrivateKey))
			if err != nil {
				return connection, errors.New("bootstrap SSH key must be an unencrypted private key")
			}
			auth = append(auth, ssh.PublicKeys(signer))
		}
		if o.Password != "" {
			auth = append(auth, ssh.Password(o.Password), ssh.KeyboardInteractive(func(_, _ string, questions []string, echo []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range answers {
					answers[i] = o.Password
				}
				return answers, nil
			}))
		}
		pinned, _, _, _, err := ssh.ParseAuthorizedKey([]byte(o.HostKey))
		if err != nil {
			return connection, err
		}
		client, err := agent.DialSSH(ctx, o.Address, &ssh.ClientConfig{User: o.User, Auth: auth, HostKeyAlgorithms: []string{pinned.Type()}, HostKeyCallback: ssh.FixedHostKey(pinned)})
		if err != nil {
			return connection, fmt.Errorf("remote Mac SSH connection from controller: %w", err)
		}
		defer client.Close()
		stopClient := context.AfterFunc(ctx, func() { client.Close() })
		defer stopClient()
		// Discover architecture before selecting a signed, bundled macOS executable.
		session, err := client.NewSession()
		if err != nil {
			return connection, err
		}
		output, err := session.Output("uname -s; uname -m")
		session.Close()
		if err != nil {
			return connection, err
		}
		facts := strings.Fields(string(output))
		if len(facts) != 2 || facts[0] != "Darwin" {
			return connection, errors.New("--remote-host requires a macOS machine")
		}
		arch := facts[1]
		if arch == "x86_64" {
			arch = "amd64"
		}
		binary, err := macBinary(arch)
		if err != nil {
			return connection, err
		}
		host.HelperSHA256 = helperSum(binary)
		if err = host.save(directory); err != nil {
			return connection, err
		}
		fmt.Fprintln(log, "Remote management access: running")
		if err = installRemoteHelper(ctx, client, host, key, binary); err != nil {
			return connection, err
		}
		fmt.Fprintln(log, "Remote management access: completed")
	}
	// A fresh failed setup removes only the VM created in this owned namespace.
	// Retain the access/ownership checkpoint so cleanup is possible after a failure.
	defer func() {
		if resultErr != nil && fresh && ctx.Err() == nil {
			rollback, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			var report CleanupReport
			cleanupErr := host.request(rollback, remoteHostRequest{Operation: "cleanup", Cleanup: CleanupOptions{DeleteVMs: true}}, &report, log)
			if cleanupErr == nil {
				cleanupErr = os.Remove(filepath.Join(directory, "remote-host.json"))
			}
			resultErr = errors.Join(resultErr, cleanupErr)
		}
	}()
	// Reuse the node's dedicated key. Persist it before asking the guest to
	// install it, so interruption cannot strand a VM with an unknown credential.
	private := host.Node.PrivateKey
	if private == "" {
		_, private, err = newSSHKey()
		if err != nil {
			return connection, err
		}
		host.Node.PrivateKey = private
		if err = host.save(directory); err != nil {
			return connection, err
		}
	}
	signer, err := ssh.ParsePrivateKey([]byte(private))
	if err != nil {
		return connection, err
	}
	public := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	err = host.request(ctx, remoteHostRequest{Operation: "prepare", CPUs: o.CPUs, MemoryGiB: o.MemoryGiB, DiskGiB: o.DiskGiB, PublicKey: public}, &connection, log)
	if err != nil {
		return connection, fmt.Errorf("prepare remote benchmark VM: %w", err)
	}
	connection.PrivateKey = private
	connection.Jump = &host.Host
	if err = connection.Validate(); err != nil {
		return connection, err
	}
	host.Node = connection
	if err = host.save(directory); err != nil {
		return connection, err
	}
	return connection, nil
}
func (h RemoteHost) request(ctx context.Context, request remoteHostRequest, result any, log io.Writer) error {
	var output bytes.Buffer
	if err := h.Host.Command(ctx, "manage", request, &output, log); err != nil {
		return fmt.Errorf("remote host operation failed: %w", err)
	}
	return json.Unmarshal(output.Bytes(), result)
}
func CleanupRemote(ctx context.Context, directory string, options CleanupOptions, log io.Writer) (CleanupReport, error) {
	host, err := loadRemote(directory)
	if os.IsNotExist(err) {
		return CleanupReport{DryRun: options.DryRun, Targets: []CleanupTarget{}, Untracked: []string{}}, nil
	}
	if err != nil {
		return CleanupReport{}, err
	}
	operation := "cleanup"
	if options.DryRun {
		operation = "inspect"
	}
	var report CleanupReport
	if err = host.request(ctx, remoteHostRequest{Operation: operation, Cleanup: options}, &report, log); err != nil {
		return report, err
	}
	if options.DeleteVMs && !options.DryRun && len(report.Untracked) == 0 {
		err = os.Remove(filepath.Join(directory, "remote-host.json"))
	}
	for i := range report.Targets {
		report.Targets[i].Name = "remote/" + report.Targets[i].Name
	}
	return report, err
}
func RemoteConfigured(directory string) bool {
	_, err := os.Lstat(filepath.Join(directory, "remote-host.json"))
	return err == nil
}

func remoteHelperInstallScript(host RemoteHost, public string) string {
	return `set -eu
umask 077
owner=` + quote(host.Owner) + `
root="$HOME/.modelgauge-remote/` + host.Owner[:16] + `"
test ! -L "$HOME/.modelgauge-remote"
test ! -L "$root"
if [ -e "$root" ]; then
 test ! -L "$root/.owner"
 test "$(cat "$root/.owner")" = "$owner"
 if [ -e "$root/helper" ]; then
  test ! -L "$root/helper"
  actual=$(/usr/bin/shasum -a 256 "$root/helper")
  test "${actual%% *}" = "$(cat "$root/.helper-sha256")" || test "${actual%% *}" = ` + quote(host.HelperSHA256) + `
 fi
else
 mkdir -p "$root"
 printf '%s\n' "$owner" > "$root/.owner"
fi
stage=$(mktemp "$root/.helper-upload.XXXXXX")
trap 'rm -f "$stage"' EXIT
cat > "$stage"
chmod 700 "$stage"
actual=$(/usr/bin/shasum -a 256 "$stage")
test "${actual%% *}" = ` + quote(host.HelperSHA256) + `
mv "$stage" "$root/helper"
printf '%s\n' ` + quote(host.HelperSHA256) + ` > "$root/.helper-sha256"
actual=$(/usr/bin/shasum -a 256 "$root/helper")
test "${actual%% *}" = ` + quote(host.HelperSHA256) + `
command="\"$root/helper\" _remote-host $owner"
# SSH forced-command strings must escape quotes and backslashes.
command=$(printf '%s' "$command" | sed 's/\\/\\\\/g; s/"/\\"/g')
if [ ! -e "$root/.authorized-key" ]; then
 printf 'restrict,command="%s" %s modelgauge-controller\n' "$command" ` + quote(public) + ` > "$root/.authorized-key"
fi
test ! -L "$root/.authorized-key"
test ! -L "$HOME/.ssh"
mkdir -p "$HOME/.ssh"
chmod 700 "$HOME/.ssh"
test ! -L "$HOME/.ssh/authorized_keys"
if [ -e "$HOME/.ssh/authorized_keys" ]; then test -f "$HOME/.ssh/authorized_keys"; fi
touch "$HOME/.ssh/authorized_keys"
chmod 600 "$HOME/.ssh/authorized_keys"
if ! grep -Fxq -f "$root/.authorized-key" "$HOME/.ssh/authorized_keys"; then
 printf '\n' >> "$HOME/.ssh/authorized_keys"
 cat "$root/.authorized-key" >> "$HOME/.ssh/authorized_keys"
fi
`
}
func installRemoteHelper(ctx context.Context, client *ssh.Client, host RemoteHost, public string, binary []byte) error {
	script := remoteHelperInstallScript(host, public)
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	session.Stdin = bytes.NewReader(binary)
	var stderr bytes.Buffer
	session.Stderr = &stderr
	// Closing the client on cancellation interrupts a blocked upload/command.
	stop := context.AfterFunc(ctx, func() { session.Close() })
	defer stop()
	if err = session.Run(script); err != nil {
		return fmt.Errorf("install remote management helper: %w: %s", err, stderr.String())
	}
	return nil
}

// The host CLI stores only a non-secret intent marker. All remote credentials
// and VM ownership records stay in the controller's persistent directory.
func remoteTargetPath() (string, error) {
	path, err := ConfigFile()
	return filepath.Join(filepath.Dir(path), "remote-target.json"), err
}
func TrackRemoteTarget() error {
	path, err := remoteTargetPath()
	if err != nil {
		return err
	}
	return platform.WriteJSON(path, map[string]bool{"remote": true}, 0600)
}
func RemoteTargetTracked() bool {
	path, err := remoteTargetPath()
	if err != nil {
		return false
	}
	_, err = os.Lstat(path)
	return err == nil
}
func ForgetRemoteTarget() error {
	path, err := remoteTargetPath()
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
