package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSHConnection is a per-node, host-key-pinned controller credential.
type SSHConnection struct {
	Address    string `json:"address"`
	User       string `json:"user"`
	PrivateKey string `json:"private_key"`
	HostKey    string `json:"host_key"`
}

func (c SSHConnection) Validate() error {
	if _, _, err := net.SplitHostPort(c.Address); err != nil {
		return fmt.Errorf("SSH address: %w", err)
	}
	if c.User == "" {
		return errors.New("SSH user required")
	}
	if _, err := ssh.ParsePrivateKey([]byte(c.PrivateKey)); err != nil {
		return fmt.Errorf("SSH private key: %w", err)
	}
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(c.HostKey)); err != nil {
		return fmt.Errorf("SSH host key: %w", err)
	}
	return nil
}
func (c SSHConnection) dial(ctx context.Context) (*ssh.Client, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	signer, _ := ssh.ParsePrivateKey([]byte(c.PrivateKey))
	host, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(c.HostKey))
	// Negotiate the pinned key rather than another key offered by the same sshd.
	algorithms := []string{host.Type()}
	switch host.Type() {
	case ssh.KeyAlgoRSA:
		algorithms = []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}
	case ssh.CertAlgoRSAv01:
		algorithms = []string{ssh.CertAlgoRSASHA512v01, ssh.CertAlgoRSASHA256v01}
	}
	connection, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", c.Address)
	if err != nil {
		return nil, err
	}
	_ = connection.SetDeadline(time.Now().Add(10 * time.Second))
	stop := context.AfterFunc(ctx, func() { connection.Close() })
	defer stop()
	verify := ssh.FixedHostKey(host)
	client, chans, requests, err := ssh.NewClientConn(connection, c.Address, &ssh.ClientConfig{User: c.User, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: func(name string, remote net.Addr, actual ssh.PublicKey) error {
		if err := verify(name, remote, actual); err != nil {
			return fmt.Errorf("%w: expected %s %s, received %s %s", err, host.Type(), ssh.FingerprintSHA256(host), actual.Type(), ssh.FingerprintSHA256(actual))
		}
		return nil
	}, HostKeyAlgorithms: algorithms})
	if err != nil {
		connection.Close()
		return nil, err
	}
	_ = connection.SetDeadline(time.Time{})
	return ssh.NewClient(client, chans, requests), nil
}

// session owns the connection, cancellation watcher and channel as one lifetime.
func (c SSHConnection) session(ctx context.Context) (*ssh.Session, func(), error) {
	client, err := c.dial(ctx)
	if err != nil {
		return nil, nil, err
	}
	stop := context.AfterFunc(ctx, func() { client.Close() })
	session, err := client.NewSession()
	if err != nil {
		stop()
		client.Close()
		return nil, nil, err
	}
	return session, func() { stop(); session.Close(); client.Close() }, nil
}

func (c SSHConnection) work(ctx context.Context, request workerRequest, unit string) (response workerResponse, resultErr error) {
	session, finish, err := c.session(ctx)
	if err != nil {
		return response, fmt.Errorf("%w: SSH session %s: %v", errWorkerTransport, c.Address, err)
	}
	defer finish()
	stdin, err := session.StdinPipe()
	if err != nil {
		return response, err
	}
	var stdout, stderr limitedBuffer
	session.Stdout = &stdout
	session.Stderr = &stderr
	if err = session.Start("work " + unit); err != nil {
		return response, fmt.Errorf("%w: start worker: %v", errWorkerTransport, err)
	}
	err = exchangeWorker(ctx, request, stdin, session.Wait)
	if err != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cleanupErr := c.stop(stopCtx, unit)
		return response, errors.Join(fmt.Errorf("%w: worker connection: %v: %s", errWorkerTransport, err, stderr.String()), cleanupErr)
	}
	return decodeWorkerResponse(stdout.Bytes())
}
func (c SSHConnection) stop(ctx context.Context, unit string) error {
	session, finish, err := c.session(ctx)
	if err != nil {
		return err
	}
	defer finish()
	return session.Run("stop " + unit)
}

// RunSSHCommand is the forced command for the dedicated controller key. Neither
// arbitrary shell commands nor forwarding are permitted by this credential.
func RunSSHCommand(ctx context.Context, command string, in io.Reader, out, stderr io.Writer) error {
	fields := regexp.MustCompile(`^(work|stop) (modelgauge-worker-[a-f0-9]{32})$`).FindStringSubmatch(command)
	if fields == nil {
		return errors.New("controller SSH command is not permitted")
	}
	var cmd *exec.Cmd
	if fields[1] == "stop" {
		cmd = exec.CommandContext(ctx, "systemctl", "--user", "stop", fields[2]+".service")
	} else {
		binary, err := os.Executable()
		if err != nil {
			return err
		}
		cmd = exec.CommandContext(ctx, "systemd-run", "--user", "--quiet", "--pipe", "--wait", "--collect", "--unit="+fields[2], "--property=KillMode=control-group", "--property=TimeoutStopSec=5s", "--property=RuntimeMaxSec=24h", "--", filepath.Clean(binary), "_guest", "worker")
	}
	cmd.Stdin = in
	cmd.Stdout = out
	cmd.Stderr = stderr
	return cmd.Run()
}
