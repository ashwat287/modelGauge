package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/nerdylucifer/modelgauge/internal/platform"
	"golang.org/x/crypto/ssh"
)

func testSSHKey(t *testing.T) (ssh.Signer, string) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw})
	signer, err := ssh.ParsePrivateKey(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return signer, string(encoded)
}
func testSSHNode(t *testing.T, extraHostKeys ...ssh.Signer) (SSHConnection, <-chan string) {
	t.Helper()
	host, _ := testSSHKey(t)
	client, key := testSSHKey(t)
	commands := make(chan string, 30)
	config := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if string(key.Marshal()) != string(client.PublicKey().Marshal()) {
			return nil, io.EOF
		}
		return nil, nil
	}}
	config.AddHostKey(host)
	for _, key := range extraHostKeys {
		config.AddHostKey(key)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				connection, channels, requests, err := ssh.NewServerConn(raw, config)
				if err != nil {
					raw.Close()
					return
				}
				defer connection.Close()
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					channel, requests, err := incoming.Accept()
					if err != nil {
						return
					}
					go func() {
						defer channel.Close()
						for req := range requests {
							if req.Type != "exec" {
								req.Reply(false, nil)
								continue
							}
							var command struct{ Command string }
							_ = ssh.Unmarshal(req.Payload, &command)
							commands <- command.Command
							req.Reply(true, nil)
							if strings.HasPrefix(command.Command, "work ") {
								var request workerRequest
								if json.NewDecoder(channel).Decode(&request) != nil {
									return
								}
								_ = json.NewEncoder(channel).Encode(workerResponse{Host: platform.Host{OS: "linux", Hostname: "ssh-node", CPUs: 2}})
							}
							_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
							return
						}
					}()
				}
			}()
		}
	}()
	return SSHConnection{Address: listener.Addr().String(), User: "benchmark", PrivateKey: key, HostKey: string(ssh.MarshalAuthorizedKey(host.PublicKey()))}, commands
}
func TestSSHWorkerUsesPinnedKeyAndRestrictedCommand(t *testing.T) {
	connection, commands := testSSHNode(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	response, err := remoteWork(ctx, Config{SSH: &connection}, workerRequest{Operation: "inspect"})
	if err != nil || response.Host.Hostname != "ssh-node" {
		t.Fatal(response, err)
	}
	if command := <-commands; !strings.HasPrefix(command, "work modelgauge-worker-") {
		t.Fatal(command)
	}
	if err = connection.stop(ctx, "modelgauge-worker-"+strings.Repeat("a", 32)); err != nil {
		t.Fatal(err)
	}
	if command := <-commands; !strings.HasPrefix(command, "stop modelgauge-worker-") {
		t.Fatal(command)
	}
	wrong, _ := testSSHKey(t)
	connection.HostKey = string(ssh.MarshalAuthorizedKey(wrong.PublicKey()))
	if _, err = remoteWork(ctx, Config{SSH: &connection}, workerRequest{Operation: "inspect"}); err == nil {
		t.Fatal("mismatched SSH host key accepted")
	}
}
func TestControllerSSHKeyCannotExecuteArbitraryCommands(t *testing.T) {
	for _, command := range []string{"sh", "work ../escape", "work modelgauge-worker-123; id", "stop modelgauge-worker-" + strings.Repeat("a", 32) + ";id", "work modelgauge-worker-" + strings.Repeat("a", 32) + "\nid"} {
		if err := RunSSHCommand(context.Background(), command, strings.NewReader(""), io.Discard, io.Discard); err == nil {
			t.Fatal("unsafe command accepted", command)
		}
	}
}

func TestSSHSessionOwnsCancellation(t *testing.T) {
	connection, commands := testSSHNode(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session, finish, err := connection.session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	// Leave the node waiting for a request body, then cancel the owning context.
	if err = session.Start("work modelgauge-worker-" + strings.Repeat("b", 32)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-commands:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not start")
	}
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled session reported success")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("session survived owning context")
	}
}

func TestSSHWorkerNegotiatesPinnedHostKey(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	other, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	connection, _ := testSSHNode(t, other)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	response, err := remoteWork(ctx, Config{SSH: &connection}, workerRequest{Operation: "inspect"})
	if err != nil || response.Host.Hostname != "ssh-node" {
		t.Fatal("server advertising ECDSA and pinned Ed25519 must connect", response, err)
	}
}

// The jump peer exposes only one loopback VM port and a management connection
// channel. Neither the host CLI nor an external forwarding process is involved.
func testSSHJump(t *testing.T, node SSHConnection, metadata SSHConnection) (SSHConnection, <-chan struct{}) {
	t.Helper()
	host, _ := testSSHKey(t)
	client, key := testSSHKey(t)
	config := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		if string(k.Marshal()) != string(client.PublicKey().Marshal()) {
			return nil, io.EOF
		}
		return nil, nil
	}}
	config.AddHostKey(host)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	released := make(chan struct{}, 10)
	go func() {
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				c, channels, requests, err := ssh.NewServerConn(raw, config)
				if err != nil {
					raw.Close()
					return
				}
				defer c.Close()
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					switch incoming.ChannelType() {
					case "direct-tcpip":
						var target struct {
							Host       string
							Port       uint32
							Origin     string
							OriginPort uint32
						}
						if ssh.Unmarshal(incoming.ExtraData(), &target) != nil || net.JoinHostPort(target.Host, fmt.Sprint(target.Port)) != node.Address {
							incoming.Reject(ssh.Prohibited, "not the owned VM")
							continue
						}
						upstream, err := net.Dial("tcp", node.Address)
						if err != nil {
							incoming.Reject(ssh.ConnectionFailed, "VM unavailable")
							continue
						}
						channel, requests, err := incoming.Accept()
						if err != nil {
							upstream.Close()
							continue
						}
						go ssh.DiscardRequests(requests)
						go func() {
							defer upstream.Close()
							defer channel.Close()
							done := make(chan struct{})
							go func() { io.Copy(upstream, channel); upstream.Close(); close(done) }()
							io.Copy(channel, upstream)
							channel.Close()
							<-done
						}()
					case "session":
						channel, requests, err := incoming.Accept()
						if err != nil {
							continue
						}
						go func() {
							defer channel.Close()
							for request := range requests {
								var command struct{ Command string }
								if request.Type != "exec" || ssh.Unmarshal(request.Payload, &command) != nil || command.Command != "connection" {
									request.Reply(false, nil)
									continue
								}
								request.Reply(true, nil)
								json.NewEncoder(channel).Encode(metadata)
								io.Copy(io.Discard, channel)
								released <- struct{}{}
								channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
								return
							}
						}()
					default:
						incoming.Reject(ssh.UnknownChannelType, "unsupported")
					}
				}
			}()
		}
	}()
	return SSHConnection{Address: listener.Addr().String(), User: "mac-user", PrivateKey: key, HostKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(host.PublicKey())))}, released
}
func TestSSHJumpUsesFreshVMPortAndClosesBothConnections(t *testing.T) {
	node, commands := testSSHNode(t)
	metadata := SSHConnection{Address: node.Address, User: node.User, HostKey: node.HostKey}
	jump, released := testSSHJump(t, node, metadata)
	node.Address = "127.0.0.1:1" // Stale saved port must never be used.
	node.Jump = &jump
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	response, err := remoteWork(ctx, Config{SSH: &node}, workerRequest{Operation: "inspect"})
	if err != nil || response.Host.Hostname != "ssh-node" {
		t.Fatal(response, err)
	}
	if command := <-commands; !strings.HasPrefix(command, "work ") {
		t.Fatal(command)
	}
	select {
	case <-released:
	case <-ctx.Done():
		t.Fatal("jump connection session outlived worker")
	}
	wrong, _ := testSSHKey(t)
	node.Jump.HostKey = string(ssh.MarshalAuthorizedKey(wrong.PublicKey()))
	if _, err = remoteWork(ctx, Config{SSH: &node}, workerRequest{Operation: "inspect"}); err == nil {
		t.Fatal("changed Mac host key accepted")
	}
}
func TestSSHJumpRejectsChangedGuestIdentityAndNonLoopbackDestinations(t *testing.T) {
	for _, mode := range []string{"key", "user", "destination"} {
		t.Run(mode, func(t *testing.T) {
			node, _ := testSSHNode(t)
			metadata := SSHConnection{Address: node.Address, User: node.User, HostKey: node.HostKey}
			switch mode {
			case "key":
				wrong, _ := testSSHKey(t)
				metadata.HostKey = string(ssh.MarshalAuthorizedKey(wrong.PublicKey()))
			case "user":
				metadata.User = "another-user"
			case "destination":
				metadata.Address = "192.0.2.5:22"
			}
			jump, _ := testSSHJump(t, node, metadata)
			node.Jump = &jump
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err := remoteWork(ctx, Config{SSH: &node}, workerRequest{Operation: "inspect"}); err == nil {
				t.Fatal("unsafe jump metadata accepted")
			}
		})
	}
}
