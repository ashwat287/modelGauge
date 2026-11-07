// Package control provisions the Linux control plane through Lima.
package control

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/nerdylucifer/modelgauge/deploy"
)

type Config struct {
	VM            string `json:"vm"`
	ControllerURL string `json:"controller_url"`
	AdminToken    string `json:"admin_token"`
	AgentToken    string `json:"agent_token"`
}
type Options struct {
	Name, Image, Source, K3sVersion, BenchmarkTarget string
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
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(path, data, 0600); err != nil {
		return err
	}
	return os.Chmod(path, 0600)
}
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

func shell(ctx context.Context, name, script string, input io.Reader, output io.Writer) error {
	// The caller's working directory may not exist in this guest. Pin a guest
	// directory so Lima diagnostics cannot contaminate captured command output.
	cmd := exec.CommandContext(ctx, "limactl", "shell", "--workdir", "/", name, "--", "bash", "-lc", script)
	cmd.Stdin = input
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("control VM command: %w", err)
	}
	return nil
}
func capture(ctx context.Context, name, script string) (string, error) {
	var out bytes.Buffer
	err := shell(ctx, name, script, nil, &out)
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, out.String())
	}
	return strings.TrimSpace(out.String()), nil
}

func Bootstrap(ctx context.Context, o Options) (Config, error) {
	if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`).MatchString(o.Name) {
		return Config{}, fmt.Errorf("invalid VM name")
	}
	if _, err := exec.LookPath("limactl"); err != nil {
		return Config{}, fmt.Errorf("install Lima first: limactl is required")
	}
	if o.Log == nil {
		o.Log = io.Discard
	}
	// List errors are not interpreted as a missing VM: avoid accidental provisioning.
	list := exec.CommandContext(ctx, "limactl", "list", "--format", "{{.Name}}")
	names, err := list.Output()
	if err != nil {
		return Config{}, fmt.Errorf("list Lima VMs: %w", err)
	}
	exists := false
	for _, name := range strings.Fields(string(names)) {
		if name == o.Name {
			exists = true
		}
	}
	args := []string{"start", "--tty=false"}
	if exists {
		args = append(args, o.Name)
	} else {
		args = append(args, "--name", o.Name, "--cpus", fmt.Sprint(o.CPUs), "--memory", fmt.Sprint(o.MemoryGiB), "--disk", fmt.Sprint(o.DiskGiB), "--containerd", "none", "template:ubuntu")
	}
	cmd := exec.CommandContext(ctx, "limactl", args...)
	cmd.Stdout = o.Log
	cmd.Stderr = o.Log
	if err := cmd.Run(); err != nil {
		return Config{}, fmt.Errorf("start control VM: %w", err)
	}
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
 sudo cp /etc/rancher/k3s/k3s.yaml "$HOME/.kube/config"
 sudo chown "$(id -u):$(id -g)" "$HOME/.kube/config"
 chmod 600 "$HOME/.kube/config"
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
export KUBECONFIG="$HOME/.kube/modelgauge-config"
sudo cat /etc/rancher/k3s/k3s.yaml > "$KUBECONFIG"
chmod 600 "$KUBECONFIG"
cd "$HOME/.local/share/modelgauge/deploy/k3s"
sh create-secrets.sh
`
	// Render the selected image before apply so a nonexistent default image is
	// never briefly scheduled. Preserve the existing controller Service/PVCs.
	script += `sed ` + quote("s|ghcr.io/nerdylucifer/modelgauge:dev|"+image+"|g") + ` controller.yaml > controller-selected.yaml
kubectl apply -f postgres.yaml
kubectl -n benchmark-data rollout status statefulset/benchmark-postgres --timeout=300s
kubectl apply -f controller-selected.yaml
kubectl -n benchmark-system rollout restart deployment/modelgauge-controller
kubectl -n benchmark-system rollout status deployment/modelgauge-controller --timeout=300s
cd ../monitoring
bash deploy.sh
kubectl -n monitoring rollout restart deployment/prometheus
kubectl -n monitoring rollout restart deployment/grafana
kubectl -n monitoring rollout status deployment/prometheus --timeout=300s
kubectl -n monitoring rollout status deployment/grafana --timeout=300s
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
	c := Config{VM: o.Name, ControllerURL: fmt.Sprintf("http://127.0.0.1:%d", o.Port), AdminToken: admin, AgentToken: agent}
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
func Forward(ctx context.Context, name, namespace, service string, local, remote int, health string) error {
	endpoint := fmt.Sprintf("http://127.0.0.1:%d%s", local, health)
	client := &http.Client{Timeout: time.Second}
	healthy := func() bool {
		req, _ := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == 200
	}
	unit := fmt.Sprintf("modelgauge-forward-%d", local)
	script := `set -euo pipefail
mkdir -p "$HOME/.config/systemd/user"
unit=` + quote(unit) + `
if [ ! -f "$HOME/.config/systemd/user/$unit.service" ]; then
 if [ -f /tmp/modelgauge-forward-` + fmt.Sprint(local) + `.log ] || [ ` + fmt.Sprint(local) + ` = 3000 -a -f /tmp/modelgauge-grafana-forward.log ]; then
  for pid in $(ss -ltnp 'sport = :` + fmt.Sprint(local) + `' | sed -n 's/.*pid=\([0-9][0-9]*\).*/\1/p' | sort -u); do
   if [ "$(cat /proc/$pid/comm 2>/dev/null || true)" = kubectl ]; then kill "$pid"; fi
  done
 fi
fi
kubectl_path=$(command -v kubectl)
cat > "$HOME/.config/systemd/user/$unit.service" <<EOF
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
chmod 600 "$HOME/.config/systemd/user/$unit.service"
sudo loginctl enable-linger "$(id -un)"
systemctl --user daemon-reload
systemctl --user enable --now "$unit"
systemctl --user is-active --quiet "$unit"
`
	if err := shell(ctx, name, script, nil, io.Discard); err != nil {
		return err
	}
	for attempt := 0; attempt < 30; attempt++ {
		if healthy() {
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
