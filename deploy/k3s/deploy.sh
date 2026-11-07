#!/usr/bin/env sh
set -eu

command -v kubectl >/dev/null || {
  echo "kubectl is required" >&2
  exit 1
}

image="${1:-ghcr.io/nerdylucifer/modelgauge:dev}"

kubectl apply -f namespaces.yaml

if ! kubectl -n benchmark-data get secret benchmark-postgres-auth >/dev/null 2>&1; then
  echo "Missing deployment secrets. Run ./create-secrets.sh first." >&2
  exit 1
fi

if ! kubectl -n benchmark-system get secret benchmark-controller-db >/dev/null 2>&1 || \
   ! kubectl -n benchmark-system get secret modelgauge-agent-auth >/dev/null 2>&1 || \
   ! kubectl -n benchmark-system get secret modelgauge-admin-auth >/dev/null 2>&1; then
  echo "Missing controller secrets. Run ./create-secrets.sh first." >&2
  exit 1
fi

kubectl apply -f postgres.yaml
kubectl -n benchmark-data rollout status statefulset/benchmark-postgres --timeout=180s

kubectl apply -f controller.yaml
kubectl -n benchmark-system set image deployment/modelgauge-controller controller="$image"
kubectl -n benchmark-system rollout status deployment/modelgauge-controller --timeout=180s

echo "ModelGauge controller is ready."
kubectl -n benchmark-system get service modelgauge-controller
