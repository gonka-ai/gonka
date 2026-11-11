# Confirmation PoC on the block beacon

**Status:** design; the beacon it consumes is being built (Steps 0–3 done, see the plan). This document covers plan **Step 6**.
**Beacon design:** [`block-bls-beacon-vote-extensions-plan.md`](block-bls-beacon-vote-extensions-plan.md) (the "plan"). Everything about producing σ lives there; this document covers only how Confirmation PoC (CPoC) consumes it.
**Code today:** `inference-chain/x/inference/module/confirmation_poc.go`

---

## 1. Problem

CPoC decides at every block whether to start a confirmation event, and later fixes a work seed for the event. Both inputs are grindable today:

| Input | Code | Value used | Who controls it |
|---|---|---|---|
| Trigger draw at height `H` | `confirmation_poc.go:164` | `sdkCtx.HeaderInfo().Hash` | During `FinalizeBlock(H)` that is the hash of block **`H` itself** (not `H-1`, despite the comment). The proposer of `H` picks the transactions and can try many tx sets until the draw triggers, or doesn't. |
| Work seed at generation start `G` | the GRACE_PERIOD → GENERATION transition (`PocSeedBlockHash`) | `sdkCtx.HeaderInfo().Hash` at `G` | The same: the proposer of block `G` can grind its own block hash. |

The trigger probability per height is `expected_confirmations_per_epoch / trigger_window_length` (a few events per epoch). A proposer that can evaluate thousands of candidate blocks can turn that into an event almost whenever it proposes, or suppress one.

## 2. What the block beacon provides

Summary of the plan (Goal, Steps 3–5):

- For every height `h`, x/bls fixes a message in `EndBlock(h-1)`:
  `m_h = sha256("GONKA_BLOCK_BEACON_V1" ‖ chain_id ‖ u64be(key_epoch) ‖ u64be(h) ‖ blockHash(h-1))`.
  It binds the **previous** block's hash, which nobody can change once `h-1` is committed.
- Each validator signs `m_h` with its BLS slot shares inside its precommit for `h` (a vote extension; no extra messages).
- The proposer of block `h+1` must include either **σ_h** (any `t+1` valid partials aggregate to the same 48 bytes, checked with one pairing) or a **proof of miss** (the extended commit of `h`, showing fewer than `t+1` valid slots). A block with neither is rejected, and the next proposer takes over.
- `FinalizeBlock(h+1)` stores `BeaconRecord{round: h, key_epoch, status, sigma}` with status `OK`, `MISS_THRESHOLD`, `MISS_NO_KEY` or `MISS_NO_INJECTION` (activation boundary only).
- The key epoch switches to a newly completed DKG a few blocks after it completes (rule R9), so there is no gap at epoch boundaries.

Properties CPoC relies on:

1. **Unique.** For a given `m_h` there is exactly one valid σ_h. No choice of partials, proposer or block content changes it.
2. **Unpredictable.** Computing σ_h needs `t+1` slot shares (51 of 100 with the default parameters).
3. **Always resolved in the next block.** At `EndBlock(H)`, round `H-1` has a record.
4. **Explicit misses.** A miss is recorded as such; there is no fallback value.

## 3. Trigger draw

New x/inference parameter `confirmation_poc_use_block_beacon` (bool, default `false`, set by governance).

At `EndBlock(H)`, inside the existing trigger window (all current gates unchanged):

```go
if params.ConfirmationPocUseBlockBeacon {
    rec, found := k.BlsKeeper.GetBlockBeacon(ctx, uint64(H-1))
    if !found || rec.Status != blstypes.OK {
        log("cpoc: beacon miss", H-1, rec.Status)
        return nil // no draw at H; never fall back to the block hash
    }
    seedHash := sha256.Sum256(append([]byte("GONKA_CPOC_DRAW_V1"), rec.Sigma...))
    seed := int64(binary.BigEndian.Uint64(seedHash[:8]))
    randFloat = calculations.DeterministicFloat(seed, fmt.Sprintf("confirmation_poc_trigger_%d", H))
} else {
    // existing hash path (fix the misleading "H-1" comment: it is the current block's hash)
}
shouldTrigger := randFloat.LessThan(triggerProbability)
```

- The draw at `H` uses round `H-1`. It is the newest round that is final in block `H`, and its record always exists at `EndBlock(H)`.
- `GONKA_CPOC_DRAW_V1` separates this use of σ from any other consumer.
- **No fallback on a miss.** Falling back to the block hash would hand the grinding lever back to whoever can cause a miss.
- The existing event field `PocSeedBlockHash` stays for compatibility (empty in beacon mode). New fields: `PocSeedBeaconRound` and `PocSeedSigmaHash` (`sha256(σ)` of the round used).

## 4. Trigger rate

A miss skips the draw for that height, so the effective per-height probability is `p × P(OK)`, where `p` is the configured trigger probability.

- With all validators healthy, `P(OK) ≈ 1`: slots are assigned from the same capped weight as voting power, so any commit normally carries well over `t+1` valid slots.
- v1 documents the effect and monitors the miss rate (`EventBlockBeacon` per round, beacon metrics). Rescaling `p` by the observed OK ratio is an open question (§9).

## 5. What participants can and can't do

| Actor | Can | Can't |
|---|---|---|
| Proposer of `H` | Nothing about the draw at `H` itself: σ_{H-1} was fixed before it proposes | Grind (σ is unique), omit σ_{H-1} (its block would be rejected), substitute a wrong σ (one pairing fails) |
| Proposer of `H` (miss lever) | Force a miss of round `H-1` by building its `LastCommit` from a >2/3-power set of precommits that really has fewer than `t+1` valid slots | Do that while the slots in its chosen commit contribute: since slots follow voting power, it needs about 16% of the slots in that commit to send no valid partials |
| Coalition with ≥ `t+1` slots (51) | Compute σ_h early and know the draw before others | Change it |
| Validators holding > `n − t − 1` slots (≥ 50) withholding partials | Make every round a miss, so no CPoC events trigger (an availability failure, visible in records and events) | Choose when an event happens |

Security assumptions for CPoC in beacon mode:

- **Unpredictability:** fewer than `t+1` (51) slots are held by colluding parties.
- **Availability:** at least `t+1` slots belong to validators whose precommits carry valid partials.

## 6. Timing and notice

- σ_{H-1} becomes computable during the precommit phase of round `H-1`, by anyone holding `t+1` valid partials, which in practice is every validator's node. It is acted on at `EndBlock(H)`.
- So validators can know the draw for `H` roughly one block interval before it settles. Today the proposer of `H` knows its own block hash, and so the draw, while building the block. Beacon mode moves early knowledge from one proposer to the whole validator set, by about one block, and removes the ability to choose.
- Event timing after a trigger (grace period, generation, validation) is unchanged.

## 7. Work seed (open decision)

The work seed captured at the GRACE_PERIOD → GENERATION transition (`G`) still reads `HeaderInfo().Hash`, so the proposer of `G` can grind it. The plan defers this (open question 2). Options that use the same beacon:

1. **σ_{G-1}:** at the transition in `EndBlock(G)`, use the record of round `G-1`, hashed with `GONKA_CPOC_SEED_V1`. On a miss, either:
   - cancel the event (simplest; the next draw can trigger a new one), or
   - wait for the first `OK` round after `G-1`, which shifts the generation start by the number of missed rounds.
2. **Derive the seed from the trigger round:** `sha256("GONKA_CPOC_SEED_V1" ‖ σ_{S-1})`, where `S` is the trigger height. There is no extra round and no miss handling, but the seed is known from the moment the event triggers, so participants get the full grace period to prepare.

Option 1 with cancellation keeps the seed secret until generation starts and is the recommended direction. It needs a decision before Step 6 ships the seed part; the trigger change (§3) doesn't depend on it.

## 8. Key epochs and switches

- The record carries the `key_epoch` that signed the round. The draw ignores it: σ is uniformly random whichever key signed it.
- Key switches (rule R9) happen at round boundaries without gaps. An event spanning a switch is unaffected.
- A DKG that fails keeps the previous key, so rounds stay `OK` while that key's holders keep signing.

## 9. Rollout, tests and open questions

**Rollout** (plan, Rollout sketch):

1. Steps 1–5 live on testnet and mainnet with CPoC still on the hash path. Records are queryable (`inferenced query bls block-beacon <round>`).
2. Observe the OK rate for at least one epoch.
3. Governance sets `confirmation_poc_use_block_beacon = true`. Turning it off later reverts to the hash path; nothing else depends on the flag.

**Tests** (plan Steps 6 and 8):

- Flag off: the existing `confirmation_poc_test.go` passes unchanged.
- Flag on: a mocked `OK` record gives a deterministic trigger decision; a miss or missing record gives no trigger and no error.
- Grind regression: changing block `H`'s transactions (and therefore `HeaderInfo().Hash`) doesn't change the decision with the flag on.
- Statistical: over 10k synthetic σ values the trigger frequency is within tolerance of `p`.
- Testermint `CPoC flag on`: triggers happen only on `OK` rounds, events carry `PocSeedSigmaHash`, and `ConfirmationPoCPassTests` pass with the flag on.

**Open questions:**

1. Work seed source and miss handling (§7).
2. Rescale the trigger probability by the observed OK ratio, or only monitor it (§4).
3. Whether CPoC should require a minimum key size (for example 100 slots / threshold 51) before it trusts the beacon, or accept any key the beacon uses.
