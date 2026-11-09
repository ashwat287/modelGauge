package control

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nerdylucifer/modelgauge/internal/buildinfo"
	"github.com/nerdylucifer/modelgauge/internal/platform"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const ownershipSchema = "modelgauge-ownership/v1"

var validOwner = regexp.MustCompile(`^[a-f0-9]{32}$`)
var validScope = regexp.MustCompile(`^modelgauge-provision-[a-f0-9]{32}\.scope$`)
var validVMName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

type ownedVM struct {
	Scopes         []string `json:"provisioning_scopes,omitempty"`
	BootstrapState string   `json:"bootstrap_state,omitempty"`

	Name         string   `json:"name"`
	ID           string   `json:"id"`
	Role         string   `json:"role"`
	Created      bool     `json:"created"`
	Dir          string   `json:"instance_directory"`
	MachineID    string   `json:"machine_id,omitempty"`
	Prepared     bool     `json:"prepared"`
	Cleaned      bool     `json:"cleaned"`
	HelperSHA256 string   `json:"helper_sha256,omitempty"`
	HelperArch   string   `json:"helper_arch,omitempty"`
	Files        []string `json:"reserved_files,omitempty"`
}
type ownershipRegistry struct {
	Schema string    `json:"schema_version"`
	VMs    []ownedVM `json:"vms"`
}
type limaInstance struct {
	Name      string `json:"name"`
	Dir       string `json:"dir"`
	Status    string `json:"status"`
	Protected bool   `json:"protected"`
}
type ownershipManager struct {
	directory string
	binary    func(context.Context, string) ([]byte, error)
	run       func(context.Context, []string, []byte) ([]byte, error)
}
type ownershipContextKey struct{}

func newOwnershipManager() (*ownershipManager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &ownershipManager{directory: filepath.Join(home, ".config", "modelgauge"), run: runLima}, nil
}
func runLima(ctx context.Context, args []string, input []byte) ([]byte, error) {
	cmd := platform.CommandContext(ctx, "limactl", args...)
	cmd.Stdin = bytes.NewReader(input)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("limactl: %w: %s", errors.Join(err, ctx.Err()), strings.TrimSpace(stderr.String()))
	}
	return out.Bytes(), nil
}
func (m *ownershipManager) path() string { return filepath.Join(m.directory, "ownership.json") }
func (m *ownershipManager) lock() (func(), error) {
	if err := os.MkdirAll(m.directory, 0700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(m.directory); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("ownership directory is unsafe")
	}
	p := filepath.Join(m.directory, ".ownership.lock")
	if info, err := os.Lstat(p); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("ownership lock is a symlink")
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("bootstrap/cleanup already in progress: %w", err)
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}
func (m *ownershipManager) load() (ownershipRegistry, error) {
	registry := ownershipRegistry{Schema: ownershipSchema}
	if info, err := os.Lstat(m.directory); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return registry, fmt.Errorf("ownership directory is a symlink")
	}
	if info, err := os.Lstat(m.path()); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return registry, fmt.Errorf("ownership registry is a symlink")
	}
	data, err := os.ReadFile(m.path())
	if errors.Is(err, os.ErrNotExist) {
		return registry, nil
	}
	if err != nil {
		return registry, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&registry); err != nil {
		return registry, err
	}
	if registry.Schema != ownershipSchema {
		return registry, fmt.Errorf("unsupported ownership manifest")
	}
	names := map[string]bool{}
	for _, v := range registry.VMs {
		if !validVMName.MatchString(v.Name) || !validOwner.MatchString(v.ID) || (v.Role != "control" && v.Role != "benchmark") || !filepath.IsAbs(v.Dir) || names[v.Name] {
			return registry, fmt.Errorf("invalid or duplicate ownership record")
		}
		if v.MachineID != "" && !validOwner.MatchString(v.MachineID) {
			return registry, fmt.Errorf("invalid machine identity")
		}
		if (v.HelperSHA256 != "" || v.HelperArch != "") && (!regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(v.HelperSHA256) || (v.HelperArch != "amd64" && v.HelperArch != "arm64")) {
			return registry, fmt.Errorf("invalid guest helper identity")
		}
		for _, scope := range v.Scopes {
			if !validScope.MatchString(scope) {
				return registry, fmt.Errorf("invalid provisioning scope")
			}
		}
		names[v.Name] = true
	}
	return registry, nil
}
func (m *ownershipManager) save(registry ownershipRegistry) error {
	return platform.WriteJSON(m.path(), registry, 0600)
}

func (m *ownershipManager) instances(ctx context.Context) (map[string]limaInstance, error) {
	data, err := m.run(ctx, []string{"list", "--json"}, nil)
	if err != nil {
		return nil, err
	}
	result := map[string]limaInstance{}
	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		var vm limaInstance
		err = dec.Decode(&vm)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if !validVMName.MatchString(vm.Name) {
			return nil, fmt.Errorf("invalid Lima instance name")
		}
		result[vm.Name] = vm
	}
	return result, nil
}
func markerPath(dir string) string { return filepath.Join(dir, ".modelgauge-owner") }
func canonicalInstanceDir(dir string) (string, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("instance directory must not be a symlink")
	}
	return filepath.EvalSymlinks(dir)
}
func verifyVM(v ownedVM, instance limaInstance) error {
	dir, err := canonicalInstanceDir(instance.Dir)
	if err != nil {
		return err
	}
	if dir != v.Dir {
		return fmt.Errorf("VM %s has been replaced or moved", v.Name)
	}
	if v.Created {
		path := markerPath(dir)
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("VM %s ownership marker missing: %w", v.Name, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("VM ownership marker is not a regular file")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(data)) != v.ID {
			return fmt.Errorf("VM %s ownership marker mismatch", v.Name)
		}
	}
	return nil
}
func (m *ownershipManager) guest(ctx context.Context, v ownedVM, operation string, files []string) ([]byte, error) {
	payload, err := ownershipPayload(v, operation, files)
	if err != nil {
		return nil, err
	}
	binary, err := m.readHelper(v)
	if err != nil {
		return nil, err
	}
	args, input, err := helperInvocation(v, operation, payload, binary)
	if err != nil {
		return nil, err
	}
	output, err := m.run(ctx, args, input)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", operation, v.Name, err)
	}
	var identity struct {
		MachineID string `json:"machine_id"`
	}
	if err = json.Unmarshal(output, &identity); err != nil {
		return nil, err
	}
	if v.MachineID != "" && identity.MachineID != v.MachineID {
		return nil, fmt.Errorf("guest %s machine identity changed", v.Name)
	}
	return output, nil
}
func reservedFiles(role string, port int) []string {
	if role == "benchmark" {
		return []string{".local/bin/modelgauge", ".config/systemd/user/modelgauge-agent.service"}
	}
	return []string{".local/bin/node_exporter", ".config/systemd/user/modelgauge-node-exporter.service", ".kube/modelgauge-config", ".kube/config", fmt.Sprintf(".config/systemd/user/modelgauge-forward-%d.service", port)}
}

// provisionVM journals ownership before installing packages or writing app data.
// Reusing a VM never grants permission to delete that VM.
func provisionVM(ctx context.Context, name, role string, cpus, memory, disk, port int, log io.Writer, options NodeOptions) (context.Context, func() error, error) {
	m, err := newOwnershipManager()
	if err != nil {
		return ctx, nil, err
	}
	if options.Source == "" && options.AgentBinary == "" && !buildinfo.Released() {
		if _, e := os.Stat("go.mod"); e == nil {
			options.Source = "."
		}
	}
	m.binary = func(ctx context.Context, arch string) ([]byte, error) { return nodeBinary(ctx, options, arch) }
	return m.provision(ctx, name, role, cpus, memory, disk, port, log)
}
func (m *ownershipManager) provision(ctx context.Context, name, role string, cpus, memory, disk, port int, log io.Writer) (managed context.Context, done func() error, resultErr error) {
	unlock, err := m.lock()
	if err != nil {
		return ctx, nil, err
	}
	failed := true
	session := &bootstrapSession{manager: m}
	session.previousConnection, _ = os.ReadFile(m.directory + "/control.json")
	defer func() {
		if failed {
			if session.newVM && session.vm.Dir != "" {
				recovery, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				err := m.rollbackNewVM(recovery, session.vm, session.previousConnection)
				cancel()
				if err != nil {
					resultErr = errors.Join(resultErr, fmt.Errorf("rollback failed; ownership retained: %w", err))
				} else {
					resultErr = errors.Join(resultErr, fmt.Errorf("rollback completed: newly created VM %s removed", name))
				}
			}
			unlock()
		}
	}()
	registry, err := m.load()
	if err != nil {
		return ctx, nil, err
	}
	instances, err := m.instances(ctx)
	if err != nil {
		return ctx, nil, err
	}
	instance, exists := instances[name]
	index := -1
	for i, v := range registry.VMs {
		if v.Name == name {
			index = i
			break
		}
	}
	if index >= 0 {
		if !exists {
			return ctx, nil, fmt.Errorf("tracked VM %s is missing; run cleanup to clear stale ownership before recreating it", name)
		}
		if registry.VMs[index].Role != role {
			return ctx, nil, fmt.Errorf("tracked VM role differs")
		}
		if err = verifyVM(registry.VMs[index], instance); err != nil {
			return ctx, nil, err
		}
	} else {
		owner := make([]byte, 16)
		if _, err = rand.Read(owner); err != nil {
			return ctx, nil, err
		}
		v := ownedVM{BootstrapState: "provisioning", Name: name, Role: role, ID: hex.EncodeToString(owner), Created: !exists, Files: reservedFiles(role, port)}
		if !exists {
			fmt.Fprintf(log, "Creating owned Lima VM %s\n", name)
			_, err = m.run(ctx, []string{"create", "--tty=false", "--name", name, "--cpus", fmt.Sprint(cpus), "--memory", fmt.Sprint(memory), "--disk", fmt.Sprint(disk), "--containerd", "none", "template:ubuntu"}, nil)
			if err != nil {
				return ctx, nil, err
			}
			instances, err = m.instances(ctx)
			if err != nil {
				return ctx, nil, err
			}
			instance, exists = instances[name]
			if !exists {
				return ctx, nil, fmt.Errorf("created VM was not found")
			}
		}
		v.Dir, err = canonicalInstanceDir(instance.Dir)
		if err != nil {
			return ctx, nil, err
		}
		if v.Created {
			f, e := os.OpenFile(markerPath(v.Dir), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if e != nil {
				return ctx, nil, e
			}
			_, e = f.WriteString(v.ID + "\n")
			if e == nil {
				e = f.Sync()
			}
			closeErr := f.Close()
			if e != nil {
				return ctx, nil, e
			}
			if closeErr != nil {
				return ctx, nil, closeErr
			}
		}
		if v.Created {
			session.newVM = true
			session.vm = v
		}
		registry.VMs = append(registry.VMs, v)
		index = len(registry.VMs) - 1
		if err = m.save(registry); err != nil {
			return ctx, nil, err
		}
	}
	fmt.Fprintf(log, "Starting %s (VM created by ModelGauge: %t)\n", name, registry.VMs[index].Created)
	if _, err = m.run(ctx, []string{"start", "--tty=false", name}, nil); err != nil {
		return ctx, nil, err
	}
	output, err := m.run(ctx, []string{"shell", "--workdir", "/", name, "--", "cat", "/etc/machine-id"}, nil)
	if err != nil {
		return ctx, nil, err
	}
	identity := strings.TrimSpace(string(output))
	if !validOwner.MatchString(identity) {
		return ctx, nil, fmt.Errorf("invalid guest machine identity")
	}
	if prior := registry.VMs[index].MachineID; prior != "" && prior != identity {
		return ctx, nil, fmt.Errorf("guest VM identity changed")
	}
	for _, scope := range registry.VMs[index].Scopes {
		if err = m.stopScope(ctx, name, scope); err != nil {
			return ctx, nil, fmt.Errorf("stop interrupted provisioning: %w", err)
		}
	}
	registry.VMs[index].Scopes = nil
	registry.VMs[index].BootstrapState = "provisioning"

	registry.VMs[index].MachineID = identity
	session.vm = registry.VMs[index]
	priorHelper := registry.VMs[index]
	var previousHelper []byte
	if !session.newVM && priorHelper.HelperSHA256 != "" {
		previousHelper, err = m.readHelper(priorHelper)
		if err != nil {
			return ctx, nil, err
		}
	}
	if err = m.prepareHelper(ctx, &registry.VMs[index]); err != nil {
		return ctx, nil, err
	}
	if err = m.save(registry); err != nil {
		return ctx, nil, err
	}
	session.vm = registry.VMs[index]
	v := registry.VMs[index]
	files := reservedFiles(role, port)
	prepared, err := m.guest(ctx, v, "bootstrap-prepare", files)
	if err != nil {
		if len(previousHelper) > 0 {
			recovery, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			restoreErr := m.restorePreviousHelper(recovery, priorHelper, previousHelper)
			cancel()
			err = errors.Join(err, restoreErr)
		}
		return ctx, nil, err
	}
	registry.VMs[index].Prepared = true
	registry.VMs[index].Cleaned = false
	var snapshot struct {
		Files map[string]any `json:"files"`
	}
	if err = json.Unmarshal(prepared, &snapshot); err != nil {
		return ctx, nil, err
	}
	registry.VMs[index].Files = nil
	for file := range snapshot.Files {
		registry.VMs[index].Files = append(registry.VMs[index].Files, file)
	}
	if err = m.save(registry); err != nil {
		return ctx, nil, err
	}
	session.vm = registry.VMs[index]
	ctx = context.WithValue(ctx, bootstrapKey{}, session)
	ctx = context.WithValue(ctx, ownershipContextKey{}, m)
	failed = false
	finish := func() error {
		defer unlock()
		if session.aborted {
			return nil
		}
		sealCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, e := m.guest(sealCtx, registry.VMs[index], "seal", nil)
		return e
	}
	return ctx, finish, nil
}

// reserveForward records an exact unit file before Forward creates or rewrites it.
func reserveForward(ctx context.Context, name string, port int) (func() error, error) {
	m, ok := ctx.Value(ownershipContextKey{}).(*ownershipManager)
	var unlock func()
	var err error
	if !ok {
		m, err = newOwnershipManager()
		if err != nil {
			return nil, err
		}
		unlock, err = m.lock()
		if err != nil {
			return nil, err
		}
	} else {
		unlock = func() {}
	}
	registry, err := m.load()
	if err != nil {
		unlock()
		return nil, err
	}
	for i, v := range registry.VMs {
		if v.Name != name {
			continue
		}
		instances, e := m.instances(ctx)
		if e != nil {
			unlock()
			return nil, e
		}
		if e = verifyVM(v, instances[name]); e != nil {
			unlock()
			return nil, e
		}
		if v.Role != "control" {
			unlock()
			return nil, fmt.Errorf("forwards require an owned control installation")
		}
		if ok {
			if _, e = m.guest(ctx, v, "seal", nil); e != nil {
				unlock()
				return nil, e
			}
		}
		file := fmt.Sprintf(".config/systemd/user/modelgauge-forward-%d.service", port)
		if _, e = m.guest(ctx, v, "prepare", []string{file}); e != nil {
			unlock()
			return nil, e
		}
		if !contains(v.Files, file) {
			registry.VMs[i].Files = append(v.Files, file)
			if e = m.save(registry); e != nil {
				unlock()
				return nil, e
			}
		}
		return func() error {
			defer unlock()
			sealCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_, e := m.guest(sealCtx, v, "seal", nil)
			return e
		}, nil
	}
	unlock()
	return nil, fmt.Errorf("control VM is untracked; bootstrap a new owned installation first")
}
func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func ownershipID(ctx context.Context, name string) (string, error) {
	m, ok := ctx.Value(ownershipContextKey{}).(*ownershipManager)
	if !ok {
		return "", fmt.Errorf("ownership context missing")
	}
	registry, err := m.load()
	if err != nil {
		return "", err
	}
	for _, v := range registry.VMs {
		if v.Name == name {
			return v.ID, nil
		}
	}
	return "", fmt.Errorf("ownership record missing")
}

type bootstrapKey struct{}
type bootstrapSession struct {
	manager            *ownershipManager
	vm                 ownedVM
	newVM              bool
	aborted            bool
	previousConnection []byte
}

// completeProvision runs on every ordinary failure and graceful cancellation.
// A fresh recovery context is necessary because provisioning may be cancelled.
func completeProvision(ctx context.Context, finish func() error, resultErr *error) {
	if recover() != nil {
		*resultErr = errors.Join(*resultErr, fmt.Errorf("unexpected provisioning failure"))
	}
	session, _ := ctx.Value(bootstrapKey{}).(*bootstrapSession)
	if session == nil {
		*resultErr = errors.Join(*resultErr, finish())
		return
	}
	defer func() { session.aborted = true; *resultErr = errors.Join(*resultErr, finish()) }()
	if *resultErr == nil {
		check, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, err := session.manager.guest(check, session.vm, "seal", nil)
		cancel()
		if err == nil {
			registry, e := session.manager.load()
			if e == nil {
				for i := range registry.VMs {
					if registry.VMs[i].ID == session.vm.ID {
						registry.VMs[i].BootstrapState = "ready"
					}
				}
				e = session.manager.save(registry)
			}
			if e == nil {
				return
			}
			err = e
		}
		*resultErr = fmt.Errorf("finalize ownership: %w", err)
	}
	if !session.newVM {
		if registry, e := session.manager.load(); e == nil {
			for i := range registry.VMs {
				if registry.VMs[i].ID == session.vm.ID {
					registry.VMs[i].BootstrapState = "recovery_required"
				}
			}
			*resultErr = errors.Join(*resultErr, session.manager.save(registry))
		}
		*resultErr = errors.Join(*resultErr, fmt.Errorf("existing VM %s retained; verified file checkpoints preserved; retry after resolving the original error", session.vm.Name))

		return
	}
	recovery, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := session.manager.rollbackNewVM(recovery, session.vm, session.previousConnection); err != nil {
		*resultErr = errors.Join(*resultErr, fmt.Errorf("rollback failed; ownership retained for %s: %w", session.vm.Name, err))

	} else {
		*resultErr = errors.Join(*resultErr, fmt.Errorf("rollback completed: newly created VM %s removed; previous installations preserved", session.vm.Name))

	}
}

// rollbackNewVM only receives provenance from the invocation that created it.
// It does not use guest file sealing as authority to delete an entire new VM.
func (m *ownershipManager) rollbackNewVM(ctx context.Context, v ownedVM, previous []byte) error {
	instances, err := m.instances(ctx)
	if err != nil {
		return err
	}
	vm, present := instances[v.Name]
	if present {
		if !v.Created {
			return fmt.Errorf("refusing rollback of a reused VM")
		}
		if vm.Protected {
			return fmt.Errorf("VM is protected")
		}
		if err = verifyVM(v, vm); err != nil {
			return err
		}
		if vm.Status == "Running" && v.MachineID != "" {
			raw, e := m.run(ctx, []string{"shell", "--workdir", "/", v.Name, "--", "cat", "/etc/machine-id"}, nil)
			if e != nil {
				return e
			}
			if strings.TrimSpace(string(raw)) != v.MachineID {
				return fmt.Errorf("guest identity changed")
			}
		}
		if _, err = m.run(ctx, []string{"stop", v.Name}, nil); err != nil {
			return err
		}
		current, e := m.instances(ctx)
		if e != nil {
			return e
		}
		latest, ok := current[v.Name]
		if !ok {
			return fmt.Errorf("VM disappeared during rollback; reconcile on retry")
		}
		if latest.Protected || latest.Status != "Stopped" {
			return fmt.Errorf("VM did not stop or became protected")
		}
		if err = verifyVM(v, latest); err != nil {
			return err
		}
		if _, err = m.run(ctx, []string{"delete", "--tty=false", v.Name}, nil); err != nil {
			return err
		}
	}
	registry, err := m.load()
	if err != nil {
		return err
	}
	if err = m.removeConnection(v); err != nil {
		return err
	}
	// Restore the previous active connection only if rollback removed ours.
	path := m.directory + "/control.json"
	if len(previous) > 0 {
		if _, e := os.Lstat(path); os.IsNotExist(e) {
			if err = platform.WriteFile(path, previous, 0600); err != nil {
				return err
			}
		}
	}
	if err = m.removeHelper(v); err != nil {
		return err
	}
	remaining := []ownedVM{}
	for _, entry := range registry.VMs {
		if entry.ID != v.ID {
			remaining = append(remaining, entry)
		}
	}
	registry.VMs = remaining
	return m.save(registry)
}

func newScopeID() (string, error) {
	value := make([]byte, 16)
	_, err := rand.Read(value)
	return hex.EncodeToString(value), err
}
