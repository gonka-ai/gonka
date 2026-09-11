# Peer RPC connection setup

Companion to [`grpc-transport-plan.md`](./grpc-transport-plan.md) (design) and
[`grpc-transport-phases.md`](./grpc-transport-phases.md) (execution order). This page is the
**mechanical** picture: how a peer opens an RPC session on InferenceUrl, why phases 1–5
stay HTTP/1.1 (children are loopback-only), and how phase 6 carries HTTP/2 **end to end**
on a published gRPC listen that **skips nginx**.

**Status.** Phase 1 ships the server handshake, the fail-closed session interceptor, and the
`/rpc/` mount (flag off by default). The client attach loop (`PeerConn`) is phase 2. HTTP/2
is phase 6: deploy publishes versiond-router (HA) or versiond (non-HA) for authenticated
`/rpc/` on `{InferenceUrl.host}:{DEVSHARD_RPC_H2_PORT}`, with HTTP/2 through to the child.
When InferenceUrl is HTTPS, versiond-router terminates the same nginx cert; nginx keeps
JSON and the public API. The URL shape and handshake do not change between those phases.

---

## Two layers that must not be mixed

| Layer | What it proves | Survives a dispute? |
|---|---|---|
| **Peer session** (`Attach` → `Watch`) | This key opened a session with this host+version | No. Token is connectivity-only. Attach checks `AllowsSender` on the URL escrow as the door; later RPCs keep that escrow's roster. |
| **Signed envelope** on each dispute-bearing RPC | This key sent **this** body at **this** timestamp | Yes. Same hash as today's `X-Devshard-Signature`. |

The session exists so later RPCs can skip ECDSA for admission and so GETs can be authorized.
It never replaces `sha256(escrowID ‖ payload ‖ timestamp_be8)`.

---

## Existing port, existing HTTP/1.1 hops

`Participant.InferenceUrl` already points at the public HTTP listener (typically nginx
`:8000` → versiond → `devshardd`). That path is forced to HTTP/1.1 on three hops:

1. nginx `location /devshard/` sets `proxy_http_version 1.1`
2. versiond's public server has no h2c
3. versiond → `devshardd` is `httputil.ReverseProxy` with the default (HTTP/1.1) transport

Native gRPC needs HTTP/2 end to end, so it cannot ride this URL without an infra rollout.
Connect over HTTP/1.1 can. The public URL **keeps** version and escrow in the path so the
existing routers keep working:

```
POST /devshard/{version}/sessions/{escrowID}/rpc/{package.Service}/{Method}
```

| Concern | Why the old port still works |
|---|---|
| nginx HTTP/1.1 + `proxy_buffering off` | Connect is HTTP/1.1-native; streams look like today's SSE |
| versiond version routing | First path segment is still `{version}` |
| HAProxy escrow stickiness | `^/[^/]+/sessions/([^/]+)` still matches |
| On-chain `InferenceUrl` | Same host, port, and scheme — no extra TLS layer |

No participant updates nginx, versiond, or HAProxy **in phases 1–5**. Only `devshardd`
changes, which versiond already updates. Phase 6 is the hop upgrade.

```
client                    nginx :8000              versiond                 devshardd
  |                          |                        |                        |
  |  HTTP/1.1 POST            |                        |                        |
  |  /devshard/v5/sessions/42/rpc/.../Attach            |                        |
  |------------------------->|  HTTP/1.1               |                        |
  |                          |----------------------->|  HTTP/1.1             |
  |                          |                        |----------------------->|
  |                          |                        |    Echo /sessions/:id/rpc/*
  |                          |                        |    strip prefix → Connect mux
```

Connect's native path is `/{package.Service}/{Method}` (no version, no escrow). Echo is the
translator:

1. Match `Any /sessions/:id/rpc/*`
2. Put `{escrowID}` on the request context
3. Strip `/sessions/{id}/rpc`, leaving `/devshard.transport.v1.PeerAuthService/Attach`
4. Hand that path to `http.ServeMux` (`transport/rpcserver.NewMux`)

The rewrite is a path splice, not a second protocol. Proxies never see the stripped form.

`DEVSHARD_RPC_SERVER_ENABLED` (default **false**) gates the Echo mount. Flag off: the
`/rpc/` route does not exist (404). Existing JSON session routes are untouched.
Flag on with no host address (no signer or recorder) panics at `Register`
instead of mounting a handler that rejects every Attach as a bad peer.
`Close` / `ClosePeerRPC` ends Watch streams, drops host sessions, and refuses
new Attach and handshake-gated RPCs with `failed_precondition` `"host shutting
down"` so `http.Server.Shutdown` can drain.

Mounted children export `devshard_peer_rpc_enabled`, Attach/gate counters, and
`devshard_session_resolution_total{route="rpc_get_signatures"}` so JSON dashboards
keep counting as traffic moves off GET `/signatures`.

The **gateway** (not the child) counts adoption:
`devshard_gateway_escrow_sessions_total{path}` once per escrow per host, and
`devshard_gateway_host_rpc{peer,mode}` while that host still has a live escrow
bind (or a ready PeerConn). Retiring the escrow drops idle `host_rpc` series.

---

## Handshake on that path

One `PeerConn` per **(host, devshard version)**, shared across every escrow that child
serves. Version is in the key because `/devshard/{version}/` selects a child; a v5
token is not valid on v6. Escrow is **not** in the key: Attach authenticates to the
host's gonka address, and the same token is sent on every `/sessions/{id}/rpc/` path
of that child.

The token is keyed to **peer identity**, not to a TCP socket — HTTP/1.1 may open
several pooled connections and they all carry the same token.

### Membership and renewal

In production, Attach only succeeds if the recovered key is already a participant of
the escrow in that URL (`AllowsSender`). Outsiders never get a token.

The URL escrow must already be open on this host so that check can run. If it is not,
Attach fails the same way a data RPC would (`unavailable` while the host is initializing,
otherwise `failed_precondition`). A peer not on that escrow's roster fails with
`permission_denied`. Signature, host address, timestamp, and nonce checks still run
first.

Renewal is a new Attach with a **new** `attach_nonce`, not a TTL refresh of the old
one:

- Same nonce while it is still live → rejected (`attach_nonce already in use`).
- Same peer, new nonce → new token; the old one stays valid for **5s** so in-flight
  RPCs still admit.
- That second Attach runs `AllowsSender` again. If they were dropped from the roster,
  they cannot renew.

The token itself is still host-wide: Attach via one open escrow you belong to, then
use it on every escrow path this child serves. Later RPCs are admitted by session id
(`x-devshard-session`); they do not repeat the Attach door. `GetSignatures` still
checks roster for **that** request's escrow.

```
  unauthenticated
        |
        |  Attach  (client UUID in attach_nonce, ECDSA over attach domain)
        v
      ready  ── session id is the client's UUID (echoed as session_token)
        |
        |  Watch  (server-stream heartbeats every ~30s)
        |
        +-- Watch dies / token at ~75% TTL → clear session → Attach again
```

The client opens the Connect channel and picks a random **attachment id** (`attach_nonce`,
16–32 bytes). This is not an inference or tx nonce; it only names this RPC attach.
That id is covered by the Attach signature together with the host gonka address and a
timestamp. The server does **not** mint a session id. `AttachResponse.session_token` is
the same `attach_nonce`. Replay of a live nonce on this host is **rejected**.
A dropped nonce stays unrebindable for the ±30 s signature window, so a
captured Attach cannot evict a newer session. An Attach whose timestamp is
older than the live session is rejected. A new id from the same peer on this
host+version replaces the previous host session.

`Watch` is a server-stream; to nginx it looks like an SSE response and inherits the same
long transfer timeouts. The server ends that stream when its token is no longer
current (re-Attach, sweep, evict), not only at the next heartbeat.

Attach signature (not `SignRequest`):

```
sha256("devshard.attach.v1" ‖ host_address ‖ timestamp
       ‖ peer_address ‖ attach_nonce ‖ protocol_version ‖ channel_binding)
```

`channel_binding` is **empty**. HTTP/1.1 Connect has no TLS layer (plan §6): `PeerConn`
dials InferenceUrl as-is, and a non-empty binding is rejected. Phase 6 does not change
that — multiplexing is not peer mTLS.

Server checks: recovered address equals `peer_address` → `host_address` equals this
process's gonka address → timestamp within ±30 s → `protocol_version` is
`devshard.transport.v1` (empty and unknown rejected) → `attach_nonce` is not already
live → `AllowsSender` for the escrow in the URL. Then it records one session for that
client peer on this child. The URL escrow is the door, not the session key.

The attachment id is still on the wire after Attach. `Watch` takes it in the protobuf
body **and** as `x-devshard-session`; every other RPC sends only the header. Signing it
stops **forgery** of a different id, not sniffing. Host address + timestamp stop the
photocopy: another host is not the signed audience, and the blob dies after drift.
Anyone who saw the handshake can still present that bearer token until TTL or Watch end
— same as a server-minted token.

**What Phase 1 actually checks.** `Attach` is the only RPC that does not require a
session. Every other procedure — including `Watch` and `GetSignatures` — is admitted
only after `x-devshard-session` looks up a live host-level token. Missing, forged, or
expired tokens are dropped with `unauthenticated` (`handshake required`) before the
handler runs. The same token is valid on escrow A and escrow B of this child. JSON
`GET .../signatures` stays unauthenticated; the RPC path does not.

While unauthenticated, later RPCs **fail fast**. They do not wait on Attach. Reconnect uses
jittered backoff (50 ms → 5 s), same idea as the CometBFT WS listener.

HTTP/1.1 keepalive is a **pool**, not one multiplexed connection. N concurrent RPCs need N
TCP connections; a 30-minute `Chat` holds one for its whole life. `MaxIdleConnsPerHost`
must rise to cover concurrent chats plus gossip, queries, and `Watch`. That is the cost of
phases 1–5 on this path. Phase 6 removes it by carrying HTTP/2 on a listen that skips nginx.

---

## HTTP/2, nginx skipped (phase 6)

Same Connect handlers. Same protos. Same `/devshard/{version}/sessions/{id}/rpc/…` path
shape. Children stay on `127.0.0.1`. Authenticated peer RPC does **not** enter nginx.

Join today (JSON and, until phase 6, RPC):

```
client → HAProxy :8000/:443 (TCP) → nginx → versiond-router :18081 → versiond :8080 → 127.0.0.1:child
         all HTTP/1.1 after the public listen
```

`devshardd` is not a public port. versiond talks to it as loopback. A second `h2_endpoint`
on the child is either unreachable or a new public hole that skips version routing and
escrow stickiness. That design is rejected.

Phase 6 publishes the hop that already hashes version + escrow, and takes nginx off `/rpc/`.
HTTP/2 is required on **every** hop of that path — not only the public listen. Connect does
not turn a Go `http.Server` into h2c; each process must opt in.

```
HA:     client → versiond-router (TLS+h2 if InferenceUrl is HTTPS, else h2c)
                 → versiond (h2c) → 127.0.0.1:child (h2c)
Non-HA: client → versiond (h2c; TLS in front if InferenceUrl is HTTPS)
                 → 127.0.0.1:child (h2c)
```

A default `httputil.ReverseProxy` (HTTP/1.1) or a child `http.ListenAndServe` without
`golang.org/x/net/http2/h2c` fans N RPCs back into N TCP connections and **fails** this
phase. JSON/SSE, `/v1`, healthz, metrics, ops GETs stay:

```
client → nginx (InferenceUrl) → …   # unchanged
```

| Hop | Change |
|---|---|
| nginx / proxy-policy | **Not on `/rpc/`.** Keep InferenceUrl for JSON and the public API. No `grpc_pass`. |
| versiond-router (HA) | Published gRPC listen. Mount `SSL_CERT_SOURCE` (`./secrets/nginx-ssl`) **on the router**. `bind … ssl crt … proto h2` when InferenceUrl is HTTPS; **`proto h2` on every backend** to versiond (h2c). Keep version + escrow hash on `:path` |
| versiond (non-HA) | Published gRPC listen wrapped with `h2c.NewHandler`. This is the first hop when there is no router. HTTPS: same cert on a tiny HAProxy in front of that listen |
| versiond (both) | `h2c.NewHandler` on the listen that serves `/rpc/`; reverse-proxy with an **HTTP/2** transport to the child (not the default HTTP/1.1 `ReverseProxy`); still accept HTTP/1.1 from nginx for JSON |
| `devshardd` | Wrap the existing loopback `http.Server` with `h2c.NewHandler`. Same Connect mux, no extra bind |

The client dials `{InferenceUrl.host}:{DEVSHARD_RPC_H2_PORT}` with InferenceUrl's
scheme (HTTPS → TLS + ALPN `h2`, same hostname for SNI/verify; HTTP → h2c). That env is
the network-wide gRPC port — same number on every participant, not a per-peer address and
not an on-chain field. Unset (or `DEVSHARD_RPC_H2_UPGRADE` off): stay on Connect over
HTTP/1.1 at InferenceUrl. Do not probe any other port. There is no `h2_endpoint` field
(proto field 4 is reserved).

Join's public `:80/:443` stay TCP-to-nginx; they do not carry `/rpc/`. A proxy-router
HTTP/h2 frontend that only dispatches to versiond-router is allowed — nginx still must
not be in the chain, and TLS still terminates on versiond-router (the cert is not
remounted on that dispatcher).

**Why not `grpc_pass`.** That was the previous phase 6 shape. nginx `limit_req` still
counts each stream, and `proxy_pass` cannot multiplex. Skipping nginx removes both the 503
and the need for native gRPC framing. Connect over HTTP/2 multiplexes through HAProxy and
Go **when every hop speaks HTTP/2**. connect-go still serves gRPC on the same mux if we
want one wire protocol.

**What HTTP/2 changes, and what it does not**

| Changes | Does not change |
|---|---|
| One TCP connection per peer **end to end** (Go default 250 streams) | Per-request `SignedEnvelope` |
| nginx `limit_req` / `limit_conn` no longer apply to `/rpc/` | Chat gzip: still application-level stream gzip |
| HPACK on repeated headers (gossip) | Authorization: still attach signature → chain address |
| Admission is Attach + session interceptors | HTTP/1.1 on InferenceUrl remains the mixed-fleet fallback |

The nginx per-IP ceiling disappears for this path. Replace it on the published listen:

- **TCP / HTTP/2 connection rate per IP** (phase 6, stick-table on `src`) — nginx
  `limit_conn` analogue. Handshake and Attach do not bound socket opens.
- **Attach-per-IP before ECDSA** (phase 4) — one connection can still flood handshake RPCs
  on streams.
- HAProxy `maxconn`, Go's stream cap, and the per-peer channel limits.

Phase 1 is not the place for the IP limiter: the child does not see client IPs on
InferenceUrl, and nginx still covers that path.

The listen is **not** an admin port and **not** an IP allowlist. It is the same
participant set as InferenceUrl: Attach (ECDSA bound to this host's gonka address,
then `AllowsSender` on the URL escrow) is the gate; anything without a completed
handshake is dropped. Later data RPCs still check roster for **that** escrow. Attach
is the one unauthenticated RPC on this listen — throttle it before ECDSA (phase 4)
before the port is public.

Phase 6 **reuses nginx's TLS cert** on the published listen: mount `SSL_CERT_SOURCE` on
versiond-router and `bind ssl crt … proto h2`. That is the same host TLS nginx already
did, not a new identity. `channel_binding` stays empty. Inner hops stay h2c. Phase 6 is
multiplexing off nginx, not peer mTLS.

Client: dial `{InferenceUrl.host}:{DEVSHARD_RPC_H2_PORT}` with InferenceUrl's scheme
when that env is set; `DEVSHARD_RPC_H2_UPGRADE=false` never changes InferenceUrl, it
only stops preferring the h2 listen. Unset port means HTTP/1.1 on InferenceUrl.

---

## What a later RPC looks like

After Attach, a `GetSignatures` (the phase 1 proof handler) is still one POST on
InferenceUrl. Phase 1 **requires** `x-devshard-session`; the mux interceptor drops the
request otherwise.

```
POST /devshard/v5/sessions/42/rpc/devshard.transport.v1.SessionService/GetSignatures
x-devshard-session: <token>
```

Echo strips to `/devshard.transport.v1.SessionService/GetSignatures`. The mux calls
`ServeGetSignatures` — the same core as `GET /sessions/42/signatures`. After phase 6, the
method, proto, token, and path shape are unchanged; a fully rolled host multiplexes that
POST on HTTP/2 on **every** hop (versiond-router → versiond → child, or versiond → child),
not nginx. The same handler serves Connect-over-HTTP/1.1 (InferenceUrl fallback) and HTTP/2.

Until phase 7, any behavioral gap between the JSON route and the RPC path is a bug in the
RPC path.
