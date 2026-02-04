# Phase 4: Dual Migration Mode (V2 Tracking)

## Motivation

Enable a migration period to measure mlnode V2 adoption before fully switching. In migration mode, one confirmation PoC per epoch uses V2 (tracking only), while the rest use V1.

Key properties:
- **No new trigger mechanism**: reuse existing probabilistic confirmation PoC triggers.
- **V2 tracking**: first confirmation event per epoch (`event_sequence == 0`) uses V2 for tracking only, no weight impact.
- **V1**: all other confirmation events use V1 with weight/slashing enforcement.
- **Manual switch**: governance sets `poc_v2_enabled=true` once adoption is sufficient (no auto-switch).
- **Grace epoch**: when switching to full V2, the transition epoch runs confirmation PoC in dry-run mode (no punishment).

## Parameter Design

```protobuf
message PocParams {
  bool poc_v2_enabled = 8;
  bool confirmation_poc_v2_enabled = 9;  // enables migration mode
}
```

### Mode Matrix

| poc_v2 | confirm_v2 | Regular PoC | Confirmation PoC |
|--------|------------|-------------|------------------|
| false | false | V1 | V1 (all events) |
| false | true | V1 | event 0: V2 tracking, event 1+: V1 |
| true | true | V2 | V2 (all events) |
| true | false | Invalid | Treated as Full V2 |

## Event Dispatch Rules

In migration mode (`poc_v2_enabled=false, confirmation_poc_v2_enabled=true`):

- `event_sequence == 0`: V2 tracking only (no weight impact)
- `event_sequence >= 1`: V1 (affects weights/slashing)

Notes:
- At most 1 V2 tracking event per epoch (best-effort, probabilistic).
- V2 tracking logs coverage metrics but does NOT modify weights.

## Grace Epoch (V2 Transition Protection)

When switching from migration mode to full V2 (`poc_v2_enabled=true`), nodes may not have been tracking V2 data from the start of the current epoch. To prevent unfair punishment:

1. The epoch when `poc_v2_enabled` becomes true is stored as the **grace epoch**.
2. Confirmation PoC events in the grace epoch run in **dry-run mode** (no weight impact).
3. Starting from the next epoch, confirmation PoC applies normal weight evaluation.

```
Epoch N (migration mode)     Epoch N (grace, V2)          Epoch N+1 (V2)
|----------------------|     |----------------------|     |----------------------|
|  event 0: V2 dryRun  |     |  event X: V2 dryRun  |     |  event X: V2 normal  |
|  event 1+: V1 normal |     |  (grace epoch)       |     |  (punishment active) |
|----------------------|     |----------------------|     |----------------------|
                        ^
                   governance sets
                   poc_v2_enabled=true
```

This is automatic - governance only needs to set `poc_v2_enabled=true`.

## Implementation

### DAPI Changes

#### poc/migration.go (centralized pure functions)

```go
// IsMigrationMode returns true when in migration mode.
// Migration mode: poc_v2_enabled=false, confirmation_poc_v2_enabled=true.
func IsMigrationMode(pocV2Enabled, confirmationPocV2Enabled bool) bool {
    return !pocV2Enabled && confirmationPocV2Enabled
}

// ShouldUseV2 determines if V2 PoC should be used.
// - Full V2 mode (pocV2Enabled=true): always V2
// - Migration mode + confirmation event_sequence == 0: V2
// - Otherwise: V1
func ShouldUseV2(pocV2Enabled, confirmationPocV2Enabled bool, confirmationEvent *types.ConfirmationPoCEvent) bool {
    if pocV2Enabled {
        return true
    }
    if IsMigrationMode(pocV2Enabled, confirmationPocV2Enabled) &&
        confirmationEvent != nil && confirmationEvent.EventSequence == 0 {
        return true
    }
    return false
}

// ShouldUseV2FromEpochState is a convenience wrapper for EpochState.
func ShouldUseV2FromEpochState(epochState *chainphase.EpochState) bool {
    if epochState == nil {
        return true // default V2
    }
    return ShouldUseV2(
        epochState.PocV2Enabled,
        epochState.ConfirmationPocV2Enabled,
        epochState.ActiveConfirmationPoCEvent,
    )
}
```

#### broker/broker.go

```go
func (b *Broker) IsV2EndpointsEnabled() bool {
    return b.phaseTracker.IsPoCv2Enabled() || b.phaseTracker.IsConfirmationPoCv2Enabled()
}

func (b *Broker) IsMigrationMode() bool {
    return !b.phaseTracker.IsPoCv2Enabled() && b.phaseTracker.IsConfirmationPoCv2Enabled()
}

func (b *Broker) shouldUseV2ForPoC(confirmationEvent *types.ConfirmationPoCEvent) bool {
    if b.IsPoCv2Enabled() {
        return true
    }
    if b.IsMigrationMode() && confirmationEvent != nil && confirmationEvent.EventSequence == 0 {
        return true
    }
    return false
}
```

#### poc/orchestrator.go

```go
// shouldUseV2 determines if V2 validation should be used.
// Handles both full V2 mode and migration mode (event_sequence == 0).
func (o *orchestratorImpl) shouldUseV2() bool {
    if o.phaseTracker == nil {
        return true // default V2
    }
    epochState := o.phaseTracker.GetCurrentEpochState()
    return ShouldUseV2FromEpochState(epochState)
}
```

#### mlnode/post_generated_artifacts_v2_handler.go

```go
if s.broker != nil && !s.broker.IsV2EndpointsEnabled() {
    return echo.NewHTTPError(http.StatusServiceUnavailable, "V2 endpoints disabled")
}
```

#### public/poc_handler.go

```go
// V1 mode: proof API is not available (batches are on-chain)
// In migration mode, V2 proofs are enabled for confirmation PoC event_sequence == 0
if s.phaseTracker != nil {
    epochState := s.phaseTracker.GetCurrentEpochState()
    if !poc.ShouldUseV2FromEpochState(epochState) {
        return echo.NewHTTPError(http.StatusServiceUnavailable, "proof API requires V2 mode")
    }
}
```

### Chain Changes

#### keeper/poc_v2_enabled_epoch.go

Tracks the epoch when `poc_v2_enabled` was first set to true (grace epoch).

```go
// SetPocV2EnabledEpoch stores the epoch when poc_v2_enabled was first set to true.
func (k Keeper) SetPocV2EnabledEpoch(ctx context.Context, epoch uint64) error

// GetPocV2EnabledEpoch returns the epoch when poc_v2_enabled was first set to true.
func (k Keeper) GetPocV2EnabledEpoch(ctx context.Context) (uint64, bool)
```

#### keeper/params.go

Auto-sets grace epoch when `poc_v2_enabled` transitions false -> true:

```go
func (k Keeper) SetParams(ctx context.Context, params types.Params) error {
    oldParams, _ := k.GetParams(ctx)
    // ... store params ...
    
    // Auto-set grace epoch when poc_v2_enabled transitions false -> true
    if params.PocParams != nil && params.PocParams.PocV2Enabled {
        wasV2Disabled := oldParams.PocParams == nil || !oldParams.PocParams.PocV2Enabled
        if wasV2Disabled {
            if _, exists := k.GetPocV2EnabledEpoch(ctx); !exists {
                if epoch, found := k.GetEffectiveEpochIndex(ctx); found {
                    k.SetPocV2EnabledEpoch(ctx, epoch)
                }
            }
        }
    }
    return nil
}
```

#### module/confirmation_poc.go

```go
func (am AppModule) updateConfirmationWeights(...) error {
    migrationState := GetMigrationStateFromParams(params.PocParams)

    useV2, dryRun := false, false
    switch migrationState {
    case ModeFullV2:
        useV2 = true
        // Grace period: dry-run for the epoch when V2 was enabled
        if graceEpoch, ok := am.keeper.GetPocV2EnabledEpoch(ctx); ok && event.EpochIndex == graceEpoch {
            dryRun = true
        }
    case ModeMigration:
        if event.EventSequence == 0 {
            useV2, dryRun = true, true
        }
    }
    am.evaluateConfirmation(ctx, event, ..., useV2, dryRun)
    return nil
}
```

## Files Summary

### DAPI

| File | Change |
|------|--------|
| `poc/migration.go` | Add `IsMigrationMode()`, `ShouldUseV2()`, `ShouldUseV2FromEpochState()` pure functions |
| `poc/orchestrator.go` | Use `ShouldUseV2FromEpochState()` for V1/V2 validation dispatch |
| `poc/commit_worker.go` | Use `ShouldUseV2FromEpochState()` instead of inline migration check |
| `broker/broker.go` | Add `IsV2EndpointsEnabled()`, `IsMigrationMode()`, `shouldUseV2ForPoC()` |
| `public/poc_handler.go` | Use `ShouldUseV2FromEpochState()` to allow V2 APIs in migration mode |
| `mlnode/post_generated_artifacts_v2_handler.go` | Use `IsV2EndpointsEnabled()` |
| `chainphase/phase_tracker.go` | `IsConfirmationPoCv2Enabled()` (existing) |

### Chain

| File | Change |
|------|--------|
| `types/keys.go` | Add `PocV2EnabledEpochPrefix` |
| `keeper/keeper.go` | Add `PocV2EnabledEpoch` collection |
| `keeper/poc_v2_enabled_epoch.go` | Get/Set for grace epoch |
| `keeper/params.go` | Auto-set grace epoch on V2 transition |
| `module/confirmation_poc.go` | Switch on migration state + grace epoch check |
| `keeper/query_confirmation_poc_events.go` | Query to list confirmation PoC events by epoch |

## Migration Sequence

1. **Deploy** with `poc_v2_enabled=false, confirmation_poc_v2_enabled=false` (Full V1).
2. **Enable migration** via governance: set `confirmation_poc_v2_enabled=true`.
3. **Monitor** V2 tracking results (first confirmation event per epoch):
   - Query `ListConfirmationPoCEvents` to get trigger heights.
   - Query V2 data (StoreCommits) at trigger heights off-chain.
   - V1 continues for other events.
4. **Manual switch**: submit governance proposal to set `poc_v2_enabled=true` once adoption is sufficient.
5. **Grace epoch**: the epoch when V2 was enabled runs confirmation PoC in dry-run mode (automatic).
6. **Full V2 active**: starting next epoch, all PoC (regular + confirmation) uses V2 with punishment.

