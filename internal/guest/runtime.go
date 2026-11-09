// Package guest implements the Linux-side ownership protocol in the same Go
// executable as the CLI and agent. OS command boundaries are injectable in tests.
package guest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nerdylucifer/modelgauge/internal/evidence"
	"github.com/nerdylucifer/modelgauge/internal/platform"
	"github.com/nerdylucifer/modelgauge/pkg/protocol"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

const HelperRelative = ".local/share/modelgauge/guest-helper"

var roots = []string{".local/share/modelgauge", ".config/modelgauge"}
var ownerPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var forwardPattern = regexp.MustCompile(`^\.config/systemd/user/modelgauge-forward-[0-9]{4,5}\.service$`)

type Request struct {
	CheckIdle  bool     `json:"check_idle,omitempty"`
	ID         string   `json:"id"`
	Role       string   `json:"role"`
	Operation  string   `json:"operation"`
	Files      []string `json:"files"`
	MachineID  string   `json:"machine_id"`
	Cleaned    bool     `json:"cleaned"`
	Prepared   bool     `json:"prepared"`
	Unprepared bool     `json:"unprepared"`
}
type State struct {
	PendingHelperSHA256 string                 `json:"pending_helper_sha256,omitempty"`
	Pending             map[string]PendingFile `json:"pending_files,omitempty"`
	Schema              int                    `json:"schema"`
	ID                  string                 `json:"id"`
	MachineID           string                 `json:"machine_id"`
	Role                string                 `json:"role"`
	Directories         []string               `json:"directories"`
	Files               map[string]*string     `json:"files"`
	HelperSHA256        string                 `json:"helper_sha256,omitempty"`
	Cleaned             bool                   `json:"cleaned,omitempty"`
}
type Runtime struct {
	Home, MachineID string
	Run             func(context.Context, []string, []string) ([]byte, error)
	HasCommand      func(string) bool
	MountPoints     func() ([]string, error)
	Executable      func() (string, error)
}

func NewRuntime() (Runtime, error) {
	if runtime.GOOS != "linux" {
		return Runtime{}, fmt.Errorf("guest helpers require Linux")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Runtime{}, err
	}
	machine, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		return Runtime{}, err
	}
	return Runtime{Home: home, MachineID: strings.TrimSpace(string(machine)), Run: runCommand, HasCommand: func(name string) bool { _, err := exec.LookPath(name); return err == nil }, MountPoints: linuxMounts, Executable: os.Executable}, nil
}
func runCommand(ctx context.Context, args, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Env = append(os.Environ(), env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return output, nil
}
func allowedFile(name string) bool {
	switch name {
	case ".local/bin/modelgauge", ".local/bin/node_exporter", ".kube/modelgauge-config", ".kube/config", ".config/systemd/user/modelgauge-agent.service", ".config/systemd/user/modelgauge-node-exporter.service":
		return true
	}
	return forwardPattern.MatchString(name)
}
func (r Runtime) safePath(relative string) (string, error) {
	if !filepath.IsAbs(r.Home) || filepath.IsAbs(relative) || filepath.Clean(relative) != relative || relative == "." || relative == ".." || strings.HasPrefix(relative, "../") {
		return "", fmt.Errorf("unsafe guest path")
	}
	home, err := os.Lstat(r.Home)
	if err != nil {
		return "", err
	}
	if !home.IsDir() || home.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("unsafe guest home")
	}
	current := r.Home
	for _, part := range strings.Split(relative, "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("refusing symlink path: %s", relative)
		}
	}
	return current, nil
}
func exists(path string) bool { _, err := os.Lstat(path); return err == nil }
func digest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
func (r Runtime) verify(state *State, owner string, fingerprints bool) error {
	if len(state.Directories) != len(roots) {
		return fmt.Errorf("unexpected directory manifest")
	}
	for i, relative := range state.Directories {
		if relative != roots[i] {
			return fmt.Errorf("unexpected directory manifest")
		}
		path, err := r.safePath(relative)
		if err != nil {
			return err
		}
		if exists(path) {
			if r.MountPoints == nil {
				return fmt.Errorf("mount inspection unavailable")
			}
			mounts, err := r.MountPoints()
			if err != nil {
				return err
			}
			for _, mount := range mounts {
				if mount == path || strings.HasPrefix(mount, path+string(filepath.Separator)) {
					return fmt.Errorf("foreign mount in managed directory")
				}
			}
			marker, err := r.safePath(relative + "/.modelgauge-owner")
			if err != nil {
				return err
			}
			info, err := os.Lstat(marker)
			if err != nil || !info.Mode().IsRegular() {
				return fmt.Errorf("directory marker is not a regular file")
			}
			data, err := os.ReadFile(marker)
			if err != nil || strings.TrimSpace(string(data)) != owner {
				return fmt.Errorf("directory ownership mismatch: %s", relative)
			}
		}
	}
	if state.HelperSHA256 != "" {
		path, err := r.safePath(HelperRelative)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("installed guest helper is missing or not regular")
		}
		actual, err := digest(path)
		if err != nil {
			return err
		}
		if actual != state.HelperSHA256 && actual != state.PendingHelperSHA256 {
			return fmt.Errorf("installed guest helper changed")
		}
	}
	for name, pending := range state.Pending {
		if _, ok := state.Files[name]; !ok || !allowedFile(name) || len(pending.SHA256) != 64 || (pending.Mode != 0600 && pending.Mode != 0755) {
			return fmt.Errorf("invalid pending file transaction")
		}
	}
	for name, expected := range state.Files {
		if !allowedFile(name) {
			return fmt.Errorf("unexpected file manifest")
		}
		path, err := r.safePath(name)
		if err != nil {
			return err
		}
		if exists(path) {
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("managed file is not regular")
			}
			if fingerprints {
				actual, err := digest(path)
				if err != nil {
					return err
				}
				if pending, ok := state.Pending[name]; ok && pending.SHA256 == actual {
					continue
				}
				if expected == nil || *expected != actual {
					return fmt.Errorf("managed file changed or is unsealed: %s", name)
				}
			}
		}
	}
	return nil
}
func (r Runtime) load(request Request) (*State, error) {
	path, err := r.safePath(".local/share/modelgauge/.ownership.json")
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("guest manifest is not a regular file")
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state State
	if err = json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	if state.Schema != 1 || state.ID != request.ID || state.Role != request.Role || state.MachineID != r.MachineID {
		return nil, fmt.Errorf("guest ownership marker mismatch")
	}
	if state.Files == nil {
		return nil, fmt.Errorf("invalid file manifest")
	}
	return &state, nil
}
func (r Runtime) save(state *State) error {
	path, err := r.safePath(".local/share/modelgauge/.ownership.json")
	if err != nil {
		return err
	}
	return platform.WriteJSON(path, state, 0600)
}
func (r Runtime) installHelper(state *State) error {
	if r.Executable == nil {
		return fmt.Errorf("guest executable unavailable")
	}
	source, err := r.Executable()
	if err != nil {
		return err
	}
	expected, err := digest(source)
	if err != nil {
		return err
	}
	destination, err := r.safePath(HelperRelative)
	if err != nil {
		return err
	}
	if exists(destination) {
		actual, err := digest(destination)
		if err != nil {
			return err
		}
		if state.HelperSHA256 != "" && actual != state.HelperSHA256 && actual != state.PendingHelperSHA256 {
			return fmt.Errorf("installed guest helper changed")
		}
		if state.HelperSHA256 == "" && actual != expected {
			return fmt.Errorf("untracked guest helper exists")
		}
		if actual == expected {
			state.HelperSHA256 = expected
			state.PendingHelperSHA256 = ""
			return nil
		}
	}
	file, err := os.CreateTemp(filepath.Dir(destination), ".guest-helper-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	input, err := os.Open(source)
	if err != nil {
		file.Close()
		return err
	}
	if err = file.Chmod(0700); err == nil {
		_, err = io.Copy(file, input)
	}
	input.Close()
	if err == nil {
		err = file.Sync()
	}
	closed := file.Close()
	if err != nil {
		return err
	}
	if closed != nil {
		return closed
	}
	state.PendingHelperSHA256 = expected
	if err = r.save(state); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), destination); err != nil {
		return err
	}
	state.HelperSHA256 = expected
	state.PendingHelperSHA256 = ""
	return nil
}
func (r Runtime) active(ctx context.Context) error {
	if r.Run == nil {
		return fmt.Errorf("guest command runner unavailable")
	}
	_, err := r.Run(ctx, []string{"pgrep", "-x", "llama-bench"}, nil)
	if err == nil {
		return fmt.Errorf("a benchmark is active; refusing cleanup")
	}
	var status interface{ ExitCode() int }
	if !errors.As(err, &status) || status.ExitCode() != 1 {
		return fmt.Errorf("cannot verify benchmark activity: %w", err)
	}
	return nil
}
func (r Runtime) checkJobs(ctx context.Context) error {
	path, err := r.safePath(".kube/modelgauge-config")
	if err != nil {
		return err
	}
	if !exists(path) {
		return nil
	}
	if r.Run == nil {
		return fmt.Errorf("guest command runner unavailable")
	}
	env := []string{"KUBECONFIG=" + path}
	probe, err := r.Run(ctx, []string{"kubectl", "--request-timeout=10s", "-n", "benchmark-data", "get", "pod", "benchmark-postgres-0", "--ignore-not-found", "-o", "name"}, env)
	if err != nil {
		return fmt.Errorf("cannot verify control job state: %w", err)
	}
	if strings.TrimSpace(string(probe)) == "" {
		return nil
	}
	sql := "SELECT count(*) FROM benchmark_plans WHERE status IN ('queued','leased','running'); SELECT count(*) FROM model_install_jobs WHERE status IN ('queued','leased','running');"
	counts, err := r.Run(ctx, []string{"kubectl", "--request-timeout=10s", "-n", "benchmark-data", "exec", "benchmark-postgres-0", "--", "psql", "-U", "benchmark_app", "-d", "benchmark", "-Atc", sql}, env)
	if err != nil {
		return fmt.Errorf("cannot verify job queues: %w", err)
	}
	fields := strings.Fields(string(counts))
	if len(fields) != 2 {
		return fmt.Errorf("invalid job state response")
	}
	for _, value := range fields {
		count, err := strconv.ParseInt(value, 10, 64)
		if err != nil || count != 0 {
			return fmt.Errorf("active or queued jobs exist, or their state could not be verified")
		}
	}
	return nil
}
func (r Runtime) lock(relative string, create bool) (func(), error) {
	path, err := r.safePath(relative)
	if err != nil {
		return nil, err
	}
	if !exists(path) && !create {
		return func() {}, nil
	}
	flags := os.O_RDONLY
	if create {
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
		flags = os.O_RDWR | os.O_CREATE
	}
	file, err := os.OpenFile(path, flags, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("another agent, download or benchmark owns %s", relative)
	}
	return func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN); _ = file.Close() }, nil
}
func (r Runtime) Execute(ctx context.Context, request Request) (*State, error) {
	if !ownerPattern.MatchString(request.ID) || !ownerPattern.MatchString(r.MachineID) || (request.Role != "control" && request.Role != "benchmark") {
		return nil, fmt.Errorf("invalid guest identity or role")
	}
	if request.MachineID != "" && request.MachineID != r.MachineID {
		return nil, fmt.Errorf("guest machine identity changed")
	}
	for _, name := range request.Files {
		if !allowedFile(name) {
			return nil, fmt.Errorf("unexpected managed file: %s", name)
		}
	}
	for _, relative := range roots {
		if _, err := r.safePath(relative); err != nil {
			return nil, err
		}
	}
	state, err := r.load(request)
	if err != nil {
		return nil, err
	}
	if state != nil {
		if err = r.verify(state, request.ID, false); err != nil {
			return nil, err
		}
		create := request.Operation == "prepare" || request.Operation == "seal"
		release, e := r.lock(".local/share/modelgauge/locks/ownership.lock", create)
		if e != nil {
			return nil, e
		}
		defer release()
		state, err = r.load(request)
		if err != nil {
			return nil, err
		}
	}
	switch request.Operation {
	case "prepare":
		if state != nil {
			if err = r.verify(state, request.ID, true); err != nil {
				return nil, err
			}
			if err = r.reconcile(state); err != nil {
				return nil, err
			}
			if request.CheckIdle && request.Role == "control" {
				if err = r.checkJobs(ctx); err != nil {
					return nil, err
				}
			} else if request.CheckIdle {
				if err = r.active(ctx); err != nil {
					return nil, err
				}
				pending, e := r.safePath(".local/share/modelgauge/results/.pending.json")
				if e != nil {
					return nil, e
				}
				if exists(pending) {
					return nil, fmt.Errorf("pending agent work blocks upgrade")
				}
				release, e := r.lock(".local/share/modelgauge/locks/execution.lock", false)
				if e != nil {
					return nil, e
				}
				release()
			}
		} else {
			for _, relative := range roots {
				p, _ := r.safePath(relative)
				if exists(p) {
					return nil, fmt.Errorf("untracked directory exists; refusing adoption: %s", relative)
				}
			}
			if request.Role == "control" && r.HasCommand != nil && r.HasCommand("k3s") {
				return nil, fmt.Errorf("existing k3s is untracked; use a dedicated control VM")
			}
			state = &State{Schema: 1, ID: request.ID, MachineID: r.MachineID, Role: request.Role, Directories: append([]string(nil), roots...), Files: map[string]*string{}}
		}
		files := []string{}
		for _, name := range request.Files {
			p, err := r.safePath(name)
			if err != nil {
				return nil, err
			}
			_, tracked := state.Files[name]
			if name == ".kube/config" && !tracked && exists(p) {
				continue
			}
			if !tracked && exists(p) {
				return nil, fmt.Errorf("untracked file exists; refusing overwrite: %s", name)
			}
			files = append(files, name)
		}
		for _, relative := range roots {
			p, _ := r.safePath(relative)
			if !exists(p) {
				if err = os.MkdirAll(p, 0700); err != nil {
					return nil, err
				}
				file, err := os.OpenFile(filepath.Join(p, ".modelgauge-owner"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
				if err != nil {
					return nil, err
				}
				_, err = file.WriteString(request.ID)
				file.Close()
				if err != nil {
					return nil, err
				}
			}
		}
		for _, name := range files {
			if _, tracked := state.Files[name]; !tracked {
				state.Files[name] = nil
			}
		}
		if err = r.save(state); err != nil {
			return nil, err
		}
		if err = r.installHelper(state); err != nil {
			return nil, err
		}
		if err = r.save(state); err != nil {
			return nil, err
		}
	case "identity", "seal":
		if state == nil {
			return nil, fmt.Errorf("guest ownership manifest is missing")
		}
		if err = r.verify(state, request.ID, false); err != nil {
			return nil, err
		}
		if request.Operation == "seal" {
			if err = r.verify(state, request.ID, true); err != nil {
				return nil, err
			}
			if err = r.reconcile(state); err != nil {
				return nil, err
			}
		}

	case "inspect", "quiesce", "clean":
		if err = r.active(ctx); err != nil {
			return nil, err
		}
		if state == nil {
			candidates := append([]string(nil), roots...)
			if !request.Cleaned {
				candidates = append(candidates, request.Files...)
			}
			absent := true
			for _, relative := range candidates {
				p, e := r.safePath(relative)
				if e != nil {
					return nil, e
				}
				if exists(p) {
					absent = false
				}
			}
			if (request.Cleaned || request.Prepared || request.Unprepared) && absent {
				return &State{MachineID: r.MachineID, Cleaned: true, Directories: []string{}, Files: map[string]*string{}}, nil
			}
			return nil, fmt.Errorf("guest ownership manifest is missing")
		}
		if err = r.verify(state, request.ID, true); err != nil {
			return nil, err
		}
		pending, err := r.safePath(".local/share/modelgauge/results/.pending.json")
		if err != nil {
			return nil, err
		}
		if exists(pending) {
			return nil, fmt.Errorf("pending experiment/download/upload exists")
		}
		if request.Role == "control" {
			if err = r.checkJobs(ctx); err != nil {
				return nil, err
			}
		}
		release, err := r.lock(".local/share/modelgauge/locks/execution.lock", false)
		if err != nil {
			return nil, err
		}
		defer release()
		if request.Operation != "inspect" {
			names := []string{}
			for name := range state.Files {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				p, _ := r.safePath(name)
				if strings.HasPrefix(name, ".config/systemd/user/") && exists(p) {
					if _, err = r.Run(ctx, []string{"systemctl", "--user", "disable", "--now", filepath.Base(name)}, nil); err != nil {
						return nil, err
					}
				}
			}
			agentRelease, err := r.lock(".local/share/modelgauge/locks/agent.lock", true)
			if err != nil {
				return nil, err
			}
			defer agentRelease()
			if exists(pending) {
				return nil, fmt.Errorf("work appeared during cleanup")
			}
		}
		if request.Operation == "clean" {
			for name, expected := range state.Files {
				p, err := r.safePath(name)
				if err != nil {
					return nil, err
				}
				if exists(p) {
					actual, e := digest(p)
					if e != nil {
						return nil, e
					}
					if pending, ok := state.Pending[name]; ok && pending.SHA256 == actual {
						expected = &pending.SHA256
					}
					if expected == nil || actual != *expected {
						return nil, fmt.Errorf("file changed during cleanup")
					}
					if err = os.Remove(p); err != nil {
						return nil, err
					}
				}
			}
			if _, err = r.Run(ctx, []string{"systemctl", "--user", "daemon-reload"}, nil); err != nil {
				return nil, err
			}
			for i := len(roots) - 1; i >= 0; i-- {
				p, err := r.safePath(roots[i])
				if err != nil {
					return nil, err
				}
				if exists(p) {
					marker, err := r.safePath(roots[i] + "/.modelgauge-owner")
					if err != nil {
						return nil, err
					}
					data, err := os.ReadFile(marker)
					if err != nil || strings.TrimSpace(string(data)) != request.ID {
						return nil, fmt.Errorf("directory marker changed during cleanup")
					}
					if err = os.RemoveAll(p); err != nil {
						return nil, err
					}
				}
			}
			state.Cleaned = true
		}
	default:
		return nil, fmt.Errorf("unknown guest ownership operation")
	}
	return state, nil
}
func (r Runtime) WriteEvidence(ctx context.Context, refs []protocol.EvidenceRef, out io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := r.safePath(".local/share/modelgauge/results")
	if err != nil {
		return err
	}
	return evidence.WriteArchive(ctx, root, refs, out)
}
func linuxMounts() ([]string, error) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	mounts := []string{}
	escaped := regexp.MustCompile(`\\([0-7]{3})`)
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 5 {
			return nil, fmt.Errorf("invalid mount information")
		}
		mounts = append(mounts, escaped.ReplaceAllStringFunc(parts[4], func(s string) string { n, _ := strconv.ParseInt(s[1:], 8, 16); return string(rune(n)) }))
	}
	return mounts, nil
}

type PendingFile struct {
	SHA256 string `json:"sha256"`
	Mode   uint32 `json:"mode"`
}

func (r Runtime) ownedState() (*State, error) {
	path, err := r.safePath(".local/share/modelgauge/.ownership.json")
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("manifest is not regular")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var state State
	if err = json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return r.load(Request{ID: state.ID, Role: state.Role})
}

// WriteManaged durably records intended bytes before replacing a reserved file.
// A crash between rename and final checkpoint can be reconciled by checksum.
func (r Runtime) WriteManaged(ctx context.Context, relative string, mode os.FileMode, input io.Reader) error {
	if mode != 0600 && mode != 0755 {
		return fmt.Errorf("invalid managed file permissions")
	}
	if !allowedFile(relative) {
		return fmt.Errorf("unexpected managed file")
	}
	state, err := r.ownedState()
	if err != nil {
		return err
	}
	if err = r.verify(state, state.ID, false); err != nil {
		return err
	}
	release, err := r.lock(".local/share/modelgauge/locks/ownership.lock", true)
	if err != nil {
		return err
	}
	defer release()
	state, err = r.ownedState()
	if err != nil {
		return err
	}
	if _, ok := state.Files[relative]; !ok {
		return fmt.Errorf("file was not reserved by this installation")
	}
	if err = r.verify(state, state.ID, true); err != nil {
		return err
	}
	destination, err := r.safePath(relative)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	stageDirectory, err := r.safePath(".local/share/modelgauge/staging")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(stageDirectory, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(stageDirectory, ".modelgauge-install-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(mode); err == nil {
		_, err = io.Copy(file, contextReader{ctx, input})
	}
	if err == nil {
		err = file.Sync()
	}
	closed := file.Close()
	if err != nil {
		return err
	}
	if closed != nil {
		return closed
	}
	sum, err := digest(file.Name())
	if err != nil {
		return err
	}
	if state.Pending == nil {
		state.Pending = map[string]PendingFile{}
	}
	state.Pending[relative] = PendingFile{SHA256: sum, Mode: uint32(mode)}
	if err = r.save(state); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), destination); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(destination))
	if err != nil {
		return err
	}
	err = dir.Sync()
	dir.Close()
	if err != nil {
		return err
	}
	return r.reconcile(state)
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
func (r Runtime) reconcile(state *State) error {
	for name, pending := range state.Pending {
		path, err := r.safePath(name)
		if err != nil {
			return err
		}
		if exists(path) {
			actual, err := digest(path)
			if err != nil {
				return err
			}
			if actual == pending.SHA256 {
				sum := actual
				state.Files[name] = &sum
				delete(state.Pending, name)
			}
		}
	}
	return r.save(state)
}

// RemoveManaged removes a single verified reserved file, retaining reservation.
func (r Runtime) RemoveManaged(ctx context.Context, relative string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	state, err := r.ownedState()
	if err != nil {
		return err
	}
	if err = r.verify(state, state.ID, false); err != nil {
		return err
	}
	release, err := r.lock(".local/share/modelgauge/locks/ownership.lock", true)
	if err != nil {
		return err
	}
	defer release()
	state, err = r.ownedState()
	if err != nil {
		return err
	}
	if _, ok := state.Files[relative]; !ok || !allowedFile(relative) {
		return fmt.Errorf("file is not reserved")
	}
	if err = r.verify(state, state.ID, true); err != nil {
		return err
	}
	path, err := r.safePath(relative)
	if err != nil {
		return err
	}
	if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	state.Files[relative] = nil
	delete(state.Pending, relative)
	return r.save(state)
}
