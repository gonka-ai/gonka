BLS Dealer Self-Vote — PR #1112 (GEB-29)
Branch: upgrade-v0.2.12  |  Chain: gonka-testnet-3  |  Date: 2026-04-30


TESTS

1.  Happy-path DKG — 3 participants, 1/1/1 slots [PASS]
    Watch epochs 1–17 on gonka-testnet-3 across genesis and join-1/join-2 nodes.
    Check: all epochs DEALING → VERIFYING → DISPUTING → COMPLETED → SIGNED; no FAILED
    epochs in full history; validDealersCount=3; groupPublicKeySize=96 every epoch.

2.  GEB-29 regression — 2 participants, 4 slots, 50/50 stake, epoch 18 [PASS]
    Query epoch state on join-1/join-2 at block ~5021 (genesis API offline); 2-node
    4-slot setup, each participant holds 2 slots, quorum=3.
    Check: candidateDealerSlots=4 requiredSlots=2 validDealersCount=2; both dealers VALID;
    DKG COMPLETED. Pre-fix simulation (commit a1119b8, self excluded): peer-only(2) < 3
    → both INVALID → epoch FAILED. Post-fix: self(2)+peer(2)=4 ≥ 3 → VALID. This is the
    exact Certik scenario.

3.  Self-vote quorum tally — epoch 2 manual verification [PASS]
    Manual tally for 3-dealer epoch: dealer_validity=[true,true,true] from all 3
    verifiers, 1 slot each, quorum=2.
    Check: Dealer 0: self(1)+V1(1)+V2(1)[V0 skipped]=3 ≥ 2 → VALID. Same result for
    dealers 1 and 2. candidateDealerSlots=3 > totalSlots/2(=1) → DISPUTING. Matches
    live log exactly.

4.  Removed warning log absent [PASS]
    Search full log history on genesis and join-2 for "cannot reach weighted quorum",
    "self-vote excluded", "Dealer cannot reach", "maxNonSelfVotingSlots".
    Check: zero results across all nodes. Removed log never fires. No regression.


FINDINGS


Empty commitments check — zero test coverage [HIGH, Coverage]
dealerSubmittedParts now requires len(Commitments) > 0, a new guard added by this PR.
No unit test in any of the 19 test files constructs DealerAddress != "" with
Commitments=[][]byte{}. A regression removing this guard would pass the entire test
suite undetected.

Double-count guard — zero test coverage [MEDIUM, Coverage]
The verifierIndex == dealerIndex skip inside the verifier loop is load-bearing for the
self-vote fix but has no dedicated test. With equal 1-slot distribution, removing the
guard changes the tally from 3 to 4, but both values exceed quorum=2 so the epoch
outcome is identical. Coverage requires a custom slot distribution where the guard
flips a dealer from VALID to INVALID. Zero coverage confirmed across all 19 test files.

Peer-exclusion and N/2 boundary not testable on this testnet [LOW, Operational]
TC-BSV-005 (minority dealer excluded by peers) and TC-BSV-006 (candidateDealerSlots
== totalSlots/2 → FAILED boundary) could not be exercised: all validators vote
unanimously for all dealers every epoch, and no partially-failed epoch occurred. Both
require controlled key access or a custom slot distribution. Unit tests are the only
viable coverage path.
