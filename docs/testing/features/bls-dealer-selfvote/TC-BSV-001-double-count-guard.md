# TC-BSV-001: Dealer's own verification submission does not add weight on top of implicit self-vote

## Description

The fix adds `validVotingSlots = dealerOwnSlots` (implicit self-vote) and then loops all verifiers skipping `verifierIndex == dealerIndex`. If that skip were absent, a dealer who also submits a verification with `DealerValidity[self] = true` would have their slot weight counted twice — once as the implicit self-vote, once as an explicit verification vote. This test constructs exactly that scenario and confirms the tally is correct.

No existing unit test covers this: all existing tests either don't submit verification from the dealer's own index or rely on the skip silently working. This test makes the skip's effect visible.

## Preconditions

- [ ] `upgrade-v0.2.12` checked out.
- [ ] `cd inference-chain && go build ./x/bls/...` succeeds.

## Setup

Write a new Go test or adapt via test helper. 2 participants, 50 slots each (totalSlots=100, quorum=51).

- Dealer 0 submits parts (address + commitments).
- **Dealer 0 also submits a verification vector with `DealerValidity[0] = true`** — i.e., it votes for itself explicitly.
- Dealer 1 (the other verifier) votes `DealerValidity[0] = false`.

Expected tally for D0:
- Self-vote (implicit): 50 slots
- D0's own verification: **skipped** by `verifierIndex == dealerIndex` guard
- D1's vote: false → not counted
- Total: 50 < 51 → **INVALID**

If the guard were absent, D0 would get 50 (implicit) + 50 (explicit) = 100 → VALID, which is wrong.

## Steps

1. Write the test case described in Setup in `inference-chain/x/bls/keeper/phase_transitions_test.go`.

2. Run it:
   ```bash
   cd inference-chain
   go test ./x/bls/keeper/... -v -run "^TestDetermineValidDealersWithConsensus_SelfVerificationNotDoubleCountedAsNo$" -count=1
   ```
   **Expected:** `PASS`, `validDealers[0] = false`.

3. To confirm the guard is load-bearing: temporarily remove the `verifierIndex == dealerIndex` skip from `determineValidDealersWithConsensus`, re-run.
   **Expected:** test flips to FAIL (D0 becomes true), proving the skip is what prevents double-counting.
   Restore the line.

## Pass criteria

- D0 is INVALID despite submitting both dealer parts and a self-approving verification vector.
- Removing the `verifierIndex == dealerIndex` line causes this test to fail.

## Fail indicators

- D0 is VALID — double-counting confirmed, skip is broken or missing.
- Test compiles but D0 is INVALID for the wrong reason (e.g., parts not found) — verify `DealerAddress` and `Commitments` are set.

## Source reference

- `inference-chain/x/bls/keeper/phase_transitions.go` — `if verifierIndex == dealerIndex { continue }` inside `determineValidDealersWithConsensus`

---

## Result — CANNOT VERIFY (zero test coverage confirmed)

### Coverage search

```bash
cd inference-chain
grep -rn "verifierIndex == dealerIndex\|DoubleCount\|SelfVerification\|double.count" x/bls/keeper/
```

Output:
```
x/bls/keeper/phase_transitions.go:404:    if verifierIndex == dealerIndex {
```

Only the implementation line. No test file references the guard at all.

The closest existing test is `TestDetermineValidDealersWithConsensus_DealerOwnsExactlyHalfSlots` where dealer 0 also submits its own verification vector with `DealerValidity[0]=true`. Removing the skip in that test still gives dealer 0: implicit(50) + explicit-own(50) + peer(50) = 150 ≥ 51 → VALID — identical result. The guard is invisible because both outcomes exceed quorum. The specific distribution described in Setup (50/50, peer votes false) is the only way to make the guard load-bearing, and it is not tested anywhere.

### What needs to happen

A dev writes `TestDetermineValidDealersWithConsensus_SelfVerificationNotDoubleCountedAsNo` with the 50/50 setup described above. Without it, a one-line regression removing the `continue` would pass the entire test suite.
