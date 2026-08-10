# versiond-router

HAProxy in front of N `versiond` instances. Multiple identical instances run as
an independent fleet behind `proxy-router`; each has two jobs:

1. **Stickiness** — every request for one session lands on the same `versiond`,
   so the instance holding that session's hot state keeps serving it.
2. **Membership and health** — a `versiond` that is starting, draining, or gone
   must not receive new work, and must start receiving it again on its own once
   it is healthy.

There is no shared router database or peer control plane. Membership comes from
DNS, health from active checks, and protocol names from dapi's existing
governance `/versions` feed. **Adding, removing, or replacing a `versiond`, and
approving a new protocol name, require no router reload.**

Router replicas do not coordinate. Given the same governance catalog, bootstrap
declarations, legacy pins, DNS pool and healthy addresses, each builds the same
consistent-hash placement. The top distributor may therefore choose any
route-ready replica.

---

## How it decides where to send a request

```text
request → normalise path → pick backend by version → pick server by escrow id
```

| Backend | Servers | Used for |
| --- | --- | --- |
| `versiond_pool_<v>` / `versiond_dynamic_<n>` | every host that passes the per-version readiness sequence below | bootstrap versions from `VERSIOND_VERSIONS` and names learned from governance |
| `versiond_legacy_<v>` | the single `VERSIOND_LEGACY_HOST` when it passes the same sequence | one per version in `VERSIOND_NON_HA_VERSIONS`, which owns pre-HA SQLite data on that host |
| `versiond_ha_pool` | every host that passes the coarse readiness sequence | non-version paths such as `/healthz`, and every version when no version is declared at all |

A version absent from both the bootstrap set and the current governance catalog
is **refused** with `503`, rather than being routed to a host that may not run it.
See [Version catalog](#version-catalog).

Every backend is rendered from `pool-backend.cfg.template`, so the routing
policy cannot drift between them. HA backends use the DNS pool; each legacy
backend uses one slot pointed at the explicit SQLite owner.

Within `versiond_ha_pool` the server is chosen by **consistent hash** of the
escrow id taken from the path (`/{version}/sessions/{escrow}/...`); other paths
hash by the path itself so non-session traffic still spreads. Consistent hashing
means adding or removing one host re-homes roughly `1/N` of sessions instead of
reshuffling all of them.

The ring is keyed on each server's **address** (`hash-key addr`), not on the
`server-template` slot it happens to occupy. That distinction is load-bearing:
DNS hands the router its answers in no particular order, so with the default
slot-id key a plain router restart could put the same hosts in different slots
and re-home every session at once. Keyed on the address, the ring depends on who
is in the pool rather than on the order they were discovered in. A host that is
recreated with a new IP does move on the ring — that is an ordinary membership
change, bounded to its own share of sessions.

The hash key is always derived from the URL and never taken from a request
header. It selects the upstream, so a client-supplied key would let a caller pin
itself to a host of its choosing.

An optional `/devshard/` prefix (present when genesis-proxy is not in front) is
normalised away before routing and stripped before forwarding.

## Membership: DNS

`VERSIOND_POOL_HOST` is one DNS name that resolves to **every** instance in the
pool. Under Docker Compose that is a network alias:

```yaml
x-versiond: &versiond
  networks:
    default:
      aliases:
        - versiond-pool
```

Both `versiond` containers answer to `versiond-pool`, so the name has one A
record per running instance. HAProxy re-resolves it every second
(`resolvers docker`) and fills a fixed set of `server-template` slots. Start a
third instance with the same alias and it appears in the pool within a couple of
seconds; stop one and its slot empties.

`VERSIOND_ROUTER_POOL_SLOTS` (default 64) caps how many instances can be in the
pool at once. Unused slots cost nothing; they exist because HAProxy allocates
server slots at startup.

## Health: one question per version

A pool-wide "are you healthy" cannot say that a host is missing *one* of several
versions. So the router asks about the version it is about to route to. Each
bootstrap or governance version gets its own backend over the HA hosts, and each
version in `VERSIOND_NON_HA_VERSIONS` gets its own backend over the one legacy
host. Each check performs two requests:

1. `GET /readyz?version=<v>` must return `200`, or `404` from a pre-v5
   `versiond` that does not implement this capability.
2. `GET /<v>/healthz` must return `200`, proving that the actual route exists.

The coarse backend uses the same sequence with `/readyz` and `/healthz`.
`503` from a v5 readiness endpoint is always a failed check, so a starting or
draining v5 host still leaves the pool before admission closes. The `404`
fallback exists only to keep a restored v4 image routable during the v5 cutover;
it is never sufficient without the successful route-health request.

A host that cannot run `v5` — its download failed, its child will not start —
answers `503` for `?version=v5` and `200` for `?version=v4`. It leaves `v5`'s
backend and keeps serving `v4`. This also applies to the legacy owner: a failed
new archive cannot hide its healthy pinned versions. Nothing else changes: no
eviction, no reload.

That also makes rolling out a version safe once it is learned. Until a host has
`v6` running it is not in `v6`'s pool, so `v6` traffic goes only to hosts that can
serve it, while `v4` and `v5` carry on untouched. Compare the alternative of one
host-wide readiness flag: the moment governance approves `v6`, *every* host would
be missing it at once, and gating on that would empty the whole pool.

Every second HAProxy runs the complete sequence against each host.

## Version catalog

`VERSIOND_VERSIONS` is a **bootstrap floor**, not a day-2 allowlist. It renders
the versions that must remain routable if dapi is temporarily unavailable while
a router starts. In normal operation every router also polls the same read-only
`GET /versions` governance snapshot that `versiond` already consumes.

HAProxy cannot create a backend at runtime, so the image pre-renders inert
`versiond_dynamic_<n>` backends. For each previously unseen governance name the
local reconciler:

1. assigns one free slot to the percent-encoded name;
2. enables its per-version health checks;
3. only then publishes the request-map entry.

Until step 3, the name receives `503`. After publication, a host is still not
selectable until both `/readyz?version=<v>` and `/<v>/healthz` pass. This keeps
the failure explicit and avoids sending a POST to a hash-selected host that does
not run the version. Existing version routes are untouched throughout.

Consequently, a normal new governance name requires **no `config.env` edit, no
router rollout, and no hoster action**. Both inner routers and `proxy-router`
learn it independently. A same-name SHA update does not even consume a new slot;
it remains the existing `version -> backend` mapping and is handled by
`versiond`'s child rolling update.

Governance approval is itself what makes the name appear in `/versions`, so this
feed cannot prove readiness before approval. Network activation automation must
approve/install first, then wait before directing new sessions to that version:

```bash
cd deploy/join
source ./config.env
./versiond-router-fleet.sh wait-version v6
```

The gate succeeds only when every configured router slot has learned `v6`, at
least `VERSIOND_ROUTER_MIN_READY` slots can serve it, and the active parent
`proxy-router` admits the same reserve. Network-wide rollout automation should
aggregate this per-host result. A future pre-approval gate would require a
separate signed staged-version feed; the approved-version endpoint cannot expose
information governance has not published yet.

Catalog additions are monotonic for one router process. Every fully projected
snapshot is also committed atomically to the slot's persistent state volume.
On replacement, a fresh snapshot is validated and rendered before HAProxy
starts, so names learned after the image was built do not disappear merely
because dapi is temporarily unavailable. The default maximum age is 24 hours;
a stale, corrupt, or future-dated snapshot is ignored and startup falls back to
the static bootstrap floor. Cached non-bootstrap names are restored into the
same bounded `versiond_dynamic_<n>` namespace; restarting a router does not
replenish consumed capacity. Startup fails loudly if a fresh cache needs more
dynamic slots than the configured capacity. Removing a name makes its child and health capacity
disappear immediately; the runtime slot is retained until replacement, while
the next valid cache snapshot no longer contains it.

Catalog URL, poll interval, fetch timeout, and capacity are validated before
HAProxy starts. Transient fetch failures preserve the last admitted map and are
logged once per failure episode. If the reconciler itself exits unexpectedly,
the entrypoint restarts it without interrupting established proxy connections.
`catalog-status --state` exposes the reconciler projection state, including an
explicit `capacity-exhausted` condition, from the persistent state directory.

`VERSIOND_ROUTER_VERSION_CAPACITY` bounds additions between router software
rollouts (default 32). Exhaustion is logged and the unassigned name remains 503;
increase the capacity and use the ordinary fleet rollout before the bound is
reached.

`VERSIOND_LEGACY_HOST`, `VERSIOND_NON_HA_VERSIONS`, `VERSIOND_POOL_HOST`, and
the router backend network define the placement function itself. Mixing old and
new values would let two routers place the same escrow on different hosts. Ordinary
`rollout` therefore refuses such a change. Schedule a devshard maintenance
window, set `VERSIOND_ROUTER_ALLOW_MAINTENANCE_OUTAGE=true` only for the command,
and use `maintenance-rollout`. It soft-stops every old slot before exposing any
new-placement slot. If the candidate fleet fails its old live-route contract,
the command drains it and recreates every previous slot from its exact image and
captured environment.

Leaving `VERSIOND_VERSIONS` empty is safe when
`VERSIOND_ROUTING_CATALOG_URL` is configured: the router starts with no version
routes and learns them from governance. Coarse mode exists only when both sources
are empty. An HA deployment refuses that mode unless
`VERSIOND_ROUTER_ALLOW_COARSE_READINESS=1`, because a host whose `v5` child went
unready would otherwise keep receiving `v5` traffic while `v4` is healthy.
Versionless endpoints — `/healthz`, `/readyz`, `/metrics`, `/stats`, and the
session observability routes — always use the host-level pool.

Declared names are taken as governance wrote them. The one limit is that a name
must be able to appear literally in a path segment: `/`, `?`, `#`, `%` and
whitespace are refused at startup, because the request path would then not match
the name at all. See [Configuration](#configuration) for how the three uses of a
name are derived.

For `/readyz?version=<v>`, `versiond` answers `200` when it is accepting traffic
and has a running child serving exactly that version — it reads the same route
table the proxy uses, so the answer cannot disagree with what a request would
actually get. For the unqualified `/readyz`, it answers `200` when it is
accepting traffic **and** has a child that still reports itself ready, having run
its full desired set at least once — a child that lost its chain subscription is
running but not ready, and takes the host out of this pool with it when it is the
only one. It answers `503` when it is
starting, when it has no usable child, and — importantly — for a few seconds
*before* it stops accepting work at shutdown.

It does **not** answer `503` merely because its last reconcile failed. Every host
reads the same oracle, so that failure arrives everywhere at once; treating it as
unreadiness would empty the pool over a control-plane hiccup while every child is
still serving. That window
(`VERSIOND_DRAIN_ANNOUNCE`, default `5s`) is what makes a graceful stop
invisible to clients: the router sees the failing check and stops sending new
requests while the host is still serving everything it already accepted.

| Setting | Value | Why |
| --- | --- | --- |
| `inter 1s` | check every second | the announce window is 5s, so several checks fit inside it |
| `fall 1` | one failed check removes the host | it announced its own drain; do not wait for a second opinion |
| `rise 2` | two passing checks restore it | do not flap a host back in on one lucky probe |

A host that has never converged reports `503` on the host-level check until it
has run its full desired set. After that the latch holds: downloading a new
archive does not retract readiness, and a version whose child is restarting drops
out of *that version's* pool alone while the rest keep serving.

## Failure handling

```haproxy
retry-on conn-failure empty-response 502
retries 2
option redispatch
http-request disable-l7-retry if !METH_GET !METH_HEAD !METH_OPTIONS
```

Retries cover the cases where the request provably did not run: the connection
failed, the peer closed without answering, or an upstream proxy answered `502`.

**`503` is never retried.** It is the answer a draining host and the HA storage
guard give, and retrying it would hit the same condition on the next host.

**Non-idempotent requests are never replayed.** `disable-l7-retry` leaves
connection-level redispatch in place for `POST`, but forbids re-sending a
request that may already have been executed — an inference must not run twice.

`http-reuse safe` never reuses a pooled connection for a request that could not
be replayed if the reuse raced with the peer closing it.

## Streaming

Inference responses are long-lived SSE streams. HAProxy does not buffer them.
SSE remains a normal HTX response, so `timeout client` and `timeout server` are
its inactivity limits: the stream can live longer than the configured value as
long as data continues to flow within that interval.

| Setting | Default | Meaning |
| --- | --- | --- |
| `VERSIOND_ROUTER_STREAM_IDLE_SECONDS` | `1200` | `timeout client` / `timeout server`: idle time allowed on a stream |
| `VERSIOND_ROUTER_TUNNEL_TIMEOUT_SECONDS` | `86400` | idle timeout after HTTP Upgrade/CONNECT; does not apply to SSE |
| `VERSIOND_ROUTER_CONNECT_TIMEOUT_SECONDS` | `2` | connect to an upstream, and header read timeout |

The SSE and tunnel settings control different HAProxy modes and are independent.
Reducing `VERSIOND_ROUTER_STREAM_IDLE_SECONDS` reduces the maximum quiet period
inside an SSE response; a larger tunnel timeout does not override it.

## The `Devshard-Ha` header

The router strips every client-supplied `Devshard-Ha` value, then stamps
`Devshard-Ha: true` on HA-pool requests when either the deployment declares
`GONKA_HA` or more than one server is currently usable in the backend selected
for that request. A per-version pool can therefore fail closed even while the
coarse host-level pool sees only one ready server.
`devshardd` uses it to refuse work it cannot safely serve: if this child's
storage is local (SQLite), a sibling could be serving the same escrow, so it
answers `503` rather than fork the session's state.

Every `versiond_legacy_<v>` backend strips the header: each has exactly one
server by definition, so no sibling can exist. Responses keep the stable
`X-Versiond-Backend: versiond_legacy` diagnostic label; the internal suffix is
only how HAProxy keeps the health results separate.

`GONKA_HA` describes the deployment, so the HA overlay sets it for the router
and every versiond host. It is the authoritative latch: it keeps the guard on
when siblings are temporarily unavailable. The runtime server count is a
fail-closed fallback for an accidentally scaled pool, not a substitute for the
deployment declaration.

## Looking at the pool

Fleet status is inspected with
`deploy/join/versiond-router-fleet.sh status`. To inspect one router's measured
versiond pool, run its internal `pool-status` formatter with `docker exec`. Its
output looks like this:

```text
versiond_pool_v4
  versiond1  172.30.0.10  UP
  versiond2  172.30.0.11  DOWN
  1 server(s) taking traffic
versiond_ha_pool
  versiond1  172.30.0.10  UP
  versiond2  172.30.0.11  UP
  2 server(s) taking traffic
```

One host is a separate server in every applicable backend, with its own health,
so the same host can be serving `v4` and out of `v5`. Pinned versions appear as
internal `versiond_legacy_<v>` backends, although their response header remains
`versiond_legacy`. This is the router's whole state, and it is read-only — a
formatter over the HAProxy Runtime API
(`/var/run/haproxy/haproxy.sock`, operator level), kept off PATH because it is an internal diagnostic
whose output the acceptance-test harness parses, not an operator CLI.

**There is no manual drain.** There was, and it was wrong: HAProxy identifies a
server by its slot in a `server-template`, and slots are reused. A drained host
that leaves DNS frees its slot, the next host to arrive lands in it and inherits
the drain — kept out of rotation with nothing to show why. Admin state belongs to
the identity of a process; the router only ever knows an address DNS lent it.

To take a host out of rotation, follow the canonical [versiond host evacuation
runbook](../devshard/docs/versiond-host-evacuation.md). A temporary stop is
graceful by construction: versiond reports unready for
`VERSIOND_DRAIN_ANNOUNCE` while it keeps serving, so the router removes it before
it stops accepting. Permanent decommission is different: it persists the new
desired replica count and removes the container so `restart: always` cannot add
the host back later.

The router fleet itself has a separate lifecycle because every slot is an
independent Compose project. `enable-router-ha.sh` invokes idempotent `apply` on
normal upgrades: an absent fleet is bootstrapped, an unchanged slot is left
alone, and a changed image or Compose config is rolled with the ready reserve.
A main-project `docker compose pull/up` cannot update containers it does not
own.

For whole-node maintenance, `versiond-router-fleet.sh stop-all --maintenance`
soft-stops all fleet-owned containers concurrently. After the main Compose
project is down, `versiond-router-fleet.sh down --maintenance` removes expected,
duplicate, and orphan fleet containers plus current or renamed fleet-owned
networks. It rejects the operation before mutation while a non-fleet endpoint is
still attached, and resources belonging to another fleet ID are never selected.
Run `prepare-networks` before the next main-project `up`, then run
`enable-router-ha.sh` after versiond is available.

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `VERSIOND_POOL_HOST` | `versiond-pool` | DNS name resolving to every pool member |
| `VERSIOND_PORT` | `8080` | upstream port |
| `VERSIOND_LEGACY_HOST` | *(none)* | single host owning pre-HA SQLite data dirs. **Required** whenever `VERSIOND_NON_HA_VERSIONS` is non-empty — the router refuses to start otherwise, because the owner of one host's data cannot default to a name that resolves to the whole pool. Unused (and may be omitted) when no version is pinned |
| `VERSIOND_NON_HA_VERSIONS` | *(empty)* | version path segments pinned to the legacy host, whitespace and/or comma separated |
| `VERSIOND_VERSIONS` | *(empty)* | static bootstrap floor, whitespace and/or comma separated; governance additions are learned without changing it |
| `VERSIOND_ROUTING_CATALOG_URL` | *(empty)* | read-only dapi `GET /versions` endpoint. The join fleet uses `http://versiond-routing-oracle:9100/versions` |
| `VERSIOND_ROUTING_CATALOG_POLL_SECONDS` | `5` | interval for discovering governance names |
| `VERSIOND_ROUTING_CATALOG_FETCH_TIMEOUT_SECONDS` | `3` | timeout for one catalog request |
| `VERSIOND_ROUTING_CATALOG_CACHE_MAX_AGE_SECONDS` | `86400` | maximum age of the persistent last-known-good catalog used at process start |
| `VERSIOND_ROUTING_ACTIVATION_TIMEOUT_SECONDS` | `2100` | fleet `wait-version` deadline; a host workflow setting, not a router-container setting |
| `VERSIOND_ROUTER_VERSION_CAPACITY` | `32` | inert per-version backends reserved for names added after process start |
| `VERSIOND_ROUTER_ALLOW_COARSE_READINESS` | *(unset)* | allow an HA deployment with neither bootstrap versions nor a catalog, accepting that a host with one unready version keeps receiving its traffic. Same boolean grammar |
| `GONKA_HA` | *(unset)* | authoritative HA deployment latch; stamps `Devshard-Ha` even while only one pool member is usable. With it off, the router still stamps the header whenever more than one host is usable in the selected backend. Booleans share one grammar with devshardd: `1/true/yes` on, empty/`0/false/no` off, anything else refuses to start |
| `VERSIOND_ROUTER_POOL_SLOTS` | `64` | maximum simultaneous pool members; the resolver accepts DNS payloads up to 8192 bytes so the default pool fits in one answer |
| `VERSIOND_ROUTER_MAX_CONNECTIONS` | `4096` | frontend `maxconn` |
| `VERSIOND_ROUTER_MAX_BODY_BYTES` | `10485760` | early 413 for an advertised `Content-Length` above this value. `devshardd` independently caps actual bytes at 10 MiB, including chunked bodies and direct-router traffic |
| `VERSIOND_ROUTER_CONNECT_TIMEOUT_SECONDS` | `2` | connect and header timeouts |
| `VERSIOND_ROUTER_STREAM_IDLE_SECONDS` | `1200` | client/server idle timeout |
| `VERSIOND_ROUTER_TUNNEL_TIMEOUT_SECONDS` | `86400` | upgraded/CONNECT idle timeout; independent of SSE |
| `VERSIOND_ROUTER_ADMIN_PORT` | `8404` | private route-readiness listener consumed by `proxy-router` and fleet health checks |
| `VERSIOND_ROUTER_STOP_GRACE_PERIOD` | `10s` | routine Compose cleanup ceiling; fleet rollout supplies its explicit longer drain timeout |
| `VERSIOND_ROUTER_ALLOW_MAINTENANCE_OUTAGE` | `false` | one-command acknowledgement required by `maintenance-rollout`; do not persist `true` |

The entrypoint validates every numeric setting, renders `haproxy.cfg` and
`non_ha.map`, runs `haproxy -c` on the result, and only then execs HAProxy. A bad
value fails the container immediately instead of producing a subtly wrong
config. Version pinning is startup configuration because each pin needs its own
health-checked backend; change `VERSIOND_NON_HA_VERSIONS` and replace the router
container to change it.

The shipped join overlay distinguishes unset from explicitly empty values.
Unset `VERSIOND_NON_HA_VERSIONS` and `VERSIOND_VERSIONS` receive the deployment
defaults; an empty export reaches this entrypoint unchanged. Coarse mode needs
empty `VERSIOND_VERSIONS`, empty `VERSIOND_ROUTING_CATALOG_URL`, and
`VERSIOND_ROUTER_ALLOW_COARSE_READINESS=true`.

Declared names are used three ways, and each is derived to suit itself: the map
key is the name exactly as governance wrote it, because it is matched against the
path segment; the health-check query is percent-encoded, because `+` in a query
decodes to a space and the check would ask about a version that does not exist;
and the backend identifier gets a hash appended when the name is not already a
valid one. A name that cannot appear literally in a path segment — one containing
`/`, `?`, `#`, `%` or whitespace — is refused at startup, because the request path
would then not match the name at all.

## Observability

| Endpoint | Where | Notes |
| --- | --- | --- |
| `/metrics` | `127.0.0.1:8405` inside the container | Prometheus exporter; loopback only, never published |
| `/livez` | private admin port `8404` | HAProxy process is serving the admin listener |
| `/readyz` | private admin port `8404` | coarse backend has capacity |
| `/readyz?version=<v>` | private admin port `8404` | the matching version backend has capacity |
| Runtime API | `/var/run/haproxy/haproxy.sock` | operator socket, no TCP bind; diagnostics only |
| Catalog writer | `/var/run/haproxy/reconciler.sock` | mode-600 admin socket shared by HAProxy and its in-container reconciler trust domain |
| `X-Upstream-Addr` | response header | which instance served the request |
| `X-Versiond-Backend` | response header | HA backend name, or the stable `versiond_legacy` label for any pinned version |

Neither the metrics endpoint nor the admin socket is reachable from outside the
container, and the container runs as the unprivileged `haproxy` user with all
Linux capabilities dropped and `no-new-privileges`. Root is used only at build
time to install `jq`/`socat` and hand over writable directories. Socket mode
`0600` protects the container boundary, not one process from another: HAProxy,
the entrypoint, and the reconciler intentionally share one Unix user and form
one trust domain. Strong per-process isolation would require a separate sidecar
or a narrow privileged broker. Scrape metrics with a sidecar or
`docker compose exec`.

## Tests

```console
$ make test-render
test-hash-ring: ok
test-render: ok
$ make test-fleet
versiond-router-fleet_test: ok
```

`test-render` renders the mixed (legacy pinning) and all-HA shapes, asserts on
the result, checks that invalid settings are rejected, and validates each
rendered config with the real HAProxy from the image the router ships.

`test-version-routing` proves the headline property against the real image: two
upstreams that disagree about which versions they serve, and a host that lacks
one version must receive none of its traffic while still receiving the rest. It
also gives the legacy owner failing generic readiness and proves that a healthy
pinned version remains routable while an unhealthy pinned version stays down.

`test-hash-ring` proves the property that cannot be seen by reading the config:
it takes the hashing directives out of the rendered file, puts the same four
addresses into the server slots in three different orders, and requires every
escrow to reach the same address each time. Remove `hash-key addr` from the
template and it fails, naming the sessions that moved.

Both require Docker.

`test-fleet` starts three router slots as separate Compose projects, proves the
main stack cannot recreate them, proves idempotent `apply` leaves current slots
untouched, drains one while an accepted stream completes through it and new
requests use peers, and enforces generic and per-version reserve. It also proves
mixed placement is rejected, full-fleet placement changes never overlap
generations, failed maintenance restores the exact previous image/environment/
routes, and acknowledged full teardown removes duplicate/orphan fleet resources
without crossing fleet ownership.

End-to-end routing, draining and host evacuation are covered by
`devshard/testenv`: `make -C devshard/testenv citest-versiond-host-evacuation`.

## Related docs

| Doc | Use |
| --- | --- |
| [versiond-host-evacuation.md](../devshard/docs/versiond-host-evacuation.md) | Whole-host evacuation, replacement, addition, decommission |
| [rolling-update.md](../devshard/docs/rolling-update.md) | Same-name SHA blue/green inside one versiond |
| [high-availability-architecture.md](../devshard/docs/high-availability-architecture.md) | Where the router sits in the stack |
