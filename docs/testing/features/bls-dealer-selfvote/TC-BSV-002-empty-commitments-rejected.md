# TC-BSV-002: Dealer with valid address but empty commitments is rejected despite passing vote quorum

## Description

The PR added `len(epochBLSData.DealerParts[dealerIndex].Commitments) > 0` to the `dealerSubmittedParts` check. Before this change, setting `DealerAddress != ""` was sufficient to count as having submitted parts — a dealer could satisfy the address check while having submitted zero commitments, which would later cause `ComputeGroupPublicKey` to silently skip that dealer or panic on an empty commitment slice.

No existing unit test sets `DealerAddress != ""` with `Commitments = [][]byte{}`. The `createTestEpochBLSData` helper initializes both as empty by default; tests that mark a dealer as "submitted" always set both fields together. This specific combination — address set, commitments empty — goes untested.

## Preconditions

- [ ] `upgrade-v0.2.12` checked out.
- [ ] Build passes.

## Setup

3 participants, 33/33/34 slots (quorum=51). All 3 verifiers approve dealer 0 unanimously.

Dealer 0:
- `DealerAddress = "participant1"` ← set
- `Commitments = [][]byte{}` ← **empty, deliberately not set**

Dealer 0 passes vote quorum comfortably: self(33) + V1(33) + V2(34) = 100 ≥ 51. The only thing keeping it out should be the new `len(Commitments) > 0` check.

## Steps

1. Write a test case in `phase_transitions_test.go`:

   ```go
   func TestDetermineValidDealersWithConsensus_EmptyCommitmentsRejected(t *testing.T) {
       k, _ := keepertest.BlsKeeper(t)
       epochBLSData := createTestEpochBLSData(uint64(99), 3)
       // Address set, commitments empty
       epochBLSData.DealerParts[0].DealerAddress = "participant1"
       epochBLSData.DealerParts[0].Commitments = [][]byte{}
       // All verifiers approve dealer 0
       for i := 0; i < 3; i++ {
           epochBLSData.VerificationSubmissions[i].DealerValidity = []bool{true, false, false}
       }
       validDealers, err := k.DetermineValidDealersWithConsensus(&epochBLSData)
       require.NoError(t, err)
       require.False(t, validDealers[0], "dealer with empty commitments must be rejected")
   }
   ```

2. Run:
   ```bash
   cd inference-chain
   go test ./x/bls/keeper/... -v -run "^TestDetermineValidDealersWithConsensus_EmptyCommitmentsRejected$" -count=1
   ```
   **Expected:** `PASS`.

3. Confirm negative confirmation: `validDealers[0]` is explicitly `false`, not absent from the slice.

## Pass criteria

- Dealer 0 is INVALID (`false`) even with unanimous approval and a valid address.
- No panic or error from the empty commitments slice.

## Fail indicators

- `validDealers[0] = true` — the new `len(Commitments) > 0` check is absent or not reached.
- Panic on `Commitments[0]` — empty slice access somewhere downstream (would indicate the check is missing in `DetermineValidDealers` and the caller doesn't guard either).

## Source reference

- `inference-chain/x/bls/keeper/phase_transitions.go` — `len(epochBLSData.DealerParts[dealerIndex].Commitments) > 0` in `dealerSubmittedParts`

---

## Result — CANNOT VERIFY (zero test coverage confirmed)

### Coverage search

```bash
cd inference-chain
grep -rn "Commitments.*\[\]\[\]byte{}\|EmptyCommitment\|empty.*commit\|len(Commitments)" x/bls/keeper/
```

Output:
```
x/bls/keeper/dkg_initiation.go:82:            Commitments: [][]byte{},
x/bls/keeper/phase_transitions_test.go:264:   Commitments: [][]byte{},
x/bls/keeper/dispute_resolution_test.go:208:  Commitments: [][]byte{},
x/bls/keeper/msg_server_dealer_test.go:63:    {DealerAddress: "", Commitments: [][]byte{}, ...}
x/bls/keeper/msg_server_dealer_test.go:184:   {DealerAddress: "", Commitments: [][]byte{}, ...}
x/bls/keeper/msg_server_dealer_test.go:222:   {DealerAddress: dealerAddr, Commitments: [][]byte{}, ...} // Already submitted
...
```

Every `Commitments: [][]byte{}` is either:
- Paired with `DealerAddress: ""` — the "not yet submitted" slot, a normal fixture setup
- `msg_server_dealer_test.go:222` — a pre-populated slot for the duplicate-submission test; it has a real `dealerAddr` but the test exercises the "already submitted" rejection path, not the empty-commitments guard

No test constructs `DealerAddress != ""` with `Commitments=[][]byte{}` and then calls `DetermineValidDealersWithConsensus`. The `len(Commitments) > 0` guard in `dealerSubmittedParts` is entirely uncovered.

### On-chain path

No `inferenced tx` or CLI flag exists to broadcast a `MsgSubmitDealerPart` with a real dealer address and an empty commitments field. The standard flow populates both fields in one shot. Testnet verification not possible without a custom binary or direct mempool injection.

### What needs to happen

A dev writes `TestDetermineValidDealersWithConsensus_EmptyCommitmentsRejected` as outlined in Steps above. Without it, removing the `len(Commitments) > 0` line silently breaks the guard with no test signal.
