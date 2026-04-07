# Issue: Shard state bounded by height — trim finished inferences

## Summary

We **must not** grow shard state **unbounded** with every historical inference. When an inference is **finished** and **validated**, **remove** it from active state and keep **aggregates / counters** instead of full per-inference records forever.

## Motivation

- Long-running devshards and mainnet shards need **predictable** state size.
- Many hosts may **validate** the same inference; the model should allow a **validation recording window** or **timeout** so validation txs do not keep stale inference rows open indefinitely.

## Possible direction

- After final validation (or timeout), **prune** inference detail from state; retain **counters**, **hashes**, or **rollup** fields required for accounting and fraud proofs.
- Define a **time or height bound** within which **all expected validations** can be recorded before the record is **compacted**.

## Status

Open — needs state-machine and storage design.
