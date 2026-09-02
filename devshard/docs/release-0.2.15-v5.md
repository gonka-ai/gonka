# Release guide: `devshard-0.2.15-v5`

Operator-facing contract for the v5 deployment line.

Previous line: [release-0.2.14-v4.md](./release-0.2.14-v4.md).
Host evacuation: [versiond-host-evacuation.md](./versiond-host-evacuation.md).
Rolling updates: [rolling-update.md](./rolling-update.md).
Architecture: [high-availability-architecture.md](./high-availability-architecture.md).
Router: [versiond-router/README.md](../../versiond-router/README.md).

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
| `up -d --no-deps --wait proxy-policy2 proxy-policy`, then `proxy` | yes | yes |
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
already use: it reads the storage lineage through a running versiond
(`/internal/storage-identity`) and compares it with the identity stored in
the database the model names. A `PGHOST` that now points at another working
database is refused, because a rolling replacement would otherwise leave old
replicas writing to one history and new replicas to another. Set
`UPDATE_ACCEPT_DATABASE_CHANGE=true` only for an intended migration to a
restored copy.

Each step replaces one service and waits for its healthcheck. Before the
replacement, the image the service ran is kept under the Docker tag
`gonka-previous/<service>`; that tag lives in Docker's image store, so it
survives a killed run and can be used by hand
(`VERSIOND_IMAGE=gonka-previous/versiond docker compose up -d versiond`).
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
`2 * N * (P + 2) + R * 5` non-reserved connections, where `N` is the number of
HA devshard children a replica runs (one per HA version), `R` the number of
versiond replicas and `P` the `DEVSHARD_POSTGRES_POOL_MAX_CONNS` pool limit
(default 4). The doubled child term covers the draining predecessor a version
may keep during a rolling binary update; the per-replica term is versiond's
session-lookup pool plus one schema-initializer session. With two replicas,
three HA versions and the default pool that is 46 connections.

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
4. `./versiond-router-fleet.sh apply` rolls the routers onto the new list.

On each **remote machine**:

1. Clone the same release, copy `config.env` from the network node, and set
   `NETWORK_NODE_PRIVATE_IP` and `VERSIOND_BIND_IP` (this machine's private
   address, the one written in the endpoint file).
2. Copy `.inference/keyring-file` from the network node into `./.inference`;
   the same `KEY_NAME` and `KEYRING_PASSWORD` apply.
3. `docker compose -f docker-compose.versiond-remote.yml up -d --wait`.

Remote hosts are updated one at a time with
`docker compose -f docker-compose.versiond-remote.yml pull && ... up -d --wait`;
the routers withdraw a host while it fails `/readyz` and readmit it afterwards.
Versions in `VERSIOND_NON_HA_VERSIONS` stay on `VERSIOND_LEGACY_HOST` (the
network node's `versiond` by default) because their state is local SQLite.

The endpoint file wins over DNS. A pre-HAProxy `VERSIOND_HOSTS` value in
`config.env` is still honoured by the fleet, but prefer the file. Editing the
list rolls the router slots one at a time, so for the duration of that
rollout (seconds per slot) routers can hold different member lists, exactly as
they do while a container joins or leaves the DNS pool. An escrow that lands
on another versiond during that window recovers its state from the shared
PostgreSQL; that is the HA design, not an error. The shared
PostgreSQL remains a single host-local process in this layout; multi-host
production needs a managed PostgreSQL with synchronous durability.

## Validation

- `shellcheck deploy/join/deployment-lock.sh deploy/join/update-devshard.sh deploy/join/update-devshard_test.sh`
- `deploy/join/update-devshard_test.sh` (command sequence for both topologies)
- `deploy/join/versiond-compose-config_test.sh` (Compose contract, endpoint overlay)
- `make -C versiond-router test-render` (includes the endpoint list rendering)
- `make -C versiond-router test-fleet` (real Docker fleet rollout)
- `deploy/join/devshard-postgres-upgrade_test.sh` (real v4 volume migration)

## Known limits

- The public `proxy-router` is one process on one host. A provider LB, VIP or
  Kubernetes Service above several hosts is later work.
- The bundled PostgreSQL is not database HA.
- Removing a governance version from the routers requires a maintenance
  procedure; runtime projection is additions-only.
- Multi-host is manual in this release: no remote execution, no coordinator.
