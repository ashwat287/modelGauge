#!/usr/bin/env sh
set -eu
cd "$(dirname "$0")"
sh create-secrets.sh
kubectl apply -f controller.yaml
kubectl -n benchmark-system rollout status deployment/modelgauge-controller --timeout=180s
