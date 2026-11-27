# Peer-gossip mempool retention

The host bounds transactions retained from peer gossip separately from local
proposals and verified challenge recovery. Per-message decoder limits alone do
not bound retention across messages (see issue #2249).

Each host session admits at most **4,096 gossip transactions** and **8 MiB of
their protobuf encoding sizes**. These are initial local admission defaults,
not consensus parameters or a measured Go heap limit. Both limits apply at
insertion, under the mempool lock. The entry limit also bounds per-entry
overhead; the budgets are per session, not shared across a process's sessions.

Gossip is best effort: when either budget would be exceeded, a new transaction
is dropped without evicting an already retained transaction. A duplicate hash
does not consume capacity or replace its existing entry. A successful gossip
RPC acknowledges processing the message, not retention of every transaction.
No new wire fields or error responses are introduced.

Local proposals use `Mempool.Add`; recovery transactions already verified by
the challenge path use `Host.AddTx` and then `Mempool.AddRecoveryTx`. These paths
remain admissible when gossip is full. They must not be replaced with raw
gossip admission. `ProposedAt == 0` alone cannot identify gossip: verified
recovery also uses zero, and a local proposal can be made at nonce zero.

When the same hash is later produced locally or accepted through verified
recovery, it leaves the gossip budget. Local proposal metadata takes precedence
over recovery and gossip, so a replay cannot erase its proposal nonce or disable
Finish rebroadcast/receipt reuse. `RemoveIncluded` releases any gossip budget
charged to removed entries, including when used by logical stale-entry cleanup.

This policy bounds only peer-gossip retention. It does not cap legitimate
local/recovery backlog, change signature or stale-transaction validation, add
per-peer fairness, or expire an occupied gossip slot automatically. A full pool
can keep rejecting new gossip until inclusion, cleanup, or promotion releases
capacity; the verified recovery path remains available. It does not enable
gossip in production `devshardd`, whose HostManager currently leaves it unwired.
Existing broadcast deduplication also means a dropped gossip transaction is not
guaranteed to be retried after capacity becomes available. This policy does not
provide eventual peer delivery; the sender's local proposal remains protected.

Regression coverage lives in `host/mempool_budget_test.go` and
`transport/rpcserver/gossip_retention_test.go`: cross-message saturation,
count/byte boundaries, deduplication, capacity release, source promotion,
concurrent admission, and real signed RPC/Host recovery with a full gossip pool.
