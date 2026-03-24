# Research: Node Health-Aware Executor Selection

**Issue:** #3  
**Date:** 2026-03-24  
**Status:** Complete — implementation tasks created (#4, #5, #6)

---

## Summary

When a validator goes offline or degrades, it continues receiving inference requests via `GetRandomExecutor` (pure staking-weight random selection). This causes missed inferences (3.25% miss rate, 81k misses in epochs 161–191), validator penalties, and slow responses.

## Key Findings

### Miss Data Flow

```
Inference expires (EndBlock, module.go:319)
  → executor.CurrentEpochStats.MissedRequests++
  → UpdateParticipantStatus() → SPRT test (getInactiveStatus)
      → INACTIVE/INVALID: removeFromEpochGroups() ← exits selection pool
  
Epoch end (accountsettle.go:227):
  → EpochPerformanceSummary.MissedRequests persisted

Next epoch start (addEpochMembers, module.go:743):
  → calculateParticipantReputation() → Reputation score 0–100
  → member.Weight = p.Weight  ← staking tokens, NOT reputation!
  → ValidationWeights[].Reputation stored but UNUSED for selection
```

### Existing Circuit Breaker

The SPRT (`getInactiveStatus`, `calculations/status.go`) IS plugged into selection — it removes nodes from EpochGroup when statistically flagged. However, SPRT is conservative and slow. A node going from 0% → 28% miss rate needs 10–50+ inferences before SPRT triggers.

**Gap:** No fast intra-epoch circuit breaker for mid-epoch degradation.

### Weighted Selection

`selectRandomParticipant` (`epochgroup/random.go`) already uses cumulative-weight random selection. The weight is staking tokens. The reputation score (0–100, computed from historical miss data) is computed but never applied to selection weight.

**The fix is surgical:** apply reputation as a weight multiplier at epoch start.

### Latency

No on-chain latency tracking. Must be handled off-chain in decentralized-api.

---

## Implementation Tasks

| # | Title | Layer | Priority |
|---|-------|-------|----------|
| [#4](https://github.com/MinglesAI/gonka/issues/4) | Reputation-adjusted selection weight | On-chain, epoch start | High |
| [#5](https://github.com/MinglesAI/gonka/issues/5) | Intra-epoch fast circuit breaker in createFilterFn | On-chain, intra-epoch | High |
| [#6](https://github.com/MinglesAI/gonka/issues/6) | Off-chain latency-aware executor retry | Off-chain, decentralized-api | Medium |

---

## Architecture

### Component A: Reputation-Weighted Selection (#4)

At `addEpochMembers`, scale `member.Weight` by `reputation / 100`:
- Node with reputation 50 → 50% traffic share relative to full-weight node
- New nodes (reputation 0) → 1% floor (not zero)
- Perfect nodes (reputation 100) → full stake weight

### Component B: Fast Circuit Breaker (#5)

In `createFilterFn`, add `createHealthFilterFn`:
- Exclude nodes with `MissedRequests / Total > 25%` when `Total >= 4`
- Compose with existing PoC filter
- Safety fallback: if ALL nodes degraded, bypass filter (no empty pool)
- New `ValidationParams` fields: `HealthCircuitBreakerMissThreshold`, `HealthCircuitBreakerMinSamples`

### Component C: Latency Retry (#6)

In `decentralized-api/internal/server/public/post_chat_handler.go`:
- In-memory EMA latency tracker per executor
- Retry selection if `EMA > 2× median` with ≥4 samples (max 2 retries)
- No on-chain changes

---

## Files Referenced

- `inference-chain/x/inference/keeper/query_get_random_executor.go` — selection entry point
- `inference-chain/x/inference/epochgroup/random.go` — `selectRandomParticipant`
- `inference-chain/x/inference/module/module.go` — `addEpochMembers`, `handleInferenceExpiry`
- `inference-chain/x/inference/calculations/status.go` — SPRT circuit breaker
- `inference-chain/x/inference/calculations/reputation.go` — reputation calculation
- `inference-chain/x/inference/keeper/accountsettle.go` — epoch settlement, miss tracking
- `inference-chain/x/inference/types/params.go` — `ValidationParams`
- `subnet/host/timeout.go` — timeout verification (no changes needed)
- `decentralized-api/internal/server/public/post_chat_handler.go` — API executor selection
