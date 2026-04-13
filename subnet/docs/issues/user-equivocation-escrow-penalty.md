# Issue: User equivocation — umbrella and migration note

## Summary

This document is the umbrella pointer for user-fraud dispute work. It is split into focused issues for protocol spec, detection scope, and runtime wiring.

## Split issues

- Protocol specification: [`dispute-protocol-spec.md`](./dispute-protocol-spec.md)
- Fraud detection scope: [`user-fraud-detection-scope.md`](./user-fraud-detection-scope.md)
- End-to-end implementation tracker: [`dispute-flow-implementation.md`](./dispute-flow-implementation.md)

## Why split

The topic grew beyond one issue:

- equivocation evidence format,
- broader user-fraud classes (attribution abuse, timestamp abuse, timeout abuse),
- chain message shape and keeper verification,
- runtime trigger wiring and retention policy.

## Equivocation baseline (kept)

At minimum, the dispute protocol must preserve the original baseline:

1. Detect conflicting state roots at the same nonce.
2. Persist slashable evidence and attribution context.
3. Allow quorum-backed escalation to an on-chain dispute path.

## Related

- [`secure-gossip-propagation.md`](./secure-gossip-propagation.md)
- [`user-fraud-detection-scope.md`](./user-fraud-detection-scope.md)
- [`inference-started-at-far-future.md`](./inference-started-at-far-future.md)
- [`timeout-flows-refusal-and-execution.md`](./timeout-flows-refusal-and-execution.md)
- [`shard-state-trim-inferences-by-height.md`](./shard-state-trim-inferences-by-height.md)
- [`../proposals/PROTOCOL_TESTING_PROPOSAL.md`](../proposals/PROTOCOL_TESTING_PROPOSAL.md)

## Status

Open - umbrella. See linked split issues for active workstreams.
