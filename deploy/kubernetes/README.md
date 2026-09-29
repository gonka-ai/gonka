# Optional Kubernetes deployment for the Gonka HA serving tier

**Scope: HA serving components only.** This package deploys the existing HA
serving tier from `devshard-0.2.x-v6` into an operator-provided Kubernetes cluster.
Docker Compose remains supported in `deploy/join`, including multi-host HA;
neither deployment method depends on the other. DAPI, the chain node, TMKMS and
ML nodes are external dependencies and are not installed by these charts. The
documented topology keeps them outside Kubernetes. This chart does not make
DAPI active-active or duplicate a validator.

## Deployment scope and future ML nodes

The `gonka-ha` chart owns the HA serving tier. The optional `gonka-postgres`
chart owns a separate database release. Neither chart installs Kubernetes
itself or packages a complete Gonka node.

Future Kubernetes support for ML nodes should have its own chart and Helm
release, with independent upgrades, GPU settings and model storage. No ML-node
chart is included here. A separate release does **not** require a separate
Kubernetes cluster:

- For one operator at one site, a shared cluster is a reasonable starting
  point: run HA services on CPU workers and ML nodes on a dedicated GPU worker
  pool, preferably in a separate namespace. Reserve GPU workers with taints
  and select the intended workers for each workload; namespaces alone do not
  isolate compute resources. The HA chart already exposes per-component
  `nodeSelector` and `tolerations` settings.
- Use a separate ML cluster when GPU infrastructure has different owners,
  locations, maintenance schedules or isolation requirements. This separates
  cluster control planes and cluster-wide changes, while adding another cluster
  and cross-cluster networking to operate. HA still depends on available ML
  capacity, even when the clusters are separate.

In either layout, a future ML release must provide stable per-node identity and
addresses reachable by external DAPI and the HA serving tier. Cluster-only DNS
or a shared load balancer must not silently replace registered ML-node identity.
GPU driver/device-plugin setup, model caches and restart behavior also need
their own implementation and validation; the HA chart does not provide them.
See the Kubernetes guidance on [GPU scheduling](https://kubernetes.io/docs/tasks/manage-gpus/scheduling-gpus/)
and [dedicated workers with taints and tolerations](https://kubernetes.io/docs/concepts/scheduling-eviction/taint-and-toleration/#example-use-cases).

## Architecture

```text
L4 load balancer / public Service
  -> ingress pods [public HAProxy -> loopback nginx policy -> loopback HAProxy]
       |                                      |
       |                                      +-> edge-api Service -> edge-api pods
       |                                      +-> external DAPI
       +-> versiond-router pods (same escrow hash)
            -> one stable Service per versiond pod -> versiond -> devshardd
                                                        |
                                      one shared writable PostgreSQL primary

External DAPI /versions -> replicated read-only HA catalog filter
                            -> versiond and both router tiers
External chain RPC/gRPC and DAPI NodeManager -> versiond/devshardd and edge-api
```

The Kubernetes topology follows the executable v6 code, not the older proposal
in [discussion #1367](https://github.com/gonka-ai/gonka/discussions/1367):

- The sticky routers are HAProxy. `versiond` already handles SIGTERM, readiness,
  request drain and child reap. No Kubernetes leader election or new drain API
  is introduced. See [host evacuation](../../devshard/docs/versiond-host-evacuation.md).
- `edge-api` remains a stateless query API. The current public proxy sends those
  routes through nginx; it has no `:18082` edge distributor. A normal Kubernetes
  Service is sufficient for this stateless pool.
- DAPI still owns local configuration, NATS, PoC and ML management. No Redis,
  NATS cluster, new signer or replacement event hub is installed.
- Governance updates still replace children **inside** versiond. Kubernetes
  replaces whole supervisors only when their pod specification changes.

## Requirements

- Kubernetes **1.33+**, IPv4 pod/service networking, Helm 3.19+. The minimum
  Kubernetes version provides native sidecars with
  the shutdown ordering needed by the ingress pair.
- At least **three schedulable worker nodes** for the defaults. Required
  anti-affinity places each component's replicas on separate nodes. Enough
  capacity for the edge/API catalog Deployments' surge pods is also needed.
- A CSI StorageClass for separate ReadWriteOnce volumes, and a CNI enforcing
  NetworkPolicy. There are no host mounts, Docker socket mounts or cluster RBAC
  permissions. Service account token automounting is disabled.
- A namespace permitting the current versiond and nginx images to run as root
  with limited capabilities (Pod Security Baseline). These images do not meet
  the Restricted Pod Security profile.
- Private routed connectivity from pod networks to external DAPI public API
  (`9000` by default), its catalog (`9100`, GET `/versions` only), NodeManager
  (`9400`), chain gRPC (`9090`) and RPC (`26657`). Use private DNS names resolvable
  inside the cluster. Firewall these external ports to the intended clients.
  Set `external.chainGrpcTls: true` for a chain gRPC endpoint using TLS;
  versiond children and edge-api then validate its hostname and certificate
  against their image's system trust roots. The default is plaintext gRPC for
  the private node listener. DAPI NodeManager still uses private plaintext gRPC.
- Outbound access to governance-approved binary download URLs and whatever
  peer/ML endpoints the current devshard protocol requires. The chart restricts
  **incoming** private traffic; it does not invent an egress allowlist for these
  dynamic destinations.
- A shared PostgreSQL primary with synchronous durability, safe failover and no
  loss of acknowledged devshard state. Direct PostgreSQL connections are used;
  transaction/statement poolers are unsupported because the runtime uses
  session advisory locks. See [storage requirements](../../devshard/docs/storage-design.md).

The PostgreSQL primary can be external **or** managed in this Kubernetes cluster
using the separate [gonka-postgres chart](postgres.md). It is not an application
subchart: application updates/uninstall do not own the database lifecycle.

## Images and supported protocols

Set explicit image references for all Gonka components. Build from this branch
and push to a registry accessible to the cluster; pin production references to
tested digests. Existing published v5 images must not be assumed to support the
new `PROXY_ROUTER_PUBLIC_BIND_ADDRESS` setting.

From the repository root, example build commands (substitute your registry/tag):

```bash
docker build -t registry.example.com/gonka/versiond:REVISION versioned
docker build -f versiond-router/Dockerfile -t registry.example.com/gonka/versiond-router:REVISION .
docker build -f proxy-router/Dockerfile -t registry.example.com/gonka/proxy-router:REVISION .
docker build -t registry.example.com/gonka/proxy:REVISION proxy
docker build -f edge-api/Dockerfile --build-arg GOOS=linux --build-arg GOARCH=amd64 \
  -t registry.example.com/gonka/edge-api:REVISION .
```

`protocols` is an explicit allowlist of **chain-approved, PostgreSQL-capable**
protocol names. The default `v6` is an example, not an on-chain activation. Check
the actual external `/versions` response and approved artifact capabilities.
The filter preserves binary URLs and digests; it never manufactures an approval.
The same filtered feed reaches supervisors and both router tiers. Malformed or
unavailable upstream responses produce an error, never a synthetic empty list.
The allowlist is **not** a list of static router bootstrap routes:
`VERSIOND_VERSIONS` is empty in both router tiers. Every initially published
version must pass the catalog's `activationMinReady` check. Once admitted, its
durable catalog entry remains available with fewer healthy peers; admission is
not a rule to withdraw an already serving version during a failure.

Legacy SQLite `v1`/`v2`/`v3` are rejected. Existing legacy escrows must remain on
their current single owner/public route until finished. Do not move a public
address serving these escrows to this HA-only chart. Other selected versions
must pass versiond's existing HA/storage preflight. Keep serving all protocols
with active sessions; removing an allowlist entry is a maintenance operation,
not a routine upgrade. Routers retain previously admitted catalog entries.

## Installation

1. Prepare PostgreSQL using [postgres.md](postgres.md), or obtain a supported
   external primary. Existing deployments must continue using their original
   database; attaching an empty replacement is not a migration.
2. Create a namespace and existing Secrets there. Use the **same participant
   identity and warm key** as external DAPI. Import only `keyring-file`, never
   validator consensus keys or the whole chain home directory:

   ```bash
   kubectl create namespace gonka
   kubectl -n gonka create secret generic gonka-keyring \
     --from-file=/secure/path/.inference/keyring-file
   kubectl -n gonka create secret generic gonka-keyring-password \
     --from-file=password=/secure/path/keyring-password
   kubectl -n gonka create secret generic gonka-postgres-app \
     --type=kubernetes.io/basic-auth \
     --from-literal=username=devshardd \
     --from-file=password=/secure/path/postgres-password
   kubectl -n gonka create secret generic gonka-postgres-ca \
     --from-file=ca.crt=/secure/path/postgres-ca.crt
   kubectl -n gonka create secret tls gonka-tls \
     --cert=/secure/path/tls.crt --key=/secure/path/tls.key
   ```

   Password files must contain the exact password, without an unintended final
   newline. Secrets are referenced by name and never embedded in Helm values.
   `PGUSER` must match the database Secret's username when using CNPG.

3. Copy [examples/values.yaml](examples/values.yaml) to a private values file.
   Set images, external addresses, participant public key and key name, database
   connection/TLS and storage classes. `extraEnv` accepts observability variables
   only; it cannot override HA storage, membership, identity or policy guards.
4. Render and install initially with the example's private `ClusterIP` ingress:

   ```bash
   helm lint deploy/kubernetes/charts/gonka-ha -f /secure/path/gonka-values.yaml
   helm template host deploy/kubernetes/charts/gonka-ha -n gonka \
     -f /secure/path/gonka-values.yaml > /tmp/gonka-ha.yaml
   helm upgrade --install host deploy/kubernetes/charts/gonka-ha -n gonka \
     -f /secure/path/gonka-values.yaml --wait --timeout 45m
   # Default replica counts. Adjust the expected count per tier if customized.
   kubectl -n gonka wait --for=jsonpath='{.status.readyReplicas}'=3 \
     statefulset/host-gonka-ha-versiond statefulset/host-gonka-ha-router \
     statefulset/host-gonka-ha-ingress --timeout=45m
   ```

   Explicitly wait for these `OnDelete` StatefulSets: Helm's `--wait` and
   `kubectl rollout status` do not provide that initial readiness check.
   Downloads, schema initialization and recovery may take time. Check pod logs
   and per-version readiness before extending timeouts. Liveness deliberately
   checks processes, not shared database/catalog availability, to avoid restart
   storms. Edge `/healthz` proves its process is running, not chain availability.

5. Verify storage from an operator environment that can reach the intended
   primary and Kubernetes API. Export an independent reference connection using
   `PGHOST`, `PGPORT`, `PGDATABASE`, `PGUSER` and appropriate password/TLS settings.
   Then run:

   ```bash
   python3 deploy/kubernetes/verify-storage.py --namespace gonka --release host
   ```

   The tool uses local `psql` and `kubectl exec`. It challenges every live HA
   child generation, checks all peers and the independent primary can read each
   nonce, and verifies neither pods nor generations changed during the check.
   It writes only the existing storage challenge field. Run checks/upgrades
   sequentially for a given database; concurrent challenges fail closed. The
   check requires current artifacts exposing the storage proof API.
6. Test representative read, inference and peer routes privately from inside
   the cluster through `host-gonka-ha-ingress`, then configure public exposure.
   Change `ingress.service.type` to `LoadBalancer` in your saved values and apply
   the same Helm command. Use provider-specific annotations as needed.

Public traffic must traverse the **Service/Pod IP**, where HAProxy listens.
`kubectl port-forward` into the ingress pod's port 80 connects to loopback and
would reach the private PROXY-protocol nginx listener instead. Use an in-cluster
client or the load balancer for public-route tests. Port forwarding versiond's
8080 for read-only diagnostics does not have this distinction.

TLS uses a standard Kubernetes TLS Secret, projected read-only into policy pods;
the existing nginx watcher reloads certificate rotations. With no `tlsSecret`,
the Service exposes HTTP only. Use a private L4 path or provision TLS before
public use. `externalTrafficPolicy: Local` preserves client source IP when the
load balancer supports it. If the provider sends PROXY protocol, explicitly set
its trusted CIDRs; leave this empty for direct connections. Generic L7 ingress
controllers are not inserted automatically because their buffering, retries,
timeouts and forwarded-header policy require their own configuration.

## Rollouts, failures and scaling

Use the operator-side coordinator for upgrades of an installed release:

```bash
python3 deploy/kubernetes/rollout.py upgrade --namespace gonka --release host \
  --values /secure/path/gonka-values.yaml
```

Change one component/image at a time in that complete saved values file.
The versiond, router and ingress StatefulSets use `OnDelete`: Helm stages their
pod templates; the coordinator owns planned replacements. Before each stop it
checks a reserve of `activationMinReady` other backends **for every admitted or
serving version**, and checks their actual admission in every parent HAProxy.
An overall `/readyz` response and the configured replica count cannot prove
that reserve. For example, three pods serving v6 do not authorize stopping the
only pod serving v7. A replacement must recover all protected versions and
parent admission before the next pod can stop. If it fails, the coordinator
restores that pod's prior ControllerRevision template; if the remaining pool
has also degraded, it stops and retains the recovery journal.

After every Helm apply, the coordinator also waits for the edge-api and oracle
Deployments: the controller must observe the applied generation, all desired
replicas must be updated, ready and available, and old replicas must retire.
Ready replicas from the previous image do not count as a completed update.
A failed Deployment update stops the command and retains the journal; correct
the image or other failing configuration and repeat with `--resume`. The
coordinator does not automatically roll back the whole Helm release.

Changing the versiond pool requires an explicit **maintenance window**:

```bash
python3 deploy/kubernetes/rollout.py maintenance-upgrade --namespace gonka --release host \
  --values /secure/path/gonka-values.yaml
```

The coordinator drains ingress completely, then drains every old inner router,
including established streams, before changing membership. During this window
the public endpoint is unavailable. It applies the new pool with both routing
tiers held at zero, starts the new routers, verifies each protected version's
reserve and admission, and only then starts public ingress. Old and new escrow
placement contracts never serve concurrently. Replica changes for any serving
tier, endpoint changes and allowlist removals are rejected by live Helm checks
while routing pods still exist, including terminating pods. `maintenance: true`
is an internal staging setting for this procedure, not a bypass flag.

Choose `fullnameOverride` at installation time. Changing the rendered serving
StatefulSet names on an installed release is unsupported, even in maintenance.
The coordinator rejects it before draining ingress, and the chart also rejects
it with both routing tiers already offline. Renaming requires a separate
migration of resources and persistent data.

The coordinator requires Python 3 and PyYAML (`python3 -m pip install PyYAML==6.0.3`
in your operator environment). Both commands use local `kubectl` and Helm credentials; optional `--context`
and `--kubeconfig` apply to both tools. No Kubernetes API access is given to
the application containers. A ConfigMap named `<release>-ha-rollout` (hashed for
long release names) prevents
concurrent coordinator runs and records protected routes and an in-progress
replacement. A failure or interruption retains it. Resolve the failure, ensure
the previous process is no longer running, and repeat the command with
`--resume` (corrected values may be supplied). An interrupted maintenance run
drains both routing tiers again before applying anything; it may leave ingress
off until recovery completes. Do not remove the journal to bypass a failed
reserve check. Previously served routes remain protected during maintenance;
this command does not authorize retiring an active protocol.

- Serialize Helm/coordinator operations, node maintenance and other pod
  disruptions. Do not use `kubectl rollout restart`, scale these StatefulSets
  directly, or apply client-rendered manifests to an installed release. Helm
  live guards cannot protect changes made outside Helm. PDBs limit voluntary
  evictions but do not enforce per-version reserves; use the same reserve
  checks before planned node work. Pod/node crashes can interrupt streams.
- The first upgrade from the earlier draft chart must install `OnDelete`
  through this coordinator before any planned replacement. `helm upgrade
  --wait` alone stages templates without verifying that every pod uses them.
- A Service for **each** versiond ordinal has one stable virtual IP and selects
  one pod. Every router hashes those same addresses. Do not replace this with
  one balancing Service or `sessionAffinity: ClientIP`. Services publish unready
  endpoints so HAProxy can inspect precise per-version readiness and observe
  the SIGTERM announce interval; Kubernetes coarse readiness is not a substitute.
- versiond receives SIGTERM and owns announce/drain/child shutdown. The default
  30-minute Kubernetes grace exceeds its 25-minute absolute shutdown budget.
  Long requests are bounded by those budgets. Forcing deletion bypasses them.
- Ingress HAProxy is a native sidecar: nginx first withdraws and exits gracefully
  with SIGQUIT while its local routing sidecar remains alive. HAProxy then uses
  SIGUSR1 to soft-stop. This includes uploads that open their upstream late.
- Inner routers also soft-stop with SIGUSR1. During an ordinary compatible
  replacement, established streams remain on the old connection and new
  requests use admitted peers. Membership changes use the offline procedure
  above; they are not ordinary rolling updates.
- PostgreSQL primary failure is different from planned app drain: loss of the
  session fence force-closes affected child listeners. versiond restarts them,
  establishes fresh fences and recovers from PostgreSQL. Existing streams can
  fail, and the restart backoff can reach 60 seconds. Test the complete database
  failover path with the operator/provider you deploy.
- Scale replicas through saved values and `maintenance-upgrade`. New
  replicas need storage and node capacity. There is no HPA in this first chart;
  replicas and explicit router membership must change together. Do not scale
  the StatefulSet independently of Helm or delete its per-ordinal Services.
  Router pool capacity is the number of explicit versiond endpoints generated
  from `versiond.replicas`; there is no separate DNS-slot setting in this chart.
- Monitor requested PostgreSQL connections across protocols and overlapping
  child generations. Each child uses `poolMaxConns + 2` dedicated/application
  connections; supervisors/schema initialization need additional connections.
  See the sizing formula in the storage design.

## Migration, retention and rollback

For an existing Compose HA host, retain the same PostgreSQL database, identity,
protocol artifacts and public route semantics. Bring up the Kubernetes stack
privately and run the storage challenge against that original primary. Cut over
the public L4 endpoint/DNS only after inference tests pass, then drain the old
serving tier. Keep DAPI/chain endpoints privately reachable. This is an operator
procedure; Helm does not move databases, DNS or legacy SQLite session ownership.

Supply a private, routable endpoint for the original database: the Compose
container name and Docker network are not automatically reachable from pods,
and this chart does not reconfigure Compose networking. Stock Compose PostgreSQL
has no TLS; on that trusted private path explicitly set `postgres.sslMode=disable`,
leave `postgres.caSecret` empty, and use `PGSSLMODE=disable` for the
independent storage verifier connection too. The chart default remains `require`;
the CloudNativePG setup uses `verify-full`. Reusing a single Compose PostgreSQL
instance also retains its existing availability boundary.

Adding/changing a database mode does not copy data. Restore/migrate the original
database with a documented PostgreSQL procedure before directing a new serving
tier to it. Do not run two independently writable restored copies for one host.

StatefulSet PVCs are retained by Kubernetes by default after scale-down or Helm
uninstall; verify your StorageClass reclaim policy before deleting any PVC. Keep
router accepted-catalog snapshots as well as versiond state. Never share one
router cache volume between independent replicas. To return to earlier app
values, use the same guarded upgrade procedure (maintenance for membership
changes); an uncoordinated `helm rollback` bypasses the rollout checks. Neither
procedure can undo PostgreSQL schema changes or recreate deleted data.

## Verification

```bash
HELM=helm python3 -m unittest discover -s deploy/kubernetes/tests -v
python3 deploy/kubernetes/charts/gonka-postgres/tests/validate.py
make -C proxy-router test-render test-pod-routing test-supervisor test-compose
python3 deploy/kubernetes/tests/protocol-admission-smoke.py
deploy/kubernetes/tests/kind-smoke.sh
deploy/kubernetes/tests/postgres-kind-smoke.sh
```

The kind smoke uses an isolated cluster, real router/policy images and **mock**
versiond/edge/control-plane dependencies. It checks Kubernetes networking,
placement, admission, stream drain, refused live membership changes, maintenance
scaling, per-version reserve, failed-candidate restoration, failed Deployment
updates with corrected resume, and rename rejection before draining or while
offline. Set `SMOKE_SUITE=completion` to run only the Deployment/rename
regressions in a fresh isolated cluster. It does not prove real inference or
PostgreSQL failover. Run real chain/devshard acceptance and a primary-failure
exercise with the exact approved artifacts before production rollout. Existing
Compose host-evacuation coverage remains applicable to the shared runtime.

The separate [PostgreSQL smoke test](postgres.md#local-validation) starts real
CloudNativePG and PostgreSQL in its own disposable kind cluster. It checks
automatic primary replacement, preservation of an acknowledged write and
reconnection through the writer Service. It does not exercise application
recovery, storage snapshots or physical node/network failure.
