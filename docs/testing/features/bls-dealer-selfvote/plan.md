# BLS Dealer Self-Vote Fix — Test Plan

**PR:** [#1112](https://github.com/gonka-ai/gonka/pull/1112)  
**Branch merged:** `upgrade-v0.2.12` (commit `633a2022`)  
**Tested:** 2026-04-30

---

## What changed (from the diff, not the description)

`determineValidDealersWithConsensus` in `phase_transitions.go`:

1. `validVotingSlots = dealerOwnSlots` — dealer implicitly votes for itself before the verifier loop
2. `if verifierIndex == dealerIndex { continue }` — dealer's own verification submission skipped to prevent double-counting
3. `dealerIsValid := effectiveTotalSlots > 0 && validVotingSlots >= quorumSlots` — uses inclusive total
4. New `excludedVerifiersByDealer map[int]map[int]struct{}` parameter with `effectiveTotalSlots` reduction (dispute path)
5. `len(epochBLSData.DealerParts[dealerIndex].Commitments) > 0` added to `dealerSubmittedParts` check
6. Removed warning log "Dealer cannot reach weighted quorum with self-vote excluded"

---

## Risk triage

| Rank | Code path | Blast radius |
|------|-----------|--------------|
| 1 | Self-vote init + loop skip (#1, #2) | Wrong quorum → epoch FAILED or bad key material |
| 2 | `effectiveTotalSlots > 0` guard (#4) | Dispute path: zero-denominator produces invalid = false for all |
| 3 | `len(Commitments) > 0` check (#5) | Dealer with address but no commitments silently passes without this |
| 4 | Removed warn log (#6) | Observability only |

---

## Unit tests — run only, not recorded as TCs

```bash
cd /tmp/gonka-pr1112/inference-chain
go test ./x/bls/keeper/... -count=1
```

Result: `ok 70.779s` — all pass.

Tests exercised: `TestDetermineValidDealersWithConsensus`, `_TieVotes`, `_DealerOwnsExactlyHalfSlots`, `_ShortVectorsCountAsNo`, `_ExcludedVerifiersAreDealerScoped`, `TestCompleteDKG_*`, `TestProcessDKGPhaseTransitionForEpoch_Verifying*`, `TestAdjudicateComplaints_*`.

---

## Test cases (paths NOT covered by existing unit tests)

| ID | Code path | Category |
|----|-----------|----------|
| TC-BSV-001 | #2 — double-count guard: dealer's own `true` verification not added on top of self-vote | Boundary |
| TC-BSV-002 | #5 — `len(Commitments) == 0` with valid address → rejected | Negative |
| TC-BSV-003 | #4 — all verifiers excluded → `effectiveTotalSlots = 0` → all INVALID | Boundary / dispute edge |
| TC-BSV-004 | Economics — majority dealer (>50% slots) gets no external votes → passes on self alone | Incentive |
| TC-BSV-005 | Economics — majority dealer cannot be rescued by self-vote when peers correctly reject bad parts | Incentive |
| TC-BSV-006 | State machine — cumulative valid-dealer slot total exactly at `totalSlots/2` vs `totalSlots/2+1` | Boundary / state transition |

---

## State diagram

```
DEALING
  └─→ VERIFYING
        ├─→ DISPUTING  (candidateDealerSlots > totalSlots/2)
        │     └─→ COMPLETED
        └─→ FAILED     (candidateDealerSlots ≤ totalSlots/2)
```

The self-vote fix only touches the VERIFYING → DISPUTING/FAILED branch point.  
The `candidateDealerSlots > totalSlots/2` downstream threshold is unchanged (intentional: threshold signing also requires strict majority).
