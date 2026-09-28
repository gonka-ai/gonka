# Optional PostgreSQL HA on Kubernetes

[`gonka-postgres`](charts/gonka-postgres/) provisions the shared devshard database
as a **separate Helm release** using a preinstalled CloudNativePG operator.
The application chart accepts either this database's writer endpoint or an
existing external HA PostgreSQL endpoint. No database or operator is installed,
upgraded or deleted as an application dependency. Docker Compose remains
supported independently.

This chart covers PostgreSQL availability. The external dapi, chain node,
NodeManager and ML workers retain the availability boundaries described in
[the application deployment guide](README.md).

## Prerequisites and failure behaviour

Use a currently supported CloudNativePG release **1.28.0 or newer**, with matching
CRDs and a working admission webhook, installed by the platform administrator.
The chart checks discovery of `postgresql.cnpg.io/v1`; this API version alone
does **not** prove the operator version. Version 1.28 introduced the native
`spec.postgresql.synchronous.failoverQuorum` field used here. Do not add the
deprecated `alpha.cnpg.io/failoverQuorum` annotation: it overrides that field.
See [CNPG failover](https://cloudnative-pg.io/docs/1.28/failover/).

The chart requires at least three instances and enforces placement on distinct
worker nodes. Provision at least three eligible nodes with sufficient resources,
and persistent storage suitable for PostgreSQL. CNPG creates a separate RWO PVC
per instance; storage performance and failure domains remain the hoster's
responsibility. Required anti-affinity leaves pods Pending when placement is
impossible. It prevents shared-node failures, but does not by itself separate
racks, zones or storage systems. See [CNPG scheduling](https://cloudnative-pg.io/docs/1.28/scheduling/)
and [storage](https://cloudnative-pg.io/docs/1.28/storage/).

Every acknowledged synchronous transaction reaches the primary and at least one
standby (`any`, `number: 1`, `dataDurability: required`, `synchronous_commit: on`).
With three healthy instances, the loss of the primary permits promotion when
both standbys remain reachable. If the primary and one standby are unavailable,
quorum protection refuses automatic promotion. With no usable standby, writes
block. These settings favour preservation of acknowledged data over continued
writes during an ambiguous failure. Do not override `synchronous_commit` in SQL
or force promotion to bypass the check during routine recovery.
[CNPG explains the quorum conditions](https://cloudnative-pg.io/docs/1.28/failover/).

## Install a new database

Examples use namespace `gonka` and database release `db`. Keep both database and
application in the same namespace to share their existing credentials Secret.
Create that namespace separately if needed. Put the password in a protected file
with no trailing newline; do not put it in chart values or Git.

```sh
kubectl -n gonka create secret generic gonka-postgres-credentials \
  --type=kubernetes.io/basic-auth \
  --from-literal=username=devshardd \
  --from-file=password=/secure/gonka-postgres-password
```

The Secret's `username` must equal chart `owner`; keys are exactly `username`
and `password`. The chart does not create or own this Secret. See
[CNPG bootstrap credentials](https://cloudnative-pg.io/docs/1.28/bootstrap/).

Create `postgres-values.yaml` using a tested **CNPG-compatible PostgreSQL 16**
image, with a concrete minor-version tag and preferably its digest:

```yaml
imageName: '<registry>/<cnpg-compatible-image>:16.<minor>-<variant>@sha256:<digest>'
credentialsSecret: gonka-postgres-credentials
database: devshardd
owner: devshardd
storage:
  size: 100Gi
  storageClass: '<your-postgres-storage-class>'
resources:
  requests: {cpu: '1', memory: 2Gi}
  limits: {memory: 4Gi}
# Optional placement constraints; all three instances must still fit.
nodeSelector: {}
tolerations: []
```

The placeholders must be replaced. No PostgreSQL image is silently selected.
The existing v5 Compose database is PostgreSQL 16; its Alpine image is not a
drop-in CNPG instance image. A matching major version does not make its raw
data directory portable across libc/image families.

```sh
helm upgrade --install db deploy/kubernetes/charts/gonka-postgres \
  --namespace gonka -f postgres-values.yaml --wait --timeout 20m
kubectl -n gonka wait --for=condition=Ready --timeout=20m \
  cluster/db-gonka-postgres
kubectl -n gonka get cluster/db-gonka-postgres
kubectl -n gonka get pods,pvc -l cnpg.io/cluster=db-gonka-postgres
```

Helm's `--wait` alone does not verify the custom resource's database readiness.
Check that CNPG reports all three instances ready on distinct nodes and the
expected primary before starting application writers. A controlled failover and
a backup/restore rehearsal against the actual storage are required production
acceptance checks; rendered YAML cannot prove them.

## Connect the application

Set these fields in the **gonka-ha** release's values:

```yaml
postgres:
  host: db-gonka-postgres-rw
  port: 5432
  database: devshardd
  user: devshardd
  credentialsSecret: gonka-postgres-credentials
  passwordKey: password
  sslMode: verify-full
  caSecret: db-gonka-postgres-ca
  poolMaxConns: 4
```

The default Cluster name is `<database-release>-gonka-postgres` (truncated to 50
characters); `fullnameOverride` changes it. CNPG maintains its `-rw` Service as
the writer endpoint. Do not use `-r`, `-ro` or a pod address. Connect directly:
devshard requires session semantics and does not support transaction pooling.
See [CNPG services](https://cloudnative-pg.io/docs/1.28/service_management/).

With operator-managed certificates, the default CA Secret is `<cluster>-ca`.
The application chart projects only `ca.crt`, excluding the CA private key.
Check `.status.certificates.serverCASecret` and your server certificate names if
the operator's certificate configuration differs. Keep its cluster DNS domain
aligned with the application's `clusterDomain`.
[CNPG certificate configuration](https://cloudnative-pg.io/docs/1.28/certificates/).

Namespace-wide default-deny policies must allow CNPG operator/API access,
replication between database instances, and application-to-database TCP 5432.
The application chart's policies do not configure PostgreSQL's network boundary.

For an existing Compose database, keep using its external endpoint until a
separately rehearsed migration is complete. An empty `initdb` cluster is **not**
a migration. Drain and stop all devshard writers, take a verified backup, perform
a logical export/import with the correct database owner, validate the restored
sessions and payloads, then change all writers to the new endpoint. Never run
writers against both histories. Keep the source and backups; after accepting new
writes, pointing back to the old database loses those writes. The existing
[Compose migration notes](../../docs/devshard-host-ha-setup.md) describe why raw
database-directory copies require matching image families.

## Backups and recovery

Replication does not protect against accidental deletion, corruption or loss of
the Kubernetes/storage system. Backups are disabled by default because CSI
capabilities and off-site storage are platform choices. Before production,
establish monitored backups, retention, off-site copies and a tested restore
procedure with an explicit recovery-point objective. A snapshot on the same
storage system may fail together with that system. For continuous off-site WAL
archiving/PITR, use a platform-managed CNPG cluster with the supported
[Barman Cloud plugin](https://cloudnative-pg.io/docs/1.28/backup/), then pass its
writer endpoint to gonka-ha. This chart does not configure an archive or PITR.

If your CSI driver, snapshot controller, `VolumeSnapshot` CRDs and
`VolumeSnapshotClass` support it, this chart can schedule **cold** snapshots:

```yaml
snapshots:
  enabled: true
  className: '<your-retained-snapshot-class>'
  schedule: '0 0 2 * * *' # six fields, starting with seconds
  immediate: true
```

The target is `prefer-standby`. CNPG temporarily fences the selected instance;
if no suitable standby exists it can fall back to the primary and interrupt
writes. Schedule backups with spare healthy replicas and verify completion.
Each snapshot includes PGDATA and `pg_wal` on the same volume. Cold snapshots
can restore that captured state without a WAL archive; the recovery point is
the snapshot, potentially behind the primary. Snapshot-only backup does not
recover transactions written afterwards. See [CNPG backup modes](https://cloudnative-pg.io/docs/1.28/backup/).

```sh
kubectl -n gonka get scheduledbackups,backups
kubectl -n gonka get volumesnapshots
```

Both Backup and VolumeSnapshot objects have no owner reference to the Helm
schedule or Cluster, so those objects survive removal of their parents. There is
no automatic snapshot pruning: maintain retention and capacity yourself.
Choose and verify `VolumeSnapshotClass.deletionPolicy: Retain`, preserve the
VolumeSnapshot manifests/annotations as well as the underlying snapshots, and
keep copies outside the failure domain. Deleting a VolumeSnapshot may otherwise
delete its backing data. See [CNPG snapshot persistence](https://cloudnative-pg.io/docs/1.28/appendixes/backup_volumesnapshot/).

For a restore drill, select a completed **cold** snapshot from this chart with
`readyToUse: true`. It must be available in the target namespace. Create a **new**
release and new PVCs, using the same PostgreSQL image family/major and enough
storage, the original database/owner, and a credentials Secret:

```sh
helm install db-restore deploy/kubernetes/charts/gonka-postgres \
  --namespace gonka -f postgres-values.yaml \
  --set snapshots.enabled=false \
  --set recovery.volumeSnapshot='<completed-cold-snapshot-name>'
kubectl -n gonka wait --for=condition=Ready --timeout=20m \
  cluster/db-restore-gonka-postgres
```

Validate schema, sessions/payloads and application access against the restored
writer endpoint before any cutover. Never set recovery values on a live Cluster
expecting an in-place restore. Preserve `recovery.volumeSnapshot` in the restored
release's values for later upgrades. Snapshots from clusters with separate WAL
volumes or tablespaces require a different recovery manifest; this chart's
single-volume recovery does not cover them. See [CNPG recovery](https://cloudnative-pg.io/docs/1.28/recovery/).

## Upgrades and deletion

Keep the database release installed across application updates and uninstalls.
Use a separate change window for PostgreSQL minor upgrades, operator upgrades,
storage changes and restore drills. Application chart version changes never
modify the database release. PostgreSQL major changes need their own migration;
the values schema deliberately rejects non-16 image tags.

The Cluster has `helm.sh/resource-policy: keep`. Helm removal therefore retains
the Cluster, but removes the schedule and orphans the retained resource from
the deleted release. Do not use database-release uninstall/reinstall as an
upgrade procedure; re-adopting retained resources needs deliberate Helm ownership
management. The external credentials Secret is not owned by either chart.
[Helm retention semantics](https://helm.sh/docs/howto/charts_tips_and_tricks/#tell-helm-not-to-uninstall-a-resource).

This annotation does not stop `kubectl delete cluster`, namespace deletion,
GitOps pruning or CRD deletion. CNPG owns its pods/PVCs; direct Cluster deletion
can cascade to those resources. A PV's `persistentVolumeReclaimPolicy` determines
whether its backing storage survives PVC deletion. Configure and verify a
`Retain` storage policy where appropriate; retaining a PV is not a backup or
automatic recovery procedure. See [Kubernetes reclaiming](https://kubernetes.io/docs/concepts/storage/persistent-volumes/#reclaiming)
and the [CNPG operator's deletion warning](https://github.com/cloudnative-pg/charts/blob/main/charts/cloudnative-pg/README.md#uninstalling).

## Local validation

```sh
python3 deploy/kubernetes/charts/gonka-postgres/tests/validate.py
```

Requires Helm 3, PyYAML and jsonschema. The test renders fresh, backup-enabled and
snapshot-recovery configurations, checks unsafe/missing settings are rejected,
and validates fields against the upstream CNPG **v1.28.0 CRDs with pinned
SHA256 checksums**. It downloads those two schemas only; `--crd-dir` accepts an
offline directory containing the original CRD filenames. No cluster is changed.
For standalone offline rendering, Helm also needs
`--api-versions postgresql.cnpg.io/v1` and, for snapshots,
`--api-versions snapshot.storage.k8s.io/v1`. Those flags simulate discovery;
they do not install an operator, webhook, snapshot controller or CSI driver.

For a real PostgreSQL failover smoke test, run:

```sh
deploy/kubernetes/tests/postgres-kind-smoke.sh
```

This additionally requires Docker, kind >= 0.30, kubectl, and registry/network
access. `KIND_BIN`, `HELM_BIN` and `KUBECTL_BIN` can select local tool binaries.
The script creates its own four-node kind cluster and temporary kubeconfig,
installs the checksum-pinned CNPG 1.28.0 minimum-version manifest and a pinned
PostgreSQL 16.13 image, and installs this chart with disposable credentials and
smaller test-only resource requests. It checks three ready database instances on
distinct Kubernetes nodes, writes a synchronous sentinel, removes the primary
pod, and checks automatic promotion, retained data and authenticated reads/writes
through the `-rw` Service. It then restores three healthy replicas and removes
only its own cluster and temporary files. `KEEP_KIND_CLUSTER=1` explicitly
retains that isolated cluster for diagnosis; it never changes the caller's
active Kubernetes context.

This exercises real database/operator failover, not inference/SSE continuation,
snapshot recovery, network partitions, disk loss or independent physical hosts.
All kind nodes share the test machine. Database connections can break during
promotion; this smoke test checks that clients can reconnect to the new writer.
Use a current supported patched operator release for production; testing the
minimum API version here does not recommend deploying that old patch level.

To reuse independently verified cached container images, use
`PRELOAD_IMAGES=1` and, if the local archive has only a tag, `POSTGRES_IMAGE` to
select that PostgreSQL 16 tag. `KIND_NODE_IMAGE` can likewise select a verified
cached node tag. These overrides are for controlled test environments; the
default run pins the upstream kind and PostgreSQL image digests. Cached mode
loads images only into the test cluster and switches the operator manifest's
image pull policy to `IfNotPresent` after verifying the original manifest hash.
The script still needs network access to download that operator manifest.
