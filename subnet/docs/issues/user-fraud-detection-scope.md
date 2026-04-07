# Issue: User fraud detection scope for subnet disputes

## Summary

Generalize dispute triggering beyond pure equivocation. Define which user-side fraud classes are detectable, what evidence is required, and what trigger threshold is needed before escalation to on-chain dispute submission.

## Initial fraud classes

1. **Equivocation at same nonce**
   - Conflicting user-signed state transitions leading to different state roots for one nonce.

2. **Fraudulent start attribution abuse**
   - Invalid `StartInference` origin/authorization relative to protocol attribution rules.

3. **Timestamp abuse**
   - Clearly invalid `started_at` / `confirmed_at` relations (for example far-future timestamps) in signed artifacts.

4. **Timeout/refusal abuse with signed protocol-invalid behavior**
   - Signed behavior that violates timeout/refusal protocol invariants.

5. **Additional findings**
   - Any newly discovered user-manipulated message parameter combinations that violate strict protocol rules.

## Deliverables

- Per-fraud-type trigger condition.
- Required evidence artifacts for canonical dispute envelope.
- Recommended local action while dispute is pending (halt / refuse / continue with safeguards).
- False-positive and replay resistance notes.

## Related

- [`inference-started-at-far-future.md`](./inference-started-at-far-future.md)
- [`timeout-flows-refusal-and-execution.md`](./timeout-flows-refusal-and-execution.md)
- [`dispute-protocol-spec.md`](./dispute-protocol-spec.md)

## Status

Open - detection policy and evidence mapping.
