# Release guide: `devshard-0.2.15-v5`

Operator-facing contract for the v5 deployment line. Alongside the ingress
and updater changes below, v5 adds **warm cutover**: a same-name SHA swap
waits for the new child's recovery backlog and background repairs to finish
before replacing a healthy generation.

Previous line: [release-0.2.14-v4.md](./release-0.2.14-v4.md).
Host evacuation: [versiond-host-evacuation.md](./versiond-host-evacuation.md).
Rolling updates: [rolling-update.md](./rolling-update.md).
Architecture: [high-availability-architecture.md](./high-availability-architecture.md).
Router: [versiond-router/README.md](../../versiond-router/README.md).
Detailed deploy verification: [v5-deploy-test-plan.md](./v5-deploy-test-plan.md).
Operator manuals: [v5-manual-height-sync.md](./v5-manual-height-sync.md), [v5-manual-residual.md](./v5-manual-residual.md).
Restore / snapshot work: [restore-host-loadsnapshot.md](./restore-host-loadsnapshot.md).
Sealed-inference index rebuild: [sealed-inference-index-rebuild-plan.md](./sealed-inference-index-rebuild-plan.md).

## What changes for an operator

- **Public ingress is HAProxy.** `proxy` is now `proxy-router`, a host-local
  HAProxy that balances two private nginx policy workers (`proxy-policy`,
  `proxy-policy2`). nginx keeps TLS, CORS, rate limits and route policy;
  HAProxy owns connection distribution and the versiond pool. Ports, TLS
  settings and `config.env` keys of the old proxy are unchanged.
- **versiond-router is an HAProxy fleet.** Three router slots run as separate
  Compose projects managed by `versiond-router-fleet.sh`. They discover
  versiond hosts through DNS or an explicit endpoint file, check
  `GET /readyz?version=<v>` on every host, and learn protocol names from the
  governance `/versions` feed. A new approved version needs no host-side edit.
- **versiond keeps at most one draining predecessor per version.** A second
  same-name SHA change that arrives while the previous generation is still
  draining is deferred and retried, so rapid catalog updates cannot stack
  generations and exhaust the shared PostgreSQL connections (#1702).
- **versiond has graceful shutdown and `/readyz`.** Both the single-versiond
  stack and the HA overlay use a Compose healthcheck on `/readyz`, so
  `docker compose up --wait` returns only when versiond can serve.
- **Local HA PostgreSQL is persistent.** `devshard-postgres` keeps `PGDATA` in
  `DEVSHARD_POSTGRES_DATA_DIR` (default `./devshards/postgres`). On the first
  v5 start its entrypoint copies the v4 anonymous volume there and leaves the
  volume untouched as the rollback copy.
- **versiond can run on other machines.** See
  [Multi-host versiond](#multi-host-versiond).

Removed with the old model:

| Removed | Replacement |
| --- | --- |
| nginx `versiond-router` singleton service | router fleet (`versiond-router-fleet.sh`) |
| `VERSIOND_HOSTS` | DNS alias `VERSIOND_POOL_HOST` or `VERSIOND_POOL_ENDPOINTS_FILE`; the old value is still accepted |
| `VERSIOND_ADMIN_LISTEN_ADDR` | `GET :8080/readyz` on the traffic listener |

## Recovery and readiness

v5 keeps shared Postgres, sticky escrow routing, validation-lease
exclusivity and the gRPC chain transport. In addition to the ingress and
router-fleet changes above, it changes the **recovery / readiness contract**:

- `devshardd` `/ready` is split into a **status code** (can this process serve:
  chain up, storage open, not draining) and a **body field** `recovery_complete`
  (is this process warm: backlog drained **and** background sealed-index /
  validation-obs repairs finished). Status is `200` within seconds of boot, so
  a solo restart is no longer force-stopped by the 60s `VERSIOND_READY_TIMEOUT`
  before it ever serves. The body field is the warm signal only a version
  replacement with a healthy old generation needs to wait on.
- `versiond`'s overlap swap waits for `recovery_complete: true` before
  publishing the new child. Solo start and the stop/start branch publish on
  status code alone — waiting with no warm generation to fall back on would
  just be an outage.
- The sealed-inference index rebuild no longer wipes and reinserts every
  sealed row on every restart. Snapshot restore does a gap fill (one id scan,
  zero writes when the index is already there); full journal replay rebuilds
  from the diff journal in batched transactions off the publish path.

---

## What's in this release

| Area | Change |
| --- | --- |
| **Ready probe split** | `/ready` status 200 = can serve; body `recovery_complete` = warm (backlog drained **and** `WaitRecoveryRepairs` returned). Counters `sessions_total`/`sessions_recovered`/`sessions_failed`/`sessions_version_skipped`/`sessions_pending` promoted from log-only to body + Prometheus |
| **Warm cutover** | Overlap swap waits for `recovery_complete: true` on the new child's `/ready` body before the route swap (`VERSIOND_RECOVERY_TIMEOUT`, default 30m). Solo start and stop/start publish on status 200. Bail-outs: absent field → cold cutover; old-child death → publish immediately; hostDraining/ctx → abort; timeout → old keeps serving |
| **Sealed-inference index** | Snapshot restore: gap fill only, no wipe (O(sealed) writes → 0). Full replay: rebuild from diff journal in batched transactions off the publish path. `WaitObsRepairs` → `WaitRecoveryRepairs` (waits both validation-obs and sealed-index repair) |
| **Nonce eviction** | A live session that fails `types.ErrInvalidNonce` is evicted and re-recovered (reuses `resolutionFailures` negative-cache so a bad client cannot spin the reload). Required for warm cutover: a long warm-up leaves the new child behind the old generation's writes |
| **versiond** | `getChildRecoveryStatus` reads the `/ready` body; `getHTTPStatus` stays status-only so `watchChildReadiness` never gates on the body (pinned by test) |

---

## Breaking / operator-facing changes

### `/ready` status vs body

The single probe that used to answer both "can serve" and "is recovery done"
is split:

| Signal | Meaning | Who reads it |
| --- | --- | --- |
| Status code 200 | Chain up, storage open, not draining | `waitForChildServingReady`, `watchChildReadiness`, K8s readinessProbe |
| Body `recovery_complete: true` | Recovery backlog drained **and** `WaitRecoveryRepairs` returned | `waitForChildRecoveryComplete` (overlap swap only) |

A pre-v5 `devshardd` does not ship `recovery_complete` on its `/ready` body.
The new `versiond` detects the absent field and skips the wait, cutting over
cold (flow B semantics) — so a mixed-version estate (new `versiond` + old
`devshardd`) keeps the v4 cold-cutover behaviour rather than hanging.

### Rolling updates (same name, new sha256) — warm before traffic, overlap only

The v4 rule was "new ready before traffic". The v5 rule is "new **warm** before
traffic, overlap only":

| Storage mode (both old + new `--print-storage-mode`) | Swap behavior |
| --- | --- |
| Exactly `postgres` | Blue/green: start new child on a new port → wait admin `/ready` 200 + public `/healthz` 2xx → **wait `recovery_complete: true`** → route new traffic to new generation → drain old → SIGTERM |
| `sqlite`, `hybrid`, `auto`/unknown, legacy binary without the flag | Exclusive stop/start (no overlapping children, no warm wait) |

Operator knobs (all defaulted; see [rolling-update.md](./rolling-update.md)):

| Env | Default | Role |
| --- | --- | --- |
| `VERSIOND_READY_TIMEOUT` | `60s` | Abort swap if incoming child never becomes *able to serve* (old keeps serving) |
| `VERSIOND_RECOVERY_TIMEOUT` | `30m` | Abort overlap swap if the new child's `recovery_complete` does not flip true in time (old keeps serving). **Not** the 60s ready timeout — recovery of a long journal is minutes to hours |
| `VERSIOND_DRAIN_TIMEOUT` | `15m` | Max wait for old proxy leases + lifecycle inflight |
| `VERSIOND_DRAIN_KILL_GRACE` | `10m` | Legacy no-status cushion / process kill backstop |
| `DEVSHARD_SHUTDOWN_GRACE` | `10m` | `devshardd` graceful HTTP shutdown after SIGTERM |

Bail-outs from the warm wait (companion *ready-on-boot-warm-cutover* flow D),
all of which cut over or abort rather than hang:

| Condition | Action |
| --- | --- |
| `recovery_complete` absent from the body | Skip the wait, cut over cold (flow B; an un-updated child cannot know about the field) |
| Old child stops being `Running` | Abandon the wait and publish immediately — a warming child plus a dead old child is an outage |
| `hostDraining` or ctx done | Abort, stop the new child |
| `VERSIOND_RECOVERY_TIMEOUT` elapsed | Abort, old keeps serving, retry next reconcile |

### Sealed-inference index rebuild

`RebuildSealedInferenceIndex` no longer runs unconditionally on every session
recovery. It is split by recovery path:

| Path | Behaviour |
| --- | --- |
| Snapshot restored (`replayFrom > 1`) | `FillSealedInferenceIndexGaps` inline: one `SealedInferenceIDs` scan, insert bare rows only for ids with no stored row. Never deletes, never downgrades an `ObsPresent = true` row. Normally writes nothing. |
| Full replay (`replayFrom == 1`) | `RebuildSealedInferenceIndexFromDiffs` in the background behind `ObsRepairGate`: wipe, then insert rich rows built from the diff journal in batched transactions. |

Net effect on the incident node (~1.5M sealed inferences): snapshot restart
goes from ~1.5M writes to **zero writes** (~0.3–0.6s of reads). Full replay
goes from ~25 min to ~11–17 s, off the publish path.

### Nonce eviction

Required on the same change as the warm wait: a child that recovered early
during a long warm-up sits behind the old generation's writes to the shared
store. The first request then fails `types.ErrInvalidNonce`. The new path
drops the stale session from `HostManager.sessions`, `Close`s the host, deletes
escrow metrics, and re-runs `recoverStoredSession` through the same
singleflight as `getOrCreate`. A bogus nonce is negative-cached via
`resolutionFailures` so a bad client cannot spin the reload; a genuine
catch-up mismatch uses a short TTL, not `permanentFailureTTL`.

---

## Binary upgrade compatibility

A same-name binary update publishes a new SHA through governance. The warm
wait bail-outs handle mixed `versiond` and `devshardd` releases:

- **New `versiond` + new `devshardd`**: full v5 behaviour — overlap swap waits
  for `recovery_complete`.
- **New `versiond` + old `devshardd`**: the old child's `/ready` body has no
  `recovery_complete`; the new `versiond` skips the wait and cuts over cold
  (v4 behaviour). No outage, no hang.
- **Old `versiond` + new `devshardd`**: the old `versiond` does not read the
  body; it publishes on status 200 (v4 behaviour). The new `devshardd`'s
  `recovery_complete` field is simply ignored. No outage.

So v5 can roll out incrementally without coordinating a simultaneous
`versiond` + `devshardd` upgrade.

---

## Updating an existing host

The update is a fixed sequence of `docker compose` commands.
`deploy/join/update-devshard.sh` runs them in the right order and prints each
one; nothing it does is hidden from the operator. Requirements: Docker Compose
2.24.4 or newer and `jq`.

From the checkout that runs the node:

```bash
git fetch origin
git checkout <release branch or tag>
cd deploy/join
source ./config.env
./update-devshard.sh --check
./update-devshard.sh
```

`--check` detects the topology, renders the Compose model, verifies that both
HA replicas share one PostgreSQL, checks that the PostgreSQL migration copy
fits, prints the images that will run, and changes nothing. `--dry-run` prints
the full command sequence without running it.

Compose files are taken from `COMPOSE_FILE` when it is set, otherwise from the
labels of the running `versiond` container (so operator overlays such as
observability files stay in the model), otherwise the stock files are used.
The topology is HA when the `versiond` service declares `GONKA_HA=true`,
which the HA overlay sets on every replica (the same declaration versiond
passes to its children and the routers turn into the `Devshard-Ha` header); a
single-versiond host stays single. The script handles every local `versiond`, `versiond2`, `versiond3`, …
service it finds in the model and never adds a replica by itself; services
whose replica count is `0` are skipped.

What the script runs, in order:

| Step | Single | HA |
| --- | --- | --- |
| `docker compose pull` of the devshard services | yes | yes |
| `up -d --no-deps --wait devshard-postgres` (entrypoint migrates v4 data) | | local PostgreSQL only |
| `versiond-router-fleet.sh prepare-networks` and `apply` | | yes |
| `up -d --no-deps --wait proxy-policy2`, then `proxy-policy`, then `proxy`; in HA the fleet's `verify-admission` then proves the new public proxy admits every router slot and live route before anything else changes | yes | yes |
| `docker rm -f versiond-router` (the old nginx singleton) | | if present |
| `up -d --no-deps --wait` of each replica, last one first, `versiond` last | `versiond` only | yes |

Every `up` uses `--wait`, so the next step starts only after the replaced
service passes its healthcheck.

### What happens on failure

The script holds the deployment lock for the whole run (the same lock as
`versiond-router-fleet.sh`), so a second operator or a manual fleet command
cannot interleave with it.

Before the first change it renders the model, checks that every replica names
the same PostgreSQL, opens that PostgreSQL with the configured credentials
from a helper container and requires a writable primary, and checks that the
v4 cluster copy fits. A wrong `DEVSHARD_POSTGRES_PASSWORD`, a read-only
replica or an unreachable managed host therefore stops the run while the
previous release is still fully serving.

It also checks that the model points at the database the running replicas
already use. Every running local replica reports its storage lineage through
`/internal/storage-identity`; they must agree, and the database the model
names must carry the same lineage. Because a physical copy keeps the
lineage, one replica then writes a random challenge through its own
PostgreSQL pool and the model's database must show it: a clone would not.
A `PGHOST` that now points at another working database, or at a copy, is
refused, because a rolling replacement would otherwise leave old replicas
writing to one history and new replicas to another. Set
`UPDATE_ACCEPT_DATABASE_CHANGE=true` only for an intended migration to a
restored copy, and only with every local and remote replica stopped first.

The supported v4-to-v5 upgrade starts with all versiond replicas on one host.
Multi-host versiond is introduced in v5; configure it after updating the
network node, following [Multi-host versiond](#multi-host-versiond).

These checks cover only the versiond replicas that the updater manages on
this host. On other hosts, run the updater's separate `--check-storage` mode
before admitting replicas to the pool; see [Multi-host versiond](#multi-host-versiond).
All replicas must connect to the same PostgreSQL database.

The write probe opens PostgreSQL with the model's credentials from a helper
container that borrows a running versiond's volumes, so a CA or client
certificate named by `PGSSL*` resolves; it creates and drops a temporary
table, so a read-only primary, revoked write rights or a full disk stop the
run. A bundled PostgreSQL that exists but is stopped is not a fresh install:
start it first. `UPDATE_SKIP_POSTGRES_PROBE=true` disables all of this and
is meant for a host that cannot reach the database at all.

Each step replaces one service and waits for its healthcheck. Before the
replacement, the image of the *healthy* container it replaces is kept under
the Docker tag `gonka-previous/<service>`, so the tag always names the last
image that was serving: a candidate that never became healthy does not
overwrite it, and a configuration-only update refreshes it. The tag lives in
Docker's image store, so it survives a killed run and can be used by hand
(`VERSIOND_IMAGE=gonka-previous/versiond docker compose up -d versiond`).
Images already on the host are not pulled again, so a rerun after a failure
does not depend on the registry.
If the replacement never becomes healthy, the script puts that image back
with the same `up`, prints the failing service, and stops. The host keeps
serving: services replaced earlier run the new release, the failed one and
everything after it run the previous release. That mixed state is the same
one a rolling update passes through by design (routers and replicas are
replaced one at a time and both releases serve side by side). Inspect
`docker compose logs <service>`, fix the cause, and run the script again;
Compose skips every service that already matches.

Putting an image back cannot undo a changed service definition. The one
place where the v5 model changes a service beyond its image is the first
public proxy cutover (nginx to HAProxy): if the new `proxy` never becomes
healthy, the previous nginx image will not pass the HAProxy healthcheck
either, and the script says so. The way back is the previous release's
Compose files: `git checkout <previous release> -- deploy/join` and
`docker compose up -d proxy`. Every later update of the same model rolls
back by image alone.

If the script itself is killed, nothing is left half-done inside a step:
Compose either recreated the service or it did not. Rerunning resumes from
the first service that still differs from the model; the deployment is
recognised through any of its containers, so a missing `versiond` after an
interrupted replacement does not turn an HA host into a single one, and a
replica added later with its own overlay is picked up from the longest
recorded file list.

Two things the script does not undo. The v4 PostgreSQL volume copy is safe
to repeat but never reversed automatically (see [Rolling back](#rolling-back)).
And a router fleet update runs inside `versiond-router-fleet.sh apply`, which
restores a slot's previous image itself when its replacement does not pass
admission, and refuses to start while a bootstrap version has no ready
router at all (a PostgreSQL or versiond outage), so an outage cannot be
rolled over silently.

A fresh bundled PostgreSQL records `.gonka-init-complete` inside `PGDATA`
through an initdb hook, and a migrated cluster records `.migrated-from-v4`
next to it before the copy is published; a persistent cluster without either
marker, or whose `pg_controldata` system identifier differs from an attached
v4 volume, is refused rather than started on the wrong history.

`--check` and `--dry-run` change no service. They do run the PostgreSQL
probe from a helper container (which may pull the pinned PostgreSQL image)
and the migration space probe (which may create the empty target directory).

Maintenance notes for the first v5 run on an HA host:

- Replacing the public proxy closes the connections the old nginx still holds.
  Restarting the shared local PostgreSQL interrupts devshard work on both
  replicas. Schedule the run outside PoC/cPoC and update one host at a time.
- The HAProxy routers start before versiond is replaced. They accept a
  pre-v5 versiond through its `/<version>/healthz` route checks, so the
  versiond replacements afterwards happen behind active checks.
- Budget up to 35 minutes for a slow versiond reconcile; healthy hosts finish
  much earlier.

### Rolling back

Rolling back is choosing the previous images and running the same sequence:

```bash
export VERSIOND_IMAGE=ghcr.io/product-science/versiond:0.2.15
export PROXY_ROUTER_IMAGE=<previous proxy image>
export PROXY_POLICY_IMAGE=<previous proxy image>
export VERSIOND_ROUTER_IMAGE=<previous router image>
./update-devshard.sh
```

Persist the values in `config.env` if the rollback should survive the next
run. Rolling back to the v0.2.15 nginx `proxy` and `versiond-router` needs the
previous Compose files as well (`git checkout <previous release> -- deploy/join`).

PostgreSQL is outside that rule. The v4 anonymous volume remains attached and
unmodified after the copy, but once the persistent database has accepted
writes, switching back to the volume would fork the history. Treat a problem
after that point as database recovery; see
[postgres-persistence-migration.md](./postgres-persistence-migration.md).

## Fresh HA installation

```bash
cd deploy/join
source ./config.env
./versiond-router-fleet.sh prepare-networks
docker compose -f docker-compose.yml -f docker-compose.versiond.yml up -d --wait
./versiond-router-fleet.sh apply
```

For managed PostgreSQL, add `docker-compose.versiond-external-postgres.yml` and
an operator override that sets the same `PGHOST`, `PGPORT`, `PGDATABASE`,
`PGUSER` and `PGPASSWORD` on every versiond service. Use only those variables:
`update-devshard.sh --check` rejects `DATABASE_URL`, `PGSERVICE`,
`PGSERVICEFILE` and `PGOPTIONS` on HA replicas, because a service file or a
session option such as `search_path` can send one process to a different
database than the tuple everybody else agreed on. Keep the same ordered
`-f` list, or `COMPOSE_FILE`, for every later command.

Size the server's `max_connections` for at least
`R * (2 * N * (P + 2) + 5)` non-reserved connections, where `R` is the number
of versiond replicas, `N` the number of HA devshard children each replica runs
(one per HA version) and `P` the `DEVSHARD_POSTGRES_POOL_MAX_CONNS` pool limit
(default 4). Every replica runs its own children, so the child term counts per
replica; the doubled child term covers the draining predecessor a version may
keep during a rolling binary update; the per-replica term is versiond's
session-lookup pool plus one schema-initializer session. With two replicas,
three HA versions and the default pool that is 82 connections; three replicas
need 123.

## Day-2 operations

| Task | Command |
| --- | --- |
| Update to a later release | `./update-devshard.sh` |
| Take `versiond2` out temporarily | `docker compose stop versiond2` |
| Put it back | `docker compose up -d --no-deps --wait versiond2` |
| Decommission `versiond2` | set `VERSIOND2_REPLICAS=0` in `config.env`, then `stop` and `rm` it |
| Add a third local replica | add `docker-compose.versiond3.yml` to the file list, `docker compose up -d --no-deps --wait versiond3` |
| Roll the router image or settings | edit `config.env`, then `./versiond-router-fleet.sh apply` |
| Inspect the fleet | `./versiond-router-fleet.sh status` |
| Wait for a newly approved version on this host | `./versiond-router-fleet.sh wait-version v9` |

The number of local replicas is not fixed. `docker-compose.versiond3.yml`
defines `versiond3` by extending the shipped `versiond2` service with its own
data directory and `VERSIOND3_REPLICAS`; copy it for `versiond4` and beyond.
Every replica joins the `versiond-pool` alias, so the routers find it without
configuration; the DNS pool holds up to 64 members
(`VERSIOND_ROUTER_POOL_SLOTS`). Keep `VERSIOND_ROUTING_ACTIVATION_MIN_READY`
at or below the number of replicas you run.

`versiond-router-fleet.sh maintenance-rollout` (placement changes: legacy
pins, pool name, coarse mode) is an acknowledged outage window. If it is
interrupted, run it again: slots already on the new placement are kept, the
rest are replaced. Its automatic rollback covers the slots the current run
captured; slots finished by an earlier interrupted run stay on the new
placement. The endpoint list is not part of that snapshot: a rolled-back
router runs the previous image with the current list, because the list is
the operator's desired membership, not a generation of the router.

Bundled PostgreSQL: a crashed postmaster is restarted by Docker
(`restart: always`); a hung one is reported unhealthy and needs an operator
(`docker compose restart devshard-postgres`). Runtime recovery inside
devshardd (fence budget, reconnect backoff, readiness of the write path) is
tracked separately from this deployment tooling.

The router slots are not part of the main Compose project. Before a full
`docker compose down`, run `./versiond-router-fleet.sh stop-all --maintenance`;
after it, `./versiond-router-fleet.sh down --maintenance`.

## Multi-host versiond

The router pool is normally the `versiond-pool` DNS alias of the Compose
network, which only local containers can join. To run versiond on other
machines, list the members explicitly. The routers then check each listed
address and keep the same consistent-hash placement on every slot.

On the **network node**:

1. Create `deploy/join/versiond-endpoints.json` (see
   `versiond-endpoints.example.json`). Local replicas are listed by container
   name, remote ones by private IP and port:

   ```json
   [
     { "id": "versiond",   "host": "versiond",   "port": 8080 },
     { "id": "versiond2",  "host": "versiond2",  "port": 8080 },
     { "id": "versiond-b", "host": "10.20.0.12", "port": 8080 }
   ]
   ```

2. In `config.env` set `VERSIOND_POOL_ENDPOINTS_FILE=./versiond-endpoints.json`
   and `GONKA_PRIVATE_BIND_IP=<private address of this machine>`.
3. Add `docker-compose.private-endpoints.yml` after
   `docker-compose.versiond.yml` in every Compose command (or in
   `COMPOSE_FILE`) and apply it: `docker compose ... up -d --no-deps node api
   devshard-postgres`. It publishes chain gRPC/RPC, the node manager and
   PostgreSQL on the private interface only. Firewall those ports from the
   public network.
4. After the remote replicas pass the database check described below,
   admit them using the fleet commands below.

On each **remote machine**:

1. Clone the same release, copy `config.env` from the network node, and set
   `NETWORK_NODE_PRIVATE_IP` and `VERSIOND_BIND_IP` (this machine's private
   address, the one written in the endpoint file).
2. Copy `.inference/keyring-file` from the network node into `./.inference`;
   the same `KEY_NAME` and `KEYRING_PASSWORD` apply.
3. `docker compose -f docker-compose.versiond-remote.yml up -d --wait`.
4. Install `psql` (the PostgreSQL client) and `jq` on this host. Copy
   `pool-postgres.env.template` to `pool-postgres.env`, set its permissions
   to `600`, and fill it with the existing pool's known working PostgreSQL
   connection settings. Obtain these independently of the replica being
   checked; certificate paths refer to files on this host. Then run:

   ```bash
   ./update-devshard.sh --check-storage --reference-env ./pool-postgres.env
   ```

   Continue only when it prints `Storage check passed` and exits with code 0.
   It checks the running `versiond` container without requiring the network
   node's Compose services. For other container names, supply `--container NAME`
   (repeat for several). Each HA devshardd writes a control value through its
   own database connection, and the checker reads it from the reference
   database. Run checks one host at a time, with no updater running elsewhere.
5. Admit the checked replicas on the network node. For a new fleet, run
   `./versiond-router-fleet.sh apply`. Changing an existing fleet's endpoint
   list requires a maintenance window:

   ```bash
   VERSIOND_ROUTER_ALLOW_MAINTENANCE_OUTAGE=true \
     ./versiond-router-fleet.sh maintenance-rollout
   ./versiond-router-fleet.sh verify-admission
   ```

Update remote hosts one at a time. Stop the remote service with
`docker compose -f docker-compose.versiond-remote.yml stop versiond` and let
it finish draining. Remove its entry from the endpoint file and apply that
membership change using the maintenance procedure above before starting
its replacement. On the remote host, run
`docker compose -f docker-compose.versiond-remote.yml pull`, then
`docker compose -f docker-compose.versiond-remote.yml up -d --wait`.
Repeat the database check before restoring its entry through the same
membership maintenance procedure.
Versions in `VERSIOND_NON_HA_VERSIONS` stay on `VERSIOND_LEGACY_HOST` (the
network node's `versiond` by default) because their state is local SQLite.

The endpoint file wins over DNS. A pre-HAProxy `VERSIOND_HOSTS` value in
`config.env` is still honoured by the fleet, but prefer the file. Each router
generation keeps its own copy of the list; editing the file does not change
running membership. Ordinary `apply` refuses a changed membership contract.
The maintenance procedure drains the old fleet before admitting the new
list, so active slots agree on session placement. A session assigned to
another versiond recovers its state from shared PostgreSQL. The shared
PostgreSQL remains a single host-local process in this layout; multi-host
production needs a managed PostgreSQL with synchronous durability.

## Validation

- `shellcheck deploy/join/deployment-lock.sh deploy/join/update-devshard.sh deploy/join/update-devshard_test.sh`
- `deploy/join/update-devshard_test.sh` (command sequence for both topologies)
- `deploy/join/versiond-compose-config_test.sh` (Compose contract, endpoint overlay)
- `make -C versiond-router test-render` (includes the endpoint list rendering)
- `make -C versiond-router test-fleet` (real Docker fleet rollout)
- `deploy/join/devshard-postgres-upgrade_test.sh` (real v4 volume migration)

### Warm-cutover test coverage

The warm cutover is pinned at three layers:

- **Unit — `devshardd` body shape** (`devshard/cmd/devshardd/lifecycle_test.go`):
  `/ready` returns 200 while recovery is still draining and flips the body's
  `recovery_complete` to `true` only after `WaitRecoveryRepairs` returns;
  draining stays 503.
- **Unit — `versiond` wait + bail-outs**
  (`versioned/internal/process/manager_recovery_wait_test.go`): the overlap
  wait returns on `recovery_complete: true`; skips on an absent field (pre-v5
  child) and on a legacy child with no admin listener; publishes immediately on
  old-child death; aborts on `hostDraining`, context cancel, and
  `VERSIOND_RECOVERY_TIMEOUT` (old keeps serving). A pin test asserts
  `watchChildReadiness` never reads the body, so recovery never evicts the host
  from the HAProxy pool. Two guard tests cover the quiet failure modes: an
  unset `RecoveryTimeout` must normalize to 30m (a zero value would make
  `context.WithTimeout` expire instantly and abort *every* overlap swap), and a
  non-postgres pair must stay overlap-ineligible so the stop/start branch never
  reaches the wait.
- **Integration — testenv boot tests**
  (`devshard/testenv/citest/versiond_warm_cutover_test.go`,
  `make citest-versiond-warm-cutover`): `TestVersiondWarmCutoverBoot` pins the
  status-vs-body split end to end — the public `/healthz` is 200 with
  `VERSIOND_RECOVERY_TIMEOUT` configured and a chat round-trip serves after
  boot.   `TestVersiondWarmCutoverOverlapWaitsThenServes` pins the swap half — a
  SHA flip drives the new sha to `running` on the target host (which only
  happens after the warm wait returns and `downloadAndSwap` publishes; a
  timed-out wait would abort the swap and the new sha would never run), new
  traffic serves, and the old child retires. The admin `/ready` body is loopback inside the versiond container on
  a dynamic port, so the body field and bail-outs are unit-pinned; the testenv
  suite pins the end-to-end effect (the wait returns, the swap completes). See
  [`testenv/docs/scenarios.md`](../testenv/docs/scenarios.md) §"Versiond warm
  cutover".

## Known limits

- The public `proxy-router` is one process on one host. A provider LB, VIP or
  Kubernetes Service above several hosts is later work.
- The bundled PostgreSQL is not database HA.
- Removing a governance version from the routers requires a maintenance
  procedure; runtime projection is additions-only.
- Multi-host is manual in this release: no remote execution, no coordinator.
