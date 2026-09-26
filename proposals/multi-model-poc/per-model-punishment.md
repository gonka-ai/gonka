# [DRAFT] Multi-Model PoC: Punishment Scope

## Problem

Proof of Compute (PoC), Confirmation PoC (cPoC), and inference routing are model-specific. However, participant active status is global. A failure on one model can therefore deactivate a participant across all models they serve.

Consider a participant `p` serving model A and model B.
Assume the scale factors for both models are 1.0.
The participant has model-local `pocWeight(A, p) = 40` and `pocWeight(B, p) = 60`.
This results in an expected `consensusWeight(p) = 100`.
If participant `p` passes cPoC for model A but fails for model B:

```text
expected consensusWeight(p) = 100
confirmed consensusWeight(p) = 40
confirmed fraction = 40 / 100
```

The current participant-wide rule can mark participant `p` inactive. This removes participant `p` from the model groups for both model A and model B. The same issue occurs when one model increases the participant's missed-request rate or invalid-inference rate while other models remain healthy.

## Punishment Scopes

### Participant-wide

Track one cPoC ratio, missed-request rate, invalid-inference rate, and active status per participant.

- The chain aggregates results from all served models.
- A participant-level threshold failure deactivates the participant. This removes them from every model group.
- Bitcoin-style rewards and the next-epoch weight cap use the participant's combined confirmed fraction.

This is the current behavior. It is simple. It treats failures as evidence about the participant as a whole. However, a failure on one model removes healthy capacity on other models.

### Per-model per participant

Track punishment state by `(participant, model_id)`.

- A failure on model B removes the participant's model-local `pocWeight(B, p)`, model-local rewards, and routing eligibility for model B.
- The participant remains active on model A. They keep their model-local `pocWeight(A, p)` as long as they pass checks for model A.
- Participant-wide active status remains available for severe failures that must deactivate the entire participant.

This isolates model-specific failures. However, it requires explicit rules to aggregate model-local states into `consensusWeight(p)`, rewards, and participant active status.

## Motivation

Models can fail independently. For example, heavy demand for the Kimi model can increase missed requests for Kimi. Meanwhile, the same participant serves another model normally. Deactivating the participant's healthy capacity on other models makes network overload worse.

The same distinction applies when a model group loses enough model-local `votingPower` that its cPoC results cannot be validated. This validation failure is model-specific. Whether the resulting punishment remains model-specific is a separate policy decision.
