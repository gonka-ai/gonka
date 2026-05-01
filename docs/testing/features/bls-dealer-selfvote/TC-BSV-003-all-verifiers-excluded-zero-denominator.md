# TC-BSV-003: All verifiers excluded for a dealer produces effectiveTotalSlots=0 → INVALID

## Description

When `excludedVerifiersByDealer` contains exclusions for all participants for a given dealer, `effectiveTotalSlots` drops to zero. The guard `effectiveTotalSlots > 0` must fire and force that dealer to INVALID, regardless of vote count or self-weight.

This is the boundary of the dispute exclusion path. It can be reached in production if adjudication determines every verifier in the epoch was a false complainer against a particular dealer — an adversarial edge case but a reachable one. No existing test pushes `effectiveTotalSlots` to zero.

## Preconditions

- [ ] `upgrade-v0.2.12` checked out.
- [ ] Build passes.

## Setup

2 participants (p0: 50 slots, p1: 50 slots). Both submitted dealer parts.

Call `determineValidDealersWithConsensus` with `excludedVerifiersByDealer` that excludes **all non-self verifiers** for dealer 0:
- Dealer 0 exclusion map: `{1: {}}` (p1 excluded)
- `effectiveTotalSlots` for dealer 0 = 100 − 50 = 50
- `quorumSlots` = 50/2 + 1 = 26
- Dealer 0 self-vote: 50 ≥ 26 → VALID in this case

Now exclude both verifiers (including self — note: self is already skipped in the loop, but its slot weight is still subtracted from `effectiveTotalSlots` when listed in `excludedVerifiers`):
- Dealer 0 exclusion map: `{0: {}, 1: {}}` (both excluded)
- `effectiveTotalSlots` = 100 − 50 − 50 = 0
- Guard fires: `effectiveTotalSlots > 0` is false → `dealerIsValid = false`

## Steps

1. Write a test in `dispute_resolution_test.go` using the bare `Keeper{}` struct (as `TestDetermineValidDealersWithConsensus_ExcludedVerifiersAreDealerScoped` does):

   Scenario A: exclude only the peer (effectiveTotalSlots=50, quorum=26) → dealer 0 should be VALID on self-vote alone.
   
   Scenario B: exclude all participants (effectiveTotalSlots=0) → dealer 0 must be INVALID despite positive self-weight.

2. Run:
   ```bash
   cd inference-chain
   go test ./x/bls/keeper/... -v -run "^TestDetermineValidDealersWithConsensus_AllVerifiersExcluded$" -count=1
   ```
   **Expected:** PASS for both scenarios.

3. Confirm: scenario A gives VALID, scenario B gives INVALID — confirming the zero-denominator guard is the difference, not vote tallying.

## Pass criteria

- Scenario A: dealer 0 VALID.
- Scenario B: dealer 0 INVALID.
- No panic on `quorumSlots = 0/2 + 1 = 1` arithmetic or zero-slot division.

## Fail indicators

- Scenario B dealer 0 VALID — guard absent or `effectiveTotalSlots` not reaching zero.
- Integer underflow panic — `excludedSlots >= effectiveTotalSlots` guard not protecting the subtraction.

## Source reference

- `inference-chain/x/bls/keeper/phase_transitions.go` — `effectiveTotalSlots > 0 &&` in `dealerIsValid` condition; `excludedSlots >= effectiveTotalSlots` underflow guard

---

## Result — CANNOT VERIFY on testnet / substitute unit test PASS

The `excludedVerifiersByDealer` path is only activated during dispute adjudication. No disputes occurred on gonka-testnet-3 during the test window, so the zero-denominator boundary was never reached on-chain.

### Substitute: closest existing unit test

```bash
cd inference-chain
go test ./x/bls/keeper/... -v -run "^TestDetermineValidDealersWithConsensus_ExcludedVerifiersAreDealerScoped$" -count=1
```

Output:
```
=== RUN   TestDetermineValidDealersWithConsensus_ExcludedVerifiersAreDealerScoped
--- PASS: TestDetermineValidDealersWithConsensus_ExcludedVerifiersAreDealerScoped (0.00s)
PASS
ok      github.com/productscience/inference/x/bls/keeper    1.198s
```

This test confirms the exclusion map is dealer-scoped, not global. It does not push `effectiveTotalSlots` to zero — Scenario B (both verifiers excluded) described in Setup above remains unwritten. The zero-denominator guard has no dedicated coverage.

### What needs to happen

A dev adds Scenario B to the excluded-verifiers test or writes a standalone `TestDetermineValidDealersWithConsensus_AllVerifiersExcluded` confirming that `effectiveTotalSlots=0` forces INVALID regardless of self-weight.