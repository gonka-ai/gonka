# Proposal: Gateway Epoch Accounting Dashboard

## Goal / Problem

Settlement credits each devshard slot with `assigned_nonces -
protocol_misses` completed inferences. The participant that owns the
slot gets that credit. A consumed nonce counts as a completed
inference unless an applied `MsgTimeoutInference`, with enough
participant votes, records a miss in `HostStats.Missed`. The count is
independent of payment: the payout is the per-slot token cost plus a
fee share, so a nonce can pay zero GNK and still count as a completed
inference.

The gateway breaks this assumption in three ways:

1. Policy consumes nonces without sending work. Probe quarantine,
   throttling, PoC routing, and capability exclusions all burn nonces as
   ghosts.
2. A sent request can stay unfinished while its required timeout is
   skipped, fails to collect votes, or fails to apply.
3. Overscheduling turns one user request into several completed
   inferences: each redundant attempt consumes its own nonce.

In cases 1 and 2 the nonce settles as a completed inference for the
participant, whatever GNK it pays. The completed count and the miss rate
are wrong, and nothing records why.

In case 3 the losing attempts execute real work and are honest
completions, but used and unused output must stay separated for the
completed count to make sense. A losing attempt that never finishes
falls into case 2 like any other send.

The existing observability dashboard
(`proposals/gateway-observability/observability.md`) explains live
behavior through Prometheus. It is not exact accounting: counters reset
on restart, history expires with retention, and nothing ties the metrics
to `HostStats.Missed` and `HostStats.Invalid`.

Operators need a per-epoch view that answers:

1. How many nonces per participant ended in each outcome?
2. How many unexecuted nonces per participant count as completed
   inferences, and why?
3. Did the gateway apply every timeout it was supposed to trigger?
4. When a timeout was not applied, which stage failed, for what reason?
5. Does gateway accounting match devshard protocol state?

The view covers this gateway's escrows only. It does not infer
participant fault from ambiguous transport failures and is not a
network-wide scoring service.

## Proposal

Add durable protocol accounting to `devshardctl` gateway mode. This is
an internal tool for work on the core devshard protocol: it defines the
target metrics we optimize before enabling settlement. The gateway keeps
in-memory counters, snapshots them to disk, and serves raw epoch totals
over a private JSON API. A minimal dashboard reads only that API.

The design keeps four questions separate:

- what the gateway did: was work sent, and if not, why;
- what the protocol recorded: receipt, finish, timeout, and invalid
  transitions in devshard state;
- whether the gateway met its obligation: every required timeout applied;
- what settlement would record: the completed counts current protocol
  state credits to each participant.

### Accounting scope

The gateway is the sole sequencer for its escrows: it runs the devshard
user session that builds every diff. Participants sign timeout votes,
but only the gateway puts `MsgTimeoutInference` into a diff. Gateway
accounting is complete: every miss in `HostStats.Missed` passed through
this gateway.

Terms:

- `consumed_nonce`: a diff nonce in `1..latest_nonce`;
- `inference_nonce`: a `consumed_nonce` carrying `MsgStartInference` with
  inference ID equal to the nonce;
- `protocol_only_nonce`: a `consumed_nonce` without `MsgStartInference`;
- `assigned_nonces`: the per-slot assignment count settlement derives
  from `latest_nonce` (`devshardAssignedUpperBoundForSlot`). It counts
  both inference and protocol-only nonces;
- `real_send`: an `inference_nonce` whose request was sent to its
  assigned participant;
- `protocol_finish`: an applied `MsgFinishInference`. Streamed bytes
  alone are not a finish;
- `timeout_required`: a `real_send` past its protocol deadline without
  `protocol_finish`. A finish applied before timeout resolution clears
  it;
- `protocol_miss`: an applied `MsgTimeoutInference`; increments
  `HostStats.Missed`;
- `protocol_invalid`: a validation verdict; increments
  `HostStats.Invalid`.

### Nonce accounting

Every `consumed_nonce` has exactly one current `disposition`:

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

no_disposition_and_no_live_attempt --------------------> unclassified
```

Terminal values:

- `protocol_only`: no inference was assigned;
- `ghost`: an inference was assigned but the request was not sent;
- `finished_used`: finished and won the race;
- `finished_unused`: finished as a known non-winner;
- `finished_usage_unknown`: finished, but winner context was lost;
- `unfinished_refused`: `timeout_required` with no applied receipt;
- `unfinished_execution`: `timeout_required` with an applied receipt.

Non-terminal values:

- `in_flight`: a `real_send` without `protocol_finish` before its
  protocol deadline. Counted directly from live session state: these are
  the attempts the gateway currently tracks;
- `unclassified`: leftover. A `consumed_nonce` with no terminal
  disposition and no live attempt, per escrow:

```text
unclassified = latest_nonce - terminal_dispositions - in_flight
```

The leftover must never be negative. It grows only across restart
windows, where live attempt state was lost; growth anywhere else is an
accounting error. `in_flight` must drain to zero once the escrow has no
active work.

The leftover buckets do not overlap: `in_flight` is live work,
`unclassified` is a nonce with no classification at all, and `unknown` is
not a disposition but the fallback value of a context or reason dimension
on an otherwise classified nonce.

Two rules:

- Protocol state decides receipt, finish, timeout, and invalid. Gateway
  state decides send, winner, and policy context. A stream that served
  the user without `protocol_finish` is still unfinished.
- The categories are fixed. Unrecognized behavior falls to `unknown`
  reasons or `unclassified` nonces and stays visible. Code changes must
  not add, remove, or reinterpret categories.

Each `inference_nonce` carries a fixed set of context fields:

- `dispatch_phase` and `timeout_evaluation_phase`: `normal`, `poc`, or
  `confirmation_poc`;
- `quarantine_mode` at dispatch: `none`, `probe`, `shadow`, or
  `probation`;
- `no_send_reason` for ghosts: `poc_unavailable_host`,
  `participant_throttled_no_send`, `participant_capability_no_send`,
  `no_compatible_request_after_stale`, or `unknown`;
- `failure_origin` for unfinished sends: `host_response`,
  `gateway_policy`, `client`, or `transport_unknown`;
- `detail_reason`: a fixed reason from the recording event, or
  `unknown`.

An unknown reason increments `unknown_reason_total`. Block heights appear
only in diagnostic nonce records, never in counters or metric labels.

Every gateway policy enters the disposition tree at `request_not_sent` or
`real_send`, or consumes no nonce at all:

```text
gateway policy
 |
 +--> request_not_sent (ghost)
 |     |
 |     +--> probe quarantine ------------> participant_throttled_no_send,
 |     |                                   quarantine_mode=probe
 |     +--> throttling ------------------> participant_throttled_no_send
 |     +--> capability exclusion --------> participant_capability_no_send
 |     +--> relaxed PoC, non-preserved --> poc_unavailable_host
 |     +--> stale retry, no compatible
 |     |    request ---------------------> no_compatible_request_after_stale
 |     +--> any unlisted policy ---------> unknown
 |
 +--> real_send (disposition follows protocol state)
 |     |
 |     +--> shadow quarantine -----------> quarantine_mode=shadow
 |     +--> probation -------------------> quarantine_mode=probation
 |     +--> relaxed PoC, preserved ------> poc phase context,
 |     |                                   normal timeout handling
 |     +--> overscheduling --------------> one nonce per redundant attempt
 |
 +--> no nonce consumed (nothing to account)
       |
       +--> phase gate rejection during PoC
```

### Timeout accounting

`timeouts_required` counts terminal `unfinished_refused` and
`unfinished_execution` nonces. Each has one current `timeout_outcome`:

- `skipped`: timeout handling did not start;
- `vote_collection_failed`: verifier requests did not complete;
- `insufficient_votes`: accept weight did not exceed the protocol
  threshold;
- `diff_send_failed`: votes were sufficient but the timeout diff was not
  applied;
- `applied`: `MsgTimeoutInference` was applied.

`timeout_outcome` is unset while handling is in progress. Non-applied
outcomes are current state: a retry can move any of them to `applied`.

Each non-applied outcome carries a fixed `timeout_reason`. `skipped`
supports at least `phase_transition_aborted`,
`long_response_after_content`, `escrow_state_root_diverged`,
`context_canceled`, and `unknown`.

A finish that arrives before timeout handling starts moves the nonce to a
finished disposition and out of `timeouts_required`. The existing gateway
labels `nonce_already_finished` and
`empty_stream_without_non_empty_winner` mark this race; they are not
`timeout_outcome` values.

An applied timeout records a `protocol_miss` against the timed-out
inference nonce and its executor slot. The carrier diff may be a later
inference nonce or a `protocol_only` nonce.

Receipt escalation, first-token, and stream-stall timers only start
redundant attempts. They are scheduling timers, not protocol timeouts.

The cross-checks hold per `epoch_index`, `model`, `participant`, and
`escrow_id`:

```text
timeout_outcome.applied       = HostStats.Missed
recorded_invalid_transitions  = HostStats.Invalid
```

`recorded_invalid_transitions` counts observed `StatusChallenged` to
`StatusInvalidated` transitions on the executor slot. Any difference is a
`cross_check_error`.

### Settlement projection

`protocol_misses` and `protocol_invalid` sum `HostStats.Missed` and
`HostStats.Invalid` over the same escrow and slot scope as the
dispositions. For each participant and model, `assigned_nonces` sums the
settlement assignment of every slot the participant owns in matching
escrows. Slot `0` first executes at nonce `slot_count`; slot `k` first
executes at nonce `k`.

Settlement happens after finalization and signature quorum. Until then
every value is a projection of current state.

The two diagrams above define the complete raw dataset: disposition
counts with their policy context, plus timeout outcomes, alongside the
values read from devshard state (`latest_nonce`, `HostStats.Missed`,
`HostStats.Invalid`). The database stores and the API returns only this
raw data. Everything below (primary counts, the split, and every rate)
is derived and is one point of view. Any other view can be computed from
the same data, so the UI can change without storage or API changes.

Primary counts:

```text
inference_nonces     = assigned_nonces - protocol_only
executed             = finished_used
                     + finished_unused
                     + finished_usage_unknown
protocol_completed   = assigned_nonces - protocol_misses
non_execution_credit = protocol_completed - executed
```

`non_execution_credit` splits exactly:

```text
non_execution_credit = protocol_only
                     + ghost
                     + timeout_accounting_failure
                     + unresolved

timeout_accounting_failure: unfinished with a non-applied timeout_outcome
timeout_pending:            unfinished with unset timeout_outcome
unresolved:                 in_flight + timeout_pending + unclassified
```

The identity holds exactly when `timeout_outcome.applied` equals
`HostStats.Missed`; a `cross_check_error` breaks it.

The split shows where credit came from, not who to blame:
`protocol_only` and `ghost` are protocol and gateway policy costs;
`timeout_accounting_failure` is something the gateway must drive to
zero; `unresolved` needs time or investigation.

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

`terminal_real_sends` is every finished and unfinished `real_send`,
excluding `in_flight`. Quality rates filter numerator and denominator by
`timeout_evaluation_phase=normal`; PoC phases stay available as separate
breakdowns.

`sent_unfinished_rate` is a gateway observation, not proof of participant
fault; `failure_origin` and `detail_reason` keep the evidence.
`miss_capture_rate` measures whether required timeouts became protocol
misses.

Rates with a zero denominator are undefined, not zero. Current-epoch
rates are projections; finalized-epoch rates are locked.

### Invalid accounting

Invalid is a protocol verdict. Transport failures, empty streams, and
timeouts never contribute to `protocol_invalid`.

The accounting view reports:

- `protocol_invalid`;
- unresolved challenges: inferences currently in `StatusChallenged`;
- the invalid cross-check difference.

Validation-duty gaps need durable validation assignment and completion
tracking. They stay outside this proposal until that source exists.

### Operator interface

The private API exposes:

- `GET /api/v1/epochs`
- `GET /api/v1/epochs/{epoch_index}/participants`
- `GET /api/v1/epochs/{epoch_index}/participants/{participant}`

`epoch_index` accepts `current` and selects escrows created in that epoch.
Collection responses contain one row per `participant` and `model`.
Both endpoints accept an optional `model` and an optional list of
`escrow_id` values. Repeated and comma-separated `escrow_id` values are
accepted. Without `escrow_id`, the response aggregates all escrows from
the selected epoch. Without `model`, participant detail returns separate
model records and never merges model-specific rates.

Each participant-model record contains:

- `schema_version`, `updated_at`, and the `latest_nonce` values the
  totals were computed from;
- raw disposition and context counts;
- per-slot `assigned_nonces` and dispositions;
- timeout requirements, outcomes, and reasons;
- `protocol_misses`, `protocol_invalid`, and unresolved challenges;
- accounting health: `in_flight`, the `unclassified` leftover,
  `cross_check_error`, `unknown_reason_total`, and writer errors.

The API returns counts, not rates. Diagnostic nonce records can be
returned from a recent window. Prompts, responses, payload hashes, raw
errors, and private material are never stored or served.

The dashboard reads only this API and stores no accounting state. The
JSON schema is the contract.

The epoch view contains:

1. Summary counts: `assigned_nonces`, execution dispositions,
   `protocol_misses`, and `non_execution_credit`.
2. The `non_execution_credit` split: `protocol_only`, `ghost`,
   `timeout_accounting_failure`, and `unresolved`.
3. One participant row per selected model: disposition counts,
   `sent_unfinished_rate`, `miss_capture_rate`, `ghost_dispatch_rate`,
   `overscheduling_rate`, `protocol_invalid_rate`, and accounting health.

Participant detail contains per-slot dispositions, the timeout funnel,
`no_send_reason`, `quarantine_mode`, phase breakdowns, `failure_origin`,
`protocol_invalid`, and unresolved challenges.

The dashboard is a minimal Python sidecar: server-rendered HTML, no build
step. It reuses the main-page layout of the gonka-tracker frontend
(https://github.com/gonka-ai/gonka-tracker/tree/main/frontend): summary
cards above one participant table, clickable rows, and highlighting for
accounting errors.

## Implementation

### Storage and recovery

Accounting stores counters, not per-nonce rows. One counter exists per
key: a distinct tuple of `escrow_id`, `slot_id`, `disposition`, and the
fixed context and timeout dimensions. Counters hold current state, not
event history: the first classification increments one key; a later
reclassify atomically moves the count to the new key. The request path
performs no accounting writes.

Non-applied timeouts are the only per-nonce rows. They remain mutable
because a retry or late finish can change their outcome after restart.
The row is removed once the timeout applies or the inference finishes.

A snapshot writer atomically upserts every counter, together with each
escrow's `latest_nonce`, into `accounting.db` every five
minutes and at escrow finalization, settlement, rotation, epoch
transition, and shutdown. A failed snapshot keeps the previous one and is
reported as a writer error. The tables hold one row per key, not history.
Every dimension is a small enum and only observed combinations create
counters, so an escrow produces at most a few hundred rows. RAM, CPU, and
disk use do not grow with request volume.

An escrow registry stores the chain `epoch_index`, model, ordered slots,
and participant addresses. Aggregation happens at query time: epoch,
model, participant, slot, and optional escrow totals join counters
through the registry. Aggregates are never stored. `in_flight` is read
from live session state at query time; `unclassified` is computed from
`latest_nonce`. Neither is stored.

`DEVSHARD_STATS_RETENTION_EPOCHS=0` disables automatic deletion. A
positive value removes only complete epochs older than the configured
count.

On restart, counters resume from the last snapshot and live attempt state
starts empty, so nonces from the lost window have no disposition and no
live attempt and fall into the `unclassified` leftover. Restart never
invents a terminal outcome; every remaining gap shows up in accounting
health.

A small in-memory ring of recent nonce records, including block heights,
supports debugging. Ring records are never stored.

### Lifecycle integration

Record at:

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

`HandleTimeout` currently uses its error return for both successful
timeout submission and operational failure. It must instead return a
structured result:

- `timeout_kind`: `refused` or `execution`;
- `timeout_outcome`;
- `timeout_reason`;
- accept, reject, and error vote weights;
- whether the timeout diff was applied.

A successfully applied timeout returns `timeout_outcome=applied`.
Operational errors remain errors but do not mean the timeout succeeded.

### Security and metrics

Add `DEVSHARD_STATS_LISTEN_ADDR`, empty by default; an empty value
disables the listener. The API has no authentication: access control is
network isolation. The listener is never published through the public
proxy or Caddy. The dashboard sidecar runs on the same private network,
configured with the stats URL only. Any container on the shared private
Docker network can read the API; that is fine for this internal tool.

The counter store also feeds Prometheus. A collector exports the
in-memory counters as `devshard_accounting_*` gauges for the current
epoch, labeled by `participant`, `model`, and the dimensions used by
each metric family. Labels never include `escrow_id`, nonce, block
heights, or `detail_reason`. The API and the metrics share one store;
exact and historical totals come from the snapshots and the API. Existing
`devshard_gateway_*` metrics remain the live operational view.

### Verification

Tests cover:

- the `unclassified` leftover: never negative, grows only across restart
  windows;
- `in_flight` draining to zero on inactive escrows;
- reclassify moving counts atomically between keys;
- replayed callbacks changing no counter;
- the `non_execution_credit` identity, including `timeout_pending`;
- settlement assignment matching, including slot `0` and
  `protocol_only_nonce`;
- every refused and execution `timeout_outcome`;
- cross-checks against `HostStats.Missed` and `HostStats.Invalid`;
- PoC, throttle, capability, and stale-exclusion ghost reasons;
- shadow quarantine and probation as `real_send`;
- phase transitions and normal-phase filtering;
- overscheduled winners and unfinished losers;
- streamed output without `protocol_finish`;
- restart recovery without inventing terminal outcomes;
- aggregation across models, slots, participants, rotated escrows, and
  epochs;
- API schema stability;
- the Prometheus export matching the counter store.

This proposal does not change devshard consensus, timeout thresholds,
validation rules, settlement payloads, participant incentives, or public
gateway APIs.
