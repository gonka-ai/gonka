#!/usr/bin/env bash
# Isolated Kubernetes wiring/lifecycle test with real routers and mock apps.
# Requires Docker, kind >=0.30, kubectl, Helm 3, Python 3 and timeout. Never uses or
# modifies the caller's active Kubernetes context. Run from any directory.
set -Eeuo pipefail

test_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_dir=$(cd -- "$test_dir/../../.." && pwd)
chart_dir="$repo_dir/deploy/kubernetes/charts/gonka-ha"
kind_bin=${KIND_BIN:-kind}
helm_bin=${HELM_BIN:-helm}
smoke_suite=${SMOKE_SUITE:-all}
case "$smoke_suite" in all|completion) ;; *) echo "unknown SMOKE_SUITE: $smoke_suite" >&2; exit 1 ;; esac
for command in docker kubectl python3 timeout "$kind_bin" "$helm_bin"; do
    command -v "$command" >/dev/null || { echo "required command missing: $command" >&2; exit 1; }
done
docker info >/dev/null

scratch=$(mktemp -d "${TMPDIR:-/tmp}/gonka-k8s-smoke.XXXXXXXX")
suffix=$(basename "$scratch" | tr '[:upper:]' '[:lower:]')
cluster_name="gonka-ha-${suffix##*.}"
kubeconfig="$scratch/kubeconfig"
namespace=gonka-smoke
context="kind-$cluster_name"
prefix=smoke-gonka-ha
fixture_image="gonka-k8s-fixture:$cluster_name"
router_image="gonka-k8s-router:$cluster_name"
public_image="gonka-k8s-public:$cluster_name"
policy_image="gonka-k8s-policy:$cluster_name"
port_forward_pid=
cluster_created=false

kube() { kubectl --kubeconfig "$kubeconfig" --context "$context" --request-timeout=15s -n "$namespace" "$@"; }
cleanup() {
    status=$?
    trap - EXIT INT TERM
    if [[ -n $port_forward_pid ]]; then
        kill "$port_forward_pid" 2>/dev/null || true
        wait "$port_forward_pid" 2>/dev/null || true
    fi
    if [[ $status -ne 0 && -f $kubeconfig ]]; then
        kube get pods -o wide >&2 || true
        kube get events --sort-by=.lastTimestamp >&2 || true
        for pod in $(kube get pods -o name 2>/dev/null); do
            kube logs "$pod" --all-containers=true --tail=60 >&2 || true
        done
    fi
    if [[ ${KEEP_KIND_CLUSTER:-0} == 1 ]]; then
        echo "Preserved isolated cluster $cluster_name; kubeconfig: $kubeconfig" >&2
    else
        if [[ $cluster_created == true ]]; then
            if ! timeout 60s "$kind_bin" delete cluster --name "$cluster_name" --kubeconfig "$kubeconfig"; then
                echo "Could not delete owned cluster $cluster_name; preserving artifacts and kubeconfig at $scratch" >&2
                if [[ $status -eq 0 ]]; then status=1; fi
                exit "$status"
            fi
        fi
        docker image rm "$fixture_image" "$router_image" "$public_image" "$policy_image" >/dev/null 2>&1 || true
        rm -rf -- "$scratch"
    fi
    exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

cat > "$scratch/kind.yaml" <<'YAML'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
  - role: worker
  - role: worker
  - role: worker
  - role: worker
YAML
# Pinned image from the kind v0.30.0 release; Kubernetes >=1.33 is required by
# the production chart's native sidecar termination contract.
node_image=${KIND_NODE_IMAGE:-kindest/node:v1.34.0@sha256:7416a61b42b1662ca6ca89f02028ac133a309a2a30ba309614e8ec94d976dc5a}
echo "Creating isolated cluster $cluster_name (artifacts: $scratch)"
cluster_created=true
timeout "${KIND_CREATE_TIMEOUT_SECONDS:-720}s" "$kind_bin" create cluster --name "$cluster_name" --kubeconfig "$kubeconfig" --config "$scratch/kind.yaml" --image "$node_image" --wait 180s

docker build -q -t "$fixture_image" "$test_dir/fixtures"
docker build -q -t "$router_image" -f "$repo_dir/versiond-router/Dockerfile" "$repo_dir"
docker build -q -t "$public_image" -f "$repo_dir/proxy-router/Dockerfile" "$repo_dir"
docker build -q -t "$policy_image" "$repo_dir/proxy"
"$kind_bin" load docker-image --name "$cluster_name" "$fixture_image" "$router_image" "$public_image" "$policy_image"
kube create namespace "$namespace"
kube create secret generic fixture-keyring --from-literal=dummy=fixture
kube create secret generic fixture-password --from-literal=password=fixture-not-a-real-secret
cat > "$scratch/external.yaml" <<YAML
apiVersion: apps/v1
kind: Deployment
metadata:
  name: fixture
spec:
  replicas: 1
  selector:
    matchLabels: {app: fixture}
  template:
    metadata:
      labels:
        app: fixture
        app.kubernetes.io/name: gonka-ha
        app.kubernetes.io/instance: smoke
        app.kubernetes.io/component: fixture
    spec:
      automountServiceAccountToken: false
      containers:
        - name: fixture
          image: $fixture_image
          imagePullPolicy: Never
          env:
            - {name: TEST_RELEASE_PREFIX, value: $prefix}
            - {name: TEST_NAMESPACE, value: $namespace}
          ports:
            - {name: http, containerPort: 9000}
          readinessProbe:
            httpGet: {path: /healthz, port: http}
---
apiVersion: v1
kind: Service
metadata:
  name: fixture
spec:
  selector: {app: fixture}
  ports:
    - {name: http, port: 9000, targetPort: http}
---
# Permit only the test client to inspect independent router replicas. Production
# policies remain unchanged; kind's default CNI does not enforce NetworkPolicy.
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: fixture-router-inspection
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: gonka-ha
      app.kubernetes.io/instance: smoke
      app.kubernetes.io/component: router
  policyTypes: [Ingress]
  ingress:
    - from:
        - podSelector:
            matchLabels: {app: fixture}
      ports:
        - {protocol: TCP, port: 8080}
YAML
kube apply -f "$scratch/external.yaml"
kube rollout status deployment/fixture --timeout=120s --request-timeout=125s

python3 - "$scratch/values.json" "$fixture_image" "$router_image" "$public_image" "$policy_image" <<'PY'
import json
import sys
path, fixture, router, public, policy = sys.argv[1:]
values = {
    'images': {'versiond': fixture, 'edgeApi': fixture, 'oracle': fixture,
               'router': router, 'proxyRouter': public, 'proxyPolicy': policy},
    'imagePullPolicy': 'Never',
    'protocols': ['v6', 'v7'],
    'external': {'oracleUrl': 'http://fixture:9000/versions', 'dapiHost': 'fixture.gonka-smoke.svc.cluster.local',
                 'dapiPort': 9000, 'chainRpcUrl': 'http://fixture:9000', 'chainGrpcUrl': 'fixture:9000',
                 'nodeManagerAddress': 'fixture:9000'},
    'identity': {'keyName': 'fixture', 'accountPubKey': 'fixture', 'keyringSecret': 'fixture-keyring',
                 'passwordSecret': 'fixture-password'},
    'postgres': {'host': 'fixture', 'credentialsSecret': 'fixture-password'},
    'ingress': {'service': {'type': 'ClusterIP'}},
}
for component in ('versiond', 'router', 'edgeApi', 'oracle', 'ingress'):
    values.setdefault(component, {})['resources'] = {'requests': {'cpu': '25m', 'memory': '64Mi'}}
with open(path, 'w') as output:
    json.dump(values, output)
PY
"$helm_bin" upgrade --install smoke "$chart_dir" --kubeconfig "$kubeconfig" --kube-context "$context" --namespace "$namespace" --values "$scratch/values.json" --wait --timeout 8m
for component in versiond router ingress; do
    # kubectl rollout status only understands RollingUpdate StatefulSets. The
    # guarded chart deliberately uses OnDelete, including the initial install.
    kube wait --for=jsonpath='{.status.readyReplicas}'=3 "statefulset/$prefix-$component" \
        --timeout=180s --request-timeout=185s
done

# Port-forward only the fixture service, which sends traffic to the public
# ingress via its ClusterIP. Forwarding the ingress Pod port directly would hit
# the loopback nginx policy listener rather than HAProxy's PodIP listener.
kube port-forward --request-timeout=0 --address 127.0.0.1 service/fixture :9000 > "$scratch/port-forward.log" 2>&1 &
port_forward_pid=$!
for ((attempt = 0; attempt < 100; attempt++)); do
    if grep -q 'Forwarding from 127.0.0.1:' "$scratch/port-forward.log"; then break; fi
    kill -0 "$port_forward_pid" 2>/dev/null || { cat "$scratch/port-forward.log" >&2; exit 1; }
    sleep 0.1
done
forward_port=$(sed -n 's/^Forwarding from 127\.0\.0\.1:\([0-9]*\) ->.*/\1/p' "$scratch/port-forward.log" | head -1)
[[ -n $forward_port ]] || { cat "$scratch/port-forward.log" >&2; exit 1; }
python3 "$test_dir/fixtures/completion_smoke.py" \
    --kubeconfig "$kubeconfig" --context "$context" --namespace "$namespace" \
    --helm "$helm_bin" --values "$scratch/values.json" --release smoke
if [[ $smoke_suite == all ]]; then
    python3 "$test_dir/fixtures/smoke.py" --kubeconfig "$kubeconfig" --context "$context" --namespace "$namespace" --base-url "http://127.0.0.1:$forward_port" --prefix "$prefix"
    python3 "$test_dir/fixtures/rollout_smoke.py" \
        --kubeconfig "$kubeconfig" --context "$context" --namespace "$namespace" \
        --base-url "http://127.0.0.1:$forward_port" --prefix "$prefix" \
        --helm "$helm_bin" --values "$scratch/values.json" --release smoke
fi
