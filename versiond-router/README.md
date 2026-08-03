# versiond-router

HAProxy in front of N `versiond` instances. It has two jobs:

1. **Stickiness** — every request for one session lands on the same `versiond`,
   so the instance holding that session's hot state keeps serving it.
2. **Membership and health** — a `versiond` that is starting, draining, or gone
   must not receive new work, and must start receiving it again on its own once
   it is healthy.

There is no router-side state and no control plane. Membership comes from DNS
and health from active health checks, so **adding, removing, or replacing a
`versiond` requires no router config change and no reload.**

---

## How it decides where to send a request

```text
request → normalise path → pick backend by version → pick server by escrow id
```

| Backend | Servers | Used for |
| --- | --- | --- |
| `versiond_pool_<v>` | every host that answers `/readyz?version=<v>` with 200 | each version listed in `VERSIOND_VERSIONS` |
| `versiond_legacy` | the single `VERSIOND_LEGACY_HOST` | versions listed in `VERSIOND_NON_HA_VERSIONS`, which own pre-HA SQLite data dirs on one host |
| `versiond_ha_pool` | every host that answers `/readyz` with 200 | non-version paths such as `/healthz`, and every version when no version is declared at all |

A version that is declared nowhere is **refused** with `503` naming the setting
that fixes it, rather than being routed to a host that may not run it. See
[Declaring versions](#declaring-versions).

All three are the same pool of hosts; what differs is the question their health
check asks. Every one of them is rendered from `pool-backend.cfg.template`, so
the routing policy cannot drift between them.

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
versions. So the router asks about the version it is about to route to: each
version in `VERSIOND_VERSIONS` gets its own backend over the same hosts, health
-checked with `GET /readyz?version=<v>`.

A host that cannot run `v5` — its download failed, its child will not start —
answers `503` for `?version=v5` and `200` for `?version=v4`. It leaves `v5`'s
ring and keeps serving `v4`. Nothing else changes: no eviction, no reload.

That also makes rolling out a version safe once it is declared. Until a host has
`v6` running it is not in `v6`'s pool, so `v6` traffic goes only to hosts that can
serve it, while `v4` and `v5` carry on untouched. Compare the alternative of one
host-wide readiness flag: the moment governance approves `v6`, *every* host would
be missing it at once, and gating on that would empty the whole pool.

Every second HAProxy asks each host its backend's question and expects `200`.

## Declaring versions

`VERSIOND_VERSIONS` is the list of versions this router can route. It is a real
operational duty, and the router will not paper over a gap in it: while any
version is declared, a request for an *undeclared* version is answered `503` here
rather than sent to a host that may not run it.

Refusing looks harsher than falling back, and it is deliberate. The fallback is
the coarse host-level check — the thing per-version pools exist to replace — so a
request would reach whichever host the hash picked and fail there with `404` if
that host does not have the version. That failure is partial, hash-dependent and
easy to mistake for flakiness. The refusal names its own cause:

```console
$ curl -i https://…/v6/sessions/abc/chat
HTTP/1.1 503 Service Unavailable
version v6 is not declared in VERSIOND_VERSIONS on this router
```

**Approving a new version is therefore two-phase**, in this order:

1. add the version to `VERSIOND_VERSIONS` and **replace** the router container —
   it gains a pool for `v6` with no healthy members, which changes nothing for
   `v4`;
2. approve `v6` in governance. Each host joins `v6`'s pool as it installs it.

```console
$ docker compose up -d --force-recreate versiond-router
```

There is no in-place way to declare a version: the backends are rendered from the
environment when the container starts, so the environment has to change and the
container has to be replaced. That replacement is not free — Compose does not
start the new container until the old one has gone, and with `stop_signal:
SIGUSR1` (HAProxy's soft stop, so carried streams finish rather than being cut)
the old one lingers until its longest stream ends.

**So declare the versions you expect before you need them.** A pool for a version
nobody runs yet has no healthy members and costs nothing but its health checks;
requests for it are refused either way, because no host can serve it. Declaring
`v4 v5 v6 v7` up front turns a governance approval into step 2 alone.

Doing it the other way round means `v6` requests are refused until step 1 lands.

Leaving `VERSIOND_VERSIONS` empty disables the whole mechanism: every version
uses the host-level pool, exactly as before per-version pools existed. Versionless
endpoints — `/healthz`, `/readyz`, `/metrics`, `/stats`, and the session
observability routes — always use it and are never refused.

Declared names must match `[A-Za-z0-9][A-Za-z0-9._-]*`. The same string is a
HAProxy backend name, a health-check query value and a map key matched against a
path segment, and a name outside that grammar cannot be all three
unambiguously — `+` alone decodes to a space in the query and would leave the
host permanently down. The router refuses such a name at startup rather than
route on a guess. The chain accepts a wider set today; narrowing it there is the
proper fix and is listed as a follow-up.

For `/readyz?version=<v>`, `versiond` answers `200` when it is accepting traffic
and has a running child serving exactly that version — it reads the same route
table the proxy uses, so the answer cannot disagree with what a request would
actually get. For the unqualified `/readyz`, it answers `200` when it is
accepting traffic **and** has a healthy child, having run its full desired set at
least once. It answers `503` when it is
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

A host that is up but still converging (downloading a new archive, restarting a
child) reports `503` and simply does not receive traffic until it is ready.

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

Inference responses are long-lived SSE streams. HAProxy does not buffer
responses, and the timeouts are set so the innermost hop is never the one that
cuts a stream:

| Setting | Default | Meaning |
| --- | --- | --- |
| `VERSIOND_ROUTER_STREAM_IDLE_SECONDS` | `1200` | `timeout client` / `timeout server`: idle time allowed on a stream |
| `VERSIOND_ROUTER_TUNNEL_TIMEOUT_SECONDS` | `86400` | `timeout tunnel`: total life of an established stream |
| `VERSIOND_ROUTER_CONNECT_TIMEOUT_SECONDS` | `2` | connect to an upstream, and header read timeout |

The tunnel timeout is deliberately far above the idle timeout: the outer Gonka
API proxy owns the client-facing limit, because it has the context to report a
useful error. The entrypoint refuses to start if the tunnel timeout is below the
idle timeout.

## The `Devshard-Ha` header

When `GONKA_HA` is set, the router stamps `Devshard-Ha: true` on requests going
to `versiond_ha_pool`, and strips any client-supplied value. `devshardd` uses it
to refuse work it cannot safely serve: if the deployment is HA but this child's
storage is local (SQLite), a sibling could be serving the same escrow, so it
answers `503` rather than fork the session's state.

`versiond_legacy` always strips the header: that backend has exactly one server
by definition, so no sibling can exist.

`GONKA_HA` describes the deployment, so it is set by the HA overlay itself, not
per host.

## Taking a host out by hand: `gonka-drain`

A graceful `versiond` stop needs nothing from the router. `gonka-drain` is for
the other direction — quiescing a host from outside, for maintenance that is not
a `versiond` shutdown:

```console
$ docker compose exec versiond-router gonka-drain status
SLOT       ADDRESS         STATE
versiond1  172.30.0.10     UP
versiond2  172.30.0.11     UP
2 server(s) taking traffic in versiond_ha_pool

$ docker compose exec versiond-router gonka-drain out versiond2
drained versiond_ha_pool/versiond2 (versiond2); 1 of 2 left serving

$ docker compose exec versiond-router gonka-drain in versiond2
returned versiond_ha_pool/versiond2 (versiond2) to rotation
```

A drained host keeps serving what it already accepted and receives no new work.
Hosts are addressed by container name or IP; slot names are an HAProxy
implementation detail and are resolved for you.

Server state in HAProxy belongs to a backend/server pair, and one host sits in
every backend it belongs to — the host-level pool, one per declared version, and
`versiond_legacy` if it owns the pre-HA data. `gonka-drain` therefore drains a
host from all of them. It snapshots each server's admin state before changing
anything and restores those exact states if one of the changes fails, so a host
is never left half out, and it holds a lock for the whole plan-and-apply so two
concurrent drains cannot each see a live peer and then leave none.

`gonka-drain out` refuses when the target is the last server *taking traffic* in
some backend, naming it. A host that is already down in a version's pool — it
does not run that version — does not keep that pool alive, so it can still be
drained from the pools where it does serve.

The legacy owner is one server in `versiond_legacy` by definition, so it cannot
be drained while any version is pinned there: emptying it would fail every
request for those versions. Stop that host only when it is genuinely being
evacuated. The guard
is advisory — `docker stop` bypasses it — and exists so that draining hosts one
by one cannot empty the pool by accident.

Drain state lives in HAProxy's memory only: a restarted router starts from a
clean pool and rebuilds its whole view from DNS and health checks within a
couple of seconds.

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `VERSIOND_POOL_HOST` | `versiond-pool` | DNS name resolving to every pool member |
| `VERSIOND_PORT` | `8080` | upstream port |
| `VERSIOND_LEGACY_HOST` | `VERSIOND_POOL_HOST` | single host owning pre-HA SQLite data dirs |
| `VERSIOND_NON_HA_VERSIONS` | *(empty)* | version path segments pinned to the legacy host, whitespace and/or comma separated |
| `VERSIOND_VERSIONS` | *(empty)* | versions to health-check individually, whitespace and/or comma separated. Empty = every version uses the host-level check; non-empty = undeclared versions are refused |
| `GONKA_HA` | *(unset)* | set by the HA overlay; stamps `Devshard-Ha` on pool traffic |
| `VERSIOND_ROUTER_POOL_SLOTS` | `64` | maximum simultaneous pool members |
| `VERSIOND_ROUTER_MAX_CONNECTIONS` | `4096` | frontend `maxconn` |
| `VERSIOND_ROUTER_CONNECT_TIMEOUT_SECONDS` | `2` | connect and header timeouts |
| `VERSIOND_ROUTER_STREAM_IDLE_SECONDS` | `1200` | client/server idle timeout |
| `VERSIOND_ROUTER_TUNNEL_TIMEOUT_SECONDS` | `86400` | established stream lifetime |

The entrypoint validates every numeric setting and the tunnel/idle relationship,
renders `haproxy.cfg` and `non_ha.map`, runs `haproxy -c` on the result, and only
then execs HAProxy. A bad value fails the container immediately instead of
producing a subtly wrong config.

Version pinning can also be changed at runtime through the Runtime API
(`add map` / `del map` on `non_ha.map`); such edits live in memory only, so
change `VERSIOND_NON_HA_VERSIONS` too if the change must survive a restart.

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
| Runtime API | `/var/run/haproxy.sock` | admin socket, no TCP bind |
| `X-Upstream-Addr` | response header | which instance served the request |
| `X-Versiond-Backend` | response header | `versiond_ha_pool` or `versiond_legacy` |

Neither the metrics endpoint nor the admin socket is reachable from outside the
container. Scrape metrics with a sidecar or `docker compose exec`.

## Tests

```console
$ make test-render
test-hash-ring: ok
test-render: ok
```

`test-render` renders the mixed (legacy pinning) and all-HA shapes, asserts on
the result, checks that invalid settings are rejected, and validates each
rendered config with the real HAProxy from the image the router ships.

`test-version-routing` proves the headline property against the real image: two
upstreams that disagree about which versions they serve, and a host that lacks
one version must receive none of its traffic while still receiving the rest.

`test-hash-ring` proves the property that cannot be seen by reading the config:
it takes the hashing directives out of the rendered file, puts the same four
addresses into the server slots in three different orders, and requires every
escrow to reach the same address each time. Remove `hash-key addr` from the
template and it fails, naming the sessions that moved.

Both require Docker.

End-to-end routing, draining and host evacuation are covered by
`devshard/testenv`: `make -C devshard/testenv citest-versiond-host-evacuation`.

## Related docs

| Doc | Use |
| --- | --- |
| [versiond-host-evacuation.md](../devshard/docs/versiond-host-evacuation.md) | Whole-host evacuation, replacement, addition, decommission |
| [rolling-update.md](../devshard/docs/rolling-update.md) | Same-name SHA blue/green inside one versiond |
| [high-availability-architecture.md](../devshard/docs/high-availability-architecture.md) | Where the router sits in the stack |
