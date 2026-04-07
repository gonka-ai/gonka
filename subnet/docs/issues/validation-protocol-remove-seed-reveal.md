# Issue: Validation protocol — remove seed-reveal phase

**Draft** — problem statement only; not reviewed for implementation.

## Summary

**Redesign the validation protocol** so it **does not rely on a separate seed-revealing phase** (or equivalent) that exists today. The new flow should preserve **security and verifiability** with a **simpler** round structure.

## Motivation

- Fewer phases → less **latency**, fewer **edge cases**, and easier **testing** (see [`PROTOCOL_TESTING_PROPOSAL.md`](../proposals/PROTOCOL_TESTING_PROPOSAL.md)).
- Seed-reveal mechanics may conflict with goals in [`shard-state-trim-inferences-by-height.md`](./shard-state-trim-inferences-by-height.md) and dynamic membership.

## Status

**Draft** — open for discussion; requires **approved protocol design** and migration plan before any code changes.
