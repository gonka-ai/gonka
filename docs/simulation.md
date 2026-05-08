# Simulation and Fuzz Testing

This document covers how to run, interpret, and extend Cosmos SDK simulation
tests for `inference-chain`. It is the operator/developer reference for
[issue gonka-ai/gonka#982](https://github.com/gonka-ai/gonka/issues/982) and
its phased implementation.

## Quick start

From repo root:

```bash
make sim-smoke-test   # 50 blocks × 20 ops, ~15 min
make sim-full-test    # 500 blocks × 200 ops, ~120 min (Phase 2+ recommended)
```

Both targets enter the `inference-chain` module and run `go test -run
TestFullAppSimulation` with the `sims` build tag. The `*_Postrun` tests
(see Test inventory) are not invoked by these targets; they call `t.Skip`
with the reason and only run when invoked directly via
`go test -tags sims ./app/...` without a `-run` filter.

## Run modes

| Target | Blocks × Ops | Seeds | Wall-clock | When to use |
|---|---|---|---|---|
| `sim-smoke-test` | 50 × 20 | 1 (`-Seed=99`) | ~15 min | Pre-PR sanity check; CI via [`.github/workflows/simulation.yml`](../.github/workflows/simulation.yml) (`workflow_dispatch` only, not a required PR check) |
| `sim-full-test` | 500 × 200 | 1 (`-Seed=99`) | ~120 min | After Phase 2 real ops land. Informative only when ops are not NoOpMsg stubs |
| direct `go test` (with `-Seed=N`) | per `-NumBlocks` / `-BlockSize` flags | 1 (the supplied `-Seed`) | depends | Reproducing a specific seed, debugging |
| direct `go test` (no `-Seed`) | per flags | simsx `defaultSeeds` (37 seeds, parallel subtests) | depends | Broad coverage sweep |

**Seed dispatch:** `TestFullAppSimulation` reads `-Seed` via `simcli.NewConfigFromFlags()`. When set, it calls `simsx.RunWithSeed` for a single-seed run (matches Make targets). When unset (CLI default sentinel), it falls through to `simsx.Run` which fans out the framework's default 37-seed list as parallel `t.Run("seed: N", ...)` subtests.

## Build tags

| Tag | Purpose |
|---|---|
| `sims` | Includes simulation test files (`sim_test.go`, `sim_bench_test.go`). Required for all simulation runs. |
| `simsbench` | Includes benchmark variant (`BenchmarkFullAppSimulation`). Used for performance profiling, not correctness. |
| `muslc` | Required when building under Alpine/musl (CI, canonical Docker). Links static `libwasmvm_muslc.x86_64.a`. |

Combine with `,` or space:

```bash
go test -tags 'sims muslc' ./app/...                  # production-tag combo
go test -tags 'sims simsbench muslc' ./app/...        # also picks up benchmark
```

## CLI flags

Read by `simcli.GetSimulatorFlags()` in `app/sim_test.go:init`:

| Flag | Default | Meaning |
|---|---|---|
| `-Enabled` | `false` | Master switch. Set to `true` to actually run simulation; without it the tests skip. |
| `-Commit` | `false` | Commit blocks during simulation. Required for any meaningful run. |
| `-NumBlocks` | (unset) | Number of blocks per simulation. |
| `-BlockSize` | (unset) | Number of ops attempted per block. |
| `-Seed` | (unset; falls back to simsx defaultSeeds list) | Specific seed to use. Override for reproducing failures. |
| `-Verbose` | `false` | Verbose logging. |

## Expected output

Successful smoke run prints per-seed summaries:

```
+++ DONE (seed: 99):
... (operation stats per module)
```

A successful determinism run (`TestAppStateDeterminism`) is silent in
non-verbose mode and exits 0. The test runs 3 seeds × 3 attempts each (9
runs internally).

A successful full run prints the same per-seed summary plus
`PrintStats(testInstance.DB)` output (db-level statistics) when `-Commit=true`.

## Failure interpretation

### Determinism failure

```
non-determinism in seed N: attempt 0 vs M
```

Different AppHash across attempts of the same seed. Common causes:

- Go map iteration in keeper code (use `Iterate` with `PrefixedPairRange`).
- `time.Now()` or other system-clock reads in state code.
- RNG used outside the `r *rand.Rand` provided by simulation.
- Floating-point arithmetic in state code (use `shopspring/decimal`).

Capture the failing seed and re-run `TestAppStateDeterminism` with `-Seed=N`
to isolate it. The test honors `-Seed` and overrides its internal triplet
(`defaultSimSeeds`) when a non-default seed is passed:

```bash
cd inference-chain
go test -mod=readonly -tags 'sims muslc' \
    -run TestAppStateDeterminism ./app/... \
    -Enabled=true -Commit=true -Seed=42 -v
```

### Bonded-pool panic on import

```
panic: bonded pool balance is different from bonded coins:  <-> NNNNstake
  at gonka-ai/cosmos-sdk@v0.53.3-ps17/x/staking/keeper/genesis.go:158
```

This is a **known fork-level inconsistency**, not a bug in the simulation.
`TestAppImportExport_Postrun` and `TestAppSimulationAfterImport_Postrun`
hit this panic by design; see the TODO blocks above each test in
`app/sim_test.go`.

Production avoids this path by using `x/upgrade` in-place handlers
(`app/upgrades/v0_2_*`) rather than vanilla `inferenced export → init`.

### Linker error: cannot find -lwasmvm_muslc.x86_64

When running outside the canonical Docker:

```
ld: cannot find -lwasmvm_muslc.x86_64: No such file or directory
```

The static lib name MUST include the architecture suffix exactly:
`libwasmvm_muslc.x86_64.a` (not `libwasmvm_muslc.a`). Download from
[CosmWasm/wasmvm releases](https://github.com/CosmWasm/wasmvm/releases),
match version with `inference-chain/go.mod`.

## Reproducing failing seeds

Smoke run prints `seed: N` for each parallelised sub-test. Re-run a single
seed:

```bash
cd inference-chain
go test -mod=readonly -tags 'sims muslc' \
    -run 'TestFullAppSimulation/seed:_<N>' ./app/ \
    -Enabled=true -Commit=true -NumBlocks=50 -BlockSize=20 -v
```

For `TestAppStateDeterminism`, the seed appears in the failure message
directly.

## Canonical Docker run

Local environments often have ABI mismatches with `bytedance/sonic` or
missing wasmvm libs. Canonical run uses Alpine + musl + static wasmvm. The
`WASMVM_VERSION` is derived inside the container from `inference-chain/go.mod`
so it stays in sync with the dependency:

```bash
docker run --rm --network host \
  -v "$PWD":/src -w /src/inference-chain \
  -v "$HOME/go/pkg/mod":/go/pkg/mod \
  golang:1.24.2-alpine3.20 sh -c '
    apk add --no-cache gcc musl-dev curl &&
    WASMVM_VERSION=$(go list -m github.com/CosmWasm/wasmvm/v2 | awk "{print \$2}") &&
    mkdir -p /lib/wasmvm-static &&
    curl -sL -o /lib/wasmvm-static/libwasmvm_muslc.x86_64.a \
      "https://github.com/CosmWasm/wasmvm/releases/download/${WASMVM_VERSION}/libwasmvm_muslc.x86_64.a" &&
    CGO_ENABLED=1 CGO_LDFLAGS="-L/lib/wasmvm-static" \
      go test -mod=readonly -tags "sims muslc" ./app/...
  '
```

`--network host` is required because Docker bridge often timeouts to
`github.com` when downloading wasmvm.

## Test inventory

| Test | What it checks | Status |
|---|---|---|
| `TestFullAppSimulation` | Top-level simsx run: random genesis → simulate → CheckExportSimulation | Green |
| `TestAppImportExport_Postrun` | Export → fresh app InitGenesis → KV-store diff | **Skipped** (tracks fork bonded-pool issue, see TODO and gonka-ai/gonka#1153) |
| `TestAppSimulationAfterImport_Postrun` | Export → fresh app InitChain → second simulation | **Skipped** (same fork issue + post-import simulation step not yet wired) |
| `TestAppStateDeterminism` | 3 seeds × 3 attempts: identical AppHash | Green |
| `TestApp_GenesisInit_*` | Genesis init produces blocked module accounts + PoC validators | Green |
| `TestDisabledOpsSimModule_*` | Wrapper produces empty WeightedOperations for staking/distribution/wasm | Green |
| `TestBankGenesisFix_SupplyRecomputed` | `fixBankGenesisState` makes Supply match Balances | Green |

## Phase status (as of 2026-05-08)

- **Phase 1** (this issue): simsx migration, smoke/full Make targets, disabled
  upstream ops, restored test semantics, fixBankGenesisState. **In progress.**
- **Phase 2**: first-wave x/inference real ops (SubmitNewParticipant,
  StartInference, FinishInference, Validation, ClaimRewards) replacing
  NoOpMsg stubs. After this phase, `sim-full-test` produces meaningful signal.
- **Phase 3**: weight tuning, store decoders, custom invariants,
  parameter-edge fuzzing. PoC-state determinism beyond AppHash.
- **Phase 4**: simulation operations for other custom modules (bls,
  bookkeeper, collateral, genesistransfer, restrictions, streamvesting).
