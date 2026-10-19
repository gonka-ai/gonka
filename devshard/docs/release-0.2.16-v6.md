# Release guide: `devshard-0.2.16-v6`

Operator-facing contract for the v6 deployment line, and the manual regression
that has to pass before the network wrapping is switched.

Release lines:

| Line | What it carries |
| --- | --- |
| `release/v0.2.16-devshard-v6.0.0` | Chain upgrade plus this devshard line |
| `release/devshard/v6.0.0` | Devshard images: `devshardd`, gateway, and the wrapping (`versiond`, `versiond-router`, `proxy`, `proxy-router`) |

Previous line: [release-0.2.15-v5.md](./release-0.2.15-v5.md).
Peer RPC path: [grpc-transport-connection.md](./grpc-transport-connection.md).
Architecture: [high-availability-architecture.md](./high-availability-architecture.md).
Load simulation: [gateway-load-simulation.md](./gateway-load-simulation.md).

## What this release is

v6 adds an authenticated peer RPC, gzip on that RPC, and rate limits keyed on
the authenticated peer. It also carries the load-simulation harness and the
security and performance patches listed below.

The binary and the wrapping do not move together.

1. Governance auto-adds the v6 `devshardd` and gateway onto the wrapping that
   is already running (v4 or v5 `versiond`, `versiond-router`, `proxy`,
   `proxy-router`). Peer RPC is Connect over HTTP/1.1 on InferenceUrl. There
   is no multiplexing: in-flight requests do not share one connection.
2. Operators then switch the wrapping to the v6 images. Peer RPC moves to
   HTTP/2 on the same InferenceUrl port. Each peer keeps one connection, end
   to end. That path is what turns on the new per-connection limits.
3. The v6 wrapping must still serve v4 and v4.2 children over the JSON path
   on InferenceUrl.

Until step 2, do not expect one connection per peer or the public
connection-rate limits. Those exist only behind the v6 `proxy-router`. There
is no new port in either step: a peer finds out in band whether this host's
port speaks HTTP/2, and uses HTTP/1.1 through nginx when it does not.

## What changes for an operator

- **Peer RPC is authenticated.** A peer proves its key once with `Attach`
  (ECDSA, bound to this host and version). Later RPCs send the session token
  (`x-devshard-session`). The token is the peer's identity, not a property of
  a TCP socket, so the several HTTP/1.1 connections of step 1 still share it.
  Dispute-bearing calls still carry the signed envelope. One session is shared
  across the escrows that child serves; version is part of the key, so a v5
  token is not a v6 token.
- **On the old wrapping, gRPC is simulated.** Connect rides InferenceUrl as
  ordinary HTTP/1.1. nginx (or the v5 policy worker) still sees `/rpc/`.
  Native gRPC is not selected. Each in-flight RPC is its own connection.
- **On the v6 wrapping, the same RPC is HTTP/2 on the same port.** A peer
  opens its connection to InferenceUrl with the HTTP/2 preface (HTTP) or
  offers ALPN `gonka-rpc/1` (HTTPS). `proxy-router` recognises that on
  `:80`/`:443` and keeps the connection in its own process, past nginx.
  HTTP/2 runs on every hop: client → `proxy-router` → `versiond-router` (HA)
  or `versiond` (non-HA) → `versiond` → the loopback child. One TCP
  connection per peer carries that peer's chats, Watch, and the other RPCs.
  Nothing new is opened in a firewall and nothing is set in `config.env`.
- **A peer whose host is still on the old wrapping is not cut off.** nginx
  answers the preface with HTTP/1.1 and negotiates no ALPN for the token;
  the client treats an explicit protocol mismatch as "no HTTP/2 here", remembers it for 30 minutes ± 10%,
  and continues over Connect on HTTP/1.1 through nginx. It probes HTTP/2
  again after that, so a host that updates later is picked up without a
  restart. Ambiguous connection closure instead starts a 30-second exponential
  backoff. `DEVSHARD_RPC_H2_UPGRADE=false` in the versiond process environment
  turns outbound probing off; changing a host file alone does not update a
  running process.
- **Rate limiting grows a second layer when that connection exists.** The
  child limits the authenticated peer on both wrappings. The public
  connection limits apply to direct H2 RPC. Procedure/IP limits are shared
  between direct H2 and Connect through the updated nginx policy workers.
- **JSON does not move.** Chat-compat, `/v1`, health, and the public API stay
  on `:80`/`:443`. v4 and v4.2 children keep using that path.

`proxy` and `proxy-router` are different ingresses. On a v4 host, `proxy` is
nginx. From v5 on, the join service named `proxy` runs the `proxy-router`
image (HAProxy); nginx is the `proxy-policy` worker behind it. Confirm the
image before deciding which transport a host is on. Only the HAProxy image
recognises peer RPC; behind the nginx image every peer stays on HTTP/1.1.

## What's in this release

| Area | Change |
| --- | --- |
| **Peer RPC and authentication** | Connect on `/devshard/{version}/sessions/{id}/rpc/...`. `Attach` opens a session; `Watch` keeps it. Admission after that is the session token. The signed envelope still covers dispute-bearing bodies |
| **gzip** | Chat responses are one gzip stream for the whole completion (compression window kept across events, not one gzip per frame). Request gzip is inflated only up to a decoded cap, and a body past that cap is rejected before the handler. `GET /devshard/stats/rpc` is gzip when the caller sends `Accept-Encoding: gzip` |
| **Per-peer rate limits** | After `Attach`, the child charges the authenticated peer: 6000 weight/min, burst 600. Concurrent chats per peer default to 2560, a quarter of the 10240 process cap. Only the escrow owner takes a chat slot. `GetDiffs` is expensive (weight 60) so it cannot spend the chat budget. Anonymous `Attach` is capped before the signature check |
| **Per-connection rate limits** | v6 `proxy-router` only, on connections it recognised as peer RPC. One HTTP/2 connection per peer. That connection is limited per client address: 32 concurrent connections, 200 new connections/s, plus a separate request budget per RPC path |
| **Gateway Postgres** | `DEVSHARD_STORAGE_MODE=hybrid` or `postgres` keeps gateway management state in Postgres. `sqlite` stays on local `gateway.db` and ignores `PGHOST`. The same mode applies to `perf.db` |
| **Load testing** | YAML scenarios drive the gateway through an isolated Docker testenv: concurrent traffic, mock ML-node faults, host pause, catch-up, escrow rotation, and miss-rate checks. In-process `loadsim` remains for gateway and `perf.db` profiles |
| **Refusal vote** | Already on `devshard/v4.2` and included here. A receipt that arrives during the refusal wait ends that wait. The gateway does not ask verifiers for a refusal once the inference is confirmed. An unsigned or forged receipt cannot cancel the vote |
| **Security patches** | Timeout evidence counts only when the executor signed it. A refusal is challenged from the executor's verified tip, and the tip is served only on the peer session. A stored payload keeps the first writer's bytes and still records the hashes. Pull-request CI does not receive the GCP key or the package token |
| **Performance patches** | Diff catch-up is paged: one read is at most 64 nonces and 8 MiB of stored transactions, and the gateway sends at most 200 diffs in one chunk. Gateway, accounting, stats, and inference-stats Postgres pools are capped. Process memory totals are read without stopping the world. Height-sync heartbeats stay off unless opted in |

Decoded caps on the RPC (the gzip bomb bound): handshake and ordinary unaries
16 KiB; chat, dispute calls, gossip transactions, `GetDiffs` and `GetMempool`
10 MiB. `GetPayload` is capped per inference, up to the existing payload
ceiling.

### Refusal vote after a late receipt

Merged on `devshard/v4.2` and included in this line. A gateway that sees no
receipt starts a refusal wait. If the executor answers during that wait, the
receipt is queued as `MsgConfirmStart` and the inference becomes `Started`.
Asking for a refusal after that makes verifiers answer `expected pending, got
1` and sign nothing.

The receipt now wakes the wait. Before any verifier is contacted, the gateway
checks the confirmation again, on the record or queued for the next diff. If
it is set, the call stops and `Reason` stays empty, so this is not counted as
a refused timeout. A still-pending inference past the deadline is challenged
as before. A receipt with no confirmation stamp, or a receipt for another
inference, leaves the wait running.

The stamp is accepted only after the same signature check as apply: the signer
recovered from `ExecutorSig` must be the assigned executor. An unsigned or
forged receipt cannot set the local confirmation and suppress the only vote
that records the miss. A warm key is stored only when that confirm is applied.

`TestHandleTimeoutDoesNotChallengeARefusalAfterTheReceipt` and the pending,
unstamped, wrong-nonce, and cancel cases next to it already cover this. A
manual pass is not required for this fix.

### Docker load tests

Scenarios live under `devshard/testenv/loadtest/scenarios/`. A run stands up
an isolated Compose environment, sends concurrent chat through the gateway,
and can pause a host or point mock ML nodes at fast, slow, failing, or
timeout behavior. The shipped scenarios cover normal load, a spike, catch-up
(including a 15-minute catch-up and a miss-rate catch-up), escrow rotation,
long escrows at 32 and 64 requests per second, and a single slow, failing, or
timing-out ML node. Profiles and CPU, heap, goroutine, and container logs are
kept with the run.

```bash
make -C devshard/testenv loadtest SCENARIO=normal-load
```

The in-process `loadsim` tag is a separate measurement. It does not start
Docker, and it does not exercise the host transport. See
[gateway-load-simulation.md](./gateway-load-simulation.md).

[Devshard load tests v6](https://github.com/gonka-ai/gonka/actions/runs/38140509094/job/114477018374)
(`8b29799`, job "Build and Test Devshard") did not run these scenarios. The
unit suite reported 6263 tests and 1 failure,
`TestPeerConn_TenEscrowsShareOneH2Conn`: ten escrows were expected to share
one HTTP/2 connection, and the test saw 9. MockEnv and E2E were skipped after
that, so the Docker scenarios did not start.

### Gateway Postgres

`DEVSHARD_STORAGE_MODE` selects the gateway store:

| Mode | Store |
| --- | --- |
| `sqlite` | Local `gateway.db`. `PGHOST` is ignored |
| `hybrid`, `postgres` | Postgres is required. Startup imports an existing `gateway.db` once and drains leftover sync-journal rows, then serves from Postgres only. If Postgres is down, the gateway does not start |

That store is gateway management state: settings, devshard rows, participant
throttle, rotation status, suspicious hosts, and escrow rotation commitments.
Escrow execution state stays on the host store. `perf.db` follows the same
mode through `PerfStore`: `sqlite` keeps the local file; `hybrid` and
`postgres` import it once into the gateway database and fail closed when
Postgres is unreachable.

### Diff catch-up is capped

A catch-up no longer decodes `1..latest` into one slice.

A stored-diff read (`GetDiffs` on JSON and on the peer RPC, replay, HA
catch-up, gateway recovery) is one page:

| Cap | Value |
| --- | --- |
| Nonces | 64 (`DiffPageMaxNonces`) |
| Stored `txs_proto` | 8 MiB (`DiffPageMaxBytes`) |

A single diff larger than 8 MiB is a page by itself. A wider range is
rejected before any transaction is decoded (`ErrDiffPageLimit`, HTTP 400 on
the JSON route). Callers walk the journal with `ReadDiffPages` /
`GetDiffPages`, apply or send that page, and drop it.

The gateway's own send is a second cap. `sendCatchUp` ships at most 200 diffs
per request (`catchUpChunkSize`), each chunk on its own 60s deadline, and
reads the next chunk only after the previous one is sent. A chat or other
request attaches at most one such chunk. A host further behind is taught the
earlier nonces first, outside that request.

### Per-peer limits (v6 child, both wrappings)

Enforced in the child after the session exists. Keyed on the authenticated
peer, not on the TCP connection and not on `RemoteAddr` (that address is
loopback). Defaults:

| Limit | Default | Env |
| --- | --- | --- |
| Weight per minute | 6000 | `DEVSHARD_RPC_MSGS_PER_MIN` |
| Burst | 600 (10% of the minute) | `DEVSHARD_RPC_MSGS_BURST` |
| Concurrent chats per peer | 2560 | `DEVSHARD_RPC_MAX_STREAMS_PER_PEER` (streams = chats + 1, so Watch still fits) |
| Concurrent chats per process | 10240 | `DEVSHARD_RPC_MAX_STREAMS_TOTAL` (streams = chats + 1) |
| Chats whose envelope is not read yet | 512 per peer, 2048 per process | fixed. Off when the matching stream cap is unlimited |
| Envelope read deadline | 15s | `PeerAuthConfig.ChatReadTimeout` |
| Deadline per chat frame write | 30s | `PeerAuthConfig.ChatSendTimeout` |
| Watch streams per peer | 8 | fixed |
| `Attach` renewal, before the signature check | burst 4, 4/min | fixed |
| Anonymous `Attach` floor | 50 tokens, refilled at 50/s | `DEVSHARD_RPC_ATTACH_PER_MIN_TOTAL` |

Watch streams per peer is 8: two replicas, two generations during a rolling
deploy, and two streams per process while a reopen is still unwinding.
Replicas of one participant share a signer and count as one peer. A second
token from that signer shares the same cap.

`DEVSHARD_RPC_LIMITS=off` disables the child buckets. Leave it unset in
production.

Weights against the 6000/min budget. A method that is the only traffic can
sustain `floor(6000 / weight)` calls per minute:

| RPC | Weight |
| --- | --- |
| `GetSignatures`, `GetState`, gossip `Nonce` | 1 |
| Gossip `Txs`, seed, repair, verify, challenge | 2 |
| `GetMempool`, `GetPayload` | 6 |
| `Chat` | 1 |
| `GetDiffs` | 60 |
| `Attach`, `Watch` | 0 (their own caps above) |

A full burst therefore admits 10 `GetDiffs` or 600 `Chat` calls immediately,
then the sustained rate. Peer A exhausting the bucket does not charge peer B.
`Chat` weighs 1 because every chat already spends a nonce the escrow pays for;
the budget is there for the RPCs that cost the caller nothing.

A chat slot and its charge are taken only after the child has read the
envelope and resolved the caller as the escrow owner, and before the envelope
signature check. A non-owner, or a caller that never finishes sending the
envelope, holds no chat slot and spends no budget. Until its envelope is read,
a chat counts only against the pending caps above. The child drops it at the
15s read deadline. Once streaming, a chat whose gateway stops reading is dropped
when one frame write passes 30s, and its slot is freed.
`devshard_peer_rpc_chat_dropped_total{reason}` counts these drops
(`pending_cap`, `read_timeout`, `send_timeout`).

### Per-connection limits (v6 `proxy-router` only)

These do not exist on HTTP/1.1. They key the client address on the
connections `proxy-router` handed to its peer-RPC frontend (HTTP/2 preface on
`:80`, ALPN `gonka-rpc/1` on `:443`). With one HTTP/2 connection per peer,
that address is that peer's connection.
A second peer (a second address) has its own connection and its own counters.

| Check | Threshold | Over-limit response |
| --- | --- | --- |
| Concurrent connections per address | 32 (`PROXY_ROUTER_RPC_MAX_CONNS_PER_SRC`) | connection closed on the preface / ALPN, before the hand-off |
| New TCP connections | 200/s | connection closed on the preface / ALPN, before the hand-off |
| `Attach`, `Watch` | 100 / 10s | Connect: HTTP 429. Native gRPC: HTTP 200, `grpc-status` 8 |
| Gossip, `GetPayload`, seed / repair / verify / challenge | 200 / 10s | same |
| `GetDiffs`, `GetMempool` | 400 / 10s | same |
| `Chat` | 2000 / 10s | same |
| `GetSignatures` | 600 / 10s | same |

Path zones are independent. A `GetDiffs` flood must 429 `GetDiffs` without
spending the `Chat` zone. The child peer budget still applies underneath, and
for `GetDiffs` it trips first: weight 60 exhausts the burst of 600 after 10
calls, while the path zone allows 400 per 10 seconds.

The listen overwrites `X-Real-IP` from the real client address. A caller-supplied
`X-Real-IP` is discarded. Inner hops must not rate-limit on `RemoteAddr`; on
HA that address is `proxy-router`, and collapsing every peer into one bucket
is a failed rollout.

The concurrent cap is what bounds idle sockets: the open rate only slows how
fast one address fills `maxconn 4096`, and an idle socket that is already
tracked holds its slot until `timeout client`. 32 leaves room for reconnect
overlap, HA replicas, and several peers behind one NAT address. The key is one
IPv4 address, or one IPv6 /64. The public port counts the connection as soon
as it sees the HTTP/2 preface or the peer RPC ALPN, before the hand-off, so a
TLS handshake that then stalls holds its slot until `timeout client-hs` (5s)
closes it instead of sitting uncounted. With `PROXY_ROUTER_PROXY_PROTOCOL_FROM`
the public port consumes the balancer's preamble first, so the cap uses the
client address it named. A socket that never sends the preface or the ALPN
token goes to nginx after the inspect delay and never counts. Without PROXY
protocol, clients behind one L4 balancer share that balancer's address and
its 32 slots.

## Manual test plan

Gherkin for the scenarios below: [release-0.2.16-v6-manual.feature](./release-0.2.16-v6-manual.feature).

The thing under test is the wrapping switch. Run the same participant
behaviour on each configuration below. A chat that works on one cell and
fails on the next is a release failure, not an environment note.

Use two peers (two keys, two source addresses) and one host version. On HA,
use two `versiond` replicas behind `versiond-router`. Keep a long chat open
while the short RPCs run, so a timeout or a buffered proxy cannot look like
success.

How to count connections, from the client, during N concurrent calls to one
peer:

```bash
ss -tn dst :443          # InferenceUrl; the same port on every cell
```

Pass on an HTTP/1.1 cell: the established count tracks in-flight RPCs, not 1.
Pass on an HTTP/2 cell: one established connection to InferenceUrl for that
peer's RPC, while N calls are in flight on it (plus whatever short JSON
connections the gateway keeps). A second peer is a second connection. On the
HAProxy host, `docker exec proxy sh -c 'echo "show sess" | socat - /var/run/haproxy/haproxy.sock'`
lists peer sessions under `rpc_h2`.

### Cells

Four shapes. `proxy` means the nginx ingress. `proxy-router` means the HAProxy
ingress (on join the service may still be named `proxy`; check the image).

| Cell | versiond | Ingress | HA |
| --- | --- | --- | --- |
| S | `versiond` | `proxy` | no |
| SR | `versiond` | `proxy-router` | no |
| H | `versiond-router` | `proxy` | yes |
| HR | `versiond-router` | `proxy-router` | yes |

### 1. v6 binary on the old wrapping

v6 `devshardd` and gateway, wrapping images left at v4 or v5. This is the
state on the day v6 is auto-added.

| Run | Wrapping | Cells |
| --- | --- | --- |
| v4 | v4 `versiond` / `versiond-router`, v4 `proxy` | S, H |
| v5 | v5 `versiond` / `versiond-router`, v5 `proxy-router` | SR, HR |

Also run the crossed ingress if the host can be composed that way (v4
`versiond` behind v5 `proxy-router`, v5 `versiond` behind nginx `proxy`).
The expected transport does not change: neither old ingress recognises the
HTTP/2 preface or the peer-RPC ALPN, so the probe misses and Connect rides
HTTP/1.1.

On every cell:

| Check | Pass |
| --- | --- |
| Chat, a second concurrent chat, gossip, `/healthz` | All succeed. HA: the same escrow stays on the same `versiond` |
| Transport | Connect over HTTP/1.1 on InferenceUrl. N in-flight RPCs are N connections. They do not share one |
| HTTP/2 probe | One probe per origin at a time: explicit protocol mismatch is cached for 30 minutes ± 10%; connection closure backs off from 30 seconds to 30 minutes. Application/stream errors do not mark the origin H1-only. Connect stays on InferenceUrl |
| Native gRPC | Not used. No `application/grpc` content type on the wire |
| `/rpc/` | Visible on the nginx / policy-worker access log |
| Child rate limit | Still enforced. See scenario A below. The public connection limits must not fire, because nothing reached the peer-RPC frontend |
| Mixed version on one escrow | A v6 peer and a v5 peer on the same escrow both complete a chat |

### 2. Switch the wrapping to the v6 branch

Replace `versiond`, `versiond-router`, `proxy`, and `proxy-router` with the
images from `release/devshard/v6.0.0` (the `release/v0.2.16-devshard-v6.0.0`
line carries the same devshard images). Re-run all four cells.

`proxy-router` cells (SR, HR), and the join service named `proxy` when its
image is `proxy-router`:

| Check | Pass |
| --- | --- |
| Listen | Client uses InferenceUrl's own port. HTTPS InferenceUrl: TLS, ALPN `gonka-rpc/1`, same certificate as nginx. HTTP InferenceUrl: cleartext HTTP/2 opened with the client preface. No other port is dialed |
| One connection | N concurrent RPCs, including a long `Chat` and `Watch`, share one TCP connection on every hop (client → `proxy-router`, `proxy-router` → `versiond-router` or `versiond`, `versiond` → child). A second peer is a second connection |
| nginx | Access log has no `/rpc/`. `:80`/`:443` still serve JSON, `/v1`, and `/healthz` |
| Stickiness | HA: escrow placement matches the HTTP/1.1 path. A version in the URL still selects that child |
| New limits | Scenarios C–F below fire on this listen and do not fire on `:443` |
| Fallback | With `DEVSHARD_RPC_H2_UPGRADE=false` the same peer returns to HTTP/1.1 on InferenceUrl and the connection count fans out again. The same happens, without any setting, when this host's `proxy` image is swapped back for one that does not recognise the preface: the peer's next Attach misses, logs once, and continues over HTTP/1.1 |

nginx `proxy` cells on the v6 branch (S, H, image is nginx rather than
HAProxy): HTTP/2 is not this ingress. Pass is the same as section 1
(HTTP/1.1, no shared connection, JSON still served). A nginx-only host that
cannot be reached over HTTP/1.1 is a failure.

### 3. Rate-limit scenarios

Run A and B on both wrappings. Run C–G only after section 2, on a v6
`proxy-router` cell (SR and HR). Two source addresses, two keys.

The production chat cap (2560 per peer) and the 6000/min budget are the wrong size
to hit by hand. For A and B, on a non-production child only:

```bash
DEVSHARD_RPC_MSGS_PER_MIN=60
DEVSHARD_RPC_MSGS_BURST=60
DEVSHARD_RPC_MAX_STREAMS_PER_PEER=3
```

Put the variables back when the scenario is done. Scenarios C–G use the
shipped proxy thresholds and need no child override.

**A. Peer budget isolates peers (both wrappings).** Peer A sends `GetDiffs`
until the child refuses (weight 60; with the lab burst of 60 the second call
is enough. On production defaults the burst of 600 admits 10, then refuses).
Refusal is `resource_exhausted` with a retry hint. Peer B's `Chat` in the
same window succeeds. `GET /devshard/stats/rpc` shows the ban on A, not on B.

**B. Stream cap is per peer (both wrappings).** With `MAX_STREAMS_PER_PEER=3`,
peer A holds 2 chats (the cap leaves one slot so `Watch` still fits). The
next `Chat` from A is `resource_exhausted` and does not spend chat weight.
`Watch` for A still opens. Peer B opens a chat at the same time.

**C. One connection, separate path zones (v6 only).** From one address, on
the one HTTP/2 connection, send `GetDiffs` past 400 requests in 10 seconds.
Further `GetDiffs` return 429. A `Chat` on that same connection still
completes, until its own zone (2000/10s) or the child budget. `Attach` from
that address has its own 100/10s zone and is not emptied by the `GetDiffs`
flood. Confirm the connection count stays 1 for the whole run.

**D. Connection rate (v6 only).** From one address, open connections that
start with the HTTP/2 preface (or the peer-RPC ALPN) faster than 200 per
second. Further handshakes die before `Attach`
(no signature work in the child logs for the refused ones). A second address,
opening a single connection in the same second, attaches and chats. Repeat
against InferenceUrl on the old wrapping: this reject must not appear.

**D2. Concurrent connections (v6 only).** From one address, open 33 TCP
connections to InferenceUrl's port at under 200/s; on each send only the
HTTP/2 preface (HTTPS: a ClientHello offering ALPN `gonka-rpc/1`) and nothing
more. The first 32 stay open; the 33rd is closed before the hand-off. A
second address opens a connection in the same window, attaches, and chats.
Close one of the 32 and the next open from the first address is accepted
again. 33 connections that send nothing at all are not capped: they go to
nginx after the inspect delay.

**E. Forged `X-Real-IP` (v6 only).** Peer A sets `X-Real-IP` to peer B's
address. A's flood still counts against A's real address. B is not pushed
over its zone. Clearing the header does not create a second bucket.

**F. HA does not merge peers (cell HR).** Two clients, two addresses, through
`versiond-router`. Each has one connection. Drive C from client A only.
Client B's `Chat` and `GetDiffs` succeed. If both clients 429 together, the
router or `versiond` is keying `RemoteAddr` (the proxy) and the cell fails.

**G. Renewal survives an Attach flood (v6 only).** Hold a live session for
peer A (`Watch` stays up). Flood anonymous `Attach` from elsewhere until the
Attach zone or the child floor refuses. A's renewal (well under 4/min) still
succeeds and the old token keeps admitting in-flight calls for its overlap
window. A's `Watch` does not drop.

**H. Diff catch-up stays inside one page (both wrappings).** On a v6 child,
request diffs for 65 nonces (`from=1&to=65`, or the `GetDiffs` RPC with the
same range). The call is rejected and names the cap: at most 64 nonces and
8388608 bytes. The same call for 64 small diffs succeeds. One diff larger
than 8 MiB is returned alone, not rejected for its size. Repeat on HTTP/1.1
and, after section 2, on HTTP/2. A range that fits one page must not fan out
into a second connection on the HTTP/2 cell.

Then put one host more than 200 diffs behind and let it catch up. Gateway
logs show `sendCatchUp` in more than one chunk, each `diffs_in_chunk` at most
200, and the following chat to that host succeeds. The process must not hold
the whole journal for that catch-up.

The page rule is already pinned by `devshard/storage/diff_page_test.go`,
`devshard/transport/server_test.go`, and `devshard/transport/client_test.go`.
The 200-diff send chunk is pinned by `devshard/user/diff_retain_test.go`.
This scenario is the operator check on a long escrow.

### 4. v6 wrapping in front of v4 and v4.2

New `versiond`, `versiond-router`, `proxy`, and `proxy-router`. Children and
gateways from `devshard/v4` and `devshard/v4.2`. Run cells SR and HR at
least; S and H if that ingress is still in the estate.

| Check | Pass |
| --- | --- |
| JSON | Chat, gossip, diffs, and `/healthz` on InferenceUrl succeed for both v4 and v4.2. HTTP/1.1. No `/rpc/` required of that binary |
| Preface on `:80`/`:443` | Recognising it does not break JSON on the same ports. A v4 or v4.2 child is not required to answer HTTP/2. A failed `/rpc/` against that child must not take down its JSON routes |
| Routing | The version in the URL still selects that child. HA stickiness still holds |
| v6 client → old host | A v6 gateway completes a chat against a v4 host and against a v4.2 host. Record whether the call stayed on JSON. A hang, or a chat that only succeeds if the old child speaks `/rpc/`, fails the cell |
| Mixed escrow | One v6 child and one v4.2 child, v6 wrapping, same escrow. The v6 peer uses its one HTTP/2 connection to the v6 host. The v4.2 peer completes the same chat on InferenceUrl. Neither path poisons the other |
| Cutover | Replacing a v4 or v4.2 child does not wait forever on a readiness field that binary does not send. The old generation keeps serving until the new one can |

### Done when

- Section 1 is green for v4 (S, H) and v5 (SR, HR): HTTP/1.1, no shared connection, child limits on, public connection limits absent.
- Section 2 is green for v6 `proxy-router` (SR, HR): one connection per peer, nginx blind to `/rpc/`, JSON intact. nginx `proxy` cells still look like section 1.
- Scenarios A–H match the table, including the second peer staying healthy and a long catch-up staying inside one page and one 200-diff chunk.
- Section 4 is green for v4 and for v4.2.

## Known limits

- `update-devshard.sh --check` does not send an HTTP/2 request. A router can look admitted (its admin `/readyz` is HTTP/1.1) while the HTTP/2 data path is not serving. After the wrapping switch, confirm it with a real RPC through the public port, not only `/readyz`.
- Discovery is per scheme/host/port. Explicit protocol mismatch is cached for 30 minutes ± 10%; connection closure uses backoff from 30 seconds to 30 minutes. Empty RPC twins during upgrade use bounded Connect/H1 forwarding through the original JSON placement. Overload may return 503; native gRPC cannot use H1. Follow the upgrade procedure in the [canonical host guide](https://github.com/gonka-ai/gonka-docs/pull/1515).
- Transport compatibility supports mixed generations: a router without an available `:8081` RPC pool uses bounded Connect/H1 fallback through the JSON placement. This does not authorize arbitrary component replacement order; follow the canonical host guide and updater rollback result.
- The public `proxy-router` is still one process on one host. The bundled PostgreSQL is still not database HA.
