# Issue: Secure propagation — gossip as a real security primitive

## Summary

**Gossip (nonce / tx propagation)** should be a **first-class, secured** control plane: **authenticated** senders, **authorized** peers, and **tamper-evident** payloads—not “best effort” disabled or wide open. Today it is **practically weak or disabled** in places; that is not acceptable for production.

## Motivation

- Without secure gossip, **partitioning**, **spoofing**, and **replay** at the propagation layer undermine timeout voting and state convergence.
- Hardening must be **testable** under adversarial scenarios.

## Evidence for punishment

When a peer sends **protocol-invalid** but **cryptographically signed** gossip (malformed tx, bad signature over payload, equivocation, wrong epoch/slot binding, etc.), honest nodes should **retain** or **forward** enough material to form **slashable evidence**: signed blobs plus context (peer identity, height/nonce, hash chain) so **mainnet** or governance can **attribute** fault and **punish**. The design should specify **what** is stored locally, **what** is committed to chain or dispute contracts, and **retention** bounds so storage stays bounded.

## Related

- **Adversarial E2E tests:** [`../proposals/PROTOCOL_TESTING_PROPOSAL.md`](../proposals/PROTOCOL_TESTING_PROPOSAL.md) (malicious host, gossip drop, bad votes).
- **Observability of gossip:** [`../proposals/OBSERVABILITY_PROPOSAL.md`](../proposals/OBSERVABILITY_PROPOSAL.md) (instrument fan-out, drops, retries).

## Status

Open — design authz model per escrow / epoch participant set; align with transport and `gossip` package.
