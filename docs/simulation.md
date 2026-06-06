# Simulation and Fuzz Testing

This document covers how to run, interpret, and extend Cosmos SDK simulation
tests for `inference-chain`. It is the operator/developer reference for
[issue gonka-ai/gonka#982](https://github.com/gonka-ai/gonka/issues/982) and
its phased implementation.

## Quick start

From repo root:

```bash
make sim-smoke-test   # 50 blocks × 20 ops, ~10 s
make sim-full-test    # 500 blocks × 200 ops, ~35 s (Phase 2+ recommended)
```

Both targets enter the `inference-chain` module and run
`TestFullAppSimulation` with the `sims` build tag; `sim-smoke-test` also
runs `TestFullSimulation_x_Inference_Integrated` (per-op state assertions).
The `*_Postrun` tests (see Test inventory) are not invoked by these
targets; they call `t.Skip` with the reason and only run when invoked
directly via `go test -tags sims ./app/...` without a `-run` filter.

## Run modes

| Target | Blocks × Ops | Seeds | Wall-clock | When to use |
|---|---|---|---|---|
| `sim-smoke-test` | 50 × 20 | 1 (`-Seed=99`) | ~10 s | Pre-PR sanity check; CI via [`.github/workflows/simulation.yml`](../.github/workflows/simulation.yml) (`workflow_dispatch` only, not a required PR check) |
| `sim-full-test` | 500 × 200 | 1 (`-Seed=99`) | ~35 s | After Phase 2 real ops land. Informative only when ops are not NoOpMsg stubs |
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
| `-GenesisTime` | `time.Now().Unix()` at process start | UNIX timestamp of the simulated genesis. The default is the **wall clock**, so it differs every process; pin it for cross-process reproducibility (see [Reproducibility](#reproducibility)). |
| `-Verbose` | `false` | Verbose logging. |

## Reproducibility

A simulation run is a pure function of **two** inputs, not one: the RNG
`-Seed` *and* `-GenesisTime`. Pinning `-Seed` alone is not enough.

`gonka-ai/cosmos-sdk@v0.53.3-ps17` registers `-GenesisTime` with a default
of `time.Now().Unix()` (`x/simulation/client/cli/flags.go:62`), evaluated
once per process. The genesis timestamp flows through `AppStateRandomizedFn`
into time-gated chain logic (gov/group voting windows, access cutoffs), so
two runs of the same `-Seed` minutes apart produce a different `opsCount`
and a different AppHash.

`TestAppStateDeterminism` does not catch this — it runs the same seed
several times *inside one process*, where `-GenesisTime` is already fixed.

`make sim-smoke-test` / `make sim-full-test` pin `-GenesisTime` (see
`inference-chain/Makefile`, `SIM_GENESIS_TIME`) so their AppHash is
reproducible across machines and CI. When reproducing a run with a direct
`go test`, pass the **same** `-GenesisTime` as the original, not just
`-Seed`.

## Validator set in long runs

A Cosmos simulation needs a non-empty validator set every block, or simsx
aborts the run (`x/simulation/simulate.go:266` — `t.Skip("empty validator
set")`).

`inference-chain`'s validator set is managed by the PoC flow:
`SetComputeValidators` (`cosmos-sdk/x/staking/keeper/compute.go`) rebuilds it
every epoch from compute results. The simulation does not run PoC, so it never
refreshes the set — while cosmos `x/slashing` keeps downtime-jailing validators
on the random vote-info simsx feeds each block. Left alone the bonded set
drains linearly to zero (observed ~block 135 of a seed-99 run) and the run
skips.

`EnsureComputeValidators` (`x/inference/simulation/bootstrap.go`) bridges the
gap: when the bonded validator count falls below a floor it feeds every
validator back through `SetComputeValidators`, which un-jails and re-bonds them
— mimicking production's per-epoch refresh. The x/inference op factories call
it, so `sim-full-test` completes a real 500-block run. Faithfully simulating
the full PoC validator rotation is separate, larger work.

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
| `TestFullSimulation_x_Inference_Integrated` | Smoke simsx run + post-run keeper-state assertions: StartInference/FinishInference/MsgValidation each reached state mutation | Green |
| `TestAppImportExport_Postrun` | Export → fresh app InitGenesis → KV-store diff | **Skipped** (tracks fork bonded-pool issue, see TODO and gonka-ai/gonka#1153) |
| `TestAppSimulationAfterImport_Postrun` | Export → fresh app InitChain → second simulation | **Skipped** (same fork issue + post-import simulation step not yet wired) |
| `TestAppStateDeterminism` | 3 seeds × 3 attempts: identical AppHash | Green |
| `TestApp_GenesisInit_*` | Genesis init produces blocked module accounts + PoC validators | Green |
| `TestDisabledOpsSimModule_*` | Wrapper produces empty WeightedOperations for staking/distribution/wasm | Green |
| `TestBankGenesisFix_SupplyRecomputed` | `fixBankGenesisState` makes Supply match Balances | Green |

## Phase status

`#982` defines a four-phase roadmap. Phases 1 and 2 are implemented; both
`sim-smoke-test` and `sim-full-test` exercise real `x/inference` operations.

- **Phase 1** — simsx migration, smoke/full Make targets, disabled upstream
  ops, restored test semantics, `fixBankGenesisState`. **Done.**
- **Phase 2** — first-wave `x/inference` real ops (SubmitNewParticipant,
  StartInference, FinishInference, Validation, ClaimRewards) replacing
  NoOpMsg stubs. **Done.**
- **Phase 3** — second-wave ops (Invalidate/Revalidate via failing
  `MsgValidation` + revalidation votes; participant-self ops:
  SubmitUnitOfComputePriceProposal, SubmitHardwareDiff,
  SubmitNewUnfundedParticipant), store decoders, custom invariants,
  realistic-frequency weight tuning, aggressive parameter-boundary
  fuzzing, multi-seed triage. **Done.**
- **Phase 4**: simulation operations for other custom modules (bls,
  bookkeeper, collateral, genesistransfer, restrictions, streamvesting).
  Not yet implemented.

## Parameter fuzzing (#982 Phase 3 bullet 5)

Each sim run derives a deterministic seeded `*rand.Rand` from
`simState.Seed` and applies it via
`x/inference/simulation/MutateSimParams` to randomize the
governance-mutable `Params` fields, pushing values to their
`Params.Validate()` **boundaries** (not safe mid-ranges).
`GenesisOnlyParams` (collateral_amount, model_init_params, ...) are NOT
touched — issue wording specifies "mutable runtime params, not genesis".

Three design rules (derived from the code, not from caution):

1. **Economic params** (prices, vesting, fees, weight ratios) are pushed
   to their `Validate()` boundaries — they are liveness-safe (an extreme
   price just makes a tx fail gracefully) and stress the
   accounting/invariant paths where findings #1265/#1269/#1273 live.
2. **Timing params** (`EpochParams`) are widened but kept internally
   consistent: `Validate()` does not enforce that PoC + validation stages
   fit inside `EpochLength`, but the simulation requires it, so the floor
   is held at the proven baseline (30) and stage durations are derived as
   fractions of `EpochLength`.
3. **Collateral slashing-activation levers** (`SlashFractionInvalid`,
   `SlashFractionDowntime`, `DowntimeMissedPercentageThreshold`,
   `GracePeriodEndEpoch`) are **NOT** fuzzed. The sim's upstream-validator
   substrate runs at `Tokens=1` (an InitChain shrink hack, see
   `app/sim_test.go::shrinkUpstreamStakingValidators`); ANY active slashing
   zeroes `Tokens`+`DelegatorShares` → `x/slashing` `Unjail` divides 0/0 →
   validators jail and never recover → the cometbft set drains and the run
   SKIPs on "empty validator set". This is a sim-substrate limitation, not
   a production constraint; those params are exercised by unit tests.

Parameter **combinations** are exercised via corner profiles: ~1/4 of
seeds drive every knob to its low boundary, ~1/4 to its high boundary,
the rest spread independently — so corner-combination effects surface
instead of averaging out. Same `-Seed=N` → same mutated params.

### Fields fuzzed

| Sub-struct | Field | Fuzz range | Note |
|---|---|---|---|
| EpochParams | `EpochLength` | [30, 120] | floor = proven baseline |
| EpochParams | `PocStageDuration` | `EpochLength/5` (≥2) | derived, stays viable |
| EpochParams | `PocValidationDuration` | `EpochLength/8` (≥1) | derived |
| EpochParams | `InferencePruningEpochThreshold` | [1, 10] | |
| ValidationParams | `MinRampUpMeasurements` | [1, 50] | |
| ValidationParams | `ExpirationBlocks` | [5, 80] | |
| ValidationParams | `MinValidationTrafficCutoff` | [0, 1000] | |
| PocParams | `DefaultDifficulty` | [1, 20] | |
| PocParams | `PocDataPruningEpochThreshold` | [1, 10] | |
| FeeParams | `MinGasPriceNgonka` | [0, 5000] | |
| FeeParams | `BaseValidationGas` | [500, 50000] | |
| CollateralParams | `BaseWeightRatio` | [0, 1] | non-slashing |
| CollateralParams | `CollateralPerWeightUnit` | [0, 100] | non-slashing |
| TokenomicsParams | `WorkVestingPeriod` | [0, 360] | |
| TokenomicsParams | `RewardVestingPeriod` | [0, 360] | |
| DynamicPricingParams | `StabilityZoneLowerBound` | [0, 0.49] | `< upper` by construction |
| DynamicPricingParams | `StabilityZoneUpperBound` | [0.51, 1.0] | |
| DynamicPricingParams | `PriceElasticity` | [0.01, 1.0] | |
| DynamicPricingParams | `UtilizationWindowDuration` | [1, 600] | |
| DynamicPricingParams | `MinPerTokenPrice` | [1, 1000] | |
| DynamicPricingParams | `BasePerTokenPrice` | [1, 2000] | |
| DynamicPricingParams | `GracePeriodPerTokenPrice` | [0, 1000] | |
| DynamicPricingParams | `GracePeriodEndEpoch` (pricing) | [0, 50] | pricing grace, not slashing |

**Held at defaults (slashing-activation levers, see design rule 3):**
`CollateralParams.{SlashFractionInvalid, SlashFractionDowntime,
DowntimeMissedPercentageThreshold, GracePeriodEndEpoch}`.

### Alternate initial values vs in-simulation governance flows

#982 Phase 3 asks to decide *which* mutable params are varied by
alternate initial values versus by in-simulation governance flows. The
decision here: **all** mutable params are varied via alternate initial
values (genesis fuzzing above). In-simulation governance-flow variation
(submitting `MsgUpdateParams` mid-run) is **not** used, because
`MsgUpdateParams` — like the participant allow-list messages
(`AddParticipantsToAllowList` / `RemoveParticipantsFromAllowList`) — is
authority-gated: its signer must be the gov module account, which has no
private key, so a simsx factory cannot sign for it. Driving it would
require the full x/gov proposal flow, and gonka does not register typed
proposal-msg sim handlers for `x/inference`. Genesis fuzzing covers the
same parameter space deterministically without that machinery.

Consequently, **authority-gated ops are intentionally not factory-driven**
(allow-list add/remove, `UpdateParams`). This is a coverage boundary, not
an oversight — the same simsx signing constraint that blocks them is
documented at the factory registration site (`module/simulation.go`).

### Multi-seed triage protocol

1. Run sim-full across at least 5 seeds. The Makefile pins `-Seed=99`,
   so call `go test` directly:

   ```bash
   for seed in 1 7 32 99 123 256 1024; do
     CGO_ENABLED=1 CGO_LDFLAGS="-L/lib/wasmvm-static" \
       go test -mod=readonly -tags "sims muslc" -timeout 600s \
       ./app/ -run "TestFullAppSimulation" \
       -Enabled=true -NumBlocks=500 -BlockSize=200 \
       -Seed=$seed -GenesisTime=1700000000 -Verbose=true \
       2>&1 | tee /tmp/sim-full-seed-$seed.log
   done
   ```

2. For each seed, record:
   - Outcome: PASS / SKIP `<reason>` / FAIL `<block, op, msg>`
   - Final height + opsCount (if PASS)
   - Invariant fires (if any) — from `subsystem=Crisis` log entries or
     post-run callback in `app/sim_test.go::checkInferenceInvariants`

3. Triage recurring failures by pattern:
   - **Same block × same op × same msg type** across seeds → real bug
     (file Finding issue à la #1205)
   - **Different block + SKIP `empty validator set`** across seeds →
     sim-fragility (substrate / validator-drain — out of P3-B scope)
   - **PASS on most, FAIL on outliers** → parameter edge case worth
     investigating; document in P3-8 weight-tuning rationale

### Custom invariants

Registered via `x/inference/keeper/invariants.go::RegisterInvariants` and
checked post-run in the sim harness:

| Invariant | Checks |
|---|---|
| `bank-backs-positive-balance` | module account balance ≥ Σ positive participant CoinBalances (solvency) |
| `no-stuck-voting` | no inference stuck in VOTING > 2 epochs past its own EpochId |
| `effective-epoch-fresh` | effective epoch index is current |
| `active-invalidations-ref-live` | every `ActiveInvalidations` entry references a live inference |

Two invariants from an earlier iteration were **removed** after the
multi-seed sweep proved them ill-specified (the fuzz caught our own bad
invariants before they shipped):

- `no-orphan-balance` (CoinBalance>0 ⟹ CurrentEpochStats activity>0) —
  false premise: `shareWorkWithValidators` legitimately credits a
  validator's CoinBalance without touching `CurrentEpochStats` (the
  validation count increments the *executor*, not the validating
  msg.Creator; the credit bypasses `AddToCoinBalance` so `EarnedCoins`
  stays 0). The money is tracked by the bookkeeper and backed per
  `bank-backs`.
- `net-balance-bounded-by-work` (|Σ CoinBalance| ≤ Σ EarnedCoins this
  epoch) — compares a cross-settle cumulative balance against a
  per-epoch counter; `shareWork` redistribution + per-participant settle
  resets break the equality legitimately. `bank-backs` is the sound
  solvency check.

### Multi-seed sweep

37-seed default-seed-list sweep (no `-Seed`, `-NumBlocks=100
-BlockSize=100`, `-GenesisTime` pinned). Of 33 seeds that complete (5
SKIP early on the `empty validator set` substrate limit), **30 break
`bank-backs-positive-balance`** — the module account ends under-funded
relative to participant CoinBalances.

**Root cause (seed=99, verbose trace):** the #1273 asymmetric invalidation
refund. 16 invalidations each emit `Invalid Inference subtracted from
Executor CoinBalance actualCost=N coinBalance=-N` — the executor is
debited the full `ActualCost` (into debt) while the validators' work-shares
are not reversed; the module, having refunded the client, is left short by
~Σ validator-shares (~1.12M ngonka gap on seed=99). `spendable == total`
on the module rules out a vesting artifact — genuine under-backing.

This is the **strongest demonstration of #1273**: not a grace-period edge
case but a solvency break on ~91% of completing seeds whenever
invalidations occur. The harness will turn green here once #1273 is fixed
upstream; until then the sweep is the regression signal for that fix.
Posted to gonka-ai/gonka#1273.
