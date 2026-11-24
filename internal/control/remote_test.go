package control

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"debug/macho"
	"encoding/json"
	"fmt"
	"github.com/nerdylucifer/modelgauge/internal/agent"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nerdylucifer/modelgauge/internal/guest"
	"golang.org/x/crypto/ssh"
)

func TestRemoteHostInstallAndForwardKeyPreserveUnrelatedFiles(t *testing.T) {
	if _, err := exec.LookPath("shasum"); err != nil {
		t.Skip("installer verifies uploads with shasum")
	}
	home := t.TempDir()
	owner := strings.Repeat("a", 32)
	binary := []byte("test helper executable\n")
	host := RemoteHost{Owner: owner, HelperSHA256: helperSum(binary)}
	public, _, err := newSSHKey()
	if err != nil {
		t.Fatal(err)
	}
	sshDir := filepath.Join(home, ".ssh")
	if err = os.Mkdir(sshDir, 0700); err != nil {
		t.Fatal(err)
	}
	unrelated := "ssh-ed25519 AAAA unrelated-key\n"
	keys := filepath.Join(sshDir, "authorized_keys")
	if err = os.WriteFile(keys, []byte(unrelated), 0600); err != nil {
		t.Fatal(err)
	}
	root, _ := remoteRoot(home, owner)
	for i := 0; i < 2; i++ {
		cmd := exec.Command("sh", "-c", remoteHelperInstallScript(host, public))
		cmd.Env = append(os.Environ(), "HOME="+home)
		cmd.Stdin = bytes.NewReader(binary)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("install %d: %s: %v", i, out, err)
		}
	}
	data, _ := os.ReadFile(keys)
	if !strings.HasPrefix(string(data), unrelated) || strings.Count(string(data), "modelgauge-controller") != 1 {
		t.Fatal("installer duplicated/replaced existing keys", string(data))
	}
	if err = authorizeRemoteForward(home, root, owner, "127.0.0.1:2222"); err != nil {
		t.Fatal(err)
	}
	if err = authorizeRemoteForward(home, root, owner, "127.0.0.1:3333"); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(keys)
	if !strings.HasPrefix(string(data), unrelated) || !bytes.Contains(data, []byte(`permitopen="127.0.0.1:3333"`)) || bytes.Contains(data, []byte(`permitopen="127.0.0.1:2222"`)) {
		t.Fatal(string(data))
	}
	recorded, _ := os.ReadFile(filepath.Join(root, ".authorized-key"))
	if _, _, _, _, err = ssh.ParseAuthorizedKey(recorded); err != nil {
		t.Fatal("invalid forced-command key", err, string(recorded))
	}
	if err = (guest.Runtime{Home: home}).ReplaceControllerKey(recorded, nil); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(keys)
	if strings.TrimSpace(string(data)) != strings.TrimSpace(unrelated) {
		t.Fatal("cleanup changed unrelated key", string(data))
	}
}
func TestRemoteRootRejectsForeignLinks(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(home, ".modelgauge-remote")); err != nil {
		t.Fatal(err)
	}
	if _, err := remoteRoot(home, strings.Repeat("a", 32)); err == nil {
		t.Fatal("linked parent accepted")
	}
	for _, owner := range []string{"../outside", "", strings.Repeat("a", 31)} {
		if _, err := remoteRoot(t.TempDir(), owner); err == nil {
			t.Fatal(owner)
		}
	}
}
func TestLimaArchiveRejectsTraversalAndSpecialFiles(t *testing.T) {
	for _, name := range []string{"../outside", "/outside", "bin/limactl"} {
		var archive bytes.Buffer
		compressed := gzip.NewWriter(&archive)
		writer := tar.NewWriter(compressed)
		kind := byte(tar.TypeReg)
		if name == "bin/limactl" {
			kind = tar.TypeLink
		}
		if err := writer.WriteHeader(&tar.Header{Name: name, Typeflag: kind, Size: 0, Mode: 0755, Linkname: "outside"}); err != nil {
			t.Fatal(err)
		}
		writer.Close()
		compressed.Close()
		if err := extractLima(archive.Bytes(), t.TempDir()); err == nil {
			t.Fatal("unsafe archive accepted", name)
		}
	}
	var archive bytes.Buffer
	compressed := gzip.NewWriter(&archive)
	writer := tar.NewWriter(compressed)
	writer.WriteHeader(&tar.Header{Name: "./bin/limactl", Typeflag: tar.TypeReg, Mode: 0755, Size: 4})
	writer.Write([]byte("lima"))
	writer.Close()
	compressed.Close()
	root := t.TempDir()
	if err := extractLima(archive.Bytes(), root); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "bin", "limactl")); err != nil || string(data) != "lima" {
		t.Fatal(string(data), err)
	}
}
func TestBundledMacHelpersMatchTargetArchitectures(t *testing.T) {
	for arch, cpu := range map[string]macho.Cpu{"amd64": macho.CpuAmd64, "arm64": macho.CpuArm64} {
		data, err := macBinary(arch)
		if err != nil {
			if !strings.Contains(err.Error(), "not bundled") {
				t.Fatal(err)
			}
			continue
		}
		binary, err := macho.NewFile(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		defer binary.Close()
		if binary.Cpu != cpu {
			t.Fatal(arch, binary.Cpu)
		}
	}
}
func TestRemotePreparationRejectsInvalidInputWithoutPersistingCredentials(t *testing.T) {
	root := t.TempDir()
	for _, options := range []RemoteOptions{
		{CPUs: 0, MemoryGiB: 3, DiskGiB: 30},
		{CPUs: 2, MemoryGiB: 3, DiskGiB: 30, Password: "secret"},
		{CPUs: 2, MemoryGiB: 3, DiskGiB: 30, Address: "127.0.0.1:1", User: "user", Password: "secret", HostKey: "not a host key"},
	} {
		if _, err := PrepareRemote(context.Background(), root, options, io.Discard); err == nil {
			t.Fatal("invalid options accepted")
		}
	}
	files, err := os.ReadDir(root)
	if err != nil || len(files) != 0 {
		t.Fatal("bootstrap credentials or invalid ownership persisted", files, err)
	}
}
func TestRemoteTargetIntentContainsNoCredentials(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	config, _ := ConfigFile()
	if err := os.MkdirAll(filepath.Dir(config), 0700); err != nil {
		t.Fatal(err)
	}
	if err := TrackRemoteTarget(); err != nil {
		t.Fatal(err)
	}
	if !RemoteTargetTracked() {
		t.Fatal("missing remote cleanup intent")
	}
	path, _ := remoteTargetPath()
	data, _ := os.ReadFile(path)
	var marker map[string]bool
	if err := json.Unmarshal(data, &marker); err != nil || len(marker) != 1 || !marker["remote"] {
		t.Fatal(string(data), err)
	}
	if err := ForgetRemoteTarget(); err != nil || RemoteTargetTracked() {
		t.Fatal(err)
	}
}
func TestRemoteCleanupAbsentDoesNotCreateState(t *testing.T) {
	root := t.TempDir()
	report, err := CleanupRemote(context.Background(), root, CleanupOptions{DeleteVMs: true}, io.Discard)
	if err != nil || len(report.Targets) != 0 {
		t.Fatal(report, err)
	}
	files, _ := os.ReadDir(root)
	if len(files) != 0 {
		t.Fatal(files)
	}
}

func TestPinnedLimaArchiveLayout(t *testing.T) {
	path := os.Getenv("MODELGAUGE_TEST_LIMA_ARCHIVE")
	if path == "" {
		t.Skip("optional validation against the pinned upstream archive")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if helperSum(data) != "2dc5b10aa3a4f26d08c1f3fe83e37e01f85a7d9db0d1d5cb6985b18af96ab07d" {
		t.Fatal("upstream archive checksum mismatch")
	}
	root := t.TempDir()
	if err = extractLima(data, root); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"bin/limactl", "share/lima/lima-guestagent.Linux-x86_64.gz", "share/lima/templates/ubuntu.yaml"} {
		if _, err = os.Stat(filepath.Join(root, path)); err != nil {
			t.Fatal(path, err)
		}
	}
}

func remoteManagementPeer(t *testing.T, handle func(remoteHostRequest) any) agent.SSHConnection {
	t.Helper()
	public, private, err := newSSHKey()
	if err != nil {
		t.Fatal(err)
	}
	allowed, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(public))
	host, hostPrivate, err := newSSHKey()
	if err != nil {
		t.Fatal(err)
	}
	signer, _ := ssh.ParsePrivateKey([]byte(hostPrivate))
	config := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if !bytes.Equal(key.Marshal(), allowed.Marshal()) {
			return nil, io.EOF
		}
		return nil, nil
	}}
	config.AddHostKey(signer)
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
				peer, channels, requests, err := ssh.NewServerConn(raw, config)
				if err != nil {
					raw.Close()
					return
				}
				defer peer.Close()
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					channel, requests, err := incoming.Accept()
					if err != nil {
						return
					}
					go func() {
						defer channel.Close()
						for request := range requests {
							var command struct{ Command string }
							if request.Type != "exec" || ssh.Unmarshal(request.Payload, &command) != nil || command.Command != "manage" {
								request.Reply(false, nil)
								continue
							}
							request.Reply(true, nil)
							var in remoteHostRequest
							if json.NewDecoder(channel).Decode(&in) != nil {
								return
							}
							json.NewEncoder(channel).Encode(handle(in))
							channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
							return
						}
					}()
				}
			}()
		}
	}()
	return agent.SSHConnection{Address: listener.Addr().String(), User: "mac-user", PrivateKey: private, HostKey: host}
}
func TestRemotePrepareAndCleanupUseControllerCredentialsAndProtocol(t *testing.T) {
	directory := t.TempDir()
	nodeHost, _, err := newSSHKey()
	if err != nil {
		t.Fatal(err)
	}
	var requests []remoteHostRequest
	var mu sync.Mutex
	peer := remoteManagementPeer(t, func(in remoteHostRequest) any {
		mu.Lock()
		requests = append(requests, in)
		mu.Unlock()
		if in.Operation == "prepare" {
			stored, err := loadRemote(directory)
			if err != nil || stored.Node.PrivateKey == "" {
				t.Error("guest key installation began before the private credential was saved", err)
			}
			return agent.SSHConnection{Address: "127.0.0.1:2222", User: "guest", HostKey: nodeHost}
		}
		return CleanupReport{DryRun: in.Operation == "inspect", Targets: []CleanupTarget{{Name: "benchmark-node", Created: true, Status: "verified"}}, Untracked: []string{}}
	})
	host := RemoteHost{Owner: strings.Repeat("b", 32), Host: peer, HelperSHA256: strings.Repeat("c", 64)}
	if err = host.save(directory); err != nil {
		t.Fatal(err)
	}
	connection, err := PrepareRemote(context.Background(), directory, RemoteOptions{CPUs: 2, MemoryGiB: 3, DiskGiB: 30}, io.Discard)
	if err != nil || connection.Jump == nil || connection.Jump.Address != peer.Address || connection.User != "guest" {
		t.Fatal(connection, err)
	}
	signed, err := ssh.ParsePrivateKey([]byte(connection.PrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	first := requests[0]
	mu.Unlock()
	if first.CPUs != 2 || first.MemoryGiB != 3 || first.PublicKey != strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signed.PublicKey()))) {
		t.Fatal("worker key and requested resources were not delivered", first)
	}
	report, err := CleanupRemote(context.Background(), directory, CleanupOptions{DryRun: true, DeleteVMs: true}, io.Discard)
	if err != nil || !report.DryRun || !RemoteConfigured(directory) {
		t.Fatal(report, err)
	}
	if _, err = CleanupRemote(context.Background(), directory, CleanupOptions{DeleteVMs: true}, io.Discard); err != nil || RemoteConfigured(directory) {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 3 || requests[1].Operation != "inspect" || requests[2].Operation != "cleanup" {
		t.Fatal(requests)
	}
}
func TestRemoteVMCreationUsesIsolatedNetworkAndNoHostMounts(t *testing.T) {
	m, f := fixture(t)
	m.remote = true
	ctx, finish, err := m.provision(context.Background(), "target", "benchmark", 2, 3, 30, 0, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	var doneErr error
	completeProvision(ctx, finish, &doneErr)
	if doneErr != nil {
		t.Fatal(doneErr)
	}
	var create string
	for _, args := range f.calls {
		if args[0] == "create" {
			create = strings.Join(args, " ")
		}
	}
	for _, want := range []string{"--vm-type=vz", ".ssh.overVsock = false", ".mounts = []", `"ignore": true`, "--containerd none"} {
		if !strings.Contains(create, want) {
			t.Fatal(want, create)
		}
	}

	// Exercise Lima's actual YQ parser when available, not just the fake CLI.
	// Quoting YAML map keys matters here: valid YAML is not necessarily valid YQ.
	if lima, err := exec.LookPath("limactl"); err == nil {
		template := filepath.Join(t.TempDir(), "template.yaml")
		if err = os.WriteFile(template, []byte("images: []\n"), 0600); err != nil {
			t.Fatal(err)
		}
		var expressions []string
		for _, args := range f.calls {
			if args[0] == "create" {
				for i, arg := range args {
					if arg == "--set" {
						expressions = append(expressions, args[i+1])
					}
				}
			}
		}
		expression := strings.Join(expressions, " | ") + " | .portForwards[0].ignore"
		cmd := exec.CommandContext(context.Background(), lima, "template", "yq", template, expression)
		output, err := cmd.CombinedOutput()
		if err != nil || strings.TrimSpace(string(output)) != "true" {
			t.Fatalf("Lima rejected generated settings: %v\n%s", err, output)
		}
	}
}

func TestRemoteDependencyDownloadsVerifyAndDiscardPartialFiles(t *testing.T) {
	data := []byte("verified dependency")
	checksum := fmt.Sprintf("%x", sha256.Sum256(data))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(data) }))
	defer server.Close()
	root := t.TempDir()
	destination := filepath.Join(root, "dependency")
	if err := downloadRemoteFile(context.Background(), server.URL, strings.Repeat("0", 64), destination, 1024); err == nil {
		t.Fatal("bad checksum accepted")
	}
	files, _ := os.ReadDir(root)
	if len(files) != 0 {
		t.Fatal("failed download retained", files)
	}
	if err := downloadRemoteFile(context.Background(), server.URL, checksum, destination, 1024); err != nil {
		t.Fatal(err)
	}
	// A cached verified file needs no network.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := downloadRemoteFile(context.Background(), "invalid-address", checksum, destination, 1024); err != nil {
		t.Fatal("verified cache not reused", err)
	}
	if err := downloadRemoteFile(ctx, server.URL, checksum, filepath.Join(root, "cancelled"), 1024); err == nil {
		t.Fatal("cancelled download succeeded")
	}
	if err := os.WriteFile(destination, []byte("modified"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := downloadRemoteFile(context.Background(), server.URL, checksum, destination, 1024); err == nil {
		t.Fatal("modified cached dependency accepted")
	}
	files, _ = os.ReadDir(root)
	if len(files) != 1 {
		t.Fatal("partial download left behind", files)
	}
}
