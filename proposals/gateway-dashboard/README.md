# Proposal: Gateway Epoch Accounting Dashboard

## Goal / Problem

Settlement derives each slot's completed inference count from
`assigned_nonces - protocol_misses`. Every `consumed_nonce` therefore
contributes execution credit unless `HostStats.Missed` records a protocol
timeout.

Gateway policy can consume a nonce without sending work. A sent request can
also remain unfinished when timeout handling is skipped, cannot collect enough
votes, or cannot apply its timeout diff. These cases receive execution credit
without recorded execution.

The existing gateway observability dashboard
(`proposals/gateway-observability/observability.md`) explains current behavior
through Prometheus metrics. Scrape intervals and retention make those metrics
unsuitable for exact historical accounting. They also cannot reconcile gateway
observations with `HostStats.Missed` and `HostStats.Invalid`.

Operators need an epoch-scoped view that answers:

1. Which participant-associated outcomes require investigation?
2. Which unexecuted nonces still receive execution credit?
3. When `timeout_required` was true, did the gateway apply the timeout?
4. If the timeout was not applied, which stage and reason prevented it?
5. Does gateway accounting match devshard protocol state?

The view covers this gateway's escrows only. It does not infer participant fault
from ambiguous transport failures and is not a network-wide scoring service.

## Proposal

Add durable protocol accounting to `devshardctl` gateway mode. The gateway
maintains one in-memory counter per accounting configuration, snapshots those
counters to disk, exposes raw epoch aggregates through a private API, and
supplies a minimal dashboard backed only by that API.

The design separates four concerns:

- `gateway_policy`: whether work was sent and why it was skipped;
- `protocol_outcome`: whether receipt, finish, timeout, and invalid transitions
  reached devshard state;
- `timeout_accounting`: whether every required timeout was applied;
- `settlement_projection`: how current protocol state would credit each
  participant.

### Accounting scope

The gateway user session is the sole sequencer for its escrow. Participants
return signed timeout votes, but only the gateway composes
`MsgTimeoutInference` into a diff. Gateway timeout accounting can therefore be
reconciled directly with `HostStats.Missed`.

Formal terms:

- `consumed_nonce`: a diff nonce in `1..latest_nonce`;
- `inference_nonce`: a `consumed_nonce` carrying `MsgStartInference` whose
  inference ID equals the diff nonce;
- `protocol_only_nonce`: a `consumed_nonce` without a matching
  `MsgStartInference`;
- `assigned_nonces`: the settlement assignment count for a slot, derived from
  `latest_nonce` with `devshardAssignedUpperBoundForSlot`;
- `real_send`: an `inference_nonce` whose request was sent to its assigned
  participant;
- `protocol_finish`: an applied `MsgFinishInference`; streamed bytes alone do
  not constitute a finish;
- `timeout_required`: a current-state predicate: a `real_send` past its
  protocol deadline without `protocol_finish`. A finish applied before
  timeout resolution clears it;
- `protocol_miss`: an applied `MsgTimeoutInference`, which increments
  `HostStats.Missed`;
- `protocol_invalid`: a validation verdict that increments
  `HostStats.Invalid`.

`assigned_nonces` includes `inference_nonce` and `protocol_only_nonce`.
Finalization and dedicated timeout-submission diffs can create
`protocol_only_nonce`.

### Nonce accounting

Every `consumed_nonce` has one current `disposition`. For each escrow,
`in_flight` is the derived residual:

```text
in_flight = latest_nonce - terminal_dispositions - unclassified
```

The residual must never be negative and must drain to zero once the escrow
has no active work. A violation is an accounting error.

```text
consumed_nonce
 |
 +--> protocol_only_nonce ----------------------------> protocol_only
 |
 +--> inference_nonce
       |
       +--> request_not_sent --------------------------> ghost
       |
       +--> real_send
             |
             +--> deadline_not_reached ----------------> in_flight
             |
             +--> protocol_finish
             |     |
             |     +--> selected_winner --------------> finished_used
             |     +--> known_non_winner -------------> finished_unused
             |     +--> usage_not_recoverable --------> finished_usage_unknown
             |
             +--> deadline_reached_without_finish
                   |
                   +--> no_receipt --------------------> unfinished_refused
                   +--> receipt_applied ---------------> unfinished_execution

insufficient_evidence ---------------------------------> unclassified
```

Terminal `disposition` values:

- `protocol_only`: no inference was assigned;
- `ghost`: an inference was assigned but the request was not sent;
- `finished_used`: `protocol_finish` was applied and the attempt won the race;
- `finished_unused`: `protocol_finish` was applied and the attempt was a known
  non-winner;
- `finished_usage_unknown`: `protocol_finish` was applied but winner context
  could not be recovered;
- `unfinished_refused`: `timeout_required` with no applied receipt;
- `unfinished_execution`: `timeout_required` with an applied receipt.

Non-terminal values:

- `in_flight`: a `real_send` without `protocol_finish` whose protocol
  deadline has not been reached;
- `unclassified`: a `consumed_nonce` that no counter accounts for, computed
  by reconciliation against `latest_nonce`. Restart windows bound this value;
  growth outside them is an accounting error.

Protocol state determines receipt, finish, timeout, and invalid outcomes.
Gateway state determines send, winner, and policy context. A stream that served
the user without `protocol_finish` remains unfinished.

The classification model is fixed; code paths are not allowed to reshape it.
Unrecognized behavior and code-level failures degrade into `unknown` reasons
or `unclassified` nonces, never into new, missing, or reinterpreted
categories. Both buckets stay visible, so the statistics keep their meaning
while the implementation evolves.

Each `inference_nonce` also records bounded context:

- `dispatch_phase` and `timeout_evaluation_phase`: `normal`, `poc`, or
  `confirmation_poc`;
- `dispatch_block_height` and `timeout_evaluation_block_height`, recorded
  only in diagnostic nonce records;
- `quarantine_mode`: `none`, `probe`, `shadow`, or `probation`;
- `no_send_reason`: `poc_unavailable_host`,
  `participant_throttled_no_send`, `participant_capability_no_send`,
  `no_compatible_request_after_stale`, or `unknown`;
- `failure_origin`: `host_response`, `gateway_policy`, `client`, or
  `transport_unknown`;
- `detail_reason`: a bounded reason owned by the lifecycle event, or `unknown`.

An unknown reason reports `unknown` and increments `unknown_reason_total`.
Block heights never appear in counters or Prometheus labels.

Probe quarantine uses `no_send_reason=participant_throttled_no_send` with
`quarantine_mode=probe`. Shadow quarantine and probation remain `real_send`;
their final `disposition` depends on protocol state.

In relaxed PoC modes, non-preserved participants can receive `ghost` while
preserved participants continue to receive `real_send`. These sends retain
normal receipt, finish, and timeout behavior but use their PoC phase context.
Strict PoC admission that consumes no nonce creates no accounting entry.

### Timeout accounting

Every terminal `unfinished_refused` and `unfinished_execution` has
`timeout_required=true`; `timeouts_required` counts these nonces. Each
required timeout has one current `timeout_outcome`:

- `skipped`: timeout handling did not start;
- `vote_collection_failed`: verifier requests did not complete;
- `insufficient_votes`: accept weight did not exceed the protocol threshold;
- `diff_send_failed`: accept weight was sufficient but the timeout diff was not
  applied;
- `applied`: `MsgTimeoutInference` was applied.

`timeout_outcome` is unset while handling is in progress. Non-applied
outcomes are current-state: a retry can move any of them to `applied`.

Each non-applied outcome includes a bounded `timeout_reason`.
`timeout_outcome=skipped` supports at least:

- `phase_transition_aborted`;
- `long_response_after_content`;
- `escrow_state_root_diverged`;
- `context_canceled`;
- `unknown`.

If a finish arrives before timeout handling begins, the nonce becomes a
finished disposition and leaves `timeouts_required`. Existing gateway labels
`nonce_already_finished` and `empty_stream_without_non_empty_winner` represent
this race; they are not final `timeout_outcome` values.

An applied timeout attributes `protocol_miss` to the timed-out
`inference_nonce` and its executor slot. The carrier diff that submits
`MsgTimeoutInference` consumes its own nonce, counted only as
`protocol_only`.

Receipt escalation, first-token timeout, and stream-stall timers can start
redundant attempts. They are scheduling timers, not protocol timeouts.

`HandleTimeout` must return structured `timeout_outcome` instead of using an
error to represent both successful timeout application and operational
failure.

The accounting invariants are evaluated per `epoch_index`, `model`,
`participant`, and optional `escrow_id`:

```text
timeout_outcome.applied       = HostStats.Missed
recorded_invalid_transitions  = HostStats.Invalid
```

`recorded_invalid_transitions` counts observed `StatusChallenged` to
`StatusInvalidated` transitions, attributed to the executor slot. A
difference is a `cross_check_error`.

### Settlement projection

Protocol counters use the same escrow and slot scope as nonce dispositions:

- `protocol_misses`: sum of `HostStats.Missed`;
- `protocol_invalid`: sum of `HostStats.Invalid`.

For each participant and model, `assigned_nonces` sums settlement assignments
for all owned slots across this gateway's escrows in the epoch. Slot `0` begins
at nonce `slot_count`; every other slot begins at its matching nonce.

Settlement requires devshard finalization and signature quorum. Before
settlement, all values are a current projection.

Primary counts:

```text
inference_nonces     = assigned_nonces - protocol_only
executed             = finished_used
                     + finished_unused
                     + finished_usage_unknown
protocol_completed   = assigned_nonces - protocol_misses
non_execution_credit = protocol_completed - executed
```

`non_execution_credit` decomposes exactly:

```text
non_execution_credit = protocol_only
                     + ghost
                     + timeout_accounting_failure
                     + unresolved

timeout_accounting_failure: unfinished with a non-applied timeout_outcome
timeout_pending:            unfinished with unset timeout_outcome
unresolved:                 in_flight + timeout_pending + unclassified
```

The identity holds when funnel `applied` equals `HostStats.Missed`; a
`cross_check_error` breaks it and is reported.

This decomposition is an accounting identity, not a fault assignment.
`protocol_only` and `ghost` are protocol or gateway policy effects.
`timeout_accounting_failure` is a gateway obligation. `unresolved` requires
more time or investigation.

Derived rates:

```text
projected_unexecuted_rate = (assigned_nonces - executed) / assigned_nonces
protocol_miss_rate        = protocol_misses / assigned_nonces
settlement_gap_rate       = non_execution_credit / assigned_nonces

sent_unfinished_rate = (unfinished_refused + unfinished_execution)
                       / terminal_real_sends
miss_capture_rate    = timeout_outcome.applied / timeouts_required
ghost_dispatch_rate  = ghost / inference_nonces
overscheduling_rate  = finished_unused
                       / (finished_used + finished_unused)
protocol_invalid_rate = protocol_invalid / protocol_completed
```

`terminal_real_sends` includes every finished and unfinished `real_send` and
excludes `in_flight`. Normal-phase quality rates filter both numerator and
denominator by `timeout_evaluation_phase=normal`. PoC phases remain available
as separate breakdowns.

`sent_unfinished_rate` is a gateway observation, not proof of participant
fault. `failure_origin` and `detail_reason` preserve the evidence needed for
investigation. `miss_capture_rate` measures whether the gateway converted
required timeouts into `protocol_miss`.

Rates with a zero denominator are absent. Current-epoch rates are projections;
finalized-epoch rates are stable.

### Invalid accounting

Invalid is a protocol verdict. Transport failures, empty streams, and timeouts
do not contribute to `protocol_invalid`.

The accounting view reports:

- `protocol_invalid`;
- unresolved protocol challenges: inferences currently in `StatusChallenged`;
- `recorded_invalid_transitions - HostStats.Invalid`.

Validation-duty gaps require durable validation assignment and completion
tracking. They remain outside this proposal until that source exists.

### Operator interface

The private API exposes:

- `GET /api/v1/epochs`
- `GET /api/v1/epochs/{epoch_index}/participants`
- `GET /api/v1/epochs/{epoch_index}/participants/{participant}`

`epoch_index` accepts `current`. Collection responses contain one row per
`participant` and `model`. Collection and detail endpoints accept optional
`model` and `escrow_id` filters. Without `model`, participant detail returns
separate model records and never merges model-specific rates.

Each participant-model record contains:

- `schema_version`, accounting watermark, and `updated_at`;
- raw disposition and context counts;
- per-slot `assigned_nonces` and dispositions;
- timeout requirements, outcomes, and reasons;
- `protocol_misses`, `protocol_invalid`, and unresolved challenges;
- the `in_flight` residual, `cross_check_error`, `unclassified`,
  `unknown_reason_total`, and writer errors.

The API returns counts, not rates. Diagnostic nonce records can be returned
from a bounded recent window. Prompts, responses, payload hashes, raw errors,
and private material are never stored or served.

The dashboard reads only this API and stores no accounting state.

The epoch view contains:

1. Summary counts for `assigned_nonces`, execution dispositions,
   `protocol_misses`, and `non_execution_credit`.
2. The `non_execution_credit` split by `protocol_only`, `ghost`,
   `timeout_accounting_failure`, and `unresolved`.
3. For the selected model, one participant row with disposition counts,
   `sent_unfinished_rate`, `miss_capture_rate`, `ghost_dispatch_rate`,
   `overscheduling_rate`, `protocol_invalid_rate`, and accounting health.

Participant detail contains per-slot dispositions, the timeout funnel,
`no_send_reason`, `quarantine_mode`, phase breakdowns, `failure_origin`,
`protocol_invalid`, and unresolved challenges.

The JSON schema is the stable interface. The dashboard is a minimal Python
sidecar: server-rendered HTML with no build step, following the main-page
layout of the gonka-tracker frontend
(https://github.com/gonka-ai/gonka-tracker/tree/main/frontend): summary cards
above one participant table with clickable rows and highlighting for
accounting errors.

## Implementation

### Storage and recovery

Accounting stores counters, not per-nonce rows. One counter exists per
configuration: a distinct tuple of `escrow_id`, `slot_id`, `disposition`, and
the bounded context and timeout dimensions. Counters live in memory and hold
current state, not event history: a first classification increments one
configuration, and a reclassification atomically moves the count to the new
configuration. The request path performs no accounting writes.

A snapshot writer atomically upserts the current value of every counter,
together with each escrow's `latest_nonce`, into dedicated `perf.db` tables
every five minutes and at escrow finalization, settlement, rotation, epoch
transition, and shutdown. A failed snapshot keeps the previous one and is
reported as a writer error. The tables hold one row per configuration, not
history. Every dimension is a small enum and only observed combinations
create counters, so an escrow produces at most a few hundred rows. RAM, CPU,
and disk use are bounded and do not grow with request volume.

Query-time aggregation produces `epoch_index`, `model`, `participant`, slot,
and optional `escrow_id` totals. Aggregates are not persisted separately from
the snapshot rows.

An escrow registry stores the immutable chain `epoch_index`, model, ordered
slots, and participant addresses. Participant-model totals include every slot
owned by that address in matching escrows.

`in_flight` is derived at query time as `latest_nonce` minus terminal and
`unclassified` counts and is never stored.

`DEVSHARD_STATS_RETENTION_EPOCHS=0` disables automatic deletion. A positive
value removes only complete epochs older than the configured count.

On restart, counters resume from the last snapshot. Nonces consumed after the
snapshot's `latest_nonce` become `unclassified`. Reconciliation never
fabricates a terminal outcome, and every remaining gap appears in accounting
health.

A bounded in-memory ring of recent nonce records, including block heights,
supports diagnosis. Ring records are never persisted.

### Lifecycle integration

Record or reconcile data at:

- session-picker nonce consumption and ghost decisions;
- timeout-submission and finalization diffs;
- real-send dispatch;
- receipt and finish state transitions;
- winner selection;
- timeout eligibility and skip decisions;
- timeout vote collection and diff submission;
- `protocol_miss` and `protocol_invalid` transitions;
- escrow finalization, settlement, rotation, and shutdown.

A repeated or replayed callback that changes no session state changes no
counter.

`HandleTimeout` returns:

- `timeout_kind`: `refused` or `execution`;
- `timeout_outcome`;
- `timeout_reason`;
- accept, reject, and error vote weights;
- whether the timeout diff was applied.

A successfully applied timeout returns `timeout_outcome=applied`. Operational
errors remain errors but do not encode protocol success.

### Security and metrics

Add `DEVSHARD_STATS_LISTEN_ADDR`, empty by default; an empty value disables
the listener. The API has no authentication: access control is network
isolation. The listener is never published through the public proxy or Caddy,
and the dashboard sidecar runs on the same private network, configured with
the stats URL only. Any container on the shared private Docker network can
read the API; this exposure is accepted.

The counter store also feeds Prometheus. A collector exports the in-memory
counters as `devshard_accounting_*` gauges for the current epoch, labeled by
`participant`, `model`, and the dimensions applicable to each metric family.
Labels never include `escrow_id`, nonce, block heights, or `detail_reason`.
The API and these metrics share one accounting store; exact and historical
epoch totals come from the snapshots and the API. Existing
`devshard_gateway_*` metrics remain the live operational view.

### Verification

Tests cover:

- the residual: never negative, drains to zero on inactive escrows;
- reclassification moving counts atomically between configurations;
- replayed callbacks changing no counter;
- the `non_execution_credit` identity, including `timeout_pending`;
- settlement assignment parity, including slot `0` and `protocol_only_nonce`;
- every refused and execution `timeout_outcome`;
- cross-checks against `HostStats.Missed` and `HostStats.Invalid`;
- PoC, throttle, capability, and stale-exclusion ghost reasons;
- shadow quarantine and probation as `real_send`;
- phase transitions and normal-phase filtering;
- overscheduled winners and unfinished losers;
- streamed output without `protocol_finish`;
- restart reconciliation without fabricated terminal outcomes;
- aggregation across models, slots, participants, rotated escrows, and epochs;
- API schema stability;
- the Prometheus export reporting the same values as the counter store.

This proposal does not change devshard consensus, timeout thresholds,
validation rules, settlement payloads, participant incentives, or public
gateway APIs.
