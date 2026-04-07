# Issue: Implement subnet dispute flow end-to-end

## Summary

Implement the runtime and bridge flow that turns detected user fraud into a submitted on-chain dispute transaction, with deterministic evidence and verification.

## Workstreams

1. **Chain message and keeper**
   - Add dispute tx type and verification logic in inference-chain.
   - Add events and query visibility.

2. **Bridge wiring**
   - Extend subnet bridge API for dispute submission.
   - Implement submission in decentralized-api bridge.
   - Add testenv parity path.

3. **Runtime detection hooks**
   - Add trigger points in gossip / transport / host for fraud classes in scope.
   - Build canonical evidence payloads.
   - Gather and validate quorum signatures.

4. **Reliability and retention**
   - Persist dispute-critical evidence long enough for submission and audit.
   - Add bounded storage and pruning policy compatibility.

5. **Testing**
   - Unit tests for message/evidence validation.
   - Integration tests for trigger-to-submit flow.
   - Testenv E2E for each fraud class.

## Dependencies

- [`dispute-protocol-spec.md`](./dispute-protocol-spec.md)
- [`user-fraud-detection-scope.md`](./user-fraud-detection-scope.md)

## Status

Open - implementation tracker.
