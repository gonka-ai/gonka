# proxy-router

`proxy-router` is the host-local HAProxy at the edge of the Compose deployment.
It has three data-plane responsibilities:

1. expose public TCP listeners on ports 80 and 443 and distribute connections
   across private nginx `proxy-policy` workers with PROXY protocol v2;
2. distribute `/devshard` requests from those workers across ready
   `versiond-router` replicas, using route-specific health checks;
3. expose only DAPI's read-only `GET /versions` catalog on the isolated inner
   router network.

The nginx workers still own TLS and HTTP/2 on `:80` and `:443`, plus CORS,
rate limits, path rewrites, and the on-chain route table. Peer RPC shares
those two ports: the public frontends recognise a peer by the first bytes
of its connection (the HTTP/2 client preface on `:80`, ALPN `gonka-rpc/1`
on `:443`) and hand it to an HAProxy frontend in the same process, so it
does not enter those workers. Every other connection is JSON and goes to
nginx exactly as before. This keeps one policy implementation for JSON while
allowing the service-pool distributors to scale and restart independently.

## Request path

```text
client
  -> proxy-router :80/:443
  -> proxy-policy2 + proxy-policy (fixed rolling slots)
       -> ordinary and edge-api routes -> existing services
       -> /devshard/* -> proxy-router :18081 -> versiond-router fleet

peer RPC (same :80/:443, skips nginx)
  -> proxy-router :80  (HTTP/2 preface)  -> rpc_h2 frontend (h2c)
  -> proxy-router :443 (ALPN gonka-rpc/1) -> rpc_h2 frontend (TLS with nginx's cert, HTTP/2)
       HA:     versiond_routers_<version>_rpc -> versiond-router-fleet:8081 proto h2
       non-HA: versiond_routers_<version>_rpc -> versiond:8080 proto h2
               (an HTTP/1.1 child uses the bounded fallback for that version only)
```

A client whose first bytes are neither (an older devshard, a browser, curl)
never sees the peer-RPC frontend. A peer whose host still runs an older
wrapping normally falls back to Connect on HTTP/1.1 through nginx. With an
HTTP URL and old nginx in `NGINX_MODE=both`, nginx can accept h2c instead:
the client-to-nginx hop stays H2, but nginx still forwards over H1. This
is not end-to-end multiplexing or the new public RPC connection policy.
Intermediate separate-port v6 testnet clients/routers are not a supported
mixed-generation combination; see the compatibility table in the
[canonical host guide](https://gonka.ai/docs/host/devshard-high-availability/). The hand-off waits for the
first bytes at most `PROXY_ROUTER_PUBLIC_INSPECT_SECONDS` (default 5); an
idle connection then goes to nginx.

The public-to-policy hop is TCP, so encrypted HTTP/2 remains end-to-end between
the client and nginx. `send-proxy-v2` carries the original source address to the
worker. That listener is bound only to the internal `proxy-policy-front`
network; nginx derives both its bind address and trusted PROXY CIDR from the
`proxy-policy-ingress` peer. Containers on the shared application network
cannot reach or spoof that hop. nginx returns `/devshard` to the private HTTP
frontend on that network; HAProxy then reaches the versiond-router pool through
the isolated `versiond-router-ingress` alias. A catalog-aware inner HAProxy
preserves the client identity derived by the trusted policy tier on this
private hop. Edge
API traffic keeps the pre-existing nginx path in this release: a single worker
targets `edge-api` directly, while the multi-instance overlay targets the
existing `edge-api-router` nginx service.

An external L4 load balancer can preserve client addresses by sending PROXY
protocol and listing only its source CIDRs in
`PROXY_ROUTER_PROXY_PROTOCOL_FROM`. Connections from those CIDRs are rejected
unless they carry a valid PROXY preamble; direct clients from every other source
continue to use ordinary TCP. An L7 load balancer that terminates HTTP must be
configured as an L4 PROXY-protocol hop for this TCP ingress tier rather than
relying on `X-Forwarded-For`.

## Versiond-router selection

Every bootstrap or governance protocol version gets a separate HAProxy backend.
A router is eligible for version `v` only while its data endpoint answers
`/<v>/healthz`. A catalog-aware HAProxy router is additionally required to
answer on its private admin endpoint:

```text
GET http://versiond-router:8404/readyz?version=v
```

The `readyz` health contract therefore verifies both the data listener and the
lifecycle state. The `legacy` contract supports the transitional nginx router,
which has no admin listener, by checking only `/healthz` or
`/<v>/healthz` on port 8080. Versionless requests use the matching coarse check.

Selection among eligible routers uses the same escrow-derived consistent hash
as the inner router. Versioned session paths, versionless session observability
paths, and `/stats/shards/<escrow>` therefore stay on one router replica and one
`versiond` placement. All router replicas use the same pool, hash key, and
legacy pins, so they independently reach the same placement. Their active-check
views can differ briefly around a host failure; for versionless GETs only, a
route-local `404` is retried on another eligible host. POSTs are never replayed
after they may have reached an application. HA versions must use shared
PostgreSQL because affinity is placement, not exclusive ownership.

## Failure boundary

HAProxy retries connection failures, empty responses, and `502` responses. L7
retry is disabled for methods other than GET, HEAD, and OPTIONS. Once a POST may
have reached an application, replaying it could execute the operation twice;
infrastructure cannot safely infer otherwise. Cross-replica idempotency for an
explicit client retry is outside this routing layer and remains unchanged.

An established SSE stream stays on the policy worker, router, versiond host,
and child generation that accepted it. Rolling a replicated inner router or
application member removes it from new selection while that process drains.
The singleton public `proxy-router` is the stated host-level failure boundary:
restarting it interrupts connections crossing that process.

## Membership and state

All pools use Docker DNS plus HAProxy active checks. Protocol names are projected
from dapi's read-only governance `/versions` feed into pre-rendered inactive
backends through a local Unix Runtime API socket. The process has no shared
routing database, leader, or peer protocol. It does not need Redis:

- `proxy-policy-front` resolves to both private nginx policy slots;
- `versiond-router-fleet` resolves to the independently managed router slots on
  the shared front network.

Each policy slot exposes `/health` through its production HTTP and HTTPS
listeners on the isolated policy network. The active check sends the same PROXY
v2 preamble as production traffic, completes HTTP or TLS on that connection,
and requires `/health` to return `200`. The endpoint is backed by the existing
policy sidecar and resolves an alias scoped to the shared application network.
Losing the data listener, PROXY support, that interface, or its Docker DNS view
therefore removes only the affected worker from the corresponding public pool,
while failures of an individual upstream application remain visible at their
own route rather than collapsing the entire policy tier.

Each policy container runs its own sidecar, and a crashed sidecar is restarted
after five seconds. Readiness deliberately remains fail-closed during that
restart: returning a fallback `200` would admit a worker whose application
network has not been verified. A defect shared by the identical sidecar binaries
can therefore withdraw both policy workers at once even while nginx itself is
running; this is a correlated software failure of the policy deployment unit.

`server-template` reserves router capacity and `PROXY_ROUTER_VERSION_CAPACITY`
reserves version-backend capacity. Neither a new router address nor a new
governance name requires a reload. Fresh router and policy slots start fully
down and become eligible only after their configured readiness checks succeed.

HAProxy retains the health state of an occupied slot when Docker DNS changes
its address. A policy-worker replacement therefore follows an explicit
generation boundary: put the old address in runtime drain, confirm withdrawal
from new selection, gracefully stop the worker, reset its health to `DOWN`,
and return the empty slot to check-enabled `READY` state. After the replacement
appears, its address must complete a new L7 `rise` before HAProxy admits it.
The host updater owns this sequence. TCP redispatch moves a new connection to
another admitted worker after the selected address stops accepting connections.

## Availability scope

The nginx policy tier and private versiond-router fleet are process-redundant on
one Docker host. The public `proxy-router` remains one process. Loss of that
host, Docker daemon, public listener, or its network is therefore a host-level
outage. Multi-host ingress belongs in a later layer above this one.

The public router and both `proxy-policy` workers are one Compose deployment
unit. Updating or rolling back that unit applies its image, environment,
network, capability, and service definitions together. The host updater restores
the captured model by targeting these three services explicitly. Unrelated
services and containers owned by other active overlays remain unchanged.

## Endpoints

| Listener | Purpose |
| --- | --- |
| `:80`, `:443` | public TCP ingress to policy workers; peer RPC connections are recognised here and kept in this process |
| policy network `:18081` | private versiond-router distributor |
| `127.0.0.1:8404/livez` | process liveness |
| `127.0.0.1:8404/readyz` | active policy-worker availability |
| `127.0.0.1:8404/readyz?component=versiond` | coarse router-fleet availability |
| `127.0.0.1:8404/readyz?version=<v>` | HA: router capacity for that version. Non-HA: that version's HTTP/2 twin |
| `127.0.0.1:8405/metrics`, `proxy-router-metrics:8405/metrics` | HAProxy Prometheus exporter; internal only, no host port |
| router-back `:9100/versions` | read-only bridge to DAPI's governance catalog; other methods and paths are rejected |
| `/var/run/haproxy/haproxy.sock` | local Runtime API |

The diagnostic Runtime API is a Unix socket with `operator` privileges, which
are sufficient for status inspection but cannot change server state. A separate
mode-600 admin socket is used by the in-container catalog reconciler. The mode
protects the container boundary; it is not process isolation because HAProxy,
entrypoint, and reconciler intentionally run as the same unprivileged Unix user.
They are one trust domain, additionally constrained by dropped capabilities and
`no-new-privileges`. Strong per-process isolation would require a sidecar or a
narrow privileged broker. The loopback admin HTTP listener reports readiness
and cannot mutate routing.

The observability overlay scrapes this tier as the `proxy-router` Prometheus
job and the inner tier as `versiond-router`. Generic readiness intentionally
tracks the policy workers so ordinary APIs remain available during a devshard
incident. Use `/readyz?component=versiond`, per-version readiness, and the
`versiond_router_*` backend metrics from the `proxy-router` job for devshard
availability.

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `PROXY_POLICY_POOL_HOST` | `proxy-policy` | policy-worker DNS alias; Compose sets the shared private `proxy-policy-front` alias |
| `PROXY_POLICY_POOL_SLOTS` | `4` | reserved policy-worker slots |
| `VERSIOND_ROUTER_POOL_HOST` | `versiond-router-fleet` | router-fleet DNS alias |
| `VERSIOND_ROUTER_FLEET_CAPACITY` | `16` | reserved router slots |
| `VERSIOND_ROUTER_PORT` | `8080` | router data port |
| `VERSIOND_ROUTER_ADMIN_PORT` | `8404` | router health port |
| `VERSIOND_ROUTER_HEALTH_CONTRACT` | `readyz` | `readyz` verifies both data health and lifecycle readiness; `legacy` checks the nginx router's data endpoint only |
| `VERSIOND_VERSIONS` | *(empty)* | static bootstrap floor; day-2 governance names are learned automatically |
| `VERSIOND_NON_HA_VERSIONS` | *(empty)* | static legacy pins; these still define placement and must match every inner router |
| `VERSIOND_ROUTING_CATALOG_URL` | *(empty)* | read-only dapi `GET /versions` endpoint; release configuration enables it together with catalog-aware router images |
| `VERSIOND_ROUTING_CATALOG_POLL_SECONDS` | `5` | governance-name discovery interval |
| `VERSIOND_ROUTING_CATALOG_FETCH_TIMEOUT_SECONDS` | `3` | one catalog request timeout |
| `PROXY_ROUTER_ACTIVATION_MIN_READY` | `1` | ready inner routers required before publishing a newly learned governance name; a later fleet overlay raises this reserve |
| `VERSIOND_ROUTING_CATALOG_ALLOW_REMOVALS` | `false` | permit route removal only during supervised maintenance |
| `VERSIOND_ROUTING_CATALOG_CACHE_MAX_AGE_SECONDS` | `86400` | maximum age of the persistent last-known-good catalog loaded before startup |
| `PROXY_ROUTER_VERSION_CAPACITY` | `32` | minimum number of backends reserved for names added after process start; a larger valid LKG cache raises this floor automatically |
| `PROXY_ROUTER_STREAM_IDLE_SECONDS` | `1200` | client/server inactivity timeout |
| `PROXY_ROUTER_PUBLIC_IDLE_SECONDS` | `86400` | TCP inactivity timeout before nginx, including WebSocket/TLS connections |
| `PROXY_ROUTER_PROXY_PROTOCOL_FROM` | *(empty)* | space-separated trusted external L4 load-balancer CIDRs that must send PROXY protocol |
| `PROXY_ROUTER_RPC_MAX_CONNS_PER_SRC` | `32` | concurrent peer RPC connections one client address may hold; `:80`/`:443` count a connection as soon as its HTTP/2 preface or peer RPC ALPN is seen, and close the next one before the hand-off. The key is one IPv4 address, or one IPv6 /64; with `PROXY_ROUTER_PROXY_PROTOCOL_FROM` it is the address in the balancer's preamble. A TLS handshake that stalls holds its slot until `timeout client-hs` (5s) closes it. A socket that never sends the preface or the ALPN token is passed to nginx after `PROXY_ROUTER_PUBLIC_INSPECT_SECONDS` and never counts. Without PROXY protocol, clients behind one L4 balancer share that balancer's address |
| `PROXY_ROUTER_CONNECT_TIMEOUT_SECONDS` | `2` | upstream connect timeout |
| `PROXY_ROUTER_METRICS_BIND_HOST` | *(empty; loopback)* | internal DNS alias whose interface receives the read-only Prometheus listener; join Compose uses `proxy-router-metrics` |
| `PROXY_ROUTER_CATALOG_BIND_HOST` | *(empty; disabled)* | DNS alias whose isolated interface receives the read-only catalog bridge |
| `PROXY_ROUTER_CATALOG_PORT` | `9100` | catalog bridge listener port |
| `PROXY_ROUTER_CATALOG_UPSTREAM_HOST` | *(empty)* | DAPI hostname; required when the bridge is enabled |
| `PROXY_ROUTER_CATALOG_UPSTREAM_PORT` | `9100` | DAPI catalog port |
| `HAPROXY_DNS_RESOLVER` | `127.0.0.11:53` | numeric DNS nameserver used by HAProxy service discovery; Docker Compose uses the default, while another runtime may inject its cluster DNS IP |

The prefixes identify which contract owns a setting:

- `PROXY_ROUTER_*` configures this outer public distributor.
- `VERSIOND_ROUTER_*` describes the inner router fleet consumed by this tier;
  the same names configure the inner router deployment where applicable.
- `VERSIOND_ROUTING_*` is the catalog projection contract shared by both tiers.

Consequently, `PROXY_ROUTER_ACTIVATION_MIN_READY` is the number of inner routers
the outer tier requires, while `VERSIOND_ROUTING_ACTIVATION_MIN_READY` is the
number of versiond hosts each inner router requires. They are separate reserves,
not aliases.

`VERSIOND_NON_HA_VERSIONS` and the bootstrap `VERSIOND_VERSIONS` must match every
inner router. Runtime additions come from the same catalog on both tiers and do
not change escrow placement. Static names preserve the inner router's existing
literal path-segment contract. Dynamically learned names use the narrower
`[A-Za-z0-9][A-Za-z0-9._+~-]{0,63}` grammar required by the Runtime API.

Invalid catalog URL, timing, or capacity values fail startup. Every fully
ready addition is persisted before its request-map entry is published. Admission
is independent per version: an unavailable candidate remains staged without
blocking another candidate that has reached its reserve. A candidate with no
slot remains unpublished as `capacity-exhausted`, while already accepted routes
continue to serve. Cached additions remain assigned to bounded dynamic slots, so
a restart never resets capacity usage. If the configured capacity was reduced
below the number of accepted cached additions, the router preserves those routes
and renders enough slots for the LKG projection.

The catalog is additions-only during normal operation. A snapshot that omits an
accepted name keeps that route and cache entry and reports `withdrawal-pending`.
Removal requires an explicit maintenance run with
`VERSIOND_ROUTING_CATALOG_ALLOW_REMOVALS=true`. Accepted snapshots are written
atomically under `/var/lib/gonka-router`; stale or future-dated timestamps are
diagnostic and do not revoke accepted routes. Malformed or empty source data
leaves the last-known-good projection untouched. `catalog-status --state`
exposes these persistent states for monitoring, and the entrypoint restarts an
unexpectedly exited reconciler without restarting HAProxy.

The router consumes DAPI's existing `{"versions":[...]}` response. Names must
be valid and unique, and the local projection only grows without an explicit
maintenance operation. A malformed or removal snapshot leaves the last admitted
routes unchanged. This is a local HA safety policy, not a governance rule.
The parent and inner routers use the same validated schema-1 cache contract.
A stale cache retains already accepted routes while fresh catalog convergence
is pending; malformed cache data is ignored and obtained again from DAPI, with
the static bootstrap floor remaining fail-closed.

## Tests

```bash
make -C proxy-router test-render
make -C proxy-router test-compose
make -C proxy-router test-routing
```

The routing test uses real Docker networks, HAProxy, policy workers, route-aware
router health, an unavailable router data port, and a legacy edge-api-router
fixture. It verifies failover, a policy replacement that receives a new Docker
IP, withdrawal before replacement admission, exactly-once POST execution in the
tested connection-failure path, and the unchanged edge-api routing path.

## Peer RPC compatibility and admission

Connect RPC arriving through nginx returns to the private `:18081` frontend,
including in non-HA deployments. It shares procedure/IP stick tables with direct
HTTP/2 RPC. nginx retains TLS for ordinary clients, JSON policy, CORS, and streaming
configuration. `DEVSHARD_RPC_POLICY_HOST` is wired by join Compose; standalone
nginx deployments with it unset retain their existing routing.

When an RPC HTTP/2 backend has no healthy server, Connect uses a bounded HTTP/1.1
hop. HA keeps the original JSON routing maps, including version and legacy
placement. On a non-HA host the hop is versiond itself: versiond selects the
child from the path, and the version map's JSON pools are not used because they
address the router fleet. No POST replay is added. Native gRPC receives `Unavailable`
because HTTP/1.1 cannot carry it. RPC data checks require HTTP 200; a listening
router with an empty RPC pool is not ready. JSON capacity is independent of
fallback admission. Non-HA keeps a coarse `/readyz?peer-rpc=1` check, with the
`peer-rpc-ok` body, on the single host-wide pool. That check stays down while
any running child is HTTP/1.1, and versioned peer RPC does not consult it.
Each version has its own twin, dialing versiond over HTTP/2, whose check is
`/readyz?peer-rpc=1&version=<name>` and requires `peer-rpc-ok`. A v4 child
fails only that twin. A v6 child stays on HTTP/2, including Connect that
arrived as HTTP/1.1 through nginx `:18081`. When the proxy was not given a
version list or a routing catalog, it projects versiond's running children
from `/healthz` onto the pre-rendered dynamic twins. On that host,
`GET /readyz?version=<v>` follows the twin, so a child that answered
`peer-rpc-ok` is ready while its JSON backend stays disabled. A failed
projection is logged when it starts and when it recovers; the last assignment
stays in place in between. HTTPS-only mode rejects
plaintext connections on the HTTP port,
including prior-knowledge H2; a successful TLS connection is required. H2 backends
start unavailable until their capability checks pass. If a child changes before
checks notice it, versiond rejects H2 RPC to an H1 child with 503 rather than
expanding the mux into unbounded H1 connections; the bounded H1 path remains
available after ingress observes the capability change.

| Environment variable | Default | Meaning |
| --- | --- | --- |
| `PROXY_ROUTER_RPC_FALLBACK_MAXCONN` | `512` | Global active H1 fallback requests across versions, including long streams |
| `PROXY_ROUTER_RPC_FALLBACK_MAXQUEUE` | `64` | Queue rejection threshold; simultaneous arrivals may overshoot this threshold |
| `PROXY_ROUTER_RPC_FALLBACK_QUEUE_MS` | `200` | Maximum wait for a fallback connection, in milliseconds |
| `PROXY_ROUTER_NONHA_PEER_RPC_POLL_SECONDS` | `1` | How often a non-HA proxy with no routing catalog re-reads versiond `/healthz` |

The fallback budgets must be positive integers. Overload returns 503; ordinary
procedure limits return Connect `ResourceExhausted` (HTTP 429 with Retry-After) or
native gRPC status 8. Do not size the active budget solely for short unary calls:
Watch and Chat on the HTTP/1.1 fallback occupy a connection for their lifetime.
HTTP/2 twins do not take those slots.

For the upgrade and rollback procedure, use the canonical
[host guide in gonka-docs](https://github.com/snevolin/gonka-docs/blob/sn/devshard-ha-v6/docs/host/devshard-high-availability.md)
([documentation PR #1515](https://github.com/gonka-ai/gonka-docs/pull/1515)).
The proxy/policy pair must both be updated to enforce the shared budgets. A public
proxy restart closes its connections; this is not a zero-downtime guarantee.

Run `make -C proxy-router test-rpc-upgrade` from the repository root with Go and a
Linux Docker daemon. The suite builds the production nginx image and runs HAProxy
3.2 with configurations rendered by the production entrypoints. It covers H1,
h2c, TLS H1, custom ALPN H2, ordinary nginx H2, streaming, shared limits, encoded
paths, fragmented prefaces, bounded fallback, and versiond upgrade/rollback.
Its versiond endpoint is simulated; it does not prove signed session continuity
through actual binary replacement or the updater's transaction/rollback logic.
Client transport tests separately cover Attach, renewal, Watch, backoff, and H2
connection accounting. After rollout, verify an authenticated RPC on the public
port as well as readiness.
