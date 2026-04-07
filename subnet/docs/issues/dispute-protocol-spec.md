# Issue: Subnet dispute protocol specification

## Summary

Define a canonical dispute protocol for user fraud in subnet sessions, including:

- canonical evidence envelope,
- deterministic verification rules,
- submission / quorum policy,
- settlement outcomes and replay protection.

This issue is protocol-first and chain-facing. It should produce a stable spec consumed by runtime and bridge implementation work.

## Scope

1. **Dispute transaction**
   - Define a new chain message type for subnet disputes.
   - Include minimal required fields:
     - escrow id,
     - dispute nonce,
     - fraud type,
     - evidence bundle,
     - quorum signatures,
     - reporter / submitter identity,
     - replay-protection fields.

2. **Evidence envelope**
   - Canonical format for each supported fraud class.
   - Deterministic serialization and signature domain separation.
   - Validation requirements that are stable across nodes.

3. **Verification and finality**
   - Quorum and signer authorization model.
   - Deterministic keeper-side verification.
   - Escrow state transition on accepted dispute.

4. **Outcome policy**
   - Forfeit / redistribution / refund policy by fraud type.
   - Required events and query surface for observability.

## Out of scope

- Runtime gossip and transport trigger plumbing.
- Local detection heuristics (tracked separately).

## Related

- [`user-equivocation-escrow-penalty.md`](./user-equivocation-escrow-penalty.md)
- [`user-fraud-detection-scope.md`](./user-fraud-detection-scope.md)
- [`dispute-flow-implementation.md`](./dispute-flow-implementation.md)

## Status

Open - protocol and keeper spec.
