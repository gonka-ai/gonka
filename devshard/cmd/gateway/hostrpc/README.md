# `hostrpc` — reading the hosts' rate-limiter counters, to see throttling before it is an outage

The `hostrpc` package polls the hosts of the live escrows for the counters their rate limiters keep. Read this before you change what the gateway learns about how hosts treat its traffic.

A host that throttles or bans this gateway answers with refusals, and a refusal looks like any other failed call: the gateway sees the error, not the limiter behind it. The host knows how many RPCs its limiter refused in the last minute and where, and publishes it at `/devshard/stats/rpc`. This package reads that and exports it.

## What it owns

- **`Poller`** — the wave: on a wall-clock cadence it reads the live hosts from the registry (`HostDials()`, once per wave, the source `hostping` uses), GETs `<base URL>/devshard/stats/rpc` on each through at most `Concurrency` workers, and hands the sink the whole wave. Every poll is bounded by `Timeout`, and the body is capped at 8 MiB *after* decompression: the poller leaves `Accept-Encoding` to the transport, which inflates the body before the cap is applied. Its transport carries the `common/httpguard` dial-time SSRF guard and no proxy, like `hostping`'s. A wave runs to the end before the next starts, so waves never overlap; a wave that outlasts the interval makes the next one start late, not stack up. `nil` when polling is off or its schedule does not hold (a non-positive value, or a `Timeout` above the `Interval`), so a misconfigured poll costs a log line rather than the boot.
- **`metrics.HostRPCRecorder`** — the sink, and the collector that renders the `devshard_gateway_rpc_*` families at scrape time from the last wave.

The route is the versionless `/devshard/stats/rpc`, not the host's versioned prefix: the versionless route is the host's own merge across its protocol versions, and `devshard_gateway_rpc_stats_partial` says when that merge was incomplete.

## What it does not own

**It decides nothing.** A host whose limiter refuses this gateway keeps its routing weight and its place in the scheduler. Ejection is `perf`'s, from real traffic; these counters are for an operator deciding whether to slow down or to ask a host operator why.

## Boundaries

- **The counters are the host's, not this gateway's.** A host counts every caller, so `devshard_gateway_rpc_requests_last_minute` and `_banned_last_minute` are host totals. A rising banned count says a limiter is firing; it does not say it fired on this gateway. The per-escrow rows a host also reports are not exported: most belong to other gateways' escrows, and a host serving many of them would add a series per escrow it serves.
- **The label is the participant key**, as everywhere else in the gateway's metrics. A participant reachable at two addresses is polled at the first, once per wave, because two series under one label would fail the scrape.
- **A host that does not answer exports only `devshard_gateway_rpc_stats_up` at 0.** The reason is logged at debug level. Its last counters are not carried forward: a stale `banned` reading would keep an alert firing on a minute that is long over.
- **A host that leaves the live set is forgotten**, because each wave replaces the last whole.
- **Names a host reports are folded to a fixed set** (`metrics/host_rpc_recorder.go`, `rpcLabel`). An endpoint, zone or reconnect reason outside the classified ones sums into `other`, so a host cannot mint a series per name.
- **Reconnects are summed over peers.** The peer is another host; labelling by it would grow with the square of the fleet.

## Knobs

`DEVSHARD_GATEWAY_RPC_STATS_DISABLED`, `DEVSHARD_GATEWAY_RPC_STATS_INTERVAL` (`15s`), `DEVSHARD_GATEWAY_RPC_STATS_TIMEOUT` (`4s`), `DEVSHARD_GATEWAY_RPC_STATS_CONCURRENCY` (8). See [`docs/configuration.md`](../docs/configuration.md), "The knobs that decide behaviour".

## Read next

- [`hostping/README.md`](../hostping/README.md) — the reachability and clock-drift probe over the same hosts.
- [`registry/README.md`](../registry/README.md) — where the live set comes from.
