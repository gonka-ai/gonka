# Proposal: Gateway Dashboard

## Goal / Problem

The gateway protects users with redundant attempts, quarantine, throttling, and
PoC-aware routing. These mechanisms can hide host failures and can leave a
protocol nonce without the timeout needed to count a miss.

The existing gateway observability dashboard
(`proposals/gateway-observability/observability.md`) explains live gateway
behavior through Prometheus metrics. It does not provide exact historical
accounting by epoch, and it cannot reconcile gateway expectations with the
misses and invalids recorded by the devshard protocol. This proposal adds the
durable, epoch-scoped protocol accounting that the observability proposal
intentionally deferred.

Operators need a private view that answers two questions:

1. Which issues should each host fix?
2. Where does gateway behavior disagree with protocol accounting?

The view must classify every consumed nonce by outcome, compare gateway
observations with devshard state, and explain every gap between them, per
epoch and participant.

PoC and confirmation PoC are separate accounting periods. The gateway does not
initiate timeouts during these phases, so their nonces must not affect normal
miss rates.

## Proposal

Add a private statistics service to `devshardctl` gateway mode. The service
runs on a second listener that is not routed through the public gateway
endpoint. It serves a JSON API only. Visualization is a separate minimal
Python dashboard that reads this API.

The service reports observations made by this gateway for its own escrows. It
is not a network-wide participant scoring service.

### Nonce disposition model

Every nonce an escrow consumes is classified by outcome, exactly once. The
model is a partition: for every escrow, the disposition counts sum to
`latest_nonce`. This invariant is checked continuously and any gap is reported
as an accounting health error, never silently absorbed.

A nonce is an inference nonce when its diff carries `MsgStartInference` with a
matching inference ID; otherwise it is a protocol-only nonce. Most protocol
transactions piggyback on the next composed diff and consume no extra nonce.
Dedicated protocol-only nonces come from two paths: the timeout submission
diff that `HandleTimeout` sends immediately after collecting votes, and
finalization rounds. A timeout submission diff can even be empty when an
inference diff composed in between already carried the pending timeout
transaction; it still consumes a nonce and is still `protocol_only`.

```text
nonce consumed (latest_nonce++)
 |
 +--> no inference assigned -------------------------> protocol_only
 |
 +--> inference assigned to a host
       |
       +--> request never sent -----------------------> ghost
       |     (by no-send reason)
       |
       +--> request sent
             |
             +--> running / deadline pending ---------> in_flight
             |
             +--> finish applied
             |     |
             |     +--> output served the user -------> finished_used
             |     +--> output not used --------------> finished_unused
             |
             +--> no finish applied
                   |
                   +--> no executor receipt ----------> unfinished_refused
                   +--> executor receipt applied -----> unfinished_execution

every unfinished_* nonce --> timeout funnel (timeouts_required)
                              +--> skipped (by reason)
                              +--> insufficient_votes
                              +--> submit_failed
                              +--> applied -----------> HostStats.Missed

consumed nonce no counter accounts for ---------------> unclassified
```

Terminal dispositions:

- `protocol_only`: no inference was assigned on this nonce.
- `ghost`: inference nonce assigned to a host, but the gateway never sent the
  request. The host had no chance to execute.
- `finished_used`: the host finished the inference and its output served the
  user (the race winner).
- `finished_unused`: the host finished the inference but the output was not
  used. This is overscheduling overhead, including shadow-quarantine sends and
  empty streams that still finished.
- `unfinished_refused`: the request was sent, no executor receipt was applied,
  and no finish was applied.
- `unfinished_execution`: the request was sent, an executor receipt was
  applied, but no finish was applied.

Non-terminal dispositions:

- `in_flight`: the attempt is still running or its timeout deadline has not
  been reached.
- `unclassified`: a consumed nonce no counter accounts for, detected by
  reconciliation against session state. Expected only after a restart window;
  a growing count outside restarts is an accounting bug.

Disposition is decided by protocol state, not by transport bytes. A host that
streamed content but never applied a finish is `unfinished_execution`, even if
the user was served from that stream.

### Context dimensions

Phase and policy are context dimensions on the counters, not additional
dispositions. Every counted nonce carries:

- dispatch phase and timeout-evaluation phase: `normal`, `poc`, or
  `confirmation_poc`, with block heights. The dispatch phase describes
  scheduling conditions; the timeout-evaluation phase decides whether a
  timeout was owed and is the phase filter for all miss rates;
- quarantine mode at dispatch: `none`, `probe`, `shadow`, or `probation`;
- for `ghost`, the no-send reason: `poc_unavailable_host`,
  `probe_quarantine`, `participant_throttled_no_send`,
  `participant_capability_no_send`, `no_compatible_request_after_stale`, or
  `unknown`;
- for terminated attempts, the termination source: `host`, `gateway`, or
  `client`. A gateway or client abort (streaming hard cap, disconnect past
  meta-drain) can leave a nonce unfinished without host fault; this field
  keeps host quality rates filterable;
- a bounded detail reason from the gateway code. Unknown or newly introduced
  behavior is reported with reason `unknown`, never dropped.

A shadow-quarantine send is a real request: its nonce lands in
`finished_unused` or `unfinished_*` based on what the host actually did, with
`shadow` recorded as context.

PoC and confirmation PoC produce ghosts only for participants that are not
preserved. Real sends to preserved participants keep normal dispositions and
normal timeout handling while the phase is active. A real request aborted by a
phase transition is `unfinished_*` with detail reason
`phase_transition_aborted` and a PoC timeout-evaluation phase, which excludes
it from normal miss rates.

### Timeout funnel

Every terminal `unfinished_refused` and `unfinished_execution` nonce requires
a protocol timeout: `timeouts_required` counts exactly these nonces. The
funnel records the outcome of that requirement, one outcome per nonce:

- `skipped`, with a bounded skip reason (for example
  `long_response_after_content`, phase gate, or policy exemption);
- `insufficient_votes`: initiated, but accept votes did not reach quorum;
- `submit_failed`: votes collected, but the timeout diff was not applied;
- `applied`: `MsgTimeoutInference` in devshard state, which increments
  `HostStats.Missed`.

A skip does not remove the nonce from `timeouts_required`: a skipped timeout
still leaves a nonce that settles as completed, so it stays visible in the
funnel.

Gateway scheduling timers such as receipt escalation, first-token timeout, and
stream stall are not protocol timeouts. They may start redundant attempts, but
only applied `MsgTimeoutInference` produces a protocol miss.

Cross-check invariant: funnel `applied` must equal `HostStats.Missed`, and
observed invalidations must equal `HostStats.Invalid`, per participant and
epoch. The two sides come from independent sources (gateway lifecycle hooks
versus devshard state), so any divergence is a lifecycle-hook bug and is
reported as an accounting health error next to the partition invariant.

### Counters and rates

The stored primitive is one counter per configuration: a distinct tuple of
escrow, slot, disposition, and context dimensions. Every rate in this
proposal is secondary: a ratio of counter sums that can be recomputed at any
time from the counters alone, never stored.

Protocol values are read from devshard state, aggregated across all devshards
owned by this gateway for the epoch and participant:

- `protocol_misses`: `HostStats.Missed`;
- `protocol_invalid`: `HostStats.Invalid`.

Settlement adds no new counters: it submits this same devshard state and can
happen at any time.

Settlement derives each slot's assigned count from `latest_nonce` alone
(`devshardAssignedUpperBoundForSlot`): every nonce increment is an assigned
inference for its round-robin slot, and everything not recorded in
`HostStats.Missed` settles as a completed inference credited to that slot's
participant. Ghosts and protocol-only nonces are not neutral: unless timed
out, each one settles as a completed inference for the slot owner.

All rates share the settlement denominator `assigned_nonces`:

```text
executed            = finished_used + finished_unused
unexecuted_rate     = (assigned_nonces - executed) / assigned_nonces
protocol_miss_rate  = protocol_misses / assigned_nonces
settlement_gap_rate = unexecuted_rate - protocol_miss_rate
```

`protocol_miss_rate` is what settles if the escrow is settled right now.
`settlement_gap_rate` counts nonces that settle as completed but were never
executed. Because the partition covers every nonce, the gap decomposes
exactly into three parts:

- deliberate subsidy: ghosts and protocol-only nonces. Timing these out would
  punish hosts for work they were never given, so this is a policy cost that
  either stays accepted or shrinks through protocol changes;
- accounting failures: unfinished sends whose timeout was skipped, got
  insufficient votes, or failed to submit. This part must be driven to zero
  through the timeout funnel;
- unresolved: `in_flight` and `unclassified` nonces. `in_flight` must trend
  to zero as the epoch ages; `unclassified` stays bounded by restart windows.
  Growth outside those bounds is an accounting health error.

Secondary rates:

```text
host_miss_rate        = (unfinished_refused + unfinished_execution)
                        / real sends
                        both filtered by timeout-evaluation phase = normal
miss_capture_rate     = applied / timeouts_required
ghost_rate            = ghost / assigned_nonces
overscheduling_rate   = finished_unused / executed
protocol_invalid_rate = protocol_invalid / executed
```

`host_miss_rate` isolates host quality: how often a host fails when actually
given work. Real sends are inference nonces that were dispatched, in other
words all inference nonces except ghosts. Numerator and denominator use the
same phase filter, so a send aborted by a phase transition leaves both.
The termination-source context separates gateway-caused unfinished nonces
from host-caused ones. `miss_capture_rate` below 1 localizes the
accounting-failure part of the gap to a funnel outcome and skip reason.
`ghost_rate` and `overscheduling_rate` split the gateway policy cost:
`ghost_rate` is the never-sent share, broken down by no-send reason and
phase, and `overscheduling_rate` is the sent-but-unused share.

The API reports raw counters per participant and per slot; the dashboard
computes every rate above. Rates never appear in API responses.
Per slot the API returns `assigned_nonces` (derived exactly like settlement)
partitioned into dispositions, so all numerators and denominators are
reconstructible. Rates with a zero denominator are absent rather than shown
as zero.

### Invalid accounting

Invalid is a protocol verdict and is not inferred from gateway transport or
stream failures. The dashboard shows `protocol_invalid` and unresolved
protocol challenges. It does not invent invalid reasons that are absent from
protocol data. Exact validation-duty gaps require durable validation
assignment tracking and are deferred until that source exists.

### API

The private listener exposes:

- `GET /api/v1/epochs`
- `GET /api/v1/epochs/{epoch_index}/participants`
- `GET /api/v1/epochs/{epoch_index}/participants/{participant}`

`epoch_index` accepts the literal `current` for the latest epoch.

The participant response contains:

- epoch, participant, and model,
- disposition counts and the `latest_nonce` reconciliation total,
- per-slot `assigned_nonces` with disposition breakdown,
- timeout funnel counters: `timeouts_required` and per-outcome counts
  (`skipped` by reason, `insufficient_votes`, `submit_failed`, `applied`),
- ghost counts by no-send reason and phase,
- phase, quarantine, and termination-source context breakdowns,
- protocol misses, protocol invalids, and unresolved challenges,
- accounting health (partition invariant, cross-check invariant, stale
  unresolved) and last update time.

Escrow detail is optional through an `escrow_id` filter. Recent nonce samples
from the in-memory ring may be returned for diagnosis, but prompts, responses,
payload hashes, and private material are never stored or served.

### Dashboard

The stats service has no built-in UI. Visualization is a separate minimal
Python dashboard: one small app, server-rendered HTML, no build step. It
reads only the private JSON API, computes every rate from the raw counters,
and never queries gateway databases directly.

The layout follows the main page of the gonka-tracker frontend
(https://github.com/gonka-ai/gonka-tracker/tree/main/frontend): a row of
summary cards above one dense participant table with clickable rows and
visual highlighting for problem participants. The main view calls
`/api/v1/epochs/current/participants` and shows:

1. Summary cards: epoch index, `assigned_nonces` total, disposition totals,
   protocol miss rate versus unexecuted rate, and the settlement gap split
   into subsidy, accounting failures, and unresolved.
2. One row per participant address with aggregated stats: assigned nonces,
   disposition breakdown, host miss rate, ghost rate, overscheduling rate,
   miss capture rate, and invalid rate, sortable by settlement gap, host
   miss rate, or invalid rate. Rows with accounting health errors are
   highlighted.

Clicking an address opens the participant detail: the timeout funnel, ghost
reasons, phase, quarantine, and termination-source context, and invalid
breakdowns.

## Implementation

### Private listener

Add `DEVSHARD_STATS_LISTEN_ADDR`, with a loopback-only default. The listener is
disabled when the value is empty. Deployment files must not expose this port
through the public proxy or Caddy configuration.

The listener serves the JSON API only. It must not share the public
OpenAI-compatible middleware stack. The API has no authentication; access
control is network isolation, so the port must be reachable only from the
private network.

The Python dashboard is a sidecar: it runs next to the gateway on the same
private network, configured with the stats URL. It holds no state of its own.

### Durable accounting

Prometheus is not the source of truth because scrape intervals and retention
cannot provide exact historical epoch totals.

Accounting is aggregate counters, not per-nonce rows: one counter per
configuration, where a configuration is a distinct tuple of escrow ID, slot
ID, and the bounded dimensions (disposition, phases, quarantine mode, no-send
reason, termination source, funnel outcome, skip reason). Counters live in
memory. A snapshot writer upserts the current cumulative value of every
counter into a dedicated table in `perf.db` every five minutes and at
lifecycle boundaries: escrow finalization, settlement, epoch transition, and
shutdown. The table holds one row per configuration, not history, and stays
separate from gateway configuration and topology in `gateway.db`.

The runtime cost is negligible by construction. Classifying a nonce is one
integer increment on an in-memory map; there are no writes on the request
path. Every dimension is a small enum and only combinations that actually
occur create counters, so an escrow produces at most a few hundred rows.
A snapshot rewrites only those rows, so RAM, CPU, and disk usage are all
bounded and do not grow with request volume.

An escrow registry table maps each escrow ID to its epoch index, model, and
group participants. Escrow ID is the only accounting key; every aggregation
joins counters through the registry, so the API rolls up by epoch, by
participant, or both.

Only terminal dispositions are counted. `in_flight` is derived at query time
as `latest_nonce` minus terminal and unclassified counts, never stored. On
restart, counters resume from the last snapshot; nonces classified in the
lost window are found by reconciliation against each session's `latest_nonce`
and land in `unclassified`, which keeps the partition invariant checkable
without per-nonce idempotency machinery. A bounded in-memory ring of recent
nonce samples supports diagnosis; samples are never persisted.

The same counter store feeds Prometheus: a collector exports the in-memory
counters as `devshard_accounting_*` metrics, labeled by epoch, participant,
and the context dimensions. Escrow ID is dropped from labels to keep
cardinality bounded. Live dashboards and alerting therefore see the same numbers as the
durable accounting, with no second bookkeeping path. Exact epoch totals come
from the snapshots and the JSON API.

### Lifecycle integration

Record accounting at existing gateway and protocol boundaries:

- nonce assignment and ghost/no-send decisions in the session picker,
- protocol-only nonce consumption in pending-diff and finalization sends,
- real attempt start, receipt/finish observation, and termination source in
  redundancy handling,
- winner selection and timeout eligibility in `finishRaceOutcome`,
- structured vote collection and timeout submission results in
  `user.Session.HandleTimeout`,
- protocol miss and invalid state transitions from every devshard runtime.

`HandleTimeout` must return a structured result that distinguishes successful
timeout application, insufficient votes, vote collection failure, and diff send
failure. Its current result reports only the timeout kind and overloads errors
for both successful timeout submission and failures, so it cannot support
correct accounting.

### Verification

Tests must cover:

- the partition invariant: disposition counts sum to `latest_nonce` per escrow,
  including protocol-only nonces from timeout submission and finalization,
- the cross-check invariant: funnel `applied` equals `HostStats.Missed` and
  observed invalidations equal `HostStats.Invalid`,
- per-slot `assigned_nonces` matching the settlement derivation in
  `devshardAssignedUpperBoundForSlot`,
- refused and execution timeout success and each funnel outcome, with skips
  counted inside `timeouts_required`,
- ghost classification for probe quarantine, throttle, capability, and PoC,
- shadow quarantine and probation sends landing in finished or unfinished
  dispositions by outcome,
- PoC and confirmation PoC phase transitions: aborted real sends leave both
  the numerator and denominator of `host_miss_rate`,
- termination-source recording for gateway and client aborts,
- overscheduled winners with failed losers,
- streamed content without protocol finish classified as unfinished,
- protocol invalidation and unresolved validation,
- snapshot persistence and restart reconciliation: nonces classified while
  accounting was down land in `unclassified`,
- aggregation across active, inactive, and rotated devshards,
- epoch and participant aggregation joining counters through the escrow
  registry,
- optional escrow filtering,
- the Prometheus export reporting the same values as the counter store,
- dashboard schema compatibility.

This proposal does not change devshard consensus, timeout thresholds, validation
rules, settlement payloads, participant incentives, or public gateway APIs.
