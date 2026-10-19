# Peer RPC connection setup

Companion to [`grpc-transport-plan.md`](./grpc-transport-plan.md) (design) and
[`grpc-transport-phases.md`](./grpc-transport-phases.md) (execution order). This page is the
**mechanical** picture: how a peer opens an RPC session on InferenceUrl, why phases 1–5
stay HTTP/1.1 (children are loopback-only), and how phase 6 carries HTTP/2 **end to end**
on InferenceUrl's own port, recognised in band by the public proxy so that it **skips
nginx**, with Connect over HTTP/1.1 as the fallback when a peer's origin does not speak it.

Join hop inventory (JSON today, `/rpc/` after phase 6):
[`high-availability-architecture.md`](./high-availability-architecture.md).
HA children share one peer-RPC token:
[Peer RPC sessions under HA](./high-availability-architecture.md#peer-rpc-sessions-under-ha).
Child binary swap vs host evacuation: [`rolling-update.md`](./rolling-update.md). Host leave the
pool: [`versiond-host-evacuation.md`](./versiond-host-evacuation.md). Path versioning:
[`upgrade.md`](./upgrade.md).

**Status.** Phase 1 ships the server handshake, the fail-closed session interceptor, and the
`/rpc/` mount (always on). The client attach loop (`PeerConn`) is phase 2. HTTP/2
is phase 6: **`proxy` (proxy-router)** recognises a peer on the public `:80`/`:443` by
the HTTP/2 preface or the ALPN token `gonka-rpc/1` and carries authenticated `/rpc/`
over HTTP/2 through versiond-router (HA) or versiond (non-HA) to the child, past nginx.
When InferenceUrl is HTTPS, `proxy` terminates the same nginx cert; nginx keeps JSON,
the public API, and Connect over HTTP/1.1 from a peer whose probe missed. The URL shape does not change between those
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
nginx container `proxy`; that is still this HTTP/1.1 path, with no phase 6 hand-off.

Connect's native path is `/{package.Service}/{Method}` (no version, no escrow). Echo is the
translator:

1. Match `Any /sessions/:id/rpc/*`
2. Put `{escrowID}` on the request context
3. Strip `/sessions/{id}/rpc`, leaving `/devshard.transport.v1.PeerAuthService/Attach`
4. Hand that path to `http.ServeMux` (`transport/rpcserver.NewMux`)

The rewrite is a path splice, not a second protocol. Proxies never see the stripped form.

Connect is always mounted at `/sessions/:id/rpc/`. There is no
`DEVSHARD_RPC_SERVER_ENABLED`. Retired Echo session routes answer
`410 http_session_retired` and are not restored. Ops GETs (`diffs`, `mempool`,
`signatures`) stay. A missing host address (no signer or recorder) panics at
`Register` instead of mounting a handler that rejects every Attach as a bad peer.
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
  `AllowsSender`. A failed refresh keeps Watch and the live token, except
  when the host no longer admits that token (below).
- `_` is not a roster, so it cannot open a session. An Attach on `_` whose
  token is expired, forged, or missing is `unauthenticated` (`session
  expired` or `handshake required`), before ECDSA and without spending the
  anonymous floor. It carries no `X-Devshard-Error`, so versiond does not
  charge the origin. The client then clears the token and Attaches on a live
  door. An older child that answers `escrow is not open` on `_` is read the
  same way.
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
        |                       token gone on host: clear token → door Attach
        +-- Watch dies → clear token → Attach again on a live door
```

`WaitReady` (Chat, GetPayload) prefers the waiter's own escrow as the next
door so a later shard is not stranded behind a settled creator id. Most
unaries fail fast on `tokenRequest` instead of waiting.

The client opens the Connect channel and picks a random **attachment id** (`attach_nonce`,
16–32 bytes). This is not an inference or tx nonce; it only names this RPC attach.
That id is covered by the Attach signature together with the host gonka address and a
timestamp. `AttachResponse.session_token` is a stateless HMAC tag over the
peer, host, version, attach time, expiry, and `sha256(attach_nonce)`
([HA layout](./high-availability-architecture.md#peer-rpc-sessions-under-ha)).
The attach time is the earlier of the signed timestamp and the host clock, so
a future-dated signature does not buy lifetime. A replay of the same signed
request returns the same token and cannot shorten another one. A new id from
the same peer adds a token; earlier tokens stay valid until their own expiry.

`Watch` is a server-stream; to nginx it looks like an SSE response and inherits the same
long transfer timeouts. It is liveness only. The server ends it on
`host shutting down` or when its token reaches `session expired`, not only at
the next heartbeat.

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
`devshard.transport.v1` (empty and unknown rejected) → `AllowsSender` for the
URL escrow unless this is a **live** peer on `_`. Then it issues the token.
Nothing is stored: every child of this host and version derives the same key
and checks the tag
([Peer RPC sessions under HA](./high-availability-architecture.md#peer-rpc-sessions-under-ha)).
A real URL escrow is the door, not the session key; `_` is Watch / live-refresh only.

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
phases 1–5 on this path. Phase 6 removes it by carrying HTTP/2 on the same port past nginx.

### HA: one token, several children

The router hashes the escrow segment of every `/{version}/sessions/{escrow}/…`
request, including each HTTP/2 stream. `Attach` uses the door escrow, `Watch`
and live renewal use `_`, and a later call uses that call's escrow. Those
three keys do not select the same child. The token is bound to the peer, host,
and version, and any child of that host and version checks its HMAC with the
same derived key. No table, barrier, or membership feed is involved, and
`GONKA_HA` does not change admission.

Shutdown or EOF on `Watch` reopens the stream with the same token; expiry and
other `Unauthenticated` clear it and `Attach` again. Refresh does not wait for
`Watch` to be up. A refresh on `_` that the host answers `session expired`
or `handshake required` also clears the token and Attaches on a door. A renewal on `_` spends a per-peer bucket (burst 4, 4/min)
before ECDSA instead of the anonymous floor.

A child allows eight concurrent Watches per peer address. That is two replicas,
two generations during a rolling deploy, and two streams per process while a
reopen is still unwinding. Replicas of one participant share a signer, so they
count as one peer, and a second token from that signer shares the cap. A
reconnect holds the live Watch and the replacement at once.

Token layout, key derivation, and lifetime:
[Peer RPC sessions under HA](./high-availability-architecture.md#peer-rpc-sessions-under-ha).

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
| Attach process floor (`spendAttachToken`) | this child | 50 tokens, refilled at 50/s | **Before ECDSA**, on an Attach without a live token. `DEVSHARD_RPC_ATTACH_PER_MIN_TOTAL` at 50 or above selects this bucket. A value `n` below 50 is `n` tokens refilled at `n`/s. `MaxSessions` (10_000) caps Watches, not this bucket. |
| Attach renewal (`takeRenewal`) | peer in a live token | burst 4, 4/min | **Before ECDSA**, on an Attach that presents a live token. Off when limits are disabled. |
| RPC peer weight | session address after Attach | **6000/min**, burst 10% (600) | Authenticated RPCs. Watch is a stream cap, not this bucket. |
| Unknown-escrow first bind (child) | recovered gonka address | **2 unique ids/min**, process floor **300/min** | Cold `GetEscrow` on Attach / owner chat / height-sync seed |
| Unknown-escrow first bind (versiond) | inbound **`X-Real-IP`** | **2 misses/min** | Same bind paths, after the child names a miss — next try never reaches the child |
| `AttachResponse.limits` | advertised + enforced | `messages_per_min`, `messages_burst`, `max_streams`; `ip_weight_per_min` / `ip_burst` = unlimited | Peer numbers match the interceptor. IP fields are unlimited: the child does not key Attach on origin IP. Zeros would look like "refuse all". `PeerConn` paces opted-in RPCs (including Chat) to in-range `messages_per_min` / `messages_burst` × `RPCProcedureWeight`; out-of-range or a wait that cannot fit in 5 s / the RPC deadline is skipped. Watch and Chat also take an advertised `max_streams` slot. |

Nginx `limit_req` / `limit_conn` still cover InferenceUrl until phase 6. They are
not the unknown-id budget and they do not key on gonka address.

### RPC channel weights

One token bucket after handshake. Watch/Chat stay a **concurrent slot cap** (`max_streams`,
default 2561, with Chat stopped at 2560; process-wide Chat stops at 10240). Only the escrow owner takes a Chat slot, after its envelope is read. Weights are protocol constants (not env). Effective max if that
event is the only traffic is `floor(budget / weight)`.

| Bucket | Key | Default | Env |
|---|---|---|---|
| **Peer** | session address after Attach | 6000/min, burst 10% (600) | `DEVSHARD_RPC_MSGS_PER_MIN`, `DEVSHARD_RPC_MSGS_BURST` |

Process-wide Attach before ECDSA is 50 tokens refilled at 50 per second
(`DEVSHARD_RPC_ATTACH_PER_MIN_TOTAL` unset, or any value of 50 or more).
The name is the configured ceiling, not the refill rate: a value below 50
is both the burst and the per-second refill, and a larger value does not
grow the bucket. `MaxSessions` (10_000) is the Watch cap.
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
| Chat | 1 | 6000/min |
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
5. Warmed cache (this host already in `Slots`, or any host that saw create):
   Attach / GetPayload skip `GetEscrow` entirely — no charge. Owner chat and
   height-sync seed still `GetEscrow` live once, but take **no** charge when
   the row names the caller as creator, and refuse with no query when the row
   names someone else. A cached `escrow_not_found` from before the row landed
   is skipped.
6. **Known creators** skip the process floor. A signer is known once it owns
   a session on this host (create or recovery) or a lookup showed it is the
   creator. It still pays the per-peer 2/min, so a key that owns one real
   escrow cannot fan out over unknown ids. The set is bounded (4096, oldest
   dropped). Strangers filling the floor therefore cannot refuse an owner's
   first chat: the owner has a warm row or is known.
7. `ErrChainUnavailable` is not cached. The attempt still charges a stranger;
   a known creator is refunded. A retry can query again.

Over budget returns `ErrEscrowLookupLimited`. JSON maps that to HTTP 429 +
`X-Devshard-Error: escrow_lookup_limited`. Attach maps it to Connect
`resource_exhausted` with the same header. `escrow_not_found` is the miss
that **did** query (or hit the 1 min cache).

The gateway does not treat `escrow_lookup_limited` as a host 429. It is not
retried inside the 5 s non-inference budget (the budget refills over a
minute), chat is never retried on the same host, and the participant limiter
records a failure strike instead of the `http_quarantine_ms` quarantine. The
header is host-set, so a host that keeps answering it reaches that quarantine
at the strike threshold (3).

Validation `GetPayload` is different. A `429`, including
`escrow_lookup_limited`, is `ErrPayloadRateLimited` and is not a verdict.
The validator keeps its lease and fetches once more after 30s, or after
`Retry-After` when that is longer. `escrow_lookup_limited` sends no
`Retry-After` and its budget is a sliding minute, so that retry waits 1 min.
The wait is capped at 1 min. A `429` on the retry votes `Valid:false` with
reason `rate_limited`. The short payload retry (two attempts, 500ms) does
not run for a `429`. A `/rpc/release` during the wait publishes nothing.

The child **must not** key this on origin IP. Mixed fleets and hop-stamped
`X-Real-IP` would collapse every client onto one 2/min slot.

### Wrong escrow id by origin IP (versiond)

The IP cap lives in **versiond**, on the hop that already sees the client.
`SetXForwarded` rewrites `X-Forwarded-*`; versiond **keeps inbound `X-Real-IP`**
from nginx / versiond-router and forwards it to the child. The child still
does not key unknown-escrow misses on it. Phase 6’s `rpc_h2` frontend on **`proxy`**
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
    ├─ versiond (Attach only): 16 already in flight for this IP?
    │     yes → wait up to 5 s for a slot, re-check misses, else 429
    │
    └─ proxy to child
           Attach: process floor → ECDSA → fetchEscrowForBind (per peer)
           JSON chat / height-sync: ECDSA → fetchEscrowForBind
                │
                └─ response X-Devshard-Error: escrow_not_found
                      → versiond records one miss for that X-Real-IP
```

`escrow_lookup_limited` from the child is **not** a miss. It means the child's
process floor is full, which says nothing about the caller: a gateway binding
a cold escrow while strangers hold the floor must not lose its IP.

An escrow miss refuses that IP's next **bind**, not its live sessions.
Session-token RPCs (Chat, GetSignatures, Watch, …) are refused only on
`invalid_session_token` misses. On the bind paths both kinds share the 2/min.

Two distinct fake ids from `203.0.113.9` still hit the child (and spend the
peer's 2/min if the recovered key is the same). The third is stopped at
versiond even if the attacker rotates gonka keys.

A miss is only recorded when the child answers, so a parallel burst would
otherwise all pass. Attach therefore also holds an **in-flight slot** per
origin IP (16) until the child's response headers arrive. A call over the
cap queues up to 5 s and re-checks the miss budget when a slot frees: a burst
of misses stops at about 16, while a burst of real binds just queues. JSON chat
and height-sync are not slotted; their responses can take as long as
inference. A second origin IP is
unaffected. A well-formed bind that returns 2xx or a 403 (wrong owner on a
real escrow) does **not** fill the IP bucket.

Together: rotate keys → child per-peer + process floor; rotate IPs → versiond
per origin; rotate both → still 300 chain lookups/min on that child.

Phase 4's process Attach floor and phase 6 stick-table `conn_rate` are
still the nginx replacements for **handshake flood** and TCP opens. This IP
limiter only stops **unknown escrow-id** fan-out on the three bind paths.

---

## HTTP/2 on the same port, nginx skipped (phase 6)

Same Connect handlers. Same protos. Same `/devshard/{version}/sessions/{id}/rpc/…` path
shape. Children stay on `127.0.0.1`. Authenticated peer RPC does **not** enter nginx.

Join, JSON (and, for a peer whose HTTP/2 probe missed, RPC):

```
client → proxy (proxy-router) :8000/:443 (TCP) → nginx (proxy-policy) → proxy :18081 → versiond-router → versiond :8080 → 127.0.0.1:child
         all HTTP/1.1 after the public listen
```

`devshardd` is not a public port. versiond talks to it as loopback. A second `h2_endpoint`
on the child is either unreachable or a new public hole that skips version routing and
escrow stickiness. That design is rejected. So is a second public port for peer RPC:
two hosts whose wrappings publish different ports cannot talk, the port is one more
thing every operator opens and forwards, and a client has to be told out of band
whether its peer has it.

Phase 6 keeps peer RPC on InferenceUrl's own port and lets the two sides agree on the
transport in band:

- **HTTP InferenceUrl.** The client opens the connection with the HTTP/2 client
  preface (prior-knowledge h2c). `proxy-router`'s `:80` is a TCP frontend: it reads
  the first 24 bytes, recognises the preface, and hands the connection to its `rpc_h2`
  frontend over an abstract socket (`send-proxy-v2`, so `src` is still the client).
  An HTTP/1.x request line goes to nginx as before.
- **HTTPS InferenceUrl.** The client offers one ALPN token, `gonka-rpc/1`
  (`transport.PeerRPCALPN`), not `h2`. `:443` routes a ClientHello carrying it to the
  `rpc_h2` TLS bind, which terminates with nginx's certificate, negotiates that token,
  and runs HTTP/2 on it. Browsers and JSON clients offer `h2` / `http/1.1` and keep
  landing on nginx.
- **An origin that does not speak it.** Cache entries are scoped to scheme,
  hostname and effective port. Explicit protocol/ALPN mismatch pins Connect to
  HTTP/1.1 for 30 minutes ± 10%. Ambiguous connection closure starts at 30 seconds,
  doubles on subsequent failures, and is capped at 30 minutes. One Attach probes
  an origin at a time; successful H2 Attach clears its failure history. Context
  deadlines, DNS/refusal, HTTP/2 stream errors and application errors (including
  429/503) do not declare the origin H1-only. A failed renewal preserves the live
  Watch/token until the normal session lifecycle ends it.

HTTP/2 is required on **every** hop of the h2 path — not only the public listen.
Connect does not turn a Go `http.Server` into h2c; each process must opt in.

```
HA:       client → proxy :80 (h2c preface) / :443 (ALPN gonka-rpc/1)
                   → versiond-router :8081 (proto h2) → versiond :8080 (h2c) → 127.0.0.1:child (h2c)
Non-HA:   client → proxy :80 / :443
                   → versiond :8080 (h2c) → 127.0.0.1:child (h2c)
Fallback: client → proxy :80 / :443 (HTTP/1.1, or TLS with h2 / http/1.1) → nginx → proxy :18081
                   → versiond-router :8080 → versiond → child, Connect over HTTP/1.1
```

versiond-router has one data frontend with two unpublished binds: `:8080` for nginx's
HTTP/1.1 JSON and the HTTP/1.1 fallback, `:8081` for the proxy's `proto h2` dial.
HAProxy detects the HTTP/2 preface on either cleartext bind, and the `_rpc` twin of a
backend is selected by the `/sessions/{id}/rpc/` path, not by port or protocol, so the
fallback reaches the same twin and the same child. The private H2 port remains
`8081`, but intermediate separate-port v6 routers rejected `/rpc/` on `8080`.
They therefore cannot provide this fallback and are not a supported mixed
combination. Follow the fleet → proxy/policy → versiond order in the
[canonical host guide](https://gonka.ai/docs/host/devshard-high-availability/),
including its old-nginx and testnet compatibility exceptions.

A default `httputil.ReverseProxy` (HTTP/1.1) or a child `http.ListenAndServe` without
`golang.org/x/net/http2/h2c` fans N RPCs back into N TCP connections and **fails** this
phase. JSON/SSE, `/v1`, healthz, metrics, ops GETs stay:

```
client → nginx (InferenceUrl) → …   # unchanged
```

| Hop | Change |
|---|---|
| nginx / proxy-policy | **Not on the h2 path.** Keeps InferenceUrl for JSON, the public API, and Connect over HTTP/1.1 from a peer that fell back. No `grpc_pass`. local-test-net's nginx container is also named `proxy` (`proxy/` image) — that is this row. |
| **proxy (proxy-router)** | `:80`/`:443` stay TCP frontends. `tcp-request inspect-delay` (`PROXY_ROUTER_PUBLIC_INSPECT_SECONDS`, 5 s) reads the first bytes; the preface or the ALPN token selects `rpc_h2` over `abns@rpc_h2c` / `abns@rpc_tls`, everything else goes to `proxy-policy`. A connection that starts with `PRI * HTTP/2.0` waits for the whole 24-byte preface rather than being accepted as HTTP on its first line. `rpc_h2`: `proto h2` to versiond-router `:8081` (HA) or `versiond:8080` (non-HA). Stick-table `conn_cur` / `conn_rate` (tracked by `:80`/`:443` on the preface or ALPN, before the hand-off) and path zones on `src,ipmask(32,64)` (one IPv4 address, or one IPv6 /64; table type `ipv6`); a trusted external balancer's client address carries through the PROXY v2 preamble of the hand-off. **Overwrite `X-Real-IP` from the full `src`** (do not forward the caller’s header). HTTPS: mount `SSL_CERT_SOURCE` (`./secrets/nginx-ssl`); `bind abns@rpc_tls … ssl crt … alpn gonka-rpc/1 proto h2`. Keep the same version + escrow hash as `versiond_router_in`. |
| versiond-router (HA) | `:8080` JSON and fallback, `:8081` the proxy's HTTP/2 dial, one frontend. `proto h2` on every backend to versiond (h2c) in the `_rpc` twins, with `check-proto h1`. Keep version + escrow hash on `:path`. Do not re-key per-IP zones (`RemoteAddr` is `proxy`). |
| versiond (both) | HTTP/1.1 and prior-knowledge HTTP/2 on the public listen (`ConfigureCleartextHTTP2`); reverse-proxy with an **HTTP/2** transport to the child (not the default HTTP/1.1 `ReverseProxy`); still accept HTTP/1.1 from nginx for JSON and for the fallback |
| `devshardd` | Wrap the existing loopback `http.Server` with `h2c.NewHandler`. Same Connect mux, no extra bind |

The client dials InferenceUrl's own origin (scheme, host, port; no path). There is no
network-wide port, no per-peer address, and no on-chain field (proto field 4 stays
reserved). `DEVSHARD_RPC_H2_UPGRADE=false` turns the probe off and stays on Connect
over HTTP/1.1; it never changes InferenceUrl. SNI and certificate verify are
InferenceUrl's hostname either way.

Join's `:80/:443` carry both. Do not put HAProxy inside the versiond image. The cert
lives on `proxy`, not on versiond-router. nginx renewal replaces `cert.pem` and reloads
nginx. proxy-router rebuilds the HAProxy PEM from that file and commits it on the admin
socket, so the `rpc_h2` TLS bind serves the renewed certificate.

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

- **Concurrent connections per address** (stick-table `conn_cur` on
  `src,ipmask(32,64)` at **`proxy`**, default 32,
  `PROXY_ROUTER_RPC_MAX_CONNS_PER_SRC`) — the nginx `limit_conn` analogue.
  One IPv4 address, or one IPv6 /64, tracked by `:80`/`:443` as soon as the
  HTTP/2 preface or the peer RPC ALPN is seen, before the hand-off (a trusted
  external balancer's preamble is consumed first, so `src` is the client it
  named). An idle socket that is already tracked holds a `maxconn` slot until
  `timeout client`. A TLS handshake that stalls holds its slot until `timeout
  client-hs` (5s) closes it.
- **TCP / HTTP/2 connection rate per address** (phase 6, stick-table `conn_rate`
  on the same key at **`proxy`**). Handshake and Attach do not bound socket opens.
- **Path zones** (phase 6) on **`proxy`**: Attach / Chat / diffs / gossip as separate
  `src,ipmask(32,64)` budgets. The Connect method is in the URL; no protobuf parse. versiond-router
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
peer in the child, per `X-Real-IP` on versiond after `escrow_not_found`.
That is not Attach-flood control and not an allowlist.
The child still must not key any limiter on `RemoteAddr`.

The listen is **not** an admin port and **not** an IP allowlist. It is the same
participant set as InferenceUrl: Attach (ECDSA bound to this host's gonka address,
then `AllowsSender` on a real URL escrow; live `_` renewals skip the door) is the
gate; anything without a completed handshake is dropped. Later data RPCs still
check roster for **that** escrow. Attach is the one unauthenticated RPC on this
listen — throttle it with the process floor before ECDSA (phase 4) before the
port is public. Per-IP TCP / path rate is phase 6.

Phase 6 **reuses nginx's TLS cert** on the `rpc_h2` TLS bind: mount `SSL_CERT_SOURCE`
on `proxy` and `bind abns@rpc_tls … ssl crt … alpn gonka-rpc/1 proto h2`. That is the
same host TLS nginx already did, not a new identity. `channel_binding` stays empty.
Inner hops stay h2c. Phase 6 is multiplexing off nginx, not peer mTLS.

Client: dial InferenceUrl's origin with the HTTP/2 preface (HTTP) or ALPN
`gonka-rpc/1` (HTTPS); a miss falls back to Connect over HTTP/1.1 on the same origin
for the miss TTL. `DEVSHARD_RPC_H2_UPGRADE=false` never changes InferenceUrl, it only
turns the HTTP/2 probe off.

---

## What a later RPC looks like

After Attach, a `GetSignatures` (the phase 1 proof handler) is still one POST with
the same path shape. Phases 1–5 send it on InferenceUrl (nginx). Phase 6 sends it on the
same InferenceUrl origin over HTTP/2 (`proxy` hands it past nginx). Phase 1 **requires**
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

## Known limitations

### Transport discovery and mixed versions

Discovery is per origin and in band. Explicit protocol mismatch can delay H2
adoption by the cache TTL; connection closure uses the shorter exponential
backoff described above. H1 and H2 Connect ingress share procedure limits on the
updated proxy/policy pair. When the selected RPC twin is empty, a bounded H1 hop
uses the original JSON placement maps. The public and inner routers each enforce
a global active fallback budget; queue admission has a threshold and a short
waiting deadline. These limits are independent of ordinary JSON capacity.
Native gRPC cannot fall back through this hop and receives an unavailable error.
Versiond also checks the selected child on each H2 RPC: a route replaced by an
H1 child returns 503 until ingress health checks select bounded H1 admission.
This closes the gap between asynchronous capability checks and request routing.
A public proxy restart still interrupts existing connections.

### The updater does not probe the HTTP/2 path end to end

`update-devshard.sh --check`, proxy `/readyz`, and `verify-admission` do not
send a peer RPC. The `_rpc` twin's check is HTTP/1.1 `GET /readyz?version=`
on the router admin port `:8404`, then `GET /<version>/healthz` on the
router's `:8081` over HTTP/2, the data dial itself. A router generation
without that listen (a v5 router after a rollback) fails the second step, the
twin empties, and the proxy's `rpc_h2` frontend sends that version's peer RPC
to the JSON sibling backend over HTTP/1.1, where the router routes `/rpc/` by
path; both versioned and coarse checks require HTTP 200 on `:8081`,
and the router verifies its selected RPC pool has an UP server. Non-HA opens
versiond's own listen over HTTP/2 the same way and falls back to versiond
over HTTP/1.1. What the checks do not prove is the child behind the router.
After a
wrapping switch, send one peer RPC through the public port (see the release
guide) rather than trusting `/readyz`. A remote endpoint `{id, host, port}` is
the versiond listen (`8080`); that process has to speak h2c, and the updater
on the network node cannot see which image a remote host runs.

### The `_rpc` twin repeats the health check

Every JSON backend has an `_rpc` twin, and that twin checks its servers on
the same `inter 1s` `fall 1` cadence. Disabled dynamic slots and template
servers with no DNS address are not checked. On a stock join (3 router
slots, 2 versiond, the declared versions) that is on the order of 20 extra
`/readyz` plus `/healthz` requests per second on each versiond, and about 2
extra admin-port checks per second on each router. `/readyz` is an
in-memory snapshot.

Leave the interval where it is. `fall 1` drops a failed server from the
hash ring in about a second. A slower check, or one check shared with the
JSON backend, would leave that server selectable for longer.
