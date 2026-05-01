## Description

When a verifier submits a validity array shorter than the dealer index being evaluated, that entry is treated as "no" vote (not counted). Tests that even with one verifier abstaining entirely (nil submission) and one submitting a short vector, dealers still pass quorum because their self-vote plus the one explicit yes is enough.

Also validates that the `dealerIndex >= len(verification.DealerValidity)` guard works correctly after the self-vote refactor — the guard is unchanged but must still fire correctly in the new logic.

## Preconditions

- [ ] `upgrade-v0.2.12` branch.
- [ ] Build passes.

## Setup

3 participants, slots [0-32]=33, [33-65]=33, [66-99]=34 (totalSlots=100, quorum=51).

All 3 submitted dealer parts.

| Verifier | D0 | D1 | D2 |
|---|---|---|---|
| V0 (33 slots) | ✓ | ✓ | ✓ |
| V1 (33 slots) | ✓ | (missing — short vector) | (missing) |
| V2 (34 slots) | (nil submission — absent) | (absent) | (absent) |

With self-vote:
- D0: self(33) + V1(33) [V0 skip as self], V2 absent = 66 ≥ 51 → **VALID**
- D1: self(33) + V0(33), V1 short-omits D1, V2 absent = 66 ≥ 51 → **VALID**
- D2: self(34) + V0(33), V1 short-omits D2, V2 absent = 67 ≥ 51 → **VALID**

## Steps

1. Run:
   ```bash
   cd inference-chain
   go test ./x/bls/keeper/... -v -run "^TestDetermineValidDealersWithConsensus_ShortVectorsCountAsNo$" -count=1
   ```
   **Expected:** `--- PASS: TestDetermineValidDealersWithConsensus_ShortVectorsCountAsNo`

2. Confirm result is `[true true true]` (was `[false false false]` pre-fix).

## Pass criteria

- Exit 0.
- All three dealers valid.

## Fail indicators

- `[false false false]` — self-vote not being applied, or short-vector guard eating implicit self-vote.
- Panic on nil submission access — bounds check missing.

## Source reference

- Test: `inference-chain/x/bls/keeper/phase_transitions_test.go:506` — `TestDetermineValidDealersWithConsensus_ShortVectorsCountAsNo`
- Implementation: `inference-chain/x/bls/keeper/phase_transitions.go:397` — `dealerIndex >= len(verification.DealerValidity)` guard

---

## Result — PASS

```bash
cd inference-chain
go test ./x/bls/keeper/... -v -run "^TestDetermineValidDealersWithConsensus_ShortVectorsCountAsNo$" -count=1
```

Output:
```
=== RUN   TestDetermineValidDealersWithConsensus_ShortVectorsCountAsNo
--- PASS: TestDetermineValidDealersWithConsensus_ShortVectorsCountAsNo (0.00s)
PASS
ok      github.com/productscience/inference/x/bls/keeper    1.335s
```

Result is `[true true true]`. Before the self-vote fix this test expected `[false false false]` — all three dealers failed because their only positive votes came from verifiers who abstained or submitted short vectors, and without the implicit self-vote there was nothing left to reach quorum. The fix initialises `validVotingSlots = dealerOwnSlots` before entering the verifier loop, so even a dealer whose peers all abstain carries its own slot weight. The `dealerIndex >= len(verification.DealerValidity)` bounds guard is unchanged and still fires correctly: short vectors are treated as abstentions, not panics.
