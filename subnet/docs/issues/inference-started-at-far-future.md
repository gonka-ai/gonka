# Issue: Timestamp abuse around start/confirm flow

## Summary

Clarify and harden timestamp validation in inference confirm flow.

The executor signs receipt data via **`MsgConfirmStart.ExecutorSig`** over **`ExecutorReceiptContent`** (which includes `StartedAt` and `ConfirmedAt`).

## Problem

- Executor can sign `MsgConfirmStart.confirmed_at` values that are far in the future or inconsistent with expected timing. It can lead to situation that inference will never be finished never be timed out.
- Current state apply path stores these fields but does not enforce a strict skew policy.

## Desired direction

1. **Executor-signed timestamp checks**
   - Validate `MsgConfirmStart.confirmed_at` consistency against policy and receipt content.
   - If executor-signed time is clearly abusive (far future / impossible relation), trigger a **voting flow to punish executor**.

2. **Evidence + punishment path**
   - Reuse timeout/dispute voting mechanics or define a dedicated vote type.
   - Vote for punishment checking confirmed_at against ever yhost local clock.

## Codebase pointers

- `subnet/state/machine.go`
  - `applyStartInference`: stores `rec.StartedAt = msg.StartedAt`.
  - `applyConfirmStart`: verifies `ExecutorSig` over `ExecutorReceiptContent{StartedAt, ConfirmedAt, ...}` and stores `rec.ConfirmedAt`.

## Related

- `timeout-flows-refusal-and-execution.md`
- `user-fraud-detection-scope.md`
- `secure-gossip-propagation.md`

## Status

Open — needs approved spec for skew bounds, canonical time source, evidence format, and executor-punishment vote flow.
