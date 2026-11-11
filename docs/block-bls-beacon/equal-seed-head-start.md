# Equal head start for beacon work seeds

**Status:** proposal. Follow-up to plan Step 6b; Step 6b is implemented as written first.
**Depends on:** [plan](../../proposals/block-bls-beacon/block-bls-beacon-vote-extensions-plan.md) Steps 3–5 (σ per round), Step 6b (work seeds from σ, stored on chain); [randomness design](../../proposals/block-bls-beacon/block-beacon-randomness.md).
**Changes:** inferenced (a local, non-consensus read of the collector's σ) and the dAPI (speculative generation start). No consensus or state change.

---

## 1. Problem: σ is known before it is on chain

Step 6b derives each work seed from σ of a fixed beacon round `r` (for regular PoC `r = P-1`) and stores it on chain at `FinalizeBlock(r+1)`. σ_r exists in the network earlier than that:

```text
block r-1 commits     m_r is fixed (PendingBeacon(r), EndBlock(r-1))
                      a coalition holding t+1 slots can compute σ_r from here    (≈ 2 blocks early)
precommit of r        each validator attaches its partials to its precommit
                      any node holding t+1 slots' partials aggregates σ_r        (≈ 1 block early)
proposal of r+1       σ_r is in Txs[0] of the proposed block, gossiped to peers  (< 1 block early)
FinalizeBlock(r+1)    BeaconRecord(r) and the work seed are stored               (on chain)
```

Every validator node already aggregates σ_r during the precommit of `r`: that's the Step 4 collector, which is how the proposer of `r+1` has σ ready. A dAPI that waits for the stored seed therefore starts about one block after anyone who reads σ from its node.

There is no proof of when a PoC nonce was computed, so a head start can't be policed on chain. It can only be made equal.

## 2. Why the seed can't stay hidden until commit

Some nodes must know σ_r before the block containing it commits: the proposer of `r+1` puts it in the block and the validators check it before voting. That holds for any value that block producers write on chain. The alternatives don't help:

| Idea | Why not |
| --- | --- |
| `seed = H(σ ‖ hash(P))` | Brings back grinding: the proposer of `P` chooses `hash(P)` and is the only one who knows the seed early. |
| Use a later round | Shifts the window by a block; the one-block lead stays. |
| Timelock encryption / VDF | Seconds to minutes of extra latency per round, new cryptography and parameters; out of proportion to a one-block lead. |

So the goal is not "nobody knows early" but **"every participant knows at the same moment, and nobody can choose the value"**.

## 3. Proposal: start from the locally aggregated σ

Each dAPI starts generation from σ as soon as **its own node** has it, verified, instead of waiting for the stored seed. The stored seed stays the binding value for validation.

### 3.1 Node → dAPI: local σ read

- inferenced serves the σ its collector built for a round: `LocalBeaconSigma(round) → {round, key_epoch, sigma}`, from memory, not from state. It is served only on the node's local interfaces (the same exposure as the existing gRPC endpoint the dAPI uses; never through public RPC).
- Sources, in order: the collector's σ (from precommit partials); else σ from `Txs[0]` of the current proposal for `r+1` (block parts reach every peer, including non-validators); else not available.
- The collector keeps 4 rounds; that's enough because the dAPI asks only for rounds it knows in advance (`P-1`, `G-1`, a challenge's round).
- **To verify during implementation:** whether CometBFT 0.38 gossips vote extensions to non-validator peers. If not, non-validator participants get σ from the proposal of `r+1` (later, but still before commit); measure the gap.

### 3.2 dAPI: speculative start

1. From chain state the dAPI knows the seed round `r` in advance (stage start `P` is in the epoch schedule; `G` is in the CPoC event; a challenge's start is in the challenge).
2. From `r`'s precommit on, it polls `LocalBeaconSigma(r)` (about every 200 ms, only inside that window).
3. It **verifies** σ before use: `PendingBeacon(r).msg` and the group public key of `key_epoch` come from committed state; one pairing check. An unverifiable σ is ignored.
4. It computes the seed exactly as the chain will (`sha256(tag ‖ u64be(height) ‖ σ_r)`, Step 6b) and starts generation on its ML nodes.
5. When the stored seed lands, the dAPI compares:
   - **Equal** (the normal case): nothing to do; validation uses the stored seed.
   - **Different**: discard the speculative nonces, restart generation with the stored seed (existing stop/start commands). This happens only when round `r` was recorded as a miss although σ_r existed (see §4).
6. If no local σ arrives by the time the stored seed lands, the dAPI behaves exactly as in Step 6b.

The stage **end** is unchanged in every case.

### 3.3 Config

dAPI setting `poc.early_seed_start` (default `false` until the testermint gate passes, then `true`). No chain parameter: generation start is local behavior; generators and validators still agree because validation uses only the stored seed.

## 4. What a byzantine proposer can still do

The proposer of `r+1` can turn round `r` into a miss by building a minimal >2/3 commit without `t+1` valid slots (randomness design §3), even though σ_r circulated. Step 6b then takes the first later `OK` round. Effect:

- Every honest dAPI that started speculatively restarts with the stored seed. Everyone loses the same head start.
- The byzantine proposer gains nothing: it doesn't know the later round's σ earlier than anyone else, and it can't choose it.
- The lever only exists when its chosen commit genuinely lacks `t+1` valid slots, and the miss is visible in `BeaconRecord` and `EventBlockBeacon`.

This is griefing (wasted head start), not an advantage. Monitor it with the restart metric below.

## 5. What remains

| Who | Lead over an honest participant | Why it's acceptable |
| --- | --- | --- |
| Coalition with ≥ `t+1` slots | ≈ 1 block (can sign `m_r` from commit of `r-1`) | Breaks the beacon's unpredictability assumption anyway (fewer than `t+1` colluding slots). |
| Well-connected node | network latency (≈ 100s of ms) | Same as with block hashes today. |
| Non-validator participant (if extensions aren't gossiped to it) | until the proposal of `r+1` | Bounded below one block; measured in testermint. |

Today the proposer of `P` alone knows the seed early **and** chooses it. After Step 6b nobody chooses it; after this proposal everyone learns it at the same moment, up to network latency.

## 6. Metrics

- dAPI: `poc_seed_early_start_total{result=used|mismatch_restart|unavailable}`, histogram `poc_seed_early_lead_seconds` (local σ verified → stored seed seen).
- Node: `beacon_local_sigma_served_total{source=collector|proposal|none}`.

## 7. Tests

- **Node:** `LocalBeaconSigma` returns the collector's σ for a known round, the proposal's σ when the collector has none, nothing for an unknown round; not served on public RPC.
- **dAPI (fake chain bridge and node):**
  - a verified local σ starts generation before the stored seed appears; the seed matches the chain's derivation (golden vector shared with the chain test);
  - an invalid σ (wrong key, wrong round, garbage) is ignored;
  - stored seed differs from the speculative one → generation restarts with the stored seed;
  - no local σ → Step 6b behavior;
  - `poc.early_seed_start=false` → Step 6b behavior.
- **Testermint:**
  - all participants start generation within one block of each other and before the stored seed lands (log timestamps);
  - a forced miss at `P-1` (withholding proposer of `P`) → speculative starts restart with the stored seed, the stage end is unchanged, weights match a run without the miss;
  - PoC weights with `early_seed_start` on equal those with it off.

## 8. Open questions

1. Local read transport: a gRPC method on the node's existing endpoint (proposed), or a file in the `bls-beacon` tmpfs volume the node and dAPI already share.
2. Whether a non-validator participant needs its own path to σ, depending on the extension-gossip check in §3.1.
3. Whether the extra ~1 block lead of a `t+1` coalition warrants anything beyond the beacon's existing assumption.
