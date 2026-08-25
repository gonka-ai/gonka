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
| `versiond_pool_<v>` | every host that passes the per-version readiness sequence below | each version listed in `VERSIOND_VERSIONS` |
| `versiond_legacy_<v>` | the single `VERSIOND_LEGACY_HOST` when it passes the same sequence | one per version in `VERSIOND_NON_HA_VERSIONS`, which owns pre-HA SQLite data on that host |
| `versiond_ha_pool` | every host that passes the coarse readiness sequence | non-version paths such as `/healthz`, and every version when no version is declared at all |

A version that is declared nowhere is **refused** with `503` naming the setting
that fixes it, rather than being routed to a host that may not run it. See
[Declaring versions](#declaring-versions).

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
version in `VERSIOND_VERSIONS` gets its own backend over the HA hosts, and each
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

That also makes rolling out a version safe once it is declared. Until a host has
`v6` running it is not in `v6`'s pool, so `v6` traffic goes only to hosts that can
serve it, while `v4` and `v5` carry on untouched. Compare the alternative of one
host-wide readiness flag: the moment governance approves `v6`, *every* host would
be missing it at once, and gating on that would empty the whole pool.

Every second HAProxy runs the complete sequence against each host.

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

There is no in-place way to declare a version: the backends are rendered from the
environment when the container starts, so the environment has to change and the
container has to be replaced. A deployment that requires seamless upgrades of
the router itself must run redundant router instances behind a stable frontend
and replace them one at a time.

**So declare the versions you expect before you need them.** A pool for a version
nobody runs yet has no healthy members and costs nothing but its health checks;
requests for it are refused either way, because no host can serve it. Declaring
`v4 v5 v6 v7` up front turns a governance approval into step 2 alone.

Doing it the other way round means `v6` requests are refused until step 1 lands.

Leaving `VERSIOND_VERSIONS` empty disables the whole mechanism: every version
uses the host-level pool, exactly as before per-version pools existed. In an HA
deployment (`GONKA_HA` set) that is refused at startup — the host-level check
answers "can this host serve *anything*", so a host whose `v5` child went unready
would keep receiving `v5` traffic as long as `v4` is healthy, failing
hash-dependently. A stack that genuinely cannot declare its versions up front
(test environments minting names dynamically) opts in to exactly that with
`VERSIOND_ROUTER_ALLOW_COARSE_READINESS=1`. Versionless endpoints — `/healthz`,
`/readyz`, `/metrics`, `/stats`, and the session observability routes — always
use the host-level pool and are never refused.

Declared names are taken as governance wrote them. The one limit is that a name
must be able to appear literally in a path segment: `/`, `?`, `#`, `%` and
whitespace are refused at startup, because the request path would then not match
the name at all. See [Configuration](#configuration) for how the three uses of a
name are derived.

For `/readyz?version=<v>`, a lifecycle-aware `versiond` answers `200` only while
it accepts traffic and has a running child serving exactly that version. The
unqualified `/readyz` is the corresponding host-level answer. Returning `503`
withdraws the host from the affected backend. A legacy process returns `404`,
so the second route-health request remains the compatibility contract until the
lifecycle-aware image has been rolled out.

| Setting | Value | Why |
| --- | --- | --- |
| `inter 1s` | check every second | membership changes are observed promptly |
| `fall 1` | one failed check removes the host | explicit unreadiness is authoritative |
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

The overlay also gives versiond the same `VERSIOND_NON_HA_VERSIONS` value as the
router. Before starting a child, versiond requires the Postgres storage contract
for every HA-routed version and refuses a legacy binary that cannot report its
storage mode. For a version pinned to the single legacy owner, versiond passes
`GONKA_HA=false` to that child to match the router backend that strips the HA
marker.

## Looking at the pool

The canonical runbook contains the full command for inspecting the pool. Its
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
(`/var/run/haproxy/haproxy.sock`), kept off PATH because it is an internal diagnostic
whose output the acceptance-test harness parses, not an operator CLI.

**There is no manual drain.** There was, and it was wrong: HAProxy identifies a
server by its slot in a `server-template`, and slots are reused. A drained host
that leaves DNS frees its slot, the next host to arrive lands in it and inherits
the drain — kept out of rotation with nothing to show why. Admin state belongs to
the identity of a process; the router only ever knows an address DNS lent it.

To take a host out of rotation, make its readiness endpoint fail and keep the
process serving long enough for the active check to observe that state. Legacy
`versiond` images do not implement that lifecycle contract; for them the router
can only react after route health fails or the process leaves DNS. The graceful
host lifecycle is intentionally delivered as a separate change.

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `VERSIOND_POOL_HOST` | `versiond-pool` | DNS name resolving to every pool member |
| `VERSIOND_PORT` | `8080` | upstream port |
| `VERSIOND_LEGACY_HOST` | *(none)* | single host owning pre-HA SQLite data dirs. **Required** whenever `VERSIOND_NON_HA_VERSIONS` is non-empty — the router refuses to start otherwise, because the owner of one host's data cannot default to a name that resolves to the whole pool. Unused (and may be omitted) when no version is pinned |
| `VERSIOND_NON_HA_VERSIONS` | *(empty)* | version path segments pinned to the legacy host, whitespace and/or comma separated |
| `VERSIOND_VERSIONS` | *(empty)* | versions to health-check individually, whitespace and/or comma separated. Empty = every version uses the host-level check (refused when `GONKA_HA` is set); non-empty = undeclared versions are refused |
| `VERSIOND_ROUTER_ALLOW_COARSE_READINESS` | *(unset)* | allow an HA deployment to run with no declared versions, accepting that a host with one unready version keeps receiving its traffic. Same boolean grammar |
| `GONKA_HA` | *(unset)* | authoritative HA deployment latch; stamps `Devshard-Ha` even while only one pool member is usable. With it off, the router still stamps the header whenever more than one host is usable in the selected backend. Booleans share one grammar with devshardd: `1/t/true/yes/on` on, empty/`0/f/false/no/off` off, anything else refuses to start |
| `VERSIOND_ROUTER_POOL_SLOTS` | `64` | maximum simultaneous pool members; the resolver accepts DNS payloads up to 8192 bytes so the default pool fits in one answer |
| `VERSIOND_ROUTER_MAX_CONNECTIONS` | `4096` | frontend `maxconn` |
| `VERSIOND_ROUTER_MAX_BODY_BYTES` | `10485760` | early 413 for an advertised `Content-Length` above this value. `devshardd` independently caps actual bytes at 10 MiB, including chunked bodies and direct-router traffic |
| `VERSIOND_ROUTER_CONNECT_TIMEOUT_SECONDS` | `2` | connect and header timeouts |
| `VERSIOND_ROUTER_STREAM_IDLE_SECONDS` | `1200` | client/server idle timeout |
| `VERSIOND_ROUTER_TUNNEL_TIMEOUT_SECONDS` | `86400` | upgraded/CONNECT idle timeout; independent of SSE |

The entrypoint validates every numeric setting, renders `haproxy.cfg` and
`non_ha.map`, runs `haproxy -c` on the result, and only then execs HAProxy. A bad
value fails the container immediately instead of producing a subtly wrong
config. Version pinning is startup configuration because each pin needs its own
health-checked backend; change `VERSIOND_NON_HA_VERSIONS` and replace the router
container to change it.

The shipped join overlay distinguishes unset from explicitly empty values.
Unset `VERSIOND_NON_HA_VERSIONS` and `VERSIOND_VERSIONS` receive the deployment
defaults; an empty export reaches this entrypoint unchanged. Coarse mode needs
both `VERSIOND_VERSIONS=""` and
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
| Runtime API | `/var/run/haproxy/haproxy.sock` | admin socket, no TCP bind |
| `X-Upstream-Addr` | response header | which instance served the request |
| `X-Versiond-Backend` | response header | HA backend name, or the stable `versiond_legacy` label for any pinned version |

Neither the metrics endpoint nor the admin socket is reachable from outside the
container, and the container runs as the unprivileged `haproxy` user from the
base image — root is used only at build time to install `socat` and hand over
the config and socket directories. Scrape metrics with a sidecar or
`docker compose exec`.

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
one version must receive none of its traffic while still receiving the rest. It
also gives the legacy owner failing generic readiness and proves that a healthy
pinned version remains routable while an unhealthy pinned version stays down.

`test-hash-ring` proves the property that cannot be seen by reading the config:
it takes the hashing directives out of the rendered file, puts the same four
addresses into the server slots in three different orders, and requires every
escrow to reach the same address each time. Remove `hash-key addr` from the
template and it fails, naming the sessions that moved.

Both require Docker.

## Related docs

| Doc | Use |
| --- | --- |
| [rolling-update.md](../devshard/docs/rolling-update.md) | Same-name SHA blue/green inside one versiond |
| [high-availability-architecture.md](../devshard/docs/high-availability-architecture.md) | Where the router sits in the stack |
