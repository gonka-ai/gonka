# Block beacon randomness: design for consumers

**Status:** design. The beacon that produces the randomness is being built (Steps 1–5 done, see the plan).
**Beacon design:** [`block-bls-beacon-vote-extensions-plan.md`](block-bls-beacon-vote-extensions-plan.md) (the "plan"). How σ is produced lives there; this document covers what the randomness guarantees, what a proposer can and cannot do, and how modules use it. Confirmation PoC (CPoC) is the first consumer (plan Step 6).

---

## 1. Problem: block hashes are grindable

Several chain decisions need a random value that nobody can choose. Today they read the current block hash, which the proposer controls:

| Use | Code | Value used | Who controls it |
| --- | --- | --- | --- |
| CPoC trigger draw at height `H` | `x/inference/module/confirmation_poc.go:164` | `sdkCtx.HeaderInfo().Hash` | During `FinalizeBlock(H)` that is the hash of block **`H` itself** (not `H-1`, despite the comment). The proposer of `H` picks the transactions and can try many tx sets until the draw comes out as it wants. |
| CPoC work seed at generation start `G` | the GRACE_PERIOD → GENERATION transition (`PocSeedBlockHash`) | `sdkCtx.HeaderInfo().Hash` at `G` | The same: the proposer of block `G` grinds its own block hash. |

Any future use of `HeaderInfo().Hash` as randomness has the same flaw. A proposer that can evaluate thousands of candidate blocks can turn a rare event into one that happens almost whenever it proposes, or suppress it.

## 2. What the block beacon provides

Summary of the plan (Goal, Steps 3–5):

- For every height `h`, x/bls fixes a message in `EndBlock(h-1)`:
  `m_h = sha256("GONKA_BLOCK_BEACON_V1" ‖ chain_id ‖ u64be(key_epoch) ‖ u64be(h) ‖ blockHash(h-1))`.
  It binds the **previous** block's hash, which nobody can change once `h-1` is committed.
- Each validator signs `m_h` with its BLS slot shares inside its precommit for `h` (a vote extension; no extra messages).
- Block `h+1` must carry either **σ_h**, the group signature over `m_h`, or a **proof of miss** (§3). A block with neither is rejected and the next proposer takes over.
- `FinalizeBlock(h+1)` stores `BeaconRecord{round: h, key_epoch, status, sigma}` with status `OK`, `MISS_THRESHOLD`, `MISS_NO_KEY` or `MISS_NO_INJECTION` (activation boundary only).
- The key switches to a newly completed DKG a few blocks after it completes (plan rule R9), so there is no gap at epoch boundaries.

Properties consumers rely on:

1. **Unique.** For a given `m_h` there is exactly one valid σ_h: any `t+1` valid partials aggregate to the same 48 bytes. No choice of partials, proposer or block content changes it.
2. **Unpredictable.** Computing σ_h needs `t+1` slot shares (51 of 100 with the default parameters).
3. **Resolved in the next block.** At `EndBlock(H)`, round `H-1` always has a record.
4. **Explicit misses.** A miss is recorded as such; there is no fallback value.

## 3. How σ gets into a block, and what a proposer can do

### Partials are part of signed votes

Each precommit carries two signatures, both from the validator's consensus key (tmkms or the local key):

```text
precommit(h) from validator V
├── vote: block ID, height, round        ← vote signature
└── extension: BeaconVoteExtension       ← extension signature
       {round h, key_epoch, slots [a..b], one BLS partial per slot}
```

- The **extension signature** binds those exact partials to V and to round `h`.
- Each **partial** is a BLS signature over `m_h` made with one of V's slot shares; it is checked against that slot's public key (`sk_slot·G2`, stored on chain since the DKG).

A proposer therefore cannot pick and choose partials inside a vote: they come as a signed unit with the vote.

### What block `h+1` must carry

- **σ_h** (the normal case): the proposer aggregates `t+1` valid partials (Lagrange) into σ_h. Every validator checks it with one pairing against the key epoch's group public key. A wrong σ is rejected.
- **Proof of miss** (only when σ can't be built): the extended commit of `h`, i.e. every vote of the block's `LastCommit` with its extension and extension signature. Validators check it vote by vote and count valid slots (see the table below). It is accepted only if fewer than `t+1` slots are valid.
- **Neither:** the block is rejected; CometBFT moves to the next round with another proposer.

### Ways a proposer could try to skip σ, and why they fail

| Attempt | Result |
| --- | --- |
| Leave the beacon tx out | Rejected (`missing`). The next round's proposer commits the height with σ. In testermint (`withholding proposer is replaced`) 14 of 40 heights were committed this way after the withholding validator's proposals were rejected. |
| Include a wrong σ | Rejected (`bad_sigma`): one pairing fails. |
| Include a proof of miss whose commit has ≥ `t+1` valid slots | Rejected (`miss_disproved`): every validator recounts the slots. |
| Empty or alter a partial in the proof | Rejected (`ext_signatures`): the extension signature was made over the real partials. |
| Swap in another validator's extension | Rejected (`ext_signatures`): wrong signer. |
| Hide a vote only in the proof (drop it, change its flag or power) | Rejected (`commit_mismatch`): the proof must match the block's own `LastCommit` vote by vote. |

### The one remaining lever: choosing a smaller commit

CometBFT requires a block's `LastCommit` to hold more than 2/3 of voting power, not every precommit the proposer received. A modified (byzantine) proposer can include only a minimal >2/3 subset, leaving other validators out as absent, and build an honest proof of miss from that subset. Validators can't know about precommits outside the block, so the block is accepted and the round is recorded as `MISS_THRESHOLD`.

This works only if that subset genuinely holds fewer than `t+1` valid slots:

- BLS slots are assigned from the same capped weight (`CapWeight`) as voting power, so any >2/3-power subset holds about 67 of 100 slots, above the threshold of 51.
- With every validator's shares working, no such subset exists and the proposer must include σ.
- The lever opens only when validators inside the chosen subset already send no valid partials (about 16 or more of its slots): dAPI or share-export outage, participants without a BLS key, new slot holders not yet validators.

Even then the proposer can only turn an `OK` round into a miss after seeing σ (a veto), never choose σ's value. The Step 3 coverage check (`EventBeaconSlotCoverageWarning`) warns when such a subset could exist; the miss rate in the records shows it in operation.

## 4. Rules for consumers

Any module using the beacon follows these rules:

1. **Read round `H-1` at `EndBlock(H)`** (or later rounds). It is always resolved by then. Never use a round that isn't final, and never use the current block's hash.
2. **Domain-separate every use:** `value = sha256("<CONSUMER_TAG>_V1" ‖ σ)`, with a distinct tag per decision (CPoC draw, CPoC seed, ...). The same σ then yields independent values.
3. **A miss means "no randomness this round".** Skip the decision or defer it to a later round. Never fall back to a block hash: that hands the grinding lever back to whoever can cause a miss.
4. **Expect early knowledge by validators.** σ_{H-1} is computable during the precommit of round `H-1`, about one block before `EndBlock(H)`. Decisions that must stay secret longer need a later round.
5. **Expect a miss rate.** An effective probability `p` becomes `p × P(OK)`; monitor misses (`EventBlockBeacon`) and decide per consumer whether to rescale.

## 5. Consumer: CPoC trigger draw

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

- The event field `PocSeedBlockHash` stays for compatibility (empty in beacon mode). New fields: `PocSeedBeaconRound` and `PocSeedSigmaHash` (`sha256(σ)` of the round used).
- **Rate:** with all validators healthy `P(OK) ≈ 1`, because slots follow voting power. v1 documents the effect and monitors the miss rate; rescaling is open (§10).

## 6. Who can do what (CPoC view)

| Actor | Can | Can't |
| --- | --- | --- |
| Proposer of `H` | Nothing about the draw at `H`: σ_{H-1} was fixed before it proposes | Grind (σ is unique), omit σ_{H-1} (its block is rejected), substitute a wrong σ |
| Proposer of `H` (commit lever, §3) | Force a miss of round `H-1` when its chosen >2/3 commit genuinely lacks `t+1` valid slots | Do so while the slots in its commit contribute |
| Coalition with ≥ `t+1` slots (51) | Compute σ early and know the draw before others | Change it |
| Validators holding > `n − t − 1` slots (≥ 50) withholding partials | Make every round a miss, so no CPoC events trigger (an availability failure, visible in records and events) | Choose when an event happens |

Security assumptions in beacon mode:

- **Unpredictability:** fewer than `t+1` (51) slots are held by colluding parties.
- **Availability:** at least `t+1` slots belong to validators whose precommits carry valid partials.

## 7. Timing and notice

- σ_{H-1} becomes computable during the precommit phase of round `H-1` (every validator's node collects the partials) and is acted on at `EndBlock(H)`.
- Validators can therefore know the draw for `H` about one block interval before it settles. Today the proposer of `H` knows its own block hash, and so the draw, while building the block. Beacon mode moves early knowledge from one proposer to the whole validator set, by about one block, and removes the ability to choose.
- CPoC event timing after a trigger (grace period, generation, validation) is unchanged.

## 8. Consumer: CPoC work seed (open decision)

The work seed captured at the GRACE_PERIOD → GENERATION transition (`G`) still reads `HeaderInfo().Hash`, so the proposer of `G` can grind it. Options using the beacon:

1. **σ_{G-1}:** at the transition in `EndBlock(G)`, use the record of round `G-1`, hashed with `GONKA_CPOC_SEED_V1`. On a miss, either:
   - cancel the event (simplest; the next draw can trigger a new one), or
   - wait for the first `OK` round after `G-1`, which shifts the generation start by the number of missed rounds.
2. **Derive the seed from the trigger round:** `sha256("GONKA_CPOC_SEED_V1" ‖ σ_{S-1})`, where `S` is the trigger height. There is no extra round and no miss handling, but the seed is known from the moment the event triggers, so participants get the full grace period to prepare.

Option 1 with cancellation keeps the seed secret until generation starts and is the recommended direction. The trigger change (§5) doesn't depend on it.

## 9. Key epochs and switches

- The record carries the `key_epoch` that signed the round. Consumers ignore it: σ is uniformly random whichever key signed it.
- Key switches (rule R9) happen at round boundaries without gaps, so a consumer spanning a switch is unaffected.
- A DKG that fails keeps the previous key, so rounds stay `OK` while that key's holders keep signing.

## 10. Rollout, tests and open questions

**Rollout** (plan, Rollout sketch):

1. Steps 1–5 live with every consumer still on its current input. Records are queryable (`inferenced query bls block-beacon <round>`).
2. Observe the OK rate for at least one epoch.
3. Switch consumers one by one; for CPoC, governance sets `confirmation_poc_use_block_beacon = true`. Turning it off reverts to the hash path.

**Tests:**

- Beacon side (plan Steps 3–4, done): every proposer trick in §3 is rejected (`TestProcessProposalRejects`, `TestBlockBeacon_SigmaInEveryBlock`, `TestBlockBeacon_ProofOfMiss`); testermint `sigma in every block` and `withholding proposer is replaced`.
- CPoC (plan Step 6): flag off leaves `confirmation_poc_test.go` unchanged; flag on gives a deterministic decision for an `OK` record and no trigger, no error on a miss; changing block `H`'s transactions doesn't change the decision; over 10k synthetic σ values the trigger frequency stays within tolerance of `p`; testermint `CPoC flag on` triggers only on `OK` rounds.

**Open questions:**

1. CPoC work seed source and miss handling (§8).
2. Rescale trigger probabilities by the observed OK ratio, or only monitor it (§4, §5).
3. Whether consumers should require a minimum key size (for example 100 slots / threshold 51) before trusting the beacon.
4. Other consumers (beyond CPoC) that read `HeaderInfo().Hash` as randomness: inventory them and move them to the beacon with their own domain tags.
