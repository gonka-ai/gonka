# `hostping` — reaching the hosts, to watch rather than to route

A host answers requests, but only when there are requests. Between them the gateway learns nothing: a host that has gone unreachable, or whose clock has drifted far enough to make its own receipt stamps meaningless, looks exactly like a host nobody happened to pick.

## What it owns

- **`Targets`** — the probe destinations, snapshotted once per wave from the registry's live escrows. `/clock` first, because that is the only answer carrying the host's own time; `/healthz` as the fallback for a host that serves no clock. Both are joined under the route prefix that host serves its protocol on.
- **`Pinger`** — the wave itself, on a wall-clock cadence, built on `common/probe`. Its transport carries the `common/httpguard` dial-time SSRF guard and, like the signed host transport, no proxy, since the targets are hosts' on-chain base URLs. `nil` when the probe is off or its schedule does not hold, so a misconfigured ping costs a log line rather than the boot.

## What it does not own

**It decides nothing.** A host that stops answering a ping keeps its routing weight and its place in the scheduler. Ejection is `perf`'s, from real traffic, and a probe is deliberately not evidence for it: an observability path that can take a host out of rotation is a way to lose a fleet to a firewall rule.

## Boundaries

- **The live set is read per wave, not subscribed to.** An escrow that retires between waves stops being pinged without anything having to tell the prober, and there is no refcounted mirror of the registry to drift out of step with it. The cost is one `HostDials()` per wave.
- **One address is pinged once.** A validator holding several slots answers on one socket; pinging per slot would say the same thing three times and label it once.
- **The label is the participant key**, as everywhere else in the gateway's metrics — not the dial host, which is an implementation detail of where that participant happens to be deployed today.
- **A host that leaves is forgotten** (`Sink.Forget`), so a retired escrow's hosts do not sit at their last reading for ever.
- **Cold dials stay out of the warm distribution.** A connect cost folded into the RTT histogram reads as the host being slow.
- **Timeout must be at most half the interval**, which `common/probe` enforces: a wave that can outlast its own period would overlap itself.

## Knobs

`DEVSHARD_GATEWAY_HOST_PING_DISABLED`, `DEVSHARD_GATEWAY_HOST_PING_INTERVAL` (`15s`), `DEVSHARD_GATEWAY_HOST_PING_TIMEOUT` (`2s`), `DEVSHARD_GATEWAY_HOST_PING_CONCURRENCY` (8). See [`docs/operations.md`](../docs/operations.md), "The knobs that decide behaviour".

## Read next

- [`registry/README.md`](../registry/README.md) — where the live set comes from.
- [`perf/README.md`](../perf/README.md) — the scoring that does move traffic.
