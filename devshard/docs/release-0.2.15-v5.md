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
service passes its healthcheck. The script stops at the first failure and
prints the failing command; fix the cause and run it again. Rerunning is safe:
Compose leaves services whose image and configuration did not change alone.

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
`PGUSER` and `PGPASSWORD` on every versiond service. Keep the same ordered
`-f` list, or `COMPOSE_FILE`, for every later command.

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
`config.env` is still honoured by the fleet, but prefer the file. The shared
PostgreSQL remains a single host-local process in this layout; multi-host
production needs a managed PostgreSQL with synchronous durability.

## Validation

- `shellcheck deploy/join/update-devshard.sh deploy/join/update-devshard_test.sh`
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
