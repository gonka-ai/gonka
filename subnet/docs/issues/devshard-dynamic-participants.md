# Issue: Dynamic devshards — add/remove participants during lifetime

## Summary

**General goal:** devshards should support **dynamic membership**: participants **added** or **removed** while the devshard is live, not only at epoch boundaries.

## Motivation

- Real networks **slash** or rotate validators for **missed cPoCs**, **missed inferences**, or other faults; the subnet model should eventually reflect **changing** participant sets without tearing down the whole shard every time.
- This is **more complex** than “finalize on epoch switch” and likely needs **state migration**, **gossip peer lists**, and **escrow / slot** rules to stay consistent.

## Relation to other issues

- **Stepwise approach:** [`devshard-finalize-on-epoch-switch.md`](./devshard-finalize-on-epoch-switch.md) can ship first with **forced finalization**; dynamic membership is the **target architecture**.

## Process

**No implementation work should start until there is an approved design proposal** (under `subnet/docs/proposals/` or the team’s agreed location). This issue tracks the *goal*; the proposal must cover membership transitions, state migration, gossip topology, escrow/slots, and security.

## Status

Open — blocked on **approved proposal**; then protocol and storage design.
