# Issue: Finalize devshard when switching epochs

## Summary

On **epoch switch**, the system should **finalize the devshard** before or as we enter the next epoch. Participants can **change** between epochs; long-term this implies **dynamic shard chains** and complex membership management. For a **first step**, keep behavior **simple**: **force finalization** of the current chain (or session) when the epoch rolls.

## Motivation

- Without a clear **epoch boundary → finalize** rule, state and escrow semantics are ambiguous when the **validator set** changes.
- A minimal policy unblocks iteration while **dynamic shards** are designed separately.

## Possible direction (high level)

- **Watch mainnet (or mock) events** for epoch change.
- When epoch changes: transition hosts to a mode where they **accept only finalization-related traffic** from the **developer / owner** (or equivalent control path) until the devshard is **closed out**; **reject** other messages until finalization completes.

## Dependencies / follow-ups

- Broader **dynamic devshard** work: [`devshard-dynamic-participants.md`](./devshard-dynamic-participants.md).

## Status

Open — design and scope TBD.
