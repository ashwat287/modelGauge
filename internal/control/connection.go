package control

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/nerdylucifer/modelgauge/internal/agent"
	"github.com/nerdylucifer/modelgauge/internal/client"
)

func connectionAction(ctx context.Context, c Config, node, action string) error {
	api := &client.Client{BaseURL: c.ControllerURL, AdminToken: c.AdminToken}
	_, err := api.DoJSON(ctx, "POST", "/v1/nodes/"+node+"/connection/"+action, "admin", nil, nil)
	return err
}

// nodeConnection discovers current Lima metadata; no saved SSH port is reused.
func (m *ownershipManager) nodeConnection(ctx context.Context, c Config, node string) (agent.SSHConnection, error) {
	var connection agent.SSHConnection
	instances, err := m.instances(ctx)
	if err != nil {
		return connection, err
	}
	instance, ok := instances[node]
	if !ok || instance.Status != "Running" {
		return connection, fmt.Errorf("benchmark VM %s is not running; start it before reconnecting", node)
	}
	if instance.SSHLocalPort < 1 || instance.SSHLocalPort > 65535 {
		return connection, fmt.Errorf("cannot discover Lima SSH port for %s", node)
	}
	gateway, err := m.run(ctx, []string{"shell", "--workdir", "/", c.VM, "--", "sh", "-c", `getent ahostsv4 host.lima.internal | awk 'NR==1 {print $1}'`}, nil)
	if err != nil {
		return connection, err
	}
	host := strings.TrimSpace(string(gateway))
	if net.ParseIP(host) == nil {
		return connection, fmt.Errorf("cannot resolve the Lima host gateway from control VM")
	}
	connection.Address = net.JoinHostPort(host, strconv.Itoa(instance.SSHLocalPort))
	user, err := m.run(ctx, []string{"shell", "--workdir", "/", node, "--", "id", "-un"}, nil)
	if err != nil {
		return connection, err
	}
	connection.User = strings.TrimSpace(string(user))
	key, err := m.run(ctx, []string{"shell", "--workdir", "/", node, "--", "cat", "/etc/ssh/ssh_host_ed25519_key.pub"}, nil)
	if err != nil {
		return connection, err
	}
	connection.HostKey = strings.TrimSpace(string(key))
	return connection, nil
}

// Probe the same host forward before spending time compiling the model runtime.
func (m *ownershipManager) checkNodeForward(ctx context.Context, c Config, node string) error {
	connection, err := m.nodeConnection(ctx, c, node)
	if err != nil {
		return err
	}
	host, port, _ := net.SplitHostPort(connection.Address)
	output, err := m.run(ctx, []string{"shell", "--workdir", "/", c.VM, "--", "ssh-keyscan", "-T", "5", "-t", "ed25519", "-p", port, host}, nil)
	if err != nil {
		return fmt.Errorf("node %s SSH forward %s is unavailable before runtime setup: %w; restart the benchmark VM and retry", node, connection.Address, err)
	}
	expected, _, _, _, err := ssh.ParseAuthorizedKey([]byte(connection.HostKey))
	if err != nil {
		return fmt.Errorf("read benchmark node host key: %w", err)
	}
	_, _, actual, _, _, err := ssh.ParseKnownHosts(output)
	if err != nil {
		return fmt.Errorf("read SSH forward host key: %w", err)
	}
	if !bytes.Equal(expected.Marshal(), actual.Marshal()) {
		return fmt.Errorf("node %s SSH forward %s host key mismatch: expected %s, received %s", node, connection.Address, ssh.FingerprintSHA256(expected), ssh.FingerprintSHA256(actual))
	}
	return nil
}

func (m *ownershipManager) enrollControllerExecutor(ctx context.Context, c Config, node string) error {
	connection, err := m.nodeConnection(ctx, c, node)
	if err != nil {
		return err
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return err
	}
	publicKey, err := ssh.NewPublicKey(public)
	if err != nil {
		return err
	}
	signer := ssh.MarshalAuthorizedKey(publicKey)
	if _, err = m.run(ctx, []string{"shell", "--workdir", "/", node, "--", "sh", "-c", `exec "$HOME/.local/bin/modelgauge" _guest controller-key`}, signer); err != nil {
		return err
	}
	connection.PrivateKey = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}))
	api := &client.Client{BaseURL: c.ControllerURL, AdminToken: c.AdminToken}
	if _, err = api.DoJSON(ctx, "PUT", "/v1/nodes/"+node+"/connection", "admin", connection, nil); err != nil {
		return err
	}
	_, err = m.run(ctx, []string{"shell", "--workdir", "/", node, "--", "sh", "-c", `exec "$HOME/.local/bin/modelgauge" _guest controller-key-commit`}, nil)
	return err
}

// ReconnectNode refreshes transport registration without installing or rebuilding anything.
func ReconnectNode(ctx context.Context, c Config, node string) error {
	m, err := newOwnershipManager()
	if err != nil {
		return err
	}
	return m.reconnectNode(ctx, c, node)
}

func (m *ownershipManager) reconnectNode(ctx context.Context, c Config, node string) (resultErr error) {
	if !validVMName.MatchString(node) {
		return fmt.Errorf("invalid node name")
	}
	err := m.prepareDirectory()
	if err != nil {
		return err
	}
	registry, err := m.load()
	if err != nil {
		return err
	}
	instances, err := m.instances(ctx)
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
	if owned == nil || owned.Role != "benchmark" || !owned.Prepared || owned.Cleaned {
		return fmt.Errorf("node %s is not a prepared managed benchmark VM", node)
	}
	instance, present := instances[node]
	if !present || instance.Status != "Running" {
		return fmt.Errorf("node %s is not running; start it with limactl before reconnecting", node)
	}
	if err = verifyVM(*owned, instance); err != nil {
		return err
	}
	if _, err = m.guest(ctx, *owned, "identity", nil); err != nil {
		return err
	}
	if err = connectionAction(ctx, c, node, "pause"); err != nil {
		return fmt.Errorf("pause node executor before reconnecting: %w", err)
	}
	if err = m.checkNodeForward(ctx, c, node); err != nil {
		return err
	}
	return m.enrollControllerExecutor(ctx, c, node)
}
