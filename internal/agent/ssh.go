package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"

	"github.com/nerdylucifer/modelgauge/pkg/protocol"
	"golang.org/x/crypto/ssh"
)

// SSHConnection is a per-node, host-key-pinned controller credential.
type SSHConnection struct {
	Address    string         `json:"address"`
	User       string         `json:"user"`
	PrivateKey string         `json:"private_key"`
	HostKey    string         `json:"host_key"`
	Jump       *SSHConnection `json:"jump,omitempty"`
}

func (c SSHConnection) Validate() error {
	if c.Jump != nil {
		if c.Jump.Jump != nil {
			return errors.New("only one SSH jump host is supported")
		}
		if err := c.Jump.Validate(); err != nil {
			return fmt.Errorf("SSH jump host: %w", err)
		}
	}
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

// DialSSH authenticates a connection with a caller-supplied host-key verifier.
// The deadline covers negotiation; cancellation also interrupts a stalled handshake.
func DialSSH(ctx context.Context, address string, config *ssh.ClientConfig) (*ssh.Client, error) {
	// Allow a slow LAN or a waking Mac to complete TCP establishment. SSH
	// negotiation retains its separate ten-second bound; credentials are not retried.
	connection, err := (&net.Dialer{Timeout: 20 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	return handshakeSSH(ctx, connection, address, config)
}
func handshakeSSH(ctx context.Context, connection net.Conn, address string, config *ssh.ClientConfig) (*ssh.Client, error) {
	ctx, cancelHandshake := context.WithTimeout(ctx, 10*time.Second)
	defer cancelHandshake()
	_ = connection.SetDeadline(time.Now().Add(10 * time.Second))
	stop := context.AfterFunc(ctx, func() { connection.Close() })
	defer stop()
	client, chans, requests, err := ssh.NewClientConn(connection, address, config)
	if err != nil {
		connection.Close()
		return nil, err
	}
	_ = connection.SetDeadline(time.Time{})
	return ssh.NewClient(client, chans, requests), nil
}
func (c SSHConnection) clientConfig() *ssh.ClientConfig {
	signer, _ := ssh.ParsePrivateKey([]byte(c.PrivateKey))
	host, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(c.HostKey))
	algorithms := []string{host.Type()}
	switch host.Type() {
	case ssh.KeyAlgoRSA:
		algorithms = []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}
	case ssh.CertAlgoRSAv01:
		algorithms = []string{ssh.CertAlgoRSASHA512v01, ssh.CertAlgoRSASHA256v01}
	}
	verify := ssh.FixedHostKey(host)
	return &ssh.ClientConfig{User: c.User, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyAlgorithms: algorithms, HostKeyCallback: func(name string, remote net.Addr, actual ssh.PublicKey) error {
		if err := verify(name, remote, actual); err != nil {
			return fmt.Errorf("%w: expected %s %s, received %s %s", err, host.Type(), ssh.FingerprintSHA256(host), actual.Type(), ssh.FingerprintSHA256(actual))
		}
		return nil
	}}
}
func (c SSHConnection) dial(ctx context.Context) (*ssh.Client, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if c.Jump == nil {
		return DialSSH(ctx, c.Address, c.clientConfig())
	}
	jump, err := c.Jump.dial(ctx)
	if err != nil {
		return nil, fmt.Errorf("Mac SSH connection: %w", err)
	}
	success := false
	defer func() {
		if !success {
			jump.Close()
		}
	}()
	cancel := context.AfterFunc(ctx, func() { jump.Close() })
	defer cancel()
	session, err := jump.NewSession()
	if err != nil {
		return nil, err
	}
	_, err = session.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := session.StdoutPipe()
	if err != nil {
		return nil, err
	}
	session.Stderr = io.Discard
	if err = session.Start("connection"); err != nil {
		return nil, fmt.Errorf("resolve owned benchmark VM: %w", err)
	}
	var current SSHConnection
	if err = json.NewDecoder(io.LimitReader(output, 65536)).Decode(&current); err != nil {
		return nil, fmt.Errorf("resolve remote benchmark VM: %w", err)
	}
	host, port, err := net.SplitHostPort(current.Address)
	if err != nil || host != "127.0.0.1" || port == "0" || current.User != c.User || current.HostKey != c.HostKey {
		return nil, errors.New("remote VM SSH identity changed; rerun setup after verifying the machine")
	}
	connection, err := jump.DialContext(ctx, "tcp", current.Address)
	if err != nil {
		return nil, err
	}
	// Close the outer SSH connection whenever the nested connection closes.
	owned := &jumpConnection{Conn: connection, jump: jump}
	client, err := handshakeSSH(ctx, owned, current.Address, c.clientConfig())
	if err != nil {
		return nil, err
	}
	success = true
	return client, nil
}

type jumpConnection struct {
	net.Conn
	jump *ssh.Client
}

func (c *jumpConnection) Close() error {
	err := c.Conn.Close()
	c.jump.Close()
	return err
}

// Command is used only with the dedicated remote-host management credential.
// JSON keepalives let the helper cancel provisioning if the controller disappears.
func (c SSHConnection) Command(ctx context.Context, command string, request any, out, log io.Writer) error {
	session, finish, err := c.session(ctx)
	if err != nil {
		return err
	}
	defer finish()
	stdin, err := session.StdinPipe()
	if err != nil {
		return err
	}
	session.Stdout, session.Stderr = out, log
	if err = session.Start(command); err != nil {
		return err
	}
	feed, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		enc := json.NewEncoder(stdin)
		if enc.Encode(request) != nil {
			return
		}
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-feed.Done():
				return
			case <-ticker.C:
				if enc.Encode(struct{}{}) != nil {
					return
				}
			}
		}
	}()
	err = session.Wait()
	cancel()
	finish()
	<-done
	return err
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

func (c SSHConnection) work(ctx context.Context, request workerRequest, unit string, log io.Writer, observe func(protocol.Measurement)) (response workerResponse, resultErr error) {
	session, finish, err := c.session(ctx)
	if err != nil {
		return response, fmt.Errorf("%w: SSH session %s: %v", errWorkerTransport, c.Address, err)
	}
	defer finish()
	stdin, err := session.StdinPipe()
	if err != nil {
		return response, err
	}
	stdout := workerStream{log: log, observe: observe}
	var stderr limitedBuffer
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
	return decodeWorkerResponse(stdout.final)
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
