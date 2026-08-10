# proxy-router

`proxy-router` is the host-local HAProxy at the edge of the Compose deployment.
It has three independent data-plane responsibilities:

1. expose public TCP listeners on ports 80 and 443 and distribute connections
   across private nginx `proxy-policy` workers with PROXY protocol v2;
2. distribute `/devshard` requests from those workers across ready
   `versiond-router` replicas, using route-specific health checks;
3. distribute Tier A read-only requests directly across ready `edge-api`
   replicas.

The nginx workers still own TLS, HTTP/2, CORS, rate limits, path rewrites, and
the on-chain route table. This keeps one policy implementation while allowing
the service-pool distributors to scale and restart independently.

## Request path

```text
client
  -> proxy-router :80/:443
  -> proxy-policy x N
       -> ordinary routes -> existing services
       -> /devshard/* -> proxy-router :18081 -> versiond-router fleet
       -> Tier A /v1/* -> proxy-router :18082 -> edge-api pool
```

The public-to-policy hop is TCP, so encrypted HTTP/2 remains end-to-end between
the client and nginx. `send-proxy-v2` carries the original source address to the
worker. That listener is bound only to the internal `proxy-policy-front`
network; nginx derives both its bind address and trusted PROXY CIDR from the
`proxy-policy-ingress` peer. Containers on the shared application network
cannot reach or spoof that hop. nginx returns the two HA routes to the private
HTTP frontends on that network; HAProxy then reaches each application pool on
its own internal network.

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

Selection among eligible routers is `leastconn`, which distributes long SSE
streams without requiring shared state. The selected inner router then applies
the deterministic escrow consistent hash. All router replicas use the same
pool, hash key, and legacy pins, so they independently reach the same placement.
Their active-check views can differ for a few seconds around a host failure; in
that interval a later request may recover on another versiond from shared
PostgreSQL. This is why HA versions cannot use per-host SQLite.

## Failure boundary

HAProxy retries connection failures, empty responses, and `502` responses. L7
retry is disabled for methods other than GET, HEAD, and OPTIONS. Once a POST may
have reached an application, replaying it could execute the operation twice;
infrastructure cannot safely infer otherwise. Operations that need exactly-once
semantics must carry an application idempotency key.

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

- `proxy-policy` resolves to private nginx workers;
- `versiond-router-fleet` resolves only to the independently managed router
  slots; the distinct name prevents a transitional singleton from satisfying
  fleet readiness during the one-time upgrade;
- `edge-api-pool` resolves to edge-api replicas.

`server-template` reserves host capacity and `PROXY_ROUTER_VERSION_CAPACITY`
reserves version-backend capacity. Neither a new address nor a new governance
name requires a reload. Application and inner-router addresses become eligible
only after successful readiness checks. Long-lived policy workers start as
connect candidates immediately; TCP redispatch moves a connection to another
worker if the selected address is not listening yet.

## Availability scope

The inner `versiond-router` tier and nginx policy tier are replicated. The
public `proxy-router` remains one process on one host in this Compose design.
Loss of that host, Docker daemon, public listener, or its network is therefore a
host-level outage. Multi-host ingress belongs in a later layer above this one
(provider LB, VIP, or Kubernetes Service).

## Endpoints

| Listener | Purpose |
| --- | --- |
| `:80`, `:443` | public TCP ingress to policy workers |
| policy network `:18081` | private versiond-router distributor |
| policy network `:18082` | private edge-api distributor |
| `127.0.0.1:8404/livez` | process liveness |
| `127.0.0.1:8404/readyz` | active policy-worker availability |
| `127.0.0.1:8404/readyz?component=versiond` | coarse router-fleet availability |
| `127.0.0.1:8404/readyz?component=edge-api` | edge-api availability |
| `127.0.0.1:8404/readyz?version=<v>` | end-to-end router capacity for one bootstrap or governance version |
| `127.0.0.1:8405/metrics` | HAProxy Prometheus exporter |
| `/var/run/haproxy/haproxy.sock` | local Runtime API |

The diagnostic Runtime API is a Unix socket with `operator` privileges, which
are sufficient for status inspection but cannot change server state. A separate
mode-600 admin socket is owned only by the in-container catalog reconciler. The
loopback admin HTTP listener reports readiness and cannot mutate routing.

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `PROXY_POLICY_POOL_HOST` | `proxy-policy` | policy-worker DNS alias; Compose uses the private `proxy-policy-front` alias |
| `PROXY_POLICY_POOL_SLOTS` | `4` | reserved policy-worker slots |
| `VERSIOND_ROUTER_POOL_HOST` | `versiond-router-fleet` | router-fleet DNS alias |
| `VERSIOND_ROUTER_FLEET_CAPACITY` | `16` | reserved router slots |
| `VERSIOND_ROUTER_PORT` | `8080` | router data port |
| `VERSIOND_ROUTER_ADMIN_PORT` | `8404` | router health port |
| `VERSIOND_VERSIONS` | *(empty)* | static bootstrap floor; day-2 governance names are learned automatically |
| `VERSIOND_NON_HA_VERSIONS` | *(empty)* | static legacy pins; these still define placement and must match every inner router |
| `VERSIOND_ROUTING_CATALOG_URL` | *(empty)* | read-only dapi `GET /versions` endpoint; join Compose uses `http://api:9100/versions` |
| `VERSIOND_ROUTING_CATALOG_POLL_SECONDS` | `5` | governance-name discovery interval |
| `VERSIOND_ROUTING_CATALOG_FETCH_TIMEOUT_SECONDS` | `3` | one catalog request timeout |
| `PROXY_ROUTER_VERSION_CAPACITY` | `32` | backends reserved for names added after process start |
| `EDGE_API_POOL_HOST` | `edge-api-pool` | edge-api DNS alias |
| `PROXY_ROUTER_STREAM_IDLE_SECONDS` | `1200` | client/server inactivity timeout |
| `PROXY_ROUTER_PUBLIC_IDLE_SECONDS` | `86400` | TCP inactivity timeout before nginx, including WebSocket/TLS connections |
| `PROXY_ROUTER_CONNECT_TIMEOUT_SECONDS` | `2` | upstream connect timeout |

`VERSIOND_NON_HA_VERSIONS` and the bootstrap `VERSIOND_VERSIONS` must match every
inner router. Runtime additions come from the same catalog on both tiers and do
not change escrow placement.

Invalid catalog URL, timing, or capacity values fail startup. A transient fetch
failure preserves admitted routes, and the entrypoint restarts an unexpectedly
exited reconciler without restarting HAProxy.

## Operations

The main Compose project owns `proxy-router`, the private policy network, and
the policy workers. It does not own the inner router slots or their two external
networks. The fleet creates/validates those networks, so main-stack `down`
cannot remove infrastructure required by stopped slots. Use:

```bash
source deploy/join/config.env
deploy/join/versiond-router-fleet.sh prepare-networks
deploy/join/versiond-router-fleet.sh up
deploy/join/versiond-router-fleet.sh status
deploy/join/versiond-router-fleet.sh rollout
```

The rollout checks both each slot's effective runtime route view and the parent
proxy's actual admission state for the coarse route and every currently
available version. It replaces one slot only after that reserve is visible at
both levels. A failed slot is restored from the exact pre-rollout image and
environment.

For a newly approved protocol, hosters do not edit `config.env` or roll either
router tier. Network activation automation can use the local end-to-end gate:

```bash
deploy/join/versiond-router-fleet.sh wait-version v6
```

It requires every slot to have learned the name and at least the configured
ready reserve to be admitted by this parent. The approved catalog cannot provide
a pre-approval signal; a staged pre-approval workflow would need a separate
signed candidate feed.

Every fleet command locks the same deployment-local file next to `config.env`.
The lock is opened read-only after creation, so invocations by the deployment
user and through `sudo` coordinate on the same inode without relying on a
per-user runtime directory.

## Tests

```bash
make -C proxy-router test-render
make -C proxy-router test-routing
make -C versiond-router test-fleet
```

The routing test uses real Docker networks, HAProxy, policy workers, route-aware
router health, an unavailable router data port, and multiple edge-api replicas.
It verifies failover and proves that a non-idempotent POST is never duplicated.
