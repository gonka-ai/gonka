# Gateway accounting implementation review

Base: `daeca28bd164257a490bb84837c0d456f81663b8`.

This file describes the current working tree, including staged local
changes.

## Result

The backend exposes every disposition and policy branch in the two
diagrams in `proposals/gateway-dashboard/README.md`.

The implementation consists of:

- one synchronous committed-diff observer;
- four synchronous gateway fact methods;
- one `devshard/accounting` package inside the existing `devshard`
  module;
- periodic and synchronous lifecycle snapshots;
- a private JSON API and current-epoch Prometheus collector.

The Python dashboard sidecar is not implemented.

## Boundary

`devshard/state/machine.go` has no accounting changes. The user session
exposes a generic diff observer and invokes it after a committed diff.
The callback runs synchronously while the session lock is held.

`devshard/accounting` depends on `devshard/types`. It owns protocol
transaction parsing, gateway vocabulary normalization, classification,
queries, SQLite snapshots, HTTP, and Prometheus.

Gateway facts enter through:

```go
e.accounting.Ghost(...)
e.accounting.RealSend(...)
e.accounting.Usage(...)
e.accounting.TimeoutResult(...)
```

Recorder methods on a nil `*accounting.Recorder` are safe no-ops.

## Classification

Committed diffs determine:

- consumed inference and protocol-only nonces;
- receipt, finish, and applied timeout transitions;
- challenged, validated, and invalidated verdicts;
- current `LatestNonce`, `HostStats`, and escrow phase.

Gateway callbacks determine:

- whether a request was sent;
- no-send policy reason and quarantine mode;
- selected winner or known loser;
- timeout outcome, reason, and failure context.

Before the protocol deadline, unfinished sent work is `in_flight`. After
the deadline it is `unfinished_refused` or `unfinished_execution`.
A late receipt can move refused work back to `in_flight`; it becomes
`unfinished_execution` only after the execution deadline.

State-root divergence is not reported as capability exclusion. It is an
unknown no-send policy with `detail_reason=escrow_state_root_diverged`.
Every non-applied timeout has a fixed reason; unrecognized or unavailable
detail becomes `unknown`.

## Recovery

Mutable nonce facts live only in memory and are not stored as per-nonce
rows. Restart recovery is intentionally best effort:

- persisted terminal counters resume from the last snapshot;
- lost live sends and pre-deadline timeout results become
  `unclassified`;
- recovered `HostStats` remain protocol ground truth;
- missing applied-timeout context remains a cross-check error;
- recovery never invents an `unfinished_refused` disposition.

Receipt, finish, and timeout ordering is supported while the process
retains the live nonce. It is not guaranteed across restart.

## Lifecycle

Finalization and settlement retain unresolved live facts for the rest of
the process. They do not immediately erase them or force them into
`unclassified`.

Periodic snapshots run in the background. Finalization, settlement,
retirement, and shutdown can also perform synchronous SQLite flushes.
The request path performs only in-memory accounting updates.

Gateway shutdown waits for registered background loser cleanup before
closing sessions and accounting, then writes the final snapshot.

## Visibility

The private API exposes:

- `GET /api/v1/epochs`;
- `GET /api/v1/epochs/{epoch}/participants`;
- `GET /api/v1/epochs/{epoch}/participants/{participant}`.

Prometheus exports current-epoch disposition, timeout, protocol, live,
pending, unknown, writer-error, recording-error, and cross-check gauges.
The existing live slot-decision metric records both `real_send` and
`ghost_no_send`.

## Verification

Focused tests cover:

- all disposition and fixed policy categories;
- deadline and late-receipt classification;
- all timeout outcomes and reason fallback;
- duplicate callbacks and in-process event ordering;
- restart gaps without invented dispositions;
- finalization retaining live facts;
- production committed-diff, ghost, winner, loser, and state-divergence
  seams;
- shutdown waiting for background race cleanup;
- API filters and representative metrics/query parity.

Latest verification:

```bash
cd devshard
go test ./accounting ./cmd/devshardctl ./state ./user -count=1
go test -race ./accounting -count=1
go vet ./accounting ./cmd/devshardctl ./state ./user
```

All commands pass. `git diff --check` and IDE lint checks are clean.
