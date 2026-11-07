#!/usr/bin/env bash
set -euo pipefail
[[ $(uname -s) == Linux ]] || { echo 'Run inside the Linux node'; exit 1; }
version=1.12.1
case $(uname -m) in aarch64|arm64) arch=arm64;; x86_64) arch=amd64;; *) echo 'Unsupported architecture'; exit 1;; esac
address=${1:?Usage: install-node-exporter.sh PRIVATE_LISTEN_IP}
work=$(mktemp -d)
archive="node_exporter-${version}.linux-${arch}.tar.gz"
curl -fL --connect-timeout 15 --max-time 180 --retry 3 "https://github.com/prometheus/node_exporter/releases/download/v${version}/${archive}" -o "$work/$archive"
curl -fL --connect-timeout 15 --max-time 180 --retry 3 "https://github.com/prometheus/node_exporter/releases/download/v${version}/sha256sums.txt" -o "$work/sha256sums.txt"
(cd "$work"; grep " ${archive}$" sha256sums.txt | sha256sum -c -)
tar -xzf "$work/$archive" -C "$work"
mkdir -p "$HOME/.local/bin" "$HOME/.config/systemd/user"
install -m 755 "$work/node_exporter-${version}.linux-${arch}/node_exporter" "$HOME/.local/bin/node_exporter.new"
mv "$HOME/.local/bin/node_exporter.new" "$HOME/.local/bin/node_exporter"
cat > "$HOME/.config/systemd/user/modelgauge-node-exporter.service" <<EOF
[Unit]
Description=ModelGauge Linux node metrics
[Service]
ExecStart=$HOME/.local/bin/node_exporter --web.listen-address=$address:9100
Restart=always
RestartSec=10
NoNewPrivileges=true
[Install]
WantedBy=default.target
EOF
systemctl --user daemon-reload
systemctl --user enable --now modelgauge-node-exporter
systemctl --user restart modelgauge-node-exporter
echo "Node exporter listening on $address:9100"
