#!/usr/bin/env sh
set -eu
kubectl apply -f namespaces.yaml
if ! kubectl -n benchmark-system get secret modelgauge-admin-auth >/dev/null 2>&1; then
 token="$(openssl rand -hex 32)"
 kubectl -n benchmark-system create secret generic modelgauge-admin-auth --from-literal=token="$token"
fi
echo 'Controller credential ready.'
