# Peer RPC connection setup

Companion to [`grpc-transport-plan.md`](./grpc-transport-plan.md) (design) and
[`grpc-transport-phases.md`](./grpc-transport-phases.md) (execution order). This page is the
**mechanical** picture: how a peer opens an RPC session on InferenceUrl, why phases 1–5
stay HTTP/1.1 (children are loopback-only), and how phase 6 carries HTTP/2 **end to end**
on a published gRPC listen that **skips nginx**.

Join hop inventory (JSON today, `/rpc/` after phase 6):
[`high-availability-architecture.md`](./high-availability-architecture.md). Child binary
swap vs host evacuation: [`rolling-update.md`](./rolling-update.md). Host leave the
pool: [`versiond-host-evacuation.md`](./versiond-host-evacuation.md). Path versioning:
[`upgrade.md`](./upgrade.md).

**Status.** Phase 1 ships the server handshake, the fail-closed session interceptor, and the
`/rpc/` mount (flag off by default). The client attach loop (`PeerConn`) is phase 2. HTTP/2
is phase 6: deploy publishes `{DEVSHARD_RPC_H2_PORT}` on **`proxy` (proxy-router)** for
authenticated `/rpc/`, with HTTP/2 through versiond-router (HA) or versiond (non-HA) to
the child. When InferenceUrl is HTTPS, `proxy` terminates the same nginx cert; nginx
keeps JSON and the public API. The URL shape does not change between those
phases. First Attach uses a live door id; Watch and TTL refresh use `_`
([Handshake](#handshake-on-that-path)).

---

## Two layers that must not be mixed

| Layer | What it proves | Survives a dispute? |
|---|---|---|
| **Peer session** (`Attach` → `Watch`) | This key opened a session with this host+version | No. Token is connectivity-only. First Attach (and any later Attach on a real escrow URL) checks `AllowsSender` on that id as the door. Live renewals on `/sessions/_/rpc` skip the door. Later data RPCs still check that request's escrow roster. |
| **Signed envelope** on each dispute-bearing RPC | This key sent **this** body at **this** timestamp | Yes. Same hash as today's `X-Devshard-Signature`. |

The session exists so later RPCs can skip ECDSA for admission and so GETs can be authorized.
It never replaces `sha256(escrowID ‖ payload ‖ timestamp_be8)`.

---

## Existing port, existing HTTP/1.1 hops

`Participant.InferenceUrl` already points at the public HTTP listener. On join that
is **`proxy` (proxy-router)** `:80/:443` in TCP mode to nginx (`proxy-policy`), which
then returns `/devshard/` to `proxy :18081` → versiond-router (HA) or versiond
(non-HA) → loopback `devshardd`. That path is forced to HTTP/1.1 on three hops:

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
client → proxy :80/:443 (TCP) → nginx → proxy :18081 → versiond-router → versiond → child
         HTTP/1.1 after the public listen
         POST /devshard/v5/sessions/42/rpc/.../Attach
         Echo /sessions/:id/rpc/*  →  strip prefix → Connect mux
```

Non-HA genesis skips versiond-router (`nginx → versiond`). local-test-net names its
nginx container `proxy`; that is still this HTTP/1.1 path, not the phase 6 h2 bind.

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

One `PeerConn` per **(host, version, BaseURL, signer)**, shared across every
escrow that child serves. Version is in the key because `/devshard/{version}/`
selects a child; a v5 token is not valid on v6. Escrow is **not** in the key:
Attach authenticates to the host's gonka address, and the same token is sent on
every `/sessions/{id}/rpc/` path of that child.

The attach loop does not start at registry insert. `SelectTransport` /
`acquirePeerConn` only takes a ref. `NewRPCClient` registers that client's
escrow as an **attach door**, then `Start`s so the first Attach already has a
real id. The last `RPCClient.Close` drops that door and `Release`s the
`PeerConn`. `_` (`HostRPCEscrowID`) is never a door.

The token is keyed to **peer identity**, not to a TCP socket — HTTP/1.1 may open
several pooled connections and they all carry the same token.

Two objects share that token:

- **`PeerConn`** — one Attach/Watch per (host, version, URL, signer). Shared
  across every escrow that child serves. First handshake and post-loss
  re-Attach use `/sessions/{door}/rpc` for a **currently bound** live
  `RPCClient` escrow. Live TTL refresh and Watch use `/sessions/_/rpc`.
- **`transport.Server` / DB session** — one per escrow. JSON `BindOwnerChat`
  and RPC `Chat` / `SeedHeightSync` call `SessionForOwner`: Existing + owner,
  or CreateSession only when the handshake peer is the escrow creator (the
  gateway). Version is this child's `boundVersion` (the URL the gateway
  chose). Slot-member Chat is PermissionDenied and does not bind.
  `ChallengeReceipt` (JSON `BindGroupPeer` or RPC `SessionForStartProof`) may
  CreateSession when the body carries a **creator-signed** `MsgStartInference`
  whose `protocol_version` matches this child — so a host that never saw
  owner chat can still be challenged without letting a peer pick the version.
  Gossip, repair, and observability GETs stay Existing-only.

A live handshake on escrow A is not a session for escrow B. Challenge on B
with the host-wide token CreateSession for B only with that start proof;
GetDiffs on B does not. Slot-member Chat on B does not bind; owner Chat on B does.

### Membership and renewal

First Attach, and any later Attach whose URL is a real escrow id, only succeed
if the recovered key may use **that** id (`AllowsSender`). `_` is not a roster
id: a live peer renewing on `/sessions/_/rpc` skips the door so TTL refresh
still works after the original shard is gone. A Watch drop that re-Attaches on
a settled real id does **not** skip — the server re-runs `AllowsSender`, the
client marks that door dead, and it tries another live escrow. Outsiders never
get a token.

If that door escrow is not open locally, Attach still admits a creator or slot
member after a chain roster check — it does **not** CreateSession / bind a
version. A stranger probing a cold id does not get a token.
Signature, host address, timestamp, and nonce checks still run first.
`escrow_not_found` / `escrow_settled` (`failed_precondition`) is a **dead
door**, not a host-down: the client keeps the `PeerConn` and retries another
bound escrow inside `DefaultAttachTimeout`.

Renewal is a new Attach with a **new** `attach_nonce`, not a TTL refresh of the old
one:

- Same nonce while it is still live → rejected (`attach_nonce already in use`).
- Same peer, new nonce → new token; the old one stays valid for **5s** so in-flight
  RPCs still admit.
- Live TTL refresh posts that Attach on `_` and does **not** re-run
  `AllowsSender`. A failed refresh keeps Watch and the live token.
- Re-Attach after Watch loss posts on a live door and **does** re-run
  `AllowsSender`. Dropped from that shard's roster, or a settled id, cannot
  use that URL as a door.

The token itself is still host-wide: Attach via one escrow you belong to, then
use it on every escrow path this child serves. Later RPCs are admitted by session id
(`x-devshard-session`); they do not repeat the Attach door. ChallengeReceipt may
CreateSession for a cold escrow with a gateway start proof; gossip, repair, and
observability GETs do not. Every data RPC still checks roster for **that**
request's escrow.

```
  SelectTransport → acquirePeerConn (ref only; loop not started)
        |
        |  NewRPCClient: addDoor(escrow) → Start
        v
  unauthenticated
        |  no usable door → WaitReady / Send fail-fast ("no attach door")
        |                   attach loop BackoffMin; PeerConn stays until last Close
        |
        |  Attach on /sessions/{door}/rpc
        |    (client UUID in attach_nonce, ECDSA over attach domain,
        |     AllowsSender on that id)
        |    settled / not open → killDoor, try next live escrow
        v
      ready  ── session id is the client's UUID (echoed as session_token)
        |
        |  Watch on /sessions/_/rpc  (server-stream heartbeats every ~30s)
        |
        +-- token at ~75% TTL → Attach on /sessions/_/rpc (skip AllowsSender)
        |                       fail: keep Watch + token, retry refresh
        +-- Watch dies → clear token → Attach again on a live door
```

`WaitReady` (Chat, GetPayload) prefers the waiter's own escrow as the next
door so a later shard is not stranded behind a settled creator id. Most
unaries fail fast on `tokenRequest` instead of waiting.

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
live → `AllowsSender` for the URL escrow unless this is a **live** peer on `_`.
Then it records one session for that client peer on this child. A real URL escrow
is the door, not the session key; `_` is Watch / live-refresh only.

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

While unauthenticated, later RPCs **fail fast**. They do not wait on Attach.
When every bound door is dead or dropped, `WaitReady` / `Send` fail immediately
(`no attach door`) instead of parking on Attach backoff. Reconnect uses
jittered backoff (50 ms → 5 s), same idea as the CometBFT WS listener, except
`errNoAttachDoor` stays at `BackoffMin` and does not `Close` the `PeerConn`.

HTTP/1.1 keepalive is a **pool**, not one multiplexed connection. N concurrent RPCs need N
TCP connections; a 30-minute `Chat` holds one for its whole life. `MaxIdleConnsPerHost`
must rise to cover concurrent chats plus gossip, queries, and `Watch`. That is the cost of
phases 1–5 on this path. Phase 6 removes it by carrying HTTP/2 on a listen that skips nginx.

---

## Rate limits

Admission (Attach + roster) is not a budget. A live token, or a first-bind that
must ask chain, can still cost CPU, ECDSA, and `GetEscrow`. Do not key the
child's buckets on `RemoteAddr` (always versiond) or on claimed `peer_address`.
Unknown-id fan-out is the special case: **per peer in the child**, **per origin
IP on versiond**.

| Layer | Key | Default | When it fires |
|---|---|---|---|
| JSON POSTs after auth (`RateLimitMiddleware`) | recovered sender | 100 rps, burst 200 | Existing session on InferenceUrl. Chat still records `no_receipt_interrupted`. |
| Attach process floor (`chargeAttach`) | this child | 10_000/min (`MaxSessions`) | **Before ECDSA.** Known-peer renewals are refunded. |
| RPC peer weight | session address after Attach | **6000/min**, burst 10% (600) | Authenticated RPCs. Watch is a stream cap, not this bucket. |
| Unknown-escrow first bind (child) | recovered gonka address | **2 unique ids/min**, process floor **300/min** | Cold `GetEscrow` on Attach / owner chat / height-sync seed |
| Unknown-escrow first bind (versiond) | inbound **`X-Real-IP`** | **2 misses/min** | Same bind paths, after the child names a miss — next try never reaches the child |
| `AttachResponse.limits` | advertised + enforced | `messages_per_min`, `messages_burst`, `max_streams`; `ip_weight_per_min` / `ip_burst` = unlimited | Peer numbers match the interceptor. IP fields are unlimited: the child does not key Attach on origin IP. Zeros would look like "refuse all". `PeerConn` paces opted-in RPCs (including Chat) to in-range `messages_per_min` / `messages_burst` × `RPCProcedureWeight`; out-of-range or a wait that cannot fit in 5 s / the RPC deadline is skipped. Watch and Chat also take an advertised `max_streams` slot. |

Nginx `limit_req` / `limit_conn` still cover InferenceUrl until phase 6. They are
not the unknown-id budget and they do not key on gonka address.

### RPC channel weights

One token bucket after handshake. Watch/Chat stay a **concurrent slot cap** (`max_streams`,
default 256). Weights are protocol constants (not env). Effective max if that
event is the only traffic is `floor(budget / weight)`.

| Bucket | Key | Default | Env |
|---|---|---|---|
| **Peer** | session address after Attach | 6000/min, burst 10% (600) | `DEVSHARD_RPC_MSGS_PER_MIN`, `DEVSHARD_RPC_MSGS_BURST` |

Process-wide Attach before ECDSA stays 10_000 (`DEVSHARD_RPC_ATTACH_PER_MIN_TOTAL`).
Per-IP Attach is not a child limiter: `RemoteAddr` is versiond, and an empty
`X-Real-IP` is not a key. TCP / path rate belongs on versiond / Phase 6 `proxy`
(`src`). `DEVSHARD_RPC_LIMITS=off` disables the interceptor buckets and the Attach floor.
`DEVSHARD_RPC_MAX_STREAMS_PER_PEER` is the stream cap. Unset env is the default
with no log; malformed, `0`, and `4294967295` warn and use the default (`-1`
is unlimited).

| Event | Weight | Implied max |
|---|---|---|
| GetSignatures, Gossip Nonce | 1 | 6000/min |
| Gossip Txs, seed / repair / verify-* | 2 | 3000/min |
| Chat | 10 | 600/min |
| GetMempool, GetPayload | 6 | 1000/min |
| GetDiffs | 60 | 100/min |
| Attach / Watch | 0 | floor / stream cap, not this bucket |

The handshake gate charges the process floor on a `Content-Length` larger
than 4 KiB, then rejects with no decode. A token issued for a real door
refunds the floor slot for a live/grace peer.

GetDiffs shares the peer pie: 100 diffs at weight 60 is the whole 6000 for
that minute. There are no dedicated GET buckets and no `attach_per_min` count
of attaches. A single RPC heavier than `messages_burst` (GetDiffs 60 vs a
10-token pulse) still runs when the bucket is full, then overdrafts; cheap
RPCs stay capped at the advertised burst.

### Per-peer (child)

After handshake, JSON POSTs spend a **per-sender** token bucket. Two peers do not
share it. Authenticated RPCs spend the **peer weight** budget above (`RPCProcedureWeight`).

The **unknown-escrow** budget is separate and stricter. `fetchEscrowForBind`
runs when owner chat or first Attach finds no local session and no fresh
`escrow_cache` row. Eligibility (owner / group slot) lives on the escrow record,
so the query has to run **before** the host knows the peer belongs. Unique unknown
or ineligible ids are charged **before** that query:

1. Process floor: 300 cold lookups/min across all peers.
2. Per peer: 2 distinct ids/min.
3. Same id is cached **1 min** (`escrow_not_found` / settled / success). A
   retry of that id does not re-query or re-charge.
4. A successful load that shows the peer is the **creator or a slot member**
   is **refunded**, so first bind of a real escrow does not consume the miss
   budget. A stranger probing a real id keeps the charge.
5. Warmed cache (this host already in `Slots`, or any host that saw create)
   skips `GetEscrow` entirely — no charge.
6. `ErrChainUnavailable` is not cached. The attempt still charges; a retry can
   query again.

Over budget returns `ErrEscrowLookupLimited`. JSON maps that to HTTP 429 +
`X-Devshard-Error: escrow_lookup_limited`. Attach maps it to Connect
`resource_exhausted` with the same header. `escrow_not_found` is the miss
that **did** query (or hit the 1 min cache).

The child **must not** key this on origin IP. Mixed fleets and hop-stamped
`X-Real-IP` would collapse every client onto one 2/min slot.

### Wrong escrow id by origin IP (versiond)

The IP cap lives in **versiond**, on the hop that already sees the client.
`SetXForwarded` rewrites `X-Forwarded-*`; versiond **keeps inbound `X-Real-IP`**
from nginx / versiond-router and forwards it to the child. The child still
does not key unknown-escrow misses on it. Phase 6’s h2 listen: **`proxy`**
overwrites `X-Real-IP` from `src` (nginx’s `$remote_addr` job); versiond still
copies, it does not re-derive from `RemoteAddr`.

Only first-bind POSTs count:

- `/sessions/{id}/chat/completions`
- `/sessions/{id}/height-sync`
- `/sessions/{id}/rpc/…/PeerAuthService/Attach`

`Watch`, `GetDiffs`, gossip, and other RPCs are not this limiter. `X-Forwarded-For`
is ignored (caller-controlled). A missing or garbage `X-Real-IP` **skips** the
bucket so an old hop that does not stamp the client cannot collapse the host onto
loopback.

The bucket fills **after** a child miss, not before ECDSA:

```
POST bind path
    │
    ├─ versiond: this origin IP already has 2 misses in the last minute?
    │     yes → 429, X-Devshard-Error: escrow_lookup_limited, do not proxy
    │
    └─ proxy to child
           Attach: process floor → ECDSA → fetchEscrowForBind (per peer)
           JSON chat / height-sync: ECDSA → fetchEscrowForBind
                │
                └─ response X-Devshard-Error in
                   {escrow_not_found, escrow_lookup_limited}
                      → versiond records one miss for that X-Real-IP
```

Two distinct fake ids from `203.0.113.9` still hit the child (and spend the
peer's 2/min if the recovered key is the same). The third is stopped at
versiond even if the attacker rotates gonka keys. A second origin IP is
unaffected. A well-formed bind that returns 2xx or a 403 (wrong owner on a
real escrow) does **not** fill the IP bucket.

Together: rotate keys → child per-peer + process floor; rotate IPs → versiond
per origin; rotate both → still 300 chain lookups/min on that child.

Phase 4's process Attach floor and phase 6 stick-table `conn_rate` are
still the nginx replacements for **handshake flood** and TCP opens. This IP
limiter only stops **unknown escrow-id** fan-out on the three bind paths.

---

## HTTP/2, nginx skipped (phase 6)

Same Connect handlers. Same protos. Same `/devshard/{version}/sessions/{id}/rpc/…` path
shape. Children stay on `127.0.0.1`. Authenticated peer RPC does **not** enter nginx.

Join today (JSON and, until phase 6, RPC):

```
client → proxy (proxy-router) :8000/:443 (TCP) → nginx (proxy-policy) → proxy :18081 → versiond-router → versiond :8080 → 127.0.0.1:child
         all HTTP/1.1 after the public listen
```

`devshardd` is not a public port. versiond talks to it as loopback. A second `h2_endpoint`
on the child is either unreachable or a new public hole that skips version routing and
escrow stickiness. That design is rejected.

Phase 6 publishes `{DEVSHARD_RPC_H2_PORT}` on the existing **`proxy`** container and takes
nginx off `/rpc/`. HTTP/2 is required on **every** hop of that path — not only the public
listen. Connect does not turn a Go `http.Server` into h2c; each process must opt in.

```
HA:     client → proxy (TLS+h2 if InferenceUrl is HTTPS, else h2c)
                 → versiond-router (h2c, proto h2) → versiond (h2c) → 127.0.0.1:child (h2c)
Non-HA: client → proxy (TLS+h2 if InferenceUrl is HTTPS, else h2c)
                 → versiond (h2c) → 127.0.0.1:child (h2c)
```

A default `httputil.ReverseProxy` (HTTP/1.1) or a child `http.ListenAndServe` without
`golang.org/x/net/http2/h2c` fans N RPCs back into N TCP connections and **fails** this
phase. JSON/SSE, `/v1`, healthz, metrics, ops GETs stay:

```
client → nginx (InferenceUrl) → …   # unchanged
```

| Hop | Change |
|---|---|
| nginx / proxy-policy | **Not on `/rpc/`.** Keep InferenceUrl for JSON and the public API. No `grpc_pass`. local-test-net's nginx container is also named `proxy` (`proxy/` image) — that is this row, not the h2 bind. |
| **proxy (proxy-router)** | Public `{DEVSHARD_RPC_H2_PORT}` bind. Skip `proxy-policy`. `proto h2` to versiond-router (HA) or `versiond:8080` (non-HA). Stick-table `conn_rate` / `sess_rate` and path zones on `src`. **Overwrite `X-Real-IP` from `src`** (do not forward the caller’s header). HTTPS: mount `SSL_CERT_SOURCE` (`./secrets/nginx-ssl`) and `bind ssl crt … proto h2`. Keep the same version + escrow hash as `versiond_router_in`. `:80/:443` stay TCP-to-nginx. |
| versiond-router (HA) | Inner hop. `proto h2` on the frontend from `proxy` **and** on every backend to versiond (h2c). Keep version + escrow hash on `:path`. Do not re-key per-IP zones (`RemoteAddr` is `proxy`). |
| versiond (both) | `h2c.NewHandler` on the listen that serves `/rpc/`; reverse-proxy with an **HTTP/2** transport to the child (not the default HTTP/1.1 `ReverseProxy`); still accept HTTP/1.1 from nginx for JSON |
| `devshardd` | Wrap the existing loopback `http.Server` with `h2c.NewHandler`. Same Connect mux, no extra bind |

The client dials `{InferenceUrl.host}:{DEVSHARD_RPC_H2_PORT}` with InferenceUrl's
scheme (HTTPS → TLS + ALPN `h2`, same hostname for SNI/verify; HTTP → h2c). That env is
the network-wide gRPC port — same number on every participant, not a per-peer address and
not an on-chain field. Unset (or `DEVSHARD_RPC_H2_UPGRADE` off): stay on Connect over
HTTP/1.1 at InferenceUrl. Do not probe any other port. There is no `h2_endpoint` field
(proto field 4 is reserved). `DEVSHARD_RPC_H2_HOST` (testenv overlay `proxy`) is the TCP
dial name only; SNI and certificate verify still use InferenceUrl’s hostname (the nginx
SAN). Do not put `proxy` in SNI.

Join's public `:80/:443` stay TCP-to-nginx; they do not carry `/rpc/`. Do not put
HAProxy inside the versiond image. The cert lives on `proxy`, not on versiond-router.

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

- **TCP / HTTP/2 connection rate per IP** (phase 6, stick-table on `src` at **`proxy`**)
  — nginx `limit_conn` analogue. Handshake and Attach do not bound socket opens.
- **Path zones** (phase 6) on **`proxy`**: Attach / Chat / diffs / gossip as separate
  `src` budgets. The Connect method is in the URL; no protobuf parse. versiond-router
  and inner versiond on HA must not re-key on `RemoteAddr` (`proxy`). The child must not
  apply Echo IP zones on `/rpc/` (loopback).
- **Process-wide Attach floor before ECDSA** (phase 4) in the child. Per-IP
  handshake rate is not keyed here (`RemoteAddr` is versiond). Phase 6 `proxy`
  path zones + TCP `conn_rate` on `src` replace nginx `limit_req` on handshake.
- **Per-peer channel limits** (phase 4) in the **child** interceptor: token-bucket plus
  per-method weights keyed on the session peer, not on IP. Dedicated per-session
  caps: `GetDiffs` 100/min, `GetMempool` 1000/min, `GetPayload` 1000/min.
- HAProxy `maxconn`, Go's stream cap, and the per-peer channel limits.

Unknown-escrow first-bind is already split (see **Rate limits** above): per gonka
peer in the child, per `X-Real-IP` on versiond after `escrow_not_found` /
`escrow_lookup_limited`. That is not Attach-flood control and not an allowlist.
The child still must not key any limiter on `RemoteAddr`.

The listen is **not** an admin port and **not** an IP allowlist. It is the same
participant set as InferenceUrl: Attach (ECDSA bound to this host's gonka address,
then `AllowsSender` on a real URL escrow; live `_` renewals skip the door) is the
gate; anything without a completed handshake is dropped. Later data RPCs still
check roster for **that** escrow. Attach is the one unauthenticated RPC on this
listen — throttle it with the process floor before ECDSA (phase 4) before the
port is public. Per-IP TCP / path rate is phase 6.

Phase 6 **reuses nginx's TLS cert** on the published listen: mount `SSL_CERT_SOURCE` on
`proxy` and `bind ssl crt … proto h2`. That is the same host TLS nginx already
did, not a new identity. `channel_binding` stays empty. Inner hops stay h2c. Phase 6 is
multiplexing off nginx, not peer mTLS.

Client: dial `{InferenceUrl.host}:{DEVSHARD_RPC_H2_PORT}` with InferenceUrl's scheme
when that env is set; `DEVSHARD_RPC_H2_UPGRADE=false` never changes InferenceUrl, it
only stops preferring the h2 listen. Unset port means HTTP/1.1 on InferenceUrl.

---

## What a later RPC looks like

After Attach, a `GetSignatures` (the phase 1 proof handler) is still one POST with
the same path shape. Phases 1–5 send it on InferenceUrl (nginx). Phase 6 sends it on
`{InferenceUrl.host}:{DEVSHARD_RPC_H2_PORT}` (`proxy`, skip nginx). Phase 1 **requires**
`x-devshard-session`; the mux interceptor drops the request otherwise.

```
POST /devshard/v5/sessions/42/rpc/devshard.transport.v1.SessionService/GetSignatures
x-devshard-session: <token>
```

Echo strips to `/devshard.transport.v1.SessionService/GetSignatures`. The mux calls
`ServeGetSignatures` — the same core as `GET /sessions/42/signatures`. After phase 6, the
method, proto, token, and path shape are unchanged; a fully rolled host multiplexes that
POST on HTTP/2 on **every** hop (`proxy` → versiond-router → versiond → child, or
`proxy` → versiond → child), not nginx. The same handler serves Connect-over-HTTP/1.1
(InferenceUrl fallback) and HTTP/2.

Until phase 7, any behavioral gap between the JSON route and the RPC path is a bug in the
RPC path.
