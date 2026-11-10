# Devshard v5.0.2

Critical fix for the overall Devshard deployment. No bounty.

Pull request: https://github.com/gonka-ai/gonka/pull/1840

## Postgres connection limit

Each running Devshard version opened a Postgres pool sized to the host CPU count. Several versions on one multicore host used every connection and the node failed.

This proposal removes the outdated v3 and v4 binaries, leaves v4.1 unchanged, and updates v5 to v5.0.2. v5.0.2 caps payload pools at the session limit (`PG_POOL_MAX_CONNS`, default 4).

## Approved versions

| Name | Binary | sha256 |
| --- | --- | --- |
| v4.1 | https://github.com/gonka-ai/gonka/releases/download/release%2Fdevshard%2Fv4.1.0/devshardd.zip | `69e58e6b6c124fc218d3ed1e38d7853c0a8ce20df660d348fc28ccd249a1ccf1` |
| v5 | https://github.com/gonka-ai/gonka/releases/download/devshard%2Fv5.0.2/devshardd.zip | `fa9f30775abfc14c40ac8d8a9bae7159f6193cd820f3dfac06a60170cd8b48a1` |

v3 and v4 are removed from `approved_versions`.

Payload pools were opened with `pgxpool.New`, which sizes `MaxConns` to the CPU count. Each HA devshard version holds its own pool, so that default exhausted `max_connections`. Both payload stores now parse the libpq config and apply `PG_POOL_MAX_CONNS` (default 4), the same cap as the devshard session pool. The session pool's own `configurePostgresPool` calls that shared helper instead of reading the env var itself.

`pgpool.ConfigureMaxConns` is that helper. Unset `PG_POOL_MAX_CONNS` means 4. A non-positive or non-integer value is an error.

## Other fixes in v5.0.2

### Inference timeouts wait in one loop

`sleepUntilDeadlineWithHeartbeat` used to recurse on every heartbeat tick. Each call left its own timer and ticker alive until the inference deadline. It now waits in one loop: the deadline timer, the heartbeat ticker, and context cancellation share that loop, and a tick only runs the heartbeat.

### Heartbeat opens one interval after turnover

A fixed ticker missed a turnover that landed just after a tick, so the next open waited almost two intervals. `Heartbeat.NextWake` sleeps until the real deadline: `lastTurnover + Interval` after a turnover, and `turnOpenedAt + TurnTimeout` while a turn is still open. A turnover that lands off the loop wakes the sleeper through `SetTurnoverWake`. A deadline already in the past waits one millisecond instead of spinning.

### Validation leases name the holding process

A refused acquire only knew that some row existed, so the log said another instance held the lease. The conflict log now reports the row's status, owner, age, whether it is past the TTL, the process id, and the container hostname. `submitted` and a young `pending` row are info. A `skipped` row and a `pending` row past the TTL are warnings, and this path no longer counts as a validation orphan.

Migration 15 adds `instance_id` and `hostname` to `devshard_validation_leases`. Each process mints a UUID at boot and stores `os.Hostname()` beside it. `SetResult`, `Release`, and `OwnsPendingLease` match the signer address plus that process id, so one replica cannot complete or delete a lease another replica holds. Hostname is only logged. A row written before this column (`instance_id` blank) is owned by nobody and is reclaimed by `AcquireOneStale` after the lease TTL. An older binary still matches on the address alone, so it can release a new binary's row until every replica is updated.
