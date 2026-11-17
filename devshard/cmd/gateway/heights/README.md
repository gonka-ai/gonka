# `heights` — carrying mainnet height into the escrow's log

Mainnet height is the escrow's logical clock. Every host and the sequencer keep a signed `(height, hash)` inside the log itself, so a verifier replaying the diffs recomputes the same time — which is what later releases need to fold timeouts and seal windows onto heights instead of wall clocks.

The protocol lives in the session (`user/heartbeat.go`, `user/heightsync_seed.go`) and the transport envelope. This package is the gateway's two decisions about it: what the clients carry, and when the cadence opens.

## What it owns

- **`BuildCourier`** — the `transport.ClientConfig` the session's host clients dial with: an anchor scheduler and the peer-tip cache it stamps from. `nil` when height sync is off, so the gateway dials exactly as it does without height sync.
- **`StartCadence`** — starts the session's heartbeat loop, which also starts its seed loop. The session's `Close` stops both. What interval it runs at is the runtime-params feed's answer, not this package's.

- **`NewOracle`** — the gateway's own follower of mainnet, over node-manager, the chain RPC feed and a direct gRPC read, behind a failover and a tip cache. `nil` when it is off or has nothing to follow, and it owns a live subscription, so shutdown closes it.

## What it does not own

**The follower is not what the gateway stamps from.** The scheduler's height source is `NewPeerTipOracleSource` over the same peer-tip cache the clients ingest host anchors into — the gateway carries what the hosts told it. The follower goes to `HeightSyncLogOracle`, which is the trust label on a carried tip: how far the gateway's own reading sits from the one it is passing on. It cannot become the escrow's time, because a sequencer-composed stamp never raises the floor `F` and never counts toward a turnover, and the heartbeat's own stamp is read from the log's floor (`referenceStampLocked`), not from any oracle.

## Boundaries

- **The scheduler and the clients must share one cache.** A scheduler built over a second, empty cache stamps nothing, and the fault shows only as a quiet escrow that never syncs. `BuildCourier` returns both halves together for that reason, and `user/httpsession.go` passes the cache to every client of the session.
- **Only a signed tip counts.** The cache refuses to serve an entry with no origin blob and signature (`RequireVerifiedBlob`), so an unattributable height never reaches the log. It also refuses one whose originator timestamp is stale.
- **Only the user side can open a turn.** A busy escrow pays nothing — a host's own stamp on `confirm`/`finish` discharges the cadence. A quiet one has no such traffic, and if nobody opens a heartbeat turn it never syncs while its hosts count the silence toward arming close-ready.
- **The cadence is on unless the fleet is told it is not.** A deployment must not go quiet because nobody set a flag. `GATEWAY_HEIGHT_SYNC_ENABLED=false` is for the fleet whose hosts do not carry height sync, which would otherwise skip a heartbeat every interval and log it.
- **A nil follower must stay unset, not stored.** A nil `*Oracle` placed into the config's interface field is not nil, and the transport would call it on every carried tip.
- **The seed breaks the bootstrap.** The floor is raised only by host-signed claims, and a heartbeat needs a floor to stamp; the session-open seed fills the cache from the hosts' own anchors, which is what lets the first turn open at all.

## Knobs

`GATEWAY_HEIGHT_SYNC_ENABLED` (on), `DEVSHARD_REQUIRE_HEIGHT_SEED` (on), `DEVSHARD_GATEWAY_CHAIN_ORACLE` (off), `DEVSHARD_HEIGHTSYNC_K` (10), `DEVSHARD_HEIGHTSYNC_SLOTS` (1). The follower dials node-manager at the address the runtime-params feed resolved (`DEVSHARD_NODE_MANAGER_ADDR`, then `NODE_MANAGER_ADDR`, then `localhost:9400`). The cadence's own schedule is not a knob here: it comes from governance through [`app/runtime_params.go`](../app/runtime_params.go). See [`docs/operations.md`](../docs/operations.md).

## Read next

- [`docs/escrows.md`](../docs/escrows.md), "Height sync" — where the cadence sits in an escrow's life.
