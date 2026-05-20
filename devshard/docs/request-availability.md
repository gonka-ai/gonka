# Devshard Request Availability

This document describes the target behavior for globally pausing new devshard
request processing without making hosts look faulty.

## Goal

Guardians need an operational switch that lets the chain tell all devshard
hosts to stop accepting new inference work. Hosts must still participate in the
devshard protocol so users and peers can distinguish an intentional pause from
executor downtime.

The switch applies to both devshard runtimes:

- the legacy in-process devshard served by decentralized-api
- external devshard host binaries served through versiond or equivalent host
  software

## Chain Parameter

Add `DevshardEscrowParams.devshard_requests_enabled`.

Default: `true`.

When `true`, devshard hosts accept and process new requests normally. When
`false`, devshard hosts acknowledge new requests but do not run model inference
for them.

The parameter is not a governance-only parameter. It needs a direct operational
transaction guarded by the genesis guardian set:

```protobuf
MsgSetDevshardRequestsEnabled {
  authority: string
  enabled: bool
}
```

`authority` must be one of `GenesisGuardianParams.guardian_addresses`. The
message should update only `devshard_requests_enabled`, leaving the rest of
`DevshardEscrowParams` untouched.

`guardian_addresses` are operational validator addresses (`gonkavaloper...`),
not account signer addresses (`gonka...`). The message signer is still an
account address, so the permission check must normalize before comparing. Use
the same address-family conversion pattern used by PoC and preserved-node code:
guardian operator address -> `sdk.ValAddressFromBech32` -> `sdk.AccAddress` for
account comparisons, or signer account address -> `sdk.ValAddress` when
comparing against the configured operator list. Do not compare the two bech32
strings directly.

`MsgUpdateParams` may still carry the field for governance updates, but the
normal operational path is the guardian transaction because this switch is meant
for fast incident response.

## Host Behavior

Every devshard host watches chain params and keeps a local history of observed
availability periods:

```text
available: bool
epoch_id: uint64
start_time: int64
end_time: int64 | open
```

The history is needed because a host may validate an old `MsgFinishInference`
after the chain has already been re-enabled.

Existing code already has two chain-observation patterns, but neither currently
stores devshard availability periods:

- decentralized-api has `OnNewBlockDispatcher.ProcessNewBlock`, which runs from
  block events, queries `Params` and `EpochInfo`, updates `ChainPhaseTracker`,
  and already caches some chain params such as validation params, bandwidth
  params, PoC params, transfer-agent access, and devshard approved versions.
  The in-process devshard should extend this path to cache
  `devshard_requests_enabled` and append availability-period transitions.
- standalone `devshardd` intentionally has no event dispatcher or block queue.
  Its `chainParamsProvider` queries `Params` and `EpochInfo` at startup and then
  refreshes every 60 seconds. It currently keeps only `logprobs_mode` and
  `current_epoch`. The standalone host should extend this poller, or a shared
  provider behind it, to track the availability flag and persist period history.

Because validators need historical checks, a current-value cache is not enough.
Both runtimes need a small availability tracker that records transitions with
timestamps and persists disabled periods by epoch for at least the devshard
retention window. The per-epoch storage layout lets the existing session pruner
drop old availability evidence with the same epoch pruning pass.

Minimal implementation:

- Add `devshard_requests_enabled` to `DevshardEscrowParams` with default `true`.
- Add `MsgSetDevshardRequestsEnabled` and a guardian permission check with
  operator/account address conversion.
- In decentralized-api, extend the existing new-block params refresh to cache the
  flag and record transitions. Do not add a new event subsystem.
- In standalone `devshardd`, extend the existing 60-second `chainParamsProvider`
  refresh to cache the flag and record transitions. Do not add block
  subscriptions to `devshardd`.
- Put the shared current value and transition history behind a tiny
  `AvailabilityProvider` used by execute and validation paths.
- Store disabled periods in the devshard storage backend with `epoch_id`,
  `start_time`, and `end_time`, and prune those rows/files together with the
  session data for that epoch.
- For the first version, record transition time from the same refresh path. Exact
  transaction event time is not required because validation compares against
  `MsgStartInference.started_at`.

When a host receives a new request while `devshard_requests_enabled=false`, it
must:

1. accept the request envelope and sign the normal executor receipt
2. skip model execution
3. emit `MsgFinishInference` with a structured non-serving reason
4. charge zero inference cost

The receipt is still important. It proves that the executor was reachable and
that the request reached the assigned executor. Without the receipt, peers would
fall back to the existing refusal-timeout path and treat the host as unavailable
or faulty.

## Finish Reason

`MsgFinishInference` should carry the reason. Do not infer reason from an empty
`response_hash`; empty hashes are too ambiguous and are already useful for more
than one non-serving case.

Add:

```protobuf
enum FinishReason {
  FINISH_REASON_UNSPECIFIED = 0;
  FINISH_REASON_COMPLETED = 1;
  FINISH_REASON_DEVSHARD_REQUESTS_DISABLED = 2;
  FINISH_REASON_POC_ABORTED = 3;
  FINISH_REASON_NO_PRESERVE_NODES = 4;
}

message MsgFinishInference {
  uint64 inference_id = 1;
  bytes  response_hash = 2;
  uint64 input_tokens = 3;
  uint64 output_tokens = 4;
  uint32 executor_slot = 5;
  bytes  proposer_sig = 6;
  string escrow_id = 7;
  FinishReason reason = 8;
}
```

Compatibility rule: existing messages with `reason=UNSPECIFIED` are interpreted
as `COMPLETED` when `response_hash` is non-empty. New code should always set
`COMPLETED` explicitly for successful inference.

For `DEVSHARD_REQUESTS_DISABLED`, `POC_ABORTED`, and `NO_PRESERVE_NODES`:

- `response_hash` must be empty
- `input_tokens` must be `0`
- `output_tokens` must be `0`
- `actual_cost` must be `0`
- reserved cost is returned to session balance
- host cost is not incremented
- missed count is not incremented

No additional availability marker is carried in `MsgFinishInference`. Validators
use the corresponding `MsgStartInference.started_at` and their local
availability/PoC period history to decide whether a non-serving finish was
valid for the original request time.

## Validation Rules

When a peer validates a `MsgFinishInference`:

- `COMPLETED` follows the current response validation flow.
- `DEVSHARD_REQUESTS_DISABLED` is valid only if the validator's local chain
  history says `devshard_requests_enabled=false` when the request started
  (`MsgStartInference.started_at`).
- `POC_ABORTED` is valid only if the executor was in a PoC path that intentionally
  aborted the request.
- `NO_PRESERVE_NODES` is valid only if the host had no preserve nodes when the
  request arrived during PoC.

Validators should reject a non-serving finish if the reason and local evidence do
not match. This prevents a host from using the global pause marker to avoid work
outside a real pause window.

If a validator does not have enough local history to prove the availability or
PoC state at `MsgStartInference.started_at`, it should not accept the
non-serving finish. The history retention window must be at least as long as the
devshard diff and settlement retention window.

## PoC And Preserve-Node Cases

Two existing local-abort paths should also produce a zero-cost
`MsgFinishInference` instead of leaving the request to timeout:

- the host aborts the request because PoC work takes over the ML node
- the host receives a new request during PoC and has no preserve nodes available

These are separate reasons because peers validate them against different local
evidence. They both use an empty `response_hash`, but the reason field is what
makes the state transition auditable.

## State Accounting

A non-serving finish is a terminal state for the inference. It should use the
same lifecycle position as a successful `MsgFinishInference` so the user can
continue the session and eventually settle.

Accounting differs from a successful finish:

- no response bytes are committed
- no inference cost is charged
- no executor reward is accrued
- no missed or invalid host stat is recorded
- validators may still count required validation work if they were assigned to
  validate this inference

This keeps intentional unavailability neutral. It is neither useful work nor a
fault.

## Open Implementation Notes

- Add a guardian permission checker separate from module governance authority.
  It must handle `gonkavaloper` guardian config versus `gonka` transaction
  signers through explicit address conversion.
- Expose the current chain param through the bridge/runtime boundary so external
  hosts do not need decentralized-api-specific state.
- Add tests for pause, resume, stale history, invalid pause claim, PoC abort, and
  no-preserve-node finish handling.
