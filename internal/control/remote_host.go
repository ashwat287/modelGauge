package control

// Remote-host operations reuse the local Lima provisioning and guest ownership
// checks. This helper runs on macOS; benchmark workers still require Linux.
import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/nerdylucifer/modelgauge/internal/agent"
	"github.com/nerdylucifer/modelgauge/internal/guest"
	"github.com/nerdylucifer/modelgauge/internal/platform"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
	"golang.org/x/crypto/ssh"
)

const remoteNodeName = "benchmark-node"
const limaVersion = "2.1.1"

type remoteHostRequest struct {
	Operation string                 `json:"operation"`
	CPUs      int                    `json:"cpus,omitempty"`
	MemoryGiB int                    `json:"memory_gib,omitempty"`
	DiskGiB   int                    `json:"disk_gib,omitempty"`
	PublicKey string                 `json:"public_key,omitempty"`
	Cleanup   CleanupOptions         `json:"cleanup,omitempty"`
	Evidence  []protocol.EvidenceRef `json:"evidence,omitempty"`
}

func remoteRoot(home, owner string) (string, error) {
	if !validOwner.MatchString(owner) {
		return "", errors.New("invalid remote ownership identity")
	}
	// macOS Unix sockets have a short path limit. The full owner identity is
	// still checked in the marker; a colliding prefix is never adopted.
	root := filepath.Join(home, ".modelgauge-remote", owner[:16])
	// Never traverse a link while installing or deleting an owned tree.
	for path := root; path != filepath.Dir(home); path = filepath.Dir(path) {
		info, err := os.Lstat(path)
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return "", fmt.Errorf("unsafe remote directory %s", path)
		}
	}
	return root, nil
}

// RunRemoteHost is only entered by the installed forced SSH command.
func RunRemoteHost(ctx context.Context, owner, command string, input io.Reader, out, log io.Writer) error {
	if runtime.GOOS != "darwin" {
		return errors.New("remote host management requires macOS")
	}
	if command != "manage" && command != "connection" {
		return errors.New("remote host command is not permitted")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	root, err := remoteRoot(home, owner)
	if err != nil {
		return err
	}
	marker, err := os.ReadFile(filepath.Join(root, ".owner"))
	if err != nil || strings.TrimSpace(string(marker)) != owner {
		return errors.New("remote ownership marker is missing or changed")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	binary, err := os.ReadFile(exe)
	if err != nil {
		return err
	}
	expected, err := os.ReadFile(filepath.Join(root, ".helper-sha256"))
	if err != nil || strings.TrimSpace(string(expected)) != helperSum(binary) {
		return errors.New("remote management helper changed")
	}
	// These environment changes affect only this short-lived helper and its children.
	if err = os.Setenv("LIMA_HOME", filepath.Join(root, "vms")); err != nil {
		return err
	}
	if err = os.Setenv("PATH", filepath.Join(root, "lima", "bin")+":"+os.Getenv("PATH")); err != nil {
		return err
	}
	m := &ownershipManager{directory: filepath.Join(root, "state"), run: runLima, remote: true}
	ctx, cancel := context.WithTimeout(ctx, 24*time.Hour)
	defer cancel()
	if command == "connection" {
		c, err := remoteGuestConnection(ctx, m, "", true)
		if err != nil {
			return err
		}
		if err = json.NewEncoder(out).Encode(c); err != nil {
			return err
		}
		// Keep this session open alongside the nested SSH transport. Caffeinate is
		// scoped to this connection, never a permanent change to power settings.
		go func() { _, _ = io.Copy(io.Discard, input); cancel() }()
		cmd := platform.CommandContext(ctx, "/usr/bin/caffeinate", "-i")
		_ = cmd.Run()
		return ctx.Err()
	}
	decoder := json.NewDecoder(io.LimitReader(input, 8<<20))
	decoder.DisallowUnknownFields()
	var request remoteHostRequest
	if err = decoder.Decode(&request); err != nil {
		return err
	}
	if request.Operation != "prepare" && request.Operation != "cleanup" && request.Operation != "inspect" && request.Operation != "evidence" {
		return errors.New("unknown remote management operation")
	}
	work, stop := context.WithCancel(ctx)
	defer stop()
	// Losing the controller must stop child provisioning and invoke normal rollback.
	watchdog := time.AfterFunc(25*time.Second, stop)
	defer watchdog.Stop()
	go func() {
		for {
			var heartbeat struct{}
			if decoder.Decode(&heartbeat) != nil {
				stop()
				return
			}
			watchdog.Reset(25 * time.Second)
		}
	}()
	switch request.Operation {
	case "prepare":
		awake, stopAwake := context.WithCancel(work)
		preventSleep := platform.CommandContext(awake, "/usr/bin/caffeinate", "-i")
		if err = preventSleep.Start(); err != nil {
			stopAwake()
			return err
		}
		defer func() { stopAwake(); _ = preventSleep.Wait() }()
		if request.CPUs < 1 || request.CPUs > 4 || request.MemoryGiB < 2 || request.DiskGiB < 10 {
			return errors.New("remote VM requires 1..4 CPUs, at least 2 GiB RAM and 10 GiB disk")
		}
		fmt.Fprintln(log, "Remote prerequisites: running")
		if err = checkRemoteMac(work, request.MemoryGiB, request.DiskGiB, root); err != nil {
			return err
		}
		if err = installRemoteLima(work, root, log); err != nil {
			return err
		}
		template, err := prepareRemoteImage(work, root, log)
		if err != nil {
			return err
		}
		m.image = template
		fmt.Fprintln(log, "Remote prerequisites: completed")
		options := NodeOptions{Name: remoteNodeName, Revision: LlamaRevision, CPUs: request.CPUs, MemoryGiB: request.MemoryGiB, DiskGiB: request.DiskGiB, Log: log, manager: m, detached: true}
		if err = BootstrapNode(work, Config{}, options); err != nil {
			return err
		}
		connection, err := remoteGuestConnection(work, m, request.PublicKey, false)
		if err != nil {
			return err
		}
		if err = authorizeRemoteForward(home, root, owner, connection.Address); err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(connection)
	case "inspect", "cleanup":
		options := request.Cleanup
		options.DryRun = request.Operation == "inspect"
		report, err := m.cleanup(work, options)
		if err != nil {
			return err
		}
		if !options.DryRun && options.DeleteVMs && len(report.Untracked) == 0 {
			recorded, err := os.ReadFile(filepath.Join(root, ".authorized-key"))
			if err != nil {
				return err
			}
			if err = (guest.Runtime{Home: home}).ReplaceControllerKey(recorded, nil); err != nil {
				return err
			}
			if _, err = remoteRoot(home, owner); err != nil {
				return err
			}
			if err = os.RemoveAll(root); err != nil {
				return err
			}
		}
		return json.NewEncoder(out).Encode(report)
	case "evidence":
		registry, err := m.load()
		if err != nil {
			return err
		}
		if len(registry.VMs) != 1 || registry.VMs[0].Cleaned {
			return errors.New("remote evidence VM is not prepared")
		}
		v := registry.VMs[0]
		instances, err := m.instances(work)
		if err != nil {
			return err
		}
		vm, ok := instances[v.Name]
		if !ok || vm.Status != "Running" {
			return errors.New("remote evidence VM must be running")
		}
		if err = verifyVM(v, vm); err != nil {
			return err
		}
		if _, err = m.guest(work, v, "identity", nil); err != nil {
			return err
		}
		payload, err := json.Marshal(request.Evidence)
		if err != nil {
			return err
		}
		script := `exec "$HOME/.local/share/modelgauge/guest-helper" _guest evidence`
		cmd := platform.CommandContext(work, "limactl", "shell", "--workdir", "/", v.Name, "--", "sh", "-c", script)
		cmd.Stdin = strings.NewReader(string(payload))
		cmd.Stdout = out
		cmd.Stderr = log
		return cmd.Run()
	}
	return nil
}

func remoteGuestConnection(ctx context.Context, m *ownershipManager, public string, start bool) (agent.SSHConnection, error) {
	var c agent.SSHConnection
	registry, err := m.load()
	if err != nil {
		return c, err
	}
	if len(registry.VMs) != 1 || !registry.VMs[0].Prepared || registry.VMs[0].Cleaned {
		return c, errors.New("remote benchmark VM is not prepared; run setup")
	}
	v := registry.VMs[0]
	instances, err := m.instances(ctx)
	if err != nil {
		return c, err
	}
	instance, ok := instances[v.Name]
	if !ok {
		return c, errors.New("owned remote benchmark VM is missing")
	}
	if err = verifyVM(v, instance); err != nil {
		return c, err
	}
	if instance.Status != "Running" {
		if !start {
			return c, errors.New("remote benchmark VM is stopped")
		}
		if _, err = m.run(ctx, []string{"start", "--tty=false", v.Name}, nil); err != nil {
			return c, err
		}
		instances, err = m.instances(ctx)
		if err != nil {
			return c, err
		}
		instance = instances[v.Name]
		if err = verifyVM(v, instance); err != nil {
			return c, err
		}
	}
	if _, err = m.guest(ctx, v, "identity", nil); err != nil {
		return c, err
	}
	if instance.SSHLocalPort < 1 || instance.SSHLocalPort > 65535 {
		return c, errors.New("invalid remote VM SSH port")
	}
	c.Address = net.JoinHostPort("127.0.0.1", strconv.Itoa(instance.SSHLocalPort))
	user, err := m.run(ctx, []string{"shell", "--workdir", "/", v.Name, "--", "id", "-un"}, nil)
	if err != nil {
		return c, err
	}
	c.User = strings.TrimSpace(string(user))
	key, err := m.run(ctx, []string{"shell", "--workdir", "/", v.Name, "--", "cat", "/etc/ssh/ssh_host_ed25519_key.pub"}, nil)
	if err != nil {
		return c, err
	}
	c.HostKey = strings.TrimSpace(string(key))
	if public != "" {
		if _, _, _, _, err = ssh.ParseAuthorizedKey([]byte(public)); err != nil {
			return c, err
		}
		for _, command := range []string{"controller-key", "controller-key-commit"} {
			script := `exec "$HOME/.local/bin/modelgauge" _guest ` + command
			if _, err = m.run(ctx, []string{"shell", "--workdir", "/", v.Name, "--", "sh", "-c", script}, []byte(public)); err != nil {
				return c, err
			}
		}
	}
	return c, nil
}

func authorizeRemoteForward(home, root, owner, address string) error {
	recorded := filepath.Join(root, ".authorized-key")
	old, err := os.ReadFile(recorded)
	if err != nil {
		return err
	}
	key, _, _, _, err := ssh.ParseAuthorizedKey(old)
	if err != nil {
		return err
	}
	command := quote(filepath.Join(root, "helper")) + " _remote-host " + owner
	next := "restrict,port-forwarding,permitopen=" + strconv.Quote(address) + ",command=" + strconv.Quote(command) + " " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))) + " modelgauge-controller\n"
	// Record both versions first so a failed replacement can be reconciled.
	if err = platform.WriteFile(recorded, []byte(string(old)+next), 0600); err != nil {
		return err
	}
	if err = (guest.Runtime{Home: home}).ReplaceControllerKey(old, []byte(next)); err != nil {
		return err
	}
	return platform.WriteFile(recorded, []byte(next), 0600)
}

func checkRemoteMac(ctx context.Context, memory, disk int, root string) error {
	if len(filepath.Join(root, "vms", remoteNodeName, "ssh.sock")) >= 104 {
		return errors.New("remote home path is too long for Lima's macOS Unix sockets")
	}
	cmd := platform.CommandContext(ctx, "/usr/bin/sw_vers", "-productVersion")
	raw, err := cmd.Output()
	if err != nil {
		return err
	}
	var major, minor int
	_, _ = fmt.Sscanf(string(raw), "%d.%d", &major, &minor)
	if major < 13 || major == 13 && minor < 5 {
		return errors.New("remote Mac requires macOS 13.5 or newer for the supported Lima VZ setup")
	}
	cmd = platform.CommandContext(ctx, "/usr/sbin/sysctl", "-n", "kern.hv_support")
	raw, err = cmd.Output()
	if err != nil || strings.TrimSpace(string(raw)) != "1" {
		return errors.New("hardware virtualization is unavailable")
	}
	cmd = platform.CommandContext(ctx, "/usr/sbin/sysctl", "-n", "hw.memsize")
	raw, err = cmd.Output()
	if err != nil {
		return err
	}
	ram, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return err
	}
	if int64(memory+2)<<30 > ram {
		return errors.New("remote VM allocation must leave at least 2 GiB RAM for macOS")
	}
	// Sparse VM disks grow on demand; require room for the requested maximum.
	cmd = platform.CommandContext(ctx, "/bin/df", "-Pk", root)
	raw, err = cmd.Output()
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 4 {
		return errors.New("cannot read remote disk capacity")
	}
	free, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil {
		return err
	}
	if free*1024 < int64(disk+2)<<30 {
		return fmt.Errorf("remote host needs at least %d GiB free disk", disk+2)
	}
	return nil
}

func installRemoteLima(ctx context.Context, root string, log io.Writer) error {
	destination := filepath.Join(root, "lima")
	sums := map[string]string{"amd64": "2dc5b10aa3a4f26d08c1f3fe83e37e01f85a7d9db0d1d5cb6985b18af96ab07d", "arm64": "b6b0e6701189cd8c4e549cc39e6d054dc681487798b9b774ad2cbd30c08b2bd8"}
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x86_64"
	}
	checksum := sums[runtime.GOARCH]
	if checksum == "" {
		return errors.New("unsupported remote Mac architecture")
	}
	if data, err := os.ReadFile(filepath.Join(destination, ".archive-sha256")); err == nil {
		if strings.TrimSpace(string(data)) != checksum {
			return errors.New("remote Lima distribution identity changed")
		}
		expected, err := os.ReadFile(filepath.Join(destination, ".limactl-sha256"))
		if err != nil {
			return err
		}
		installed, err := os.ReadFile(filepath.Join(destination, "bin", "limactl"))
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(expected)) != helperSum(installed) {
			return errors.New("remote limactl executable changed")
		}
		cmd := platform.CommandContext(ctx, filepath.Join(destination, "bin", "limactl"), "--version")
		return cmd.Run()
	}
	fmt.Fprintln(log, "Downloading pinned Lima "+limaVersion)
	stage, err := os.MkdirTemp(root, ".lima-install-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	archive := filepath.Join(stage, "lima.tar.gz")
	address := "https://github.com/lima-vm/lima/releases/download/v" + limaVersion + "/lima-" + limaVersion + "-Darwin-" + arch + ".tar.gz"
	if err = downloadRemoteFile(ctx, address, checksum, archive, 128<<20); err != nil {
		return err
	}
	data, err := os.ReadFile(archive)
	if err != nil {
		return err
	}
	unpacked := filepath.Join(stage, "unpacked")
	if err = os.Mkdir(unpacked, 0700); err != nil {
		return err
	}
	if err = extractLima(data, unpacked); err != nil {
		return err
	}
	if err = platform.WriteFile(filepath.Join(unpacked, ".archive-sha256"), []byte(checksum), 0600); err != nil {
		return err
	}
	installed, err := os.ReadFile(filepath.Join(unpacked, "bin", "limactl"))
	if err != nil {
		return err
	}
	if err = platform.WriteFile(filepath.Join(unpacked, ".limactl-sha256"), []byte(helperSum(installed)), 0600); err != nil {
		return err
	}
	if _, err = os.Lstat(destination); err == nil {
		return errors.New("refusing to replace an unverified Lima installation")
	}
	return os.Rename(unpacked, destination)
}
func extractLima(data []byte, destination string) error {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer reader.Close()
	archive := tar.NewReader(reader)
	var total int64
	for {
		header, err := archive.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(header.Name)
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return errors.New("unsafe Lima archive path")
		}
		if header.Typeflag == tar.TypeSymlink {
			continue
		} // limactl is a real binary; optional aliases are unnecessary.
		if header.Typeflag != tar.TypeDir && header.Typeflag != tar.TypeReg {
			return errors.New("unsupported Lima archive entry")
		}
		path := filepath.Join(destination, name)
		if header.Typeflag == tar.TypeDir {
			if err = os.MkdirAll(path, 0755); err != nil {
				return err
			}
			continue
		}
		total += header.Size
		if total > 512<<20 || header.Size < 0 {
			return errors.New("Lima archive too large")
		}
		if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(header.Mode)&0755)
		if err != nil {
			return err
		}
		_, err = io.Copy(file, archive)
		closed := file.Close()
		if err != nil {
			return err
		}
		if closed != nil {
			return closed
		}
	}
}

// Keep the OS image in the owned tree rather than Lima's shared host cache.
// Ubuntu 24.04's GA kernel also avoids the Intel VZ kernel-6.12 boot regression.
func prepareRemoteImage(ctx context.Context, root string, log io.Writer) (string, error) {
	arch, digest := "amd64", "04e8f2461a16a44e78a370284471f29616cdd9baf1893df25807b71f985b6c94"
	limaArch := "x86_64"
	if runtime.GOARCH == "arm64" {
		arch = "arm64"
		limaArch = "aarch64"
		digest = "50e24e1fb79e89116e939d90aa7738f7af48c442fa1bc2510131e0b39c50348e"
	}
	image := filepath.Join(root, "ubuntu-24.04-"+arch+".img")
	fmt.Fprintln(log, "Preparing pinned Ubuntu 24.04 image")
	address := "https://cloud-images.ubuntu.com/releases/noble/release-20261017/ubuntu-24.04-server-cloudimg-" + arch + ".img"
	if err := downloadRemoteFile(ctx, address, digest, image, 3<<30); err != nil {
		return "", err
	}
	template := filepath.Join(root, "benchmark.yaml")
	data, err := json.Marshal(map[string]any{"images": []any{map[string]string{"location": image, "arch": limaArch, "digest": "sha256:" + digest}}})
	if err != nil {
		return "", err
	}
	return template, platform.WriteFile(template, data, 0600)
}
func downloadRemoteFile(ctx context.Context, address, checksum, destination string, limit int64) error {
	if info, err := os.Lstat(destination); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("download destination is not regular")
		}
		actual, err := platform.SHA256File(ctx, destination)
		if err != nil {
			return err
		}
		if actual != checksum {
			return errors.New("cached remote dependency checksum mismatch")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, "GET", address, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 30 * time.Minute, CheckRedirect: func(r *http.Request, previous []*http.Request) error {
		if r.URL.Scheme != "https" {
			return errors.New("refusing an insecure dependency redirect")
		}
		if len(previous) > 10 {
			return errors.New("too many dependency redirects")
		}
		return nil
	}}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("dependency download: %s", response.Status)
	}
	file, err := os.CreateTemp(filepath.Dir(destination), ".download-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	hash := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, limit+1))
	if copyErr == nil {
		copyErr = file.Sync()
	}
	closed := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closed != nil {
		return closed
	}
	if size > limit || fmt.Sprintf("%x", hash.Sum(nil)) != checksum {
		return errors.New("remote dependency size or checksum mismatch")
	}
	return os.Rename(file.Name(), destination)
}
