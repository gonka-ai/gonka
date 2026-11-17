# `metrics` — the Prometheus surface

The only package that knows Prometheus exists.

## What it owns

- **The registry** (`metrics.go`) and every collector: limits, capacity, perf, registry, chain, funding (`funding_collector.go`: per-model escrow money, counts, planner decisions and guarantee, read from plain `FundingModel` values), cache, capture, transport, accounting, journal, nonce ledger.
- **Recorders** (`race.go`, `dispatch_recorder.go`, `limit_recorder.go`, `stats_tokenizer_recorder.go`) — the write side, called by the subsystems that produce the facts.
- **Label discipline** (`labels.go`) — what may become a label and what may not.

## Boundaries

- **Family names are frozen as `devshard_*`.** A dashboard and an operator's alert select on these strings; a rename is a silent alert that stops firing.
- **Unbounded labels stay out.** A nonce or a request id is never a label: either would grow the series without end. An escrow id is a label only where [`docs/rules.md`](../docs/rules.md), "11. Labels, ordering and determinism", allows it — the three dispatch counters, deleted when the escrow's dispatcher is reaped, and the registry gauges, rebuilt from the live escrow set on every scrape. What a metric cannot carry, the JSON accounting API serves instead.
- **A collector reads its source; it does not keep its own copy**, so a gauge cannot disagree with the thing it reports.

## Cardinality in practice

The five places an unbounded label could get in, and what stops it:

- **The route label is a pattern, never a raw path.** `InstrumentRoute` takes the pattern (`/devshard/{id}/v1/status`) as a fixed string; passing `r.URL.Path` would mint a series per request.
- **The method label is folded to a known set.** `net/http` hands the handler any RFC 7230 token the client sent, so labelling with `r.Method` directly would let one unauthenticated caller mint a permanent series per probe. Anything outside the seven standard methods becomes `other`.
- **Escrow series are deleted when the escrow retires.** Escrow ids are monotonic and never reused, so without `EscrowRetired` each rotation would leave one nonce-holds series, one budget series and one ghost series per reason, permanently. The sibling registry collector avoids the same growth differently — it rebuilds from the live escrow set on every scrape, so it has nothing to delete.
- **Participant series age out.** Participants and models churn with the network, so the race recorder deletes a pair's series from all fifteen participant-labelled families once nothing has written the pair for `perf_host_staleness_seconds`, sweeping at most once per tenth of that window from the write path itself (`race.go`, `RaceRecorder.markSeen`). A write that lands later — a timeout vote settled after its host went quiet — brings the pair back, and it ages out again. A pair is marked under the aging lock before its series are written, and a sweep deletes under the same lock, so a sweep never removes a series a concurrent write is about to add.

- **Host RPC series go with the last escrow that binds the host.** `devshard_gateway_host_rpc{peer,mode}` is keyed by host, so it would grow with the fleet's churn; `transport.PeerRPCAdoption` deletes a host's series when `ReleaseEscrow` drops the last escrow binding it. The label is `peer`: the participant and its route prefix.

## Histogram buckets

Two ladders, and both are sized by what they must be able to *report*, not by what they expect:

- **`latencyBuckets`** — nineteen exponential buckets from 10 ms reach 2621 s, past the 2400 s drain timeout. A quantile cannot report above the highest finite bound, so a short ladder would silently pin p95 there instead of showing the slowest attempt the engine actually permits.
- **`chunkGapBuckets`** — fifteen exponential buckets from 5 ms reach 81.92 s, past the 30 s default stall timeout (`engine_inter_chunk_stall_ms`), so a stall still lands in a finite bucket. `latencyBuckets` would reach that far as well; this ladder starts one doubling lower and stops four buckets sooner, and both inter-chunk histograms are written per participant and model, so each pair carries four fewer bucket series in each of them.

## Race labels

The race recorder's own label strings live in `vocabulary.go`, beside the package's other wire strings.

- **`outcomeFailure`** (`failure`) is a request-level outcome with no engine counterpart: attempts report `failed`, a request reports `failure`.
- **`statusNoCode`** (`0`) labels an attempt that failed without an upstream status.
- **The reasons.** `none` is what a successful request, or an event with no reason, carries; `escrow_missing`, `balance_exhausted` and `no_attempts` name a failed request's cause ahead of its first attempt's own failure reason.

## How a collector reaches its source

- **Collectors are registered at construction time**, where a duplicate family is a wiring bug that must stop the process rather than silently drop a series.
- **Some sources are typed as plain integers rather than a shared struct** (`CacheSource`, `CaptureSource`). [`api`](../api/)'s own tests read this package, so importing `api` back here would close a cycle.
- **`RegistrySources` takes functions rather than objects**, which is what keeps [`registry`](../registry/) free of an edge to [`limits`](../limits/).
- **In-flight load is reported once per participant.** [`perf`](../perf/) counts it per participant while ejection is per participant *and* model, so emitting it inside the per-model loop would be a duplicate series.
- **The limits collector folds the configured model list into the models with live traffic**, so a model with no in-flight request still reports its capacity, and no model is emitted twice.

The response writer that records a status also forwards `Flush`, so streaming handlers keep working when wrapped, and exposes `Unwrap` for the standard-library calls that reach the underlying writer by type assertion.

## Read next

- [`docs/operations.md`](../docs/operations.md), "Metrics" — the families by the question they answer, and the ones no longer emitted.
