# Proposal: Devshard v4.2

This proposal removes v5 from the approved devshard versions and adds v4.2. The existing v4.1 entry stays unchanged.

Devshard v4.2 polishes the v4 runtime with fixes for production issues and performance improvements. It uses less CPU and memory, reduces repeated validation work, fixes validation recovery, refusal handling, and gateway routing. The changes are listed below.

## Proposal

Update the approved devshard versions:

- Remove `v5`.
- Add `v4.2` with the fixes and performance improvements below.
- Keep the existing `v4.1` entry unchanged.

This update is approved through devshard governance and does not require a full chain software upgrade.

## Fixes and Improvements

### Fixes

1. Inference validation enforces every token in a streamed chunk instead of only the first.
2. ML requests and validation leases use the same 32-minute timeout.
3. Validation leases are scoped to the escrow's epoch, so expired escrows cannot create new validation work in a later epoch.
4. Recovery submits a validation vote for challenged inferences and skips queued work that is already resolved.
5. A refusal recheck accepts the timeout if the executor stops advancing or reports a mismatched state. An advancing executor gets at most three further challenges to return a valid receipt.
6. A confirmed inference stops the refusal attempt. Executor receipts must pass signature verification before they can cancel a refusal.
7. Executors return challenge receipts before execution finishes, so a running inference does not hold up the refusal check.
8. Pooled chat routes use the resolved model. Omitting the model cannot route a request to a different model with stricter access rules ([#1976](https://github.com/gonka-ai/gonka/pull/1976)).
9. DeepSeek V4 Flash 0731 validation handles known prompt-formatting differences between MLNode versions. It accepts exactly 78 or 79 extra claimed input tokens while retaining output and logit checks.

### Performance

1. The live inference set uses a running sum of Ristretto255 points, so adding, updating, or removing a record does not require hashing the whole set.
2. Diff application journals changed records for rollback instead of making full-state copies.
3. Payload signing keeps the decrypted key in memory and reloads it when its file changes.
4. Validation walks a journaled set of outstanding inference IDs instead of scanning all live inferences.
5. Each successful inference earns one per-model validation credit for that process, valid for 60 minutes. Each ML validation dispatch spends one credit, limiting validation load when generation slows.
6. Repeated lease conflicts are deferred to reduce scheduling overhead and log noise.
7. Refusal challenges forward only missing diffs when the executor's state matches. Full-history fallback remains for other cases.
8. Gateway catch-up reads history in pages, saves session state in each diff transaction, and trims cached history.
9. Auto-seal logs contain counts instead of full inference lists.

### Testing and Diagnostics

1. Regression tests cover validation, recovery, refusal handling, state updates, and routing.
2. A v4.2 load-testing harness covers sustained load, escrow rotation, and slow or failing ML nodes ([#1953](https://github.com/gonka-ai/gonka/pull/1953)).
3. Memory diagnostics run every 10 minutes without the stop-the-world memory-stat read.

## Implementation

Submit an on-chain governance proposal to update the approved devshard versions. The new entry uses:

- Register protocol `v4.2`.
- Add the binary URL when the final release is published.
- Add the SHA-256 checksum of that artifact.

The final binary URL and checksum must be added before submission. The existing `v4.1` binary URL and checksum remain unchanged.

`versiond` downloads and verifies the approved binary and serves it under `/devshard/v4.2`. Host and gateway releases are built from the same v4.2 source. Gateways must use v4.2 escrows to use the new runtime.

## Contributors (sorted alphabetically)

- @a-kuprin
- @aikuznetsov
- @gmorgachev
- @qdanik
- @vbgd0
