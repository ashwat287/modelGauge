// Package control provisions the Linux control plane through Lima.
package control

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nerdylucifer/modelgauge/deploy"
	"github.com/nerdylucifer/modelgauge/internal/platform"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type Config struct {
	VM            string `json:"vm"`
	OwnerID       string `json:"owner_id,omitempty"`
	ControllerURL string `json:"controller_url"`
	AdminToken    string `json:"admin_token"`
	AgentToken    string `json:"agent_token"`
}
type Options struct {
	ReadinessTimeout                                 time.Duration
	Name, Image, Source, K3sVersion, BenchmarkTarget string
	HelperBinary                                     string
	CPUs, MemoryGiB, DiskGiB, Port                   int
	Log                                              io.Writer
}

func ConfigFile() (string, error) {
	home, err := os.UserHomeDir()
	return filepath.Join(home, ".config", "modelgauge", "control.json"), err
}
func Load() (Config, error) {
	path, err := ConfigFile()
	if err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	err = json.Unmarshal(data, &c)
	return c, err
}
func Save(c Config) error {
	path, err := ConfigFile()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("refusing to overwrite a non-regular control connection")
	}
	return platform.WriteJSON(path, c, 0600)
}
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

func capture(ctx context.Context, name, script string) (string, error) {
	var out bytes.Buffer
	err := shell(ctx, name, script, nil, &out)
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, out.String())
	}
	return strings.TrimSpace(out.String()), nil
}

func Bootstrap(ctx context.Context, o Options) (result Config, resultErr error) {
	if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`).MatchString(o.Name) {
		return Config{}, fmt.Errorf("invalid VM name")
	}
	if _, err := exec.LookPath("limactl"); err != nil {
		return Config{}, fmt.Errorf("install Lima first: limactl is required")
	}
	if o.Log == nil {
		o.Log = io.Discard
	}
	var paused []string
	var previous Config
	queuedPaused := false
	if saved, err := Load(); err == nil && saved.VM == o.Name {
		previous = saved
		paused, queuedPaused, err = pauseExecutors(ctx, saved)
		if err != nil {
			return Config{}, fmt.Errorf("pause executors before control upgrade: %w", err)
		}
		defer func() {
			recovery, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			target := previous
			if result.ControllerURL != "" {
				target = result
			}
			for _, node := range paused {
				resultErr = errors.Join(resultErr, connectionAction(recovery, target, node, "resume"))
			}
		}()
	}

	ctx, finishOwnership, err := provisionVM(ctx, o.Name, "control", o.CPUs, o.MemoryGiB, o.DiskGiB, o.Port, o.Log, NodeOptions{Source: o.Source, AgentBinary: o.HelperBinary, queuedJobsPaused: queuedPaused})
	if err != nil {
		return Config{}, err
	}
	defer completeProvision(ctx, finishOwnership, &resultErr)
	if o.ReadinessTimeout <= 0 {
		o.ReadinessTimeout = 10 * time.Minute
	}
	readiness := fmt.Sprintf("%ds", int64(o.ReadinessTimeout.Seconds()))

	fmt.Fprintln(o.Log, "Installing k3s...")
	install := `set -euo pipefail
if ! command -v k3s >/dev/null; then
 sudo apt-get update
 sudo apt-get install -y curl ca-certificates openssl
 curl -fsSL --connect-timeout 15 --max-time 120 --retry 3 https://get.k3s.io -o /tmp/modelgauge-install-k3s.sh
 sudo env INSTALL_K3S_VERSION=` + quote(o.K3sVersion) + ` INSTALL_K3S_EXEC='server --disable traefik --disable servicelb' sh /tmp/modelgauge-install-k3s.sh
fi
sudo systemctl start k3s
mkdir -p "$HOME/.kube"
if [ ! -f "$HOME/.kube/config" ]; then
 sudo cat /etc/rancher/k3s/k3s.yaml | "$HOME/.local/share/modelgauge/guest-helper" _guest write-file .kube/config 600
fi
for attempt in $(seq 1 60); do
 if sudo k3s kubectl get nodes -o name 2>/dev/null | grep -q '^node/'; then break; fi
 sleep 2
done
sudo k3s kubectl wait --for=condition=Ready nodes --all --timeout=180s
mkdir -p "$HOME/.local/share/modelgauge/deploy"
`
	if err := shell(ctx, o.Name, install, nil, o.Log); err != nil {
		return Config{}, err
	}
	controlIP, err := capture(ctx, o.Name, `ip -4 -o addr show scope global | awk 'NR==1 {split($4,a,"/"); print a[1]}'`)
	if err != nil {
		return Config{}, err
	}
	if !regexp.MustCompile(`^[0-9.]+$`).MatchString(controlIP) {
		return Config{}, fmt.Errorf("could not discover control VM IPv4 address")
	}
	if o.BenchmarkTarget != "" && !regexp.MustCompile(`^[a-zA-Z0-9.-]+:[0-9]+$`).MatchString(o.BenchmarkTarget) {
		return Config{}, fmt.Errorf("benchmark target must be host:port")
	}
	// Always select the kubeconfig of this VM, not a pre-existing user's context.
	var assets bytes.Buffer
	tw := tar.NewWriter(&assets)
	err = fs.WalkDir(deploy.Assets, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := deploy.Assets.ReadFile(path)
		if err != nil {
			return err
		}
		if path == "monitoring/stack.yaml" {
			text := strings.ReplaceAll(string(data), "192.168.105.3:9100", controlIP+":9100")
			if o.BenchmarkTarget != "" {
				text = strings.ReplaceAll(text, "192.168.105.1:9100", o.BenchmarkTarget)
			} else {
				text = strings.ReplaceAll(text, "          - targets: [192.168.105.1:9100]\n            labels: {node: benchmark-node-temp}\n", "")
			}
			data = []byte(text)
		}
		if err = tw.WriteHeader(&tar.Header{Name: path, Mode: 0600, Size: int64(len(data))}); err != nil {
			return err
		}
		_, err = tw.Write(data)
		return err
	})
	if err != nil {
		return Config{}, err
	}
	if err = tw.Close(); err != nil {
		return Config{}, err
	}
	if err = shell(ctx, o.Name, `tar -xf - -C "$HOME/.local/share/modelgauge/deploy"`, &assets, o.Log); err != nil {
		return Config{}, err
	}
	if err = shell(ctx, o.Name, `bash "$HOME/.local/share/modelgauge/deploy/monitoring/install-node-exporter.sh" `+quote(controlIP), nil, o.Log); err != nil {
		return Config{}, err
	}
	image := o.Image
	if o.Source != "" {
		image, err = buildImage(ctx, o.Name, o.Source, o.Log)
		if err != nil {
			return Config{}, err
		}
	}
	if image == "" {
		return Config{}, fmt.Errorf("provide --image with a published controller image, or --source with the Go repository")
	}

	script := `set -euo pipefail
export MODELGAUGE_ROLLOUT_TIMEOUT=` + quote(readiness) + `
on_failure() {
 status=$?
 trap - ERR
 set +e
 kubectl --request-timeout=10s --kubeconfig "$HOME/.kube/modelgauge-config" get pods -A >&2
 kubectl --request-timeout=10s --kubeconfig "$HOME/.kube/modelgauge-config" get events -A --sort-by=.metadata.creationTimestamp | tail -n 30 >&2
 exit "$status"
}
trap on_failure ERR
export KUBECONFIG="$HOME/.kube/modelgauge-config"
sudo cat /etc/rancher/k3s/k3s.yaml | "$HOME/.local/share/modelgauge/guest-helper" _guest write-file .kube/modelgauge-config 600
cd "$HOME/.local/share/modelgauge/deploy/k3s"
sh create-secrets.sh
`
	// Render the selected image before apply so a nonexistent default image is
	// never briefly scheduled. Preserve the existing controller Service/PVCs.
	script += `sed ` + quote("s|ghcr.io/nerdylucifer/modelgauge:dev|"+image+"|g") + ` controller.yaml > controller-selected.yaml
kubectl apply -f postgres.yaml
kubectl -n benchmark-data rollout status statefulset/benchmark-postgres --timeout="${MODELGAUGE_ROLLOUT_TIMEOUT:-600s}"
kubectl apply -f controller-selected.yaml
kubectl -n benchmark-system rollout restart deployment/modelgauge-controller
kubectl -n benchmark-system rollout status deployment/modelgauge-controller --timeout="${MODELGAUGE_ROLLOUT_TIMEOUT:-600s}"
cd ../monitoring
bash deploy.sh
kubectl -n monitoring rollout restart deployment/prometheus
kubectl -n monitoring rollout restart deployment/grafana
kubectl -n monitoring rollout status deployment/prometheus --timeout="${MODELGAUGE_ROLLOUT_TIMEOUT:-600s}"
kubectl -n monitoring rollout status deployment/grafana --timeout="${MODELGAUGE_ROLLOUT_TIMEOUT:-600s}"
`
	if strings.ContainsAny(image, "|\n\r&") {
		return Config{}, fmt.Errorf("invalid image reference")
	}
	if err = shell(ctx, o.Name, script, nil, o.Log); err != nil {
		return Config{}, err
	}
	tokenScript := `export KUBECONFIG="$HOME/.kube/modelgauge-config"; kubectl -n benchmark-system get secret modelgauge-admin-auth -o jsonpath='{.data.token}' | base64 --decode`
	admin, err := capture(ctx, o.Name, tokenScript)
	if err != nil {
		return Config{}, err
	}
	agent, err := capture(ctx, o.Name, strings.ReplaceAll(tokenScript, "modelgauge-admin-auth", "modelgauge-agent-auth"))
	if err != nil {
		return Config{}, err
	}
	ownerID, err := ownershipID(ctx, o.Name)
	if err != nil {
		return Config{}, err
	}
	c := Config{VM: o.Name, OwnerID: ownerID, ControllerURL: fmt.Sprintf("http://127.0.0.1:%d", o.Port), AdminToken: admin, AgentToken: agent}

	if err = Forward(ctx, c.VM, "benchmark-system", "modelgauge-controller", o.Port, 8080, "/healthz"); err != nil {
		return Config{}, err
	}

	if err = Save(c); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Forward holds a guest localhost port-forward independently of this CLI.
// Lima's host agent forwards that guest port to the host loopback interface.
func Forward(ctx context.Context, name, namespace, service string, local, remote int, health string) (resultErr error) {
	finishOwnership, err := reserveForward(ctx, name, local)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, finishOwnership())
	}()
	previous, err := capture(ctx, name, `if [ -f "$HOME/.config/systemd/user/modelgauge-forward-`+fmt.Sprint(local)+`.service" ]; then cat "$HOME/.config/systemd/user/modelgauge-forward-`+fmt.Sprint(local)+`.service"; fi`)
	if err != nil {
		return err
	}
	previousActive, err := capture(ctx, name, `if systemctl --user is-active --quiet modelgauge-forward-`+fmt.Sprint(local)+`; then printf active; else printf inactive; fi`)
	if err != nil {
		return err
	}
	previousEnabled, err := capture(ctx, name, `if systemctl --user is-enabled --quiet modelgauge-forward-`+fmt.Sprint(local)+`; then printf enabled; else printf disabled; fi`)
	if err != nil {
		return err
	}
	defer func() {
		if resultErr == nil {
			return
		}
		recovery, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		unit := "modelgauge-forward-" + fmt.Sprint(local)
		restore := `set -euo pipefail
unit=` + quote(unit) + `
state=$(systemctl --user show "$unit" -p LoadState --value 2>/dev/null || true)
if [ "$state" = loaded ]; then systemctl --user stop "$unit"; fi
`
		if previous != "" {
			restore += `printf '%s\n' ` + quote(previous) + ` | "$HOME/.local/share/modelgauge/guest-helper" _guest write-file ".config/systemd/user/$unit.service" 600
systemctl --user daemon-reload
`
			if previousEnabled == "enabled" {
				restore += `systemctl --user enable "$unit"` + "\n"
			} else {
				restore += `systemctl --user disable "$unit"` + "\n"
			}
			if previousActive == "active" {
				restore += `systemctl --user start "$unit"` + "\n"
			}
		} else {
			restore += `if [ -f "$HOME/.config/systemd/user/$unit.service" ]; then systemctl --user disable "$unit"; "$HOME/.local/share/modelgauge/guest-helper" _guest remove-file ".config/systemd/user/$unit.service"; fi
systemctl --user daemon-reload
`
		}
		resultErr = errors.Join(resultErr, rawShell(recovery, name, restore, nil, io.Discard))
	}()
	unit := fmt.Sprintf("modelgauge-forward-%d", local)
	if previous != "" {
		if err := shell(ctx, name, "systemctl --user stop "+quote(unit), nil, io.Discard); err != nil {
			return err
		}
	}
	if err := requireForwardPortFree(ctx, local, previousActive == "active"); err != nil {
		return err
	}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d%s", local, health)
	script := `set -euo pipefail
mkdir -p "$HOME/.config/systemd/user"
unit=` + quote(unit) + `
kubectl_path=$(command -v kubectl)
"$HOME/.local/share/modelgauge/guest-helper" _guest write-file ".config/systemd/user/$unit.service" 600 <<EOF
[Unit]
Description=ModelGauge local service forward
[Service]
Environment=KUBECONFIG=$HOME/.kube/modelgauge-config
ExecStart=$kubectl_path -n ` + namespace + ` port-forward svc/` + service + ` ` + fmt.Sprintf("%d:%d", local, remote) + `
Restart=always
RestartSec=2
[Install]
WantedBy=default.target
EOF
sudo loginctl enable-linger "$(id -un)"
systemctl --user daemon-reload
systemctl --user enable --now "$unit"
systemctl --user is-active --quiet "$unit"
`
	if err := shell(ctx, name, script, nil, io.Discard); err != nil {
		return err
	}
	for attempt := 0; attempt < 30; attempt++ {
		if forwardHealthy(ctx, endpoint, service) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("local service tunnel did not become ready at %s", endpoint)
}

func Status(ctx context.Context, c Config, out io.Writer) error {
	return shell(ctx, c.VM, `export KUBECONFIG="$HOME/.kube/modelgauge-config"; kubectl get nodes; kubectl -n benchmark-system get deploy; kubectl -n benchmark-data get statefulset,pvc; kubectl -n monitoring get deploy,pvc`, nil, out)
}

// A systemd scope supervises guest descendants even if the local SSH client dies.
func shell(ctx context.Context, name, script string, input io.Reader, output io.Writer) (err error) {
	session, _ := ctx.Value(bootstrapKey{}).(*bootstrapSession)
	if session == nil {
		return rawShell(ctx, name, script, input, output)
	}
	id, e := newScopeID()
	if e != nil {
		return e
	}
	scope := "modelgauge-provision-" + id + ".scope"
	if err = session.trackScope(scope, true); err != nil {
		return err
	}
	wrapped := scopeCommand(scope, script)
	err = rawShell(ctx, name, wrapped, input, output)
	recovery, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	stopErr := session.manager.stopScope(recovery, name, scope)
	cancel()
	if stopErr != nil {
		return errors.Join(err, fmt.Errorf("could not confirm guest provisioning stopped: %w", stopErr))
	}
	return errors.Join(err, session.trackScope(scope, false))
}
func rawShell(ctx context.Context, name, script string, input io.Reader, output io.Writer) error {
	cmd := platform.CommandContext(ctx, "limactl", "shell", "--workdir", "/", name, "--", "bash", "-lc", script)
	cmd.Stdin = input
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("control VM command: %w", errors.Join(err, ctx.Err()))
	}
	return nil
}
func (m *ownershipManager) stopScope(ctx context.Context, name, scope string) error {
	if !validScope.MatchString(scope) {
		return fmt.Errorf("invalid provisioning scope")
	}
	script := `set -eu
unit=` + quote(scope) + `
state=$(systemctl --user show "$unit" -p LoadState --value 2>/dev/null || true)
case "$state" in
 not-found) ;;
 loaded) systemctl --user stop "$unit" ;;
 *) echo "Cannot verify provisioning scope state" >&2; exit 1 ;;
esac
`
	_, err := m.run(ctx, []string{"shell", "--workdir", "/", name, "--", "sh", "-c", script}, nil)
	return err
}
func (s *bootstrapSession) trackScope(scope string, add bool) error {
	registry, err := s.manager.load()
	if err != nil {
		return err
	}
	for i := range registry.VMs {
		if registry.VMs[i].ID == s.vm.ID {
			var scopes []string
			for _, value := range registry.VMs[i].Scopes {
				if value != scope {
					scopes = append(scopes, value)
				}
			}
			if add {
				scopes = append(scopes, scope)
			}
			registry.VMs[i].Scopes = scopes
			return s.manager.save(registry)
		}
	}
	return fmt.Errorf("bootstrap ownership disappeared")
}

func scopeCommand(scope, script string) string {
	return "exec systemd-run --user --scope --quiet --collect --unit=" + quote(scope) + " bash -lc " + quote(script)
}

// Stop our old forward first: an occupied host port must not impersonate this VM.
func requireForwardPortFree(ctx context.Context, port int, wait bool) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("invalid forward port")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for {
		if err := probeCtx.Err(); err != nil {
			return err
		}
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			return listener.Close()
		}
		if !wait {
			return fmt.Errorf("host port %d is already in use; choose another port: %w", port, err)
		}
		select {
		case <-probeCtx.Done():
			return fmt.Errorf("host port %d did not become free after stopping the owned forward: %w", port, probeCtx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func forwardHealthy(ctx context.Context, endpoint, service string) bool {
	client := &http.Client{Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var response struct {
		Status   string `json:"status"`
		Database string `json:"database"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&response) != nil {
		return false
	}
	if service == "modelgauge-controller" {
		return response.Status == "ok" && response.Database == "reachable"
	}
	return response.Database == "ok"
}
