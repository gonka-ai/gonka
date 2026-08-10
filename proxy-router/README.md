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

Every declared protocol version gets a separate HAProxy backend. A router is
eligible for version `v` only while its private admin endpoint answers:

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

All pools use Docker DNS plus HAProxy active checks. The process has no routing
database, leader, or peer protocol. It does not need Redis:

- `proxy-policy` resolves to private nginx workers;
- `versiond-router` resolves to the independently managed router slots;
- `edge-api-pool` resolves to edge-api replicas.

`server-template` reserves capacity without requiring a reload when addresses
appear or disappear. Application and inner-router addresses become eligible
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
| `127.0.0.1:8404/readyz?version=<v>` | end-to-end router capacity for one declared version |
| `127.0.0.1:8405/metrics` | HAProxy Prometheus exporter |
| `/var/run/haproxy/haproxy.sock` | local Runtime API |

The Runtime API is a Unix socket only and has `operator` privileges, which are
sufficient for status inspection but cannot change server state. The loopback
admin HTTP listener reports readiness and cannot mutate routing.

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `PROXY_POLICY_POOL_HOST` | `proxy-policy` | policy-worker DNS alias; Compose uses the private `proxy-policy-front` alias |
| `PROXY_POLICY_POOL_SLOTS` | `4` | reserved policy-worker slots |
| `VERSIOND_ROUTER_POOL_HOST` | `versiond-router` | router-fleet DNS alias |
| `VERSIOND_ROUTER_FLEET_CAPACITY` | `16` | reserved router slots |
| `VERSIOND_ROUTER_PORT` | `8080` | router data port |
| `VERSIOND_ROUTER_ADMIN_PORT` | `8404` | router health port |
| `EDGE_API_POOL_HOST` | `edge-api-pool` | edge-api DNS alias |
| `PROXY_ROUTER_STREAM_IDLE_SECONDS` | `1200` | client/server inactivity timeout |
| `PROXY_ROUTER_PUBLIC_IDLE_SECONDS` | `86400` | TCP inactivity timeout before nginx, including WebSocket/TLS connections |
| `PROXY_ROUTER_CONNECT_TIMEOUT_SECONDS` | `2` | upstream connect timeout |

`VERSIOND_NON_HA_VERSIONS` and `VERSIOND_VERSIONS` must match every inner
router. They determine the generated version map and route-specific backends.

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

The rollout checks both each slot's local route view and the parent proxy's
actual admission state for the coarse route and every currently available
declared version. It replaces one slot only after that reserve is visible at
both levels. A failed slot is restored from the exact pre-rollout image and
environment.

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
