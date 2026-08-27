# proxy-router

`proxy-router` is the host-local HAProxy at the edge of the Compose deployment.
It has four data-plane responsibilities:

1. expose public TCP listeners on ports 80 and 443 and distribute connections
   across private nginx `proxy-policy` workers with PROXY protocol v2;
2. distribute Tier A requests from those workers across ready `edge-api`
   replicas with round-robin selection;
3. distribute `/devshard` requests from those workers across ready
   `versiond-router` replicas, using route-specific health checks;
4. expose only DAPI's read-only `GET /versions` catalog on the isolated inner
   router network.

The nginx workers still own TLS, HTTP/2, CORS, rate limits, path rewrites, and
the on-chain route table. This keeps one policy implementation while allowing
the service-pool distributors to scale and restart independently.

## Request path

```text
client
  -> proxy-router :80/:443
  -> proxy-policy2 + proxy-policy (fixed rolling slots)
       -> ordinary routes -> existing services
       -> Tier A routes -> proxy-router :18082 -> edge-api pool
       -> /devshard/* -> proxy-router :18081 -> versiond-router fleet
```

The public-to-policy hop is TCP, so encrypted HTTP/2 remains end-to-end between
the client and nginx. `send-proxy-v2` carries the original source address to the
worker. That listener is bound only to the internal `proxy-policy-front`
network; nginx derives both its bind address and trusted PROXY CIDR from the
`proxy-policy-ingress` peer. Containers on the shared application network
cannot reach or spoof that hop. nginx returns `/devshard` to the private HTTP
frontends on that network. HAProxy then reaches the versiond-router or edge-api
pool. nginx still decides which Tier A paths are public and preserves their
rate limits, CORS, rewrites, and method split; it no longer owns edge-api pool
membership.

## Edge-api selection

The single topology resolves one `edge-api` service. The multi overlay assigns
`edge-api-pool` to all three replicas. HAProxy round-robins new Tier A requests
across addresses that pass active health checks.

During rolling adoption, a legacy image without `/readyz` remains eligible only
when `/healthz` passes. A lifecycle-aware image reports `/readyz` 503 before
listener shutdown, stays alive for the announcement window, and drains accepted
requests after HAProxy withdraws it. Edge readiness is component-scoped: an
empty edge pool makes `/readyz?component=edge-api` fail but does not make the
generic public readiness fail, because ordinary and devshard APIs are still
independently usable.

## Versiond-router selection

Every bootstrap or governance protocol version gets a separate HAProxy backend.
A router is eligible for version `v` only while its private admin endpoint
answers:

```text
GET http://versiond-router:8404/readyz?version=v
```

The data request still goes to that router's port 8080. Separating health and
traffic ports keeps lifecycle state off the public API. Versionless requests use
the coarse `/readyz` check.

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
- `edge-api-pool` resolves to all edge-api replicas in the multi overlay (the
  single topology uses the `edge-api` service name directly);
- `versiond-router` resolves to the router instances declared by the active
  Compose topology. A later fleet overlay can supply a multi-address alias
  without changing this routing contract.

`server-template` reserves router capacity and `PROXY_ROUTER_VERSION_CAPACITY`
reserves version-backend capacity. Neither a new router address nor a new
governance name requires a reload. Inner-router addresses become eligible only
after successful readiness checks. Long-lived policy workers start as connect
candidates immediately; TCP redispatch moves a connection to another worker if
the selected address is not listening yet.

## Availability scope

The nginx policy tier is replicated, and the private versiond distributor is
ready for multiple independently managed routers. The current join overlay
still declares one `versiond-router`; fleet management is a separate follow-up.
The public `proxy-router` also remains one process on one host. Loss of that
host, Docker daemon, public listener, or its network is therefore a host-level
outage. Multi-host ingress belongs in a later layer above this one.

## Endpoints

| Listener | Purpose |
| --- | --- |
| `:80`, `:443` | public TCP ingress to policy workers |
| policy network `:18081` | private versiond-router distributor |
| policy network `:18082` | private edge-api distributor |
| `127.0.0.1:8404/livez` | process liveness |
| `127.0.0.1:8404/readyz` | active policy-worker availability |
| `127.0.0.1:8404/readyz?component=edge-api` | edge-api pool availability |
| `127.0.0.1:8404/readyz?component=versiond` | coarse router-fleet availability |
| `127.0.0.1:8404/readyz?version=<v>` | end-to-end router capacity for one bootstrap or governance version |
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

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `PROXY_POLICY_POOL_HOST` | `proxy-policy` | policy-worker DNS alias; Compose sets the shared private `proxy-policy-front` alias |
| `PROXY_POLICY_POOL_SLOTS` | `4` | reserved policy-worker slots |
| `EDGE_API_POOL_HOST` | `edge-api` | edge-api DNS name; the multi overlay sets `edge-api-pool` |
| `EDGE_API_PORT` | `18080` | edge-api data and readiness port |
| `PROXY_EDGE_API_POOL_SLOTS` | `16` | reserved edge-api DNS slots |
| `PROXY_EDGE_API_PORT` | `18082` | private policy-to-edge distributor port |
| `VERSIOND_ROUTER_POOL_HOST` | `versiond-router-fleet` | router-fleet DNS alias |
| `VERSIOND_ROUTER_FLEET_CAPACITY` | `16` | reserved router slots |
| `VERSIOND_ROUTER_PORT` | `8080` | router data port |
| `VERSIOND_ROUTER_ADMIN_PORT` | `8404` | router health port |
| `VERSIOND_VERSIONS` | *(empty)* | static bootstrap floor; day-2 governance names are learned automatically |
| `VERSIOND_NON_HA_VERSIONS` | *(empty)* | static legacy pins; these still define placement and must match every inner router |
| `VERSIOND_ROUTING_CATALOG_URL` | *(empty)* | read-only dapi `GET /versions` endpoint; release configuration enables it together with catalog-aware router images |
| `VERSIOND_ROUTING_CATALOG_POLL_SECONDS` | `5` | governance-name discovery interval |
| `VERSIOND_ROUTING_CATALOG_FETCH_TIMEOUT_SECONDS` | `3` | one catalog request timeout |
| `PROXY_ROUTER_ACTIVATION_MIN_READY` | `1` | ready inner routers required before publishing a newly learned governance name; a later fleet overlay raises this reserve |
| `VERSIOND_ROUTING_CATALOG_ALLOW_REMOVALS` | `false` | permit route removal only during supervised maintenance |
| `VERSIOND_ROUTING_CATALOG_CACHE_MAX_AGE_SECONDS` | `86400` | maximum age of the persistent last-known-good catalog loaded before startup |
| `PROXY_ROUTER_VERSION_CAPACITY` | `32` | backends reserved for names added after process start |
| `PROXY_ROUTER_STREAM_IDLE_SECONDS` | `1200` | client/server inactivity timeout |
| `PROXY_ROUTER_PUBLIC_IDLE_SECONDS` | `86400` | TCP inactivity timeout before nginx, including WebSocket/TLS connections |
| `PROXY_ROUTER_CONNECT_TIMEOUT_SECONDS` | `2` | upstream connect timeout |
| `PROXY_ROUTER_METRICS_BIND_HOST` | *(empty; loopback)* | internal DNS alias whose interface receives the read-only Prometheus listener; join Compose uses `proxy-router-metrics` |
| `PROXY_ROUTER_CATALOG_BIND_HOST` | *(empty; disabled)* | DNS alias whose isolated interface receives the read-only catalog bridge |
| `PROXY_ROUTER_CATALOG_PORT` | `9100` | catalog bridge listener port |
| `PROXY_ROUTER_CATALOG_UPSTREAM_HOST` | *(empty)* | DAPI hostname; required when the bridge is enabled |
| `PROXY_ROUTER_CATALOG_UPSTREAM_PORT` | `9100` | DAPI catalog port |
| `HAPROXY_DNS_RESOLVER` | `127.0.0.11:53` | numeric DNS nameserver used by HAProxy service discovery; Docker Compose uses the default, while another runtime may inject its cluster DNS IP |

`VERSIOND_NON_HA_VERSIONS` and the bootstrap `VERSIOND_VERSIONS` must match every
inner router. Runtime additions come from the same catalog on both tiers and do
not change escrow placement.

Invalid catalog URL, timing, or capacity values fail startup. Every fully
ready addition is persisted before its request-map entry is published. Admission
is independent per version: an unavailable candidate remains staged without
blocking another candidate that has reached its reserve. A candidate with no
slot remains unpublished as `capacity-exhausted`, while already accepted routes
continue to serve. Cached additions remain assigned to bounded dynamic slots, so
a restart never resets capacity usage; startup fails if reduced capacity cannot
represent the accepted cache.

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
router health, an unavailable router data port, and a mixed edge-api pool. It
verifies legacy `/healthz` fallback, readiness withdrawal while the listener is
still serving, edge-only outage isolation, policy failover, and that the proxy
does not replay a non-idempotent POST in the tested connection-failure path.
