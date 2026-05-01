# TC-BSV-005: Peers with strict majority can still exclude any dealer, including a large-stake one

## Description

The other side of TC-BSV-004: if a dealer does NOT hold a majority of slots, peers can still vote it out. A dealer with 40/100 slots gets self(40) + zero peer votes = 40 < 51 → INVALID. This confirms the fix does not hand unconditional immunity to any dealer — only majority-stake dealers are self-sufficient; everyone else still needs peer support.

This is the negative confirmation for TC-BSV-004. Together they define the exact boundary: self-vote alone is sufficient at and above 51 slots, insufficient below.

## Preconditions

- [ ] `upgrade-v0.2.12` checked out.
- [ ] Build passes.

## Setup

3 participants: p0=40 slots, p1=30 slots, p2=30 slots (totalSlots=100, quorum=51).

All 3 submitted parts. p1 and p2 both vote **false** for dealer 0.

Dealer 0 tally: self(40) + no peer approval = 40 < 51 → INVALID.

## Steps

1. Construct epoch manually with 40/30/30 distribution. All verifiers vote false for dealer 0.

2. Run `DetermineValidDealersWithConsensus`. Confirm dealer 0 is INVALID.

3. To verify the exact boundary: change p0 to 51 slots (p1=25, p2=24) and confirm dealer 0 flips to VALID without any peer votes.

4. Run:
   ```bash
   cd inference-chain
   go test ./x/bls/keeper/... -v -run "^TestDetermineValidDealersWithConsensus_MinorityDealerExcludedByPeers$" -count=1
   ```
   **Expected:** PASS.

## Pass criteria

- Dealer with 40/100 slots is INVALID when all peers reject it.
- Same dealer with 51/100 slots is VALID when all peers reject it (boundary flip).
- Negative confirmation: peer rejection is only overridden by self-vote when the dealer holds strict majority.

## Fail indicators

- Dealer with 40 slots is VALID — self-vote logic applied incorrectly (e.g., quorum threshold changed).
- Boundary flip doesn't occur at exactly 51 — off-by-one in `quorumSlots = totalSlots/2 + 1`.

## Source reference

- `inference-chain/x/bls/keeper/phase_transitions.go` — `quorumSlots := effectiveTotalSlots/2 + 1` and `validVotingSlots >= quorumSlots`

---

## Result — CANNOT VERIFY on this testnet

All three validators on gonka-testnet-3 vote unanimously `true` for every dealer in every epoch. There is no mechanism to withhold a verification submission short of SSH-ing into the node and stopping the DAPI process during VERIFYING phase — which would also stop that node from submitting its own dealer part in the next DEALING phase, breaking the test setup.

### What was attempted

```bash
# Tried to find a way to suppress verification from one validator without stopping the node
ssh ubuntu@<genesis-ip> "docker logs node 2>&1 | grep 'dealer_validity\|DealerValidity' | tail -20"
```

All observed submissions show `dealer_validity_count=3` with all entries `true`. No partial-submission path is exposed through the DAPI API.

### What needs to happen

This TC requires either:
- Direct control of a node's private key to sign a custom verification tx with selected `false` entries
- A `devshardctl` flag to suppress verification from a specific participant
- A unit test with the 40/30/30 setup described above (currently no coverage in any test file)

The boundary flip at 51 slots described in Step 3 is also not covered by any existing test.
