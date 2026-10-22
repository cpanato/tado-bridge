#!/usr/bin/env bash
# Install/test the chart only in the dedicated ephemeral Kind fixture cluster.
# Uses GoReleaser's existing snapshots; never builds or publishes images.
set -euo pipefail

cluster="${KIND_CLUSTER_NAME:-tado-bridge-ci}"
namespace=tado-bridge-ci
if [ "$(kubectl config current-context)" != "kind-$cluster" ]; then
  printf 'Refusing to run outside the dedicated Kind test context.\n' >&2
  exit 1
fi
if kubectl get namespace "$namespace" >/dev/null 2>&1; then
  printf 'Fixture namespace already exists; refusing to overwrite it.\n' >&2
  exit 1
fi

fixture_dir="$(mktemp -d)"
cleanup() {
  result=$?
  if [ "$result" -ne 0 ]; then
    kubectl --namespace "$namespace" get pods -o wide || true
    kubectl --namespace "$namespace" logs deployment/tado-bridge --tail=50 || true
    kubectl --namespace "$namespace" logs deployment/tado-fake --tail=50 || true
    kubectl --namespace "$namespace" logs deployment/history-postgres --tail=50 || true
  fi
  kubectl delete namespace "$namespace" --wait=false >/dev/null 2>&1 || true
  rm -rf "$fixture_dir"
  rm -f .local/helm-ci-values.yaml
  return "$result"
}
trap cleanup EXIT

kind load docker-image ko.local:snapshot ko.local:fixture --name "$cluster"
docker run --rm --network none --user "$(id -u):$(id -g)" \
  --mount "type=bind,src=$fixture_dir,dst=/out" \
  ko.local:fixture prepare /out
kubectl create namespace "$namespace"
kubectl --namespace "$namespace" create secret generic tado-fixture-tls \
  --from-file=tls.crt="$fixture_dir/tls.crt" \
  --from-file=tls.key="$fixture_dir/tls.key" \
  --from-file=ca.crt="$fixture_dir/tls.crt"
kubectl --namespace "$namespace" create secret generic tado-bridge-secrets \
  --from-literal=api-token=synthetic-chart-token
kubectl apply -f .github/helm/fixtures.yaml
kubectl --namespace "$namespace" rollout status deployment/history-postgres --timeout=120s
kubectl --namespace "$namespace" rollout status deployment/tado-fake --timeout=120s
fixture_ip="$(kubectl --namespace "$namespace" get service tado-fake -o jsonpath='{.spec.clusterIP}')"
mkdir -p .local
cat > .local/helm-ci-values.yaml <<YAML
hostAliases:
  - ip: "$fixture_ip"
    hostnames:
      - my.tado.com
      - login.tado.com
      - hops.tado.com
YAML
ct install --config .github/ct.yaml --skip-clean-up
kubectl --namespace "$namespace" exec deployment/tado-fake -- /ko-app/tadofake check

# Cache readiness precedes the history write. Poll the dedicated fixture DB,
# rather than assuming that an HTTP-ready pod already committed its snapshot.
history_count=0
for _ in $(seq 1 30); do
  history_count="$(kubectl --namespace "$namespace" exec deployment/history-postgres -- \
    psql -U tado_bridge_test -d tado_bridge_test -Atc 'SELECT count(*) FROM tado_bridge_snapshots' 2>/dev/null || printf '0')"
  if [ "$history_count" -ge 1 ]; then
    break
  fi
  sleep 1
done
test "$history_count" -ge 1

# Recreate must release the lock and keep rotated credentials across pod replacement.
kubectl --namespace "$namespace" rollout restart deployment/tado-bridge
kubectl --namespace "$namespace" rollout status deployment/tado-bridge --timeout=120s
kubectl --namespace "$namespace" exec deployment/tado-fake -- /ko-app/tadofake check --reused-token

# The new collector must append to history without losing the previous rows.
new_history_count=0
for _ in $(seq 1 30); do
  new_history_count="$(kubectl --namespace "$namespace" exec deployment/history-postgres -- \
    psql -U tado_bridge_test -d tado_bridge_test -Atc 'SELECT count(*) FROM tado_bridge_snapshots')"
  if [ "$new_history_count" -gt "$history_count" ]; then
    break
  fi
  sleep 1
done
test "$new_history_count" -gt "$history_count"
