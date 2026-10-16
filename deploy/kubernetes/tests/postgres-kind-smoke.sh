#!/usr/bin/env bash
# Real CloudNativePG/PostgreSQL primary-loss smoke in an isolated kind cluster.
# Requires Docker, kind >=0.30, kubectl, Helm 3 and Python 3. This does not test
# inference/SSE, CSI snapshots, physical host loss, or off-site disaster recovery.
# No caller kubeconfig or current context is read or modified.
set -Eeuo pipefail

test_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_dir=$(cd -- "$test_dir/../../.." && pwd)
chart_dir="$repo_dir/deploy/kubernetes/charts/gonka-postgres"
kind_bin=${KIND_BIN:-kind}
helm_bin=${HELM_BIN:-helm}
kubectl_bin=${KUBECTL_BIN:-kubectl}
for command in docker python3 timeout "$kind_bin" "$helm_bin" "$kubectl_bin"; do
    command -v "$command" >/dev/null || { echo "required command missing: $command" >&2; exit 1; }
done
docker info >/dev/null

scratch=$(mktemp -d "${TMPDIR:-/tmp}/gonka-pg-smoke.XXXXXXXX")
suffix=$(basename "$scratch" | tr '[:upper:]' '[:lower:]')
cluster_name="gonka-pg-${suffix##*.}"
kubeconfig="$scratch/kubeconfig"
context="kind-$cluster_name"
namespace=gonka-pg-smoke
db_cluster=smoke-gonka-postgres
cluster_created=false

kctl() { "$kubectl_bin" --kubeconfig "$kubeconfig" --context "$context" --request-timeout=20s "$@"; }
kube() { kctl --namespace "$namespace" "$@"; }
sql_exec() {
    local pod=$1
    shift
    timeout 60s "$kubectl_bin" --kubeconfig "$kubeconfig" --context "$context" \
        --namespace "$namespace" --request-timeout=20s exec "$pod" -c postgres -- "$@"
}
cleanup() {
    status=$?
    trap - EXIT
    if [[ $status -ne 0 && -s $kubeconfig ]]; then
        echo "PostgreSQL smoke failed; diagnostics for $cluster_name" >&2
        kctl get nodes -o wide >&2 || true
        kube get clusters,failoverquorums,pods,pvc,services -o wide >&2 || true
        kube get events --sort-by=.lastTimestamp >&2 || true
        kctl -n cnpg-system logs deployment/cnpg-controller-manager --tail=100 >&2 || true
        for pod in $(kube get pods -l "cnpg.io/cluster=$db_cluster" -o name 2>/dev/null); do
            kube logs "$pod" -c postgres --tail=40 >&2 || true
        done
    fi
    if [[ ${KEEP_KIND_CLUSTER:-0} == 1 ]]; then
        echo "Preserved isolated cluster $cluster_name; kubeconfig: $kubeconfig" >&2
    else
        if [[ $cluster_created == true ]]; then
            if ! timeout 120s "$kind_bin" delete cluster --name "$cluster_name" --kubeconfig "$kubeconfig"; then
                echo "Cleanup failed for owned cluster $cluster_name; retained kubeconfig: $kubeconfig" >&2
                exit 1
            fi
        fi
        rm -rf -- "$scratch"
    fi
    exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Exact minimum supported operator release; inspect/download upstream first,
# verify its complete manifest checksum, then apply only to this test cluster.
python3 - "$scratch/operator.yaml" <<'PY'
import hashlib
import pathlib
import sys
import urllib.request
url = 'https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/v1.28.0/releases/cnpg-1.28.0.yaml'
with urllib.request.urlopen(url, timeout=45) as response:
    content = response.read()
expected = 'b7f53dd5353ef2e0d8c67006645ab632d68f961053c220449337c8ea5751735e'
assert hashlib.sha256(content).hexdigest() == expected, 'operator manifest SHA256 mismatch'
pathlib.Path(sys.argv[1]).write_bytes(content)
PY

cat > "$scratch/kind.yaml" <<'YAML'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
  - role: worker
  - role: worker
  - role: worker
YAML
# Same immutable Kubernetes image as kind-smoke.sh.
node_image=${KIND_NODE_IMAGE:-kindest/node:v1.34.0@sha256:7416a61b42b1662ca6ca89f02028ac133a309a2a30ba309614e8ec94d976dc5a}
# Official multi-architecture PG16 image, registry index checked before pinning.
postgres_image=${POSTGRES_IMAGE:-ghcr.io/cloudnative-pg/postgresql:16.13-system-trixie@sha256:91d4e86f4ca9dbcb1052105cc131d68d22d2151047436189f9d966d71a0503d1}
echo "Creating isolated PostgreSQL test cluster $cluster_name (artifacts: $scratch)"
cluster_created=true
timeout 12m "$kind_bin" create cluster --name "$cluster_name" --kubeconfig "$kubeconfig" \
    --config "$scratch/kind.yaml" --image "$node_image" --wait 180s
if [[ ${PRELOAD_IMAGES:-0} == 1 ]]; then
    # Optional offline/cached-image path. The caller must verify the cached
    # image provenance first; the default online run uses the pinned PG digest.
    operator_image=ghcr.io/cloudnative-pg/cloudnative-pg:1.28.0
    docker image inspect "$postgres_image" "$operator_image" >/dev/null
    timeout 5m "$kind_bin" load docker-image --name "$cluster_name" "$postgres_image" "$operator_image"
    python3 - "$scratch/operator.yaml" <<'PY'
import pathlib
import sys
p = pathlib.Path(sys.argv[1])
text = p.read_text()
assert text.count('imagePullPolicy: Always') == 1
p.write_text(text.replace('imagePullPolicy: Always', 'imagePullPolicy: IfNotPresent'))
PY
fi
kctl apply --server-side -f "$scratch/operator.yaml"
kctl -n cnpg-system rollout status deployment/cnpg-controller-manager --timeout=5m
kctl wait --for=condition=Established --timeout=60s crd/clusters.postgresql.cnpg.io crd/failoverquorums.postgresql.cnpg.io
kctl create namespace "$namespace"
kube create secret generic postgres-credentials --type=kubernetes.io/basic-auth \
    --from-literal=username=devshardd --from-literal=password=fixture-not-a-real-secret

# Requests are reduced for this disposable test only; production defaults and
# all three replicas, separate PVCs, anti-affinity and quorum settings stay intact.
cat > "$scratch/values.yaml" <<YAML
imageName: $postgres_image
credentialsSecret: postgres-credentials
storage:
  size: 1Gi
resources:
  requests: {cpu: 100m, memory: 256Mi}
  limits: {memory: 1Gi}
YAML
"$helm_bin" upgrade --install smoke "$chart_dir" --kubeconfig "$kubeconfig" --kube-context "$context" \
    --namespace "$namespace" --values "$scratch/values.yaml" --wait --timeout=5m

ready_topology() {
    kube get cluster "$db_cluster" -o json > "$scratch/cluster.json" || return 1
    kube get pods -l "cnpg.io/cluster=$db_cluster,cnpg.io/podRole=instance" -o json > "$scratch/pods.json" || return 1
    python3 - "$scratch/cluster.json" "$scratch/pods.json" <<'PY'
import json
import sys
cluster = json.load(open(sys.argv[1]))
pods = json.load(open(sys.argv[2]))['items']
ready = [p for p in pods if not p['metadata'].get('deletionTimestamp') and
         any(c['type'] == 'Ready' and c['status'] == 'True' for c in p.get('status', {}).get('conditions', []))]
ok = (cluster.get('status', {}).get('readyInstances') == 3 and len(ready) == 3
      and len({p['spec']['nodeName'] for p in ready}) == 3)
sys.exit(0 if ok else 1)
PY
}

wait_ready() {
    local deadline=$((SECONDS + $1))
    until ready_topology; do
        [[ $SECONDS -lt $deadline ]] || { echo "Timed out waiting for 3 ready PostgreSQL instances on distinct nodes" >&2; return 1; }
        sleep 5
    done
}

echo "Waiting for three independent PostgreSQL instances"
wait_ready 600
kube get cluster "$db_cluster"
kube get pods -l "cnpg.io/cluster=$db_cluster,cnpg.io/podRole=instance" -o wide
kube get failoverquorum "$db_cluster"
python3 - "$scratch/cluster.json" <<'PY'
import json
import sys
c = json.load(open(sys.argv[1]))
s = c['spec']['postgresql']
assert s['synchronous']['failoverQuorum'] is True
assert s['synchronous']['dataDurability'] == 'required'
assert s['synchronous']['number'] == 1
assert s['parameters']['synchronous_commit'] == 'on'
PY

old_primary=$(kube get cluster "$db_cluster" -o jsonpath='{.status.currentPrimary}')
[[ -n $old_primary ]] || { echo "Missing primary" >&2; exit 1; }
old_node=$(kube get pod "$old_primary" -o jsonpath='{.spec.nodeName}')
marker="durable-$cluster_name"
echo "Writing synchronous sentinel on $old_primary"
sql_exec "$old_primary" psql -U postgres -d devshardd -v ON_ERROR_STOP=1 -c \
    "CREATE TABLE ha_smoke (marker text PRIMARY KEY); INSERT INTO ha_smoke VALUES ('$marker'); GRANT SELECT, INSERT ON ha_smoke TO devshardd;"
[[ $(sql_exec "$old_primary" psql -U postgres -d devshardd -Atc 'SHOW synchronous_commit') == on ]]
sql_exec "$old_primary" psql -U postgres -d devshardd -Atc 'SHOW synchronous_standby_names'

# Prevent immediate restart on the same local-path PVC, then remove the primary.
# This is an abrupt Pod loss, not a requested CNPG switchover. Every command is
# scoped to our uniquely named disposable cluster; the caller's nodes are untouched.
echo "Failing primary Pod $old_primary on $old_node"
kctl cordon "$old_node"
kube delete pod "$old_primary" --grace-period=0 --force --wait=false
deadline=$((SECONDS + 240))
new_primary=
while [[ $SECONDS -lt $deadline ]]; do
    candidate=$(kube get cluster "$db_cluster" -o jsonpath='{.status.currentPrimary}')
    if [[ -n $candidate && $candidate != "$old_primary" ]]; then
        if [[ $(sql_exec "$candidate" psql -U postgres -d devshardd -Atc 'SELECT pg_is_in_recovery()' 2>/dev/null) == f ]]; then
            new_primary=$candidate
            break
        fi
    fi
    sleep 3
done
[[ -n $new_primary ]] || { echo "CNPG did not promote another primary within 240 seconds" >&2; exit 1; }
[[ $(sql_exec "$new_primary" psql -U postgres -d devshardd -Atc 'SELECT marker FROM ha_smoke') == "$marker" ]]

# Check EndpointSlice membership as well as SQL through the public writer Service.
deadline=$((SECONDS + 90))
until kube get endpointslices -l "kubernetes.io/service-name=$db_cluster-rw" -o json | \
    python3 -c 'import json,sys; endpoints=[e for s in json.load(sys.stdin)["items"] for e in s.get("endpoints",[]) if e.get("conditions",{}).get("ready")]; sys.exit(0 if len(endpoints)==1 and endpoints[0].get("targetRef",{}).get("name")==sys.argv[1] else 1)' "$new_primary"; do
    [[ $SECONDS -lt $deadline ]] || { echo "Writer Service did not select the new primary" >&2; exit 1; }
    sleep 3
done
via_service=$(sql_exec "$new_primary" env \
    PGPASSWORD=fixture-not-a-real-secret PGCONNECT_TIMEOUT=5 PGSSLMODE=require \
    psql -h "$db_cluster-rw" -U devshardd -d devshardd -v ON_ERROR_STOP=1 -qAtc \
    "SELECT marker FROM ha_smoke; SELECT pg_is_in_recovery(); INSERT INTO ha_smoke VALUES ('after-failover') RETURNING marker;")
[[ $via_service == "$marker"$'\nf\nafter-failover' ]] || { echo "Unexpected writer Service result: $via_service" >&2; exit 1; }

echo "New primary $new_primary retained the sentinel; -rw reads and writes succeeded"
kctl uncordon "$old_node"
wait_ready 240
echo "PostgreSQL HA smoke passed: 3 nodes, synchronous commit, automatic primary replacement, durable data and writer Service recovery"
