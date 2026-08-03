# edge-api-router

HAProxy in front of multiple `edge-api` replicas. It distributes the stateless,
read-only Tier A API with round-robin balancing, discovers replicas through
DNS, and admits only replicas that pass an active readiness check.

```text
public proxy (/v1/*)
          |
          v
edge-api-router :18080
          |
          +-- edge-api-0 :18080
          +-- edge-api-1 :18080
          `-- edge-api-2 :18080
```

The public edge `proxy/` remains the client-facing nginx. This router is the
internal service-pool hop selected through
`EDGE_API_SERVICE_NAME=edge-api-router` in a multi-instance deployment.

## Why HAProxy

The previous router was nginx OSS. Its entrypoint rendered the space-separated
`EDGE_API_HOSTS` value into an upstream block at container startup. nginx could
re-resolve the address of a listed hostname, but changing the set of replicas
required a new host list and a newly rendered router configuration. Its
upstream checks were passive, so it learned that a replica was unusable only
after an operation failed.

The HA deployment needs the router to observe two independent facts:

1. which replicas currently exist;
2. which existing replicas may receive new requests.

HAProxy supplies both directly. `server-template` follows a shared DNS name,
and an active `GET /readyz` check controls eligibility. A stopping replica can
therefore leave rotation before it stops accepting, while requests it already
accepted continue on their established connections.

The corresponding design rationale for both pool routers is documented in
[the HA architecture](../devshard/docs/high-availability-architecture.md).

## Membership: DNS

`EDGE_API_POOL_HOST` is one DNS name that resolves to every replica. The
Compose overlay gives each `edge-api` service the same network alias:

```yaml
services:
  edge-api:
    networks:
      default:
        aliases:
          - edge-api-pool
```

Docker's embedded DNS publishes one A record per running container. HAProxy
re-resolves the name and fills a fixed set of `server-template` slots:

```haproxy
server-template edgeapi 64 edge-api-pool:18080 \
    check inter 1s fall 1 rise 2 \
    resolvers docker init-addr none init-state fully-down
```

Starting another container with the alias adds an address. Stopping one removes
its address. The router does not contain a replica-name list and keeps no
membership state to recover after restart.

`EDGE_API_ROUTER_POOL_SLOTS` limits the simultaneous pool size and defaults to
64. Slots are allocated when HAProxy starts; unused slots have no upstream
address.

## Readiness

Every second HAProxy asks each resolved replica for `GET /readyz` and expects
HTTP 200. `init-state fully-down` prevents a newly discovered address from
receiving traffic before its first passing check.

`edge-api` separates readiness from liveness:

| Endpoint | Meaning |
| --- | --- |
| `GET /healthz` | the process and HTTP server are alive |
| `GET /readyz` | the replica may receive new traffic |

Readiness fails when the replica cannot reach the chain or has begun graceful
shutdown. The chain probe has a two-second budget and a three-second cache, so
one health check per second does not become one chain query per second. HAProxy
allows a check three seconds, which is longer than the probe's own budget.

One unavailable replica leaves rotation without making the other replicas
unready. It rejoins after two consecutive passing checks.

## Graceful shutdown

On `SIGTERM` or `SIGINT`, `edge-api` performs readiness-first shutdown:

```text
signal
  |
  +-- latch /readyz to 503
  +-- keep serving for EDGE_API_DRAIN_ANNOUNCE
  +-- stop accepting new connections
  +-- wait for accepted requests within EDGE_API_SHUTDOWN_BUDGET
  `-- exit, or force-close on budget expiry / another signal
```

The default announce window is five seconds. It gives the one-second HAProxy
check enough time to remove the replica before the listener closes. The
shutdown budget defaults to two minutes, matching the router's default read
timeout. The Compose `EDGE_API_STOP_GRACE_PERIOD` defaults to three minutes and
is the outer Docker SIGKILL backstop.

An established response stays on its original connection after readiness
fails. HAProxy sends only later requests to another replica.

## Failure and retry policy

The pool retries another replica for a connection failure, an empty response,
or upstream HTTP 502. It deliberately does not retry HTTP 503: readiness and
application guards use 503 to report a condition that another replica may
share. L7 retries are disabled for methods other than `GET`, `HEAD`, and
`OPTIONS`, so a future non-idempotent route cannot be executed twice by the
router.

The router preserves the forwarding contract of the previous nginx hop:

- append the peer to `X-Forwarded-For`;
- overwrite `X-Real-IP` with the immediate client address;
- set `X-Forwarded-Proto`;
- expose the selected address as response header `X-Upstream-Addr`.

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `EDGE_API_POOL_HOST` | `edge-api-pool` | DNS name resolving to every replica |
| `EDGE_API_PORT` | `18080` | upstream `edge-api` port |
| `EDGE_API_ROUTER_PORT` | `18080` | router listen port |
| `EDGE_API_ROUTER_POOL_SLOTS` | `64` | maximum simultaneous DNS-discovered replicas |
| `EDGE_API_ROUTER_MAX_CONNECTIONS` | `4096` | HAProxy frontend connection limit |
| `EDGE_API_ROUTER_CONNECT_TIMEOUT_SECONDS` | `30` | connect and request-header timeout |
| `EDGE_API_ROUTER_READ_TIMEOUT_SECONDS` | `120` | client and upstream response timeout |

The entrypoint validates numeric values and the pool hostname, renders
`haproxy.cfg.template`, validates the result with `haproxy -c`, and then execs
HAProxy. Invalid configuration fails the container before it begins serving.

The image runs as the unprivileged `haproxy` user. Its Runtime API is a local
Unix socket and is not published as a network admin endpoint.

## Deployment

The join overlay adds two replicas and points the public proxy at the router:

```console
docker compose \
  -f deploy/join/docker-compose.yml \
  -f deploy/join/docker-compose.edge-api-multi.yml \
  up -d
```

Adding a replica still means creating a container through the deployment
system and attaching it to the same network alias. The router discovers that
container automatically; it does not create replicas itself.

## Observability and tests

HAProxy logs to stdout. Its Prometheus endpoint listens on loopback
`127.0.0.1:8405/metrics`, and responses include `X-Upstream-Addr` for routing
diagnostics.

Render and policy checks run with:

```console
make -C edge-api-router test-render
```

The target verifies DNS membership, round-robin, readiness, forwarding headers,
retry policy, input validation, and the rendered configuration against the
HAProxy image shipped by the router.

Related documentation:

- [HA architecture](../devshard/docs/high-availability-architecture.md)
- [versiond host evacuation](../devshard/docs/versiond-host-evacuation.md)
- [0.2.15 release guide](../devshard/docs/release-0.2.15-v5.md)
