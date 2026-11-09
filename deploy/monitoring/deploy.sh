#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
kubectl get namespace monitoring >/dev/null 2>&1 || kubectl create namespace monitoring
if ! kubectl -n monitoring get secret grafana-admin >/dev/null 2>&1; then
  password=$(openssl rand -hex 24)
  kubectl -n monitoring create secret generic grafana-admin --from-literal=password="$password"
fi
kubectl -n monitoring create configmap modelgauge-dashboards --from-file=dashboards --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f stack.yaml
kubectl -n monitoring rollout status deployment/prometheus --timeout="${MODELGAUGE_ROLLOUT_TIMEOUT:-600s}"
kubectl -n monitoring rollout status deployment/grafana --timeout="${MODELGAUGE_ROLLOUT_TIMEOUT:-600s}"
echo 'Ready. Use kubectl -n monitoring port-forward svc/grafana 3000:3000'
