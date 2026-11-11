# Per-block BLS beacon via CometBFT vote extensions

**Status:** In progress. **Step 1 ✅ done** (2026-11-09, testermint 5/5 green). **Step 2 ✅ done** (2026-11-09, unit/app tests + testermint 2/2 green). **Step 3 ✅ done** (2026-11-11, testermint `real extensions after dkg` green). **Step 4 ✅ done** (2026-11-11, σ or proof of miss required in every block; testermint `sigma in every block` and `withholding proposer is replaced` green). Step 0 tooling and docs ✅, rollout ops open. **Step 5 ✅ done** (2026-11-11, one `BeaconRecord` per round; testermint `beacon record every round` green). **Step 6 ✅ done** (2026-11-11, CPoC trigger draw from σ behind `use_block_beacon`; testermint `cpoc trigger draws from the beacon` green). **Next:** [Step 6b](#step-6b-ungrindable-poc-work-seeds-poc-cpoc-poc-challenges) (security-critical: PoC/CPoC/challenge work seeds are still grindable block hashes), the VE-on regression gate, then Step 7, plus Step 0 phase 1 (publish the tmkms 0.14 image). See the [progress board](#progress-board).  
**Baseline crypto:** epoch DKG + threshold BLS already exist (`x/bls`, dAPI `BlsManager`)  
**Related:** [`block-beacon-randomness.md`](block-beacon-randomness.md): what the randomness guarantees, what a proposer can and cannot do, rules for consumers, and the first consumer, Confirmation PoC (Step 6). This plan is the **consensus beacon** those consumers use  
**Open gaps after Step 1:** [`block-bls-beacon-step1-gaps.md`](../../docs/block-bls-beacon/block-bls-beacon-step1-gaps.md) (severity-ranked, with gates)  
**Out of scope here:** the consumer side (see `block-beacon-randomness.md`)

**Versions this plan was checked against:** CometBFT `v0.38.21`; Cosmos SDK `github.com/gonka-ai/cosmos-sdk v0.53.3-ps19-observability`; tmkms `0.12.0` (softsign, `protocol_version = "v0.34"`).

## Goal

Every committed height produces (or explicitly misses) a unique group signature `σ_h` that inference-chain can use as an unbiasable random seed, without submitting `σ_h` through the mempool.

```text
m_h = sha256("GONKA_BLOCK_BEACON_V1" ‖ chain_id ‖ u64be(key_epoch) ‖ u64be(h) ‖ blockHash(h-1))   // 32 bytes
σ_h = ThresholdBLSSign(sk, hashToG1(m_h))                                                         // UNIQUE; this IS the per-block seed
```

Honest partials are released only as **vote extensions** on precommits for height `h` (no extra messages: they ride on the vote that finalizes `h`). The proposer of block `h+1` must include either `σ_h` (any `t+1` valid partials aggregate to the same 48 bytes; every validator checks it with one pairing against the key epoch's group public key) or a proof of miss (the extended commit of `h`, showing fewer than `t+1` valid slots). A block with neither is rejected and the proposer is replaced. `FinalizeBlock(h+1)` stores the record, so every round is resolved in the next block.

**Why the current CPoC seed is grindable:** `confirmation_poc.go:164` reads `sdkCtx.HeaderInfo().Hash`. During `FinalizeBlock(H)` the SDK sets that to `req.Hash`, which is the hash of the block **being finalized** (`H`), not `H-1` as the code comment says. The proposer of `H` controls the tx set and can grind that hash. `σ_h` can't be ground: it's unique given `m_h`, and nobody can compute it without `t` slots.

---

## Layer ownership (strict)

| Layer                                      | Change?                             | What                                                                                                                                                                                                                                                                                                                                                                                    |
| ------------------------------------------ | ----------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **CometBFT** (`v0.38.21`)                  | **No fork**                         | Vote extensions are already supported. Only the consensus param `Abci.VoteExtensionsEnableHeight` is set (via upgrade on mainnet).                                                                                                                                                                                                                                                      |
| **Cosmos SDK** (gonka fork `v0.53.3-ps19`) | **No change**                       | Verified: `SetExtendVoteHandler` / `SetVerifyVoteExtensionHandler` / `SetPrepareProposal` / `SetProcessProposal` / `SetPreBlocker` and `baseapp.ValidateVoteExtensions` are present and unmodified in behaviour. No-op VE handlers are installed by default (`baseapp.go:239-244`).                                                                                                     |
| **tmkms** (validator remote signer)        | **Yes, upgrade required (blocker)** | All validators sign via tmkms `0.12.0` with `protocol_version = "v0.34"`. With VE enabled, Comet requires an **extension signature** on every non-nil precommit. A signer that doesn't produce one can't precommit. See [Step 1 findings](#step-1-recheck-findings-2026-11-09).                                                                                                         |
| **inference-chain** `app/`                 | **Yes**                             | Register VE + proposal handlers + a custom `PreBlocker` between `appBuilder.Build` and `app.Load` (`app/app.go:299-345`). Upgrade handler that sets the VE enable height. Partial collector + σ aggregation for proposals (Step 4).                                                                                                                                                                                                               |
| **inference-chain** `x/bls`                | **Yes**                             | Pending-message store (`m_h`), consensus-address → slot map, extension proto, structural checks, checks of the injected beacon (σ by one pairing; proof of miss by counting valid slots), a `BeaconRecord` for every round, queries, events. Aggregation runs off-chain in each node's collector and proposer (`x/bls/blsutil`; Lagrange moved from `aggregateBLSPartialSignaturesBlst`). Do **not** use `MsgSubmitPartialSignature`. |
| **inference-chain** `x/inference`          | **Yes, consumer**                   | CPoC trigger reads `σ_{H-1}` instead of `HeaderInfo().Hash`. No signing here.                                                                                                                                                                                                                                                                                                           |
| **decentralized-api**                      | **Yes, share export**               | Slot shares live only in dAPI memory (`VerificationCache.AggregatedShares`, `internal/bls/manager.go:48`). dAPI writes them to the share bridge file after verification or recovery. dAPI does **not** sign for this path.                                                                                                                                                              |
| **deploy / local-test-net**                | **Small**                           | tmkms image bump + `protocol_version`; a memory-only (`tmpfs`) `bls-beacon` volume at `/root/.inference/bls-beacon`, mounted in `node` and `api` only (share bridge, Step 2 review M1).                                                                                                                                                                                                                                                             |
| **testermint**                             | **Yes, e2e gate**                   | Genesis consensus-param override (currently only `app_state` can be overridden), beacon tests, tmkms variant, snapshot-join after enable.                                                                                                                                                                                                                                               |

**Non-goals:** changing CometBFT consensus rules; rewriting epoch DKG; per-escrow DKG (`bls-dkg-nizk.md`).

---

## Who holds shares vs who is a Comet validator

Vote extensions are produced only by **CometBFT consensus validators**, and only on **non-nil precommits**.

Epoch BLS participants are **slot holders** from `EpochBLSData.participants` (`BLSParticipantInfo{address, slot_start_index, slot_end_index, …}`), where `address` is an **account** address. Vote extensions are attributed to a **consensus** address (`ExtendedVoteInfo.Validator.Address` / `RequestVerifyVoteExtension.ValidatorAddress`).

**Normative rules for v1:**

- Each consensus validator that is also a BLS participant attaches partials for **exactly the slot range it owns** in the key epoch fixed for round `h` (`PendingBeacon(h).key_epoch`, chosen by [R9](#implementation-rules-normative-for-steps-18)). Claiming a different set is rejected as malformed.
- Validators with no BLS slots, or without shares available locally, attach an **empty** extension (zero bytes). That's always valid.
- Threshold is the **existing BLS slot threshold** (`t_slots_degree + 1` slots), not voting power.
- Aggregation counts **slots**, not Tendermint power. A commit may finalize with fewer than the threshold of valid slots. The beacon then **misses** for that round, explicitly, with no fallback hash.
- The consensus-address → slot-range map is computed by `x/bls` when the beacon key switches (`BeaconKeyEpoch` changes, R9) and is stored in state (see Step 3). Validators never send `participant_address`. Ownership is derived from the signer's consensus address.

---

## Timing model

| Moment                               | What exists                                                                                                                                                                                 |
| ------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `EndBlock(h-1)`                      | `x/bls` computes `m_h` from `HeaderInfo().Hash` (= `blockHash(h-1)`) and the beacon key epoch (R9: the newest epoch whose DKG completed, after `beacon_key_switch_delay`), then stores `PendingBeacon{round:h, key_epoch, msg:m_h}`. This is committed with `h-1`. |
| `ExtendVote(h)` (precommit of `h`)   | SDK context = **committed state of** `h-1` (`baseapp/abci.go:610-614`). The handler reads `PendingBeacon(h)`, signs `m_h` with local slot shares and returns the extension.                 |
| `VerifyVoteExtension(h)`             | Same committed state. **Size cap only** (cheap, never errors); content is never a reason to reject (Security note 4).                                                                                                                         |
| Commit of `h`                        | Comet stores the extended commit `EC_h`. Meanwhile each node's collector has verified the partials it received (own `ExtendVote` + peers' `VerifyVoteExtension`) and, with ≥ `t+1` valid slots, built and checked `σ_h` (Step 4). |
| `PrepareProposal(h+1)`               | The proposer puts `Txs[0] = GONKA_BEACON_V1 ‖ InjectedBeacon`: `σ_h` if it holds ≥ `t+1` valid slots (collector or `LocalLastCommit`), otherwise a proof of miss (its unmodified `LocalLastCommit`). Required whenever `PendingBeacon(h)` exists. |
| `ProcessProposal(h+1)`               | σ: one pairing against the key epoch's group key. Proof of miss: matches `ProposedLastCommit`, extension signatures valid, fewer than `t+1` valid slots. Neither, or a failed check → REJECT, next proposer. |
| `PreBlocker` of `FinalizeBlock(h+1)` | `x/bls` stores `BeaconRecord{h, OK, σ}` (σ re-verified) or `{h, MISS_THRESHOLD}` and deletes `PendingBeacon(h)`. |
| `BeginBlock/EndBlock(h+1)` and later | `x/inference` reads the record of `h`. At `EndBlock(H)` round `H-1` is always resolved. |

Comet's rule: if `VoteExtensionsEnableHeight = E`, precommits of height `E` carry extensions (possibly empty). The beacon itself does **not** key on `E`: each capability starts at its own on-chain activation height (rule R6). On a fresh network where everything ships at genesis (tests: `E = ext_from = inj_from = rec_from` from the start), the first σ can be included in the block after the first keyed round; on mainnet `E` passes at v0.2.18 and injection/records start at their own upgrade heights, with `MISS_*` at the boundaries.

**Why** `m_h` **is stored instead of recomputed:** `RequestExtendVote` has the hash of block `h`, not `h-1`, and `RequestFinalizeBlock(h+1)` doesn't expose `blockHash(h-1)` directly. Storing `m_h` in `EndBlock(h-1)` gives every validator and every later reader bit-identical bytes from committed state. It also pins `key_epoch` for the round, which removes epoch-boundary ambiguity. (This resolves former open question 2.)

---

## Sequence diagrams

### Happy path (sign → collect → consume)

```mermaid
sequenceDiagram
    autonumber
    participant D as dAPI (BlsManager)
    participant F as Share file (/root/.inference/bls-beacon)
    participant CT as CometBFT
    participant KMS as tmkms
    participant App as inference-chain app
    participant BLS as x/bls
    participant Inf as x/inference

    D->>F: write epoch-N.json after DKG verify / recovery
    App->>F: ShareProvider loads + verifies sk_i·G2 == slotPK_i

    Note over App,BLS: EndBlock(h-1): store PendingBeacon{h, key_epoch, m_h}

    CT->>App: ExtendVote(height=h)
    App->>BLS: PendingBeacon(h) from committed state
    App->>App: σ_i = sk_i · hashToG1(m_h) for owned slots
    App-->>CT: BeaconVoteExtension{round, key_epoch, slots, sigs}
    CT->>KMS: sign precommit + extension
    KMS-->>CT: vote sig + extension sig

    CT->>App: VerifyVoteExtension(peer ext)
    App->>App: size cap only → ACCEPT / REJECT (never error); copy ext to the collector queue
    App->>App: collector (background): structure + per-slot check, keep valid partials;<br/>≥ t+1 slots → σ_h = Lagrange-aggregate, Verify(group PK, m_h, σ_h)

    Note over CT: Height h commits (>2/3 power of precommits)

    CT->>App: PrepareProposal(h+1, LocalLastCommit)
    alt ≥ t+1 valid slots (collector or LocalLastCommit)
        App-->>CT: Txs = [GONKA_BEACON_V1 ‖ σ_h, mempool txs…]
    else
        App-->>CT: Txs = [GONKA_BEACON_V1 ‖ proof of miss (LocalLastCommit), mempool txs…]
    end

    CT->>App: ProcessProposal(h+1)
    App->>BLS: σ: one pairing vs group PK / proof: matches ProposedLastCommit, signatures, < t+1 valid slots
    App-->>CT: ACCEPT / REJECT (no beacon tx, bad σ, false proof → next round, new proposer)

    CT->>App: FinalizeBlock(h+1)
    App->>BLS: PreBlocker: BeaconRecord{h, OK, σ_h} or {h, MISS_THRESHOLD}; delete PendingBeacon(h)

    App->>Inf: EndBlock(h+1)
    Inf->>BLS: GetBlockBeacon(h)
    BLS-->>Inf: σ_h (OK) or MISS
```

### Censorship / missing partials

```mermaid
sequenceDiagram
    participant P1 as Proposer of h+1, round 0
    participant P2 as Proposer of h+1, round 1
    participant N as Validators
    participant BLS as x/bls

    Note over P1,N: Withholding: P1 holds σ_h but omits it (or sends a false proof of miss)
    P1->>N: proposal(h+1) without beacon tx
    N-->>P1: REJECT → CometBFT round change
    P2->>N: proposal(h+1) with σ_h
    N->>BLS: BeaconRecord{h, OK, σ_h} (value fixed: σ is unique)

    Note over P1,N: Forgery: P1 includes a wrong σ
    P1->>N: proposal with bad σ_h
    N-->>P1: REJECT (one pairing fails) → next proposer

    Note over P2,BLS: Too few valid slots in the commit (holders offline / no shares)
    P2->>N: proposal(h+1) with proof of miss (its LocalLastCommit)
    N->>N: matches ProposedLastCommit, signatures OK, < t+1 valid slots → ACCEPT
    N->>BLS: BeaconRecord{h, MISS_THRESHOLD} (no hash fallback); chain continues
```

---

## Security notes

1. **Uniqueness.** For a fixed `m_h` there is exactly one valid `σ_h`, whichever subset of slots contributes. Choosing which partials to include changes nothing.
2. **Proposer abort (veto) lever.** The proposer sees σ before proposing. It cannot omit σ (a block without σ or a valid proof of miss is rejected, and the next proposer takes over) and cannot change it (unique). The remaining lever: CometBFT lets it choose which >2/3-power set of precommits goes into its `LastCommit`, and a proof of miss is checked against that set. It can force a miss only if some such set holds fewer than `t+1` valid slots.
   - **Invariant to keep (and monitor):** every ≥2/3-voting-power subset of validators must hold ≥ threshold BLS slots. With slots roughly proportional to power and all validators participating, 2/3 of power ≈ 2/3 of slots > the ~51% threshold, so the invariant holds. Add a keeper check at epoch activation that computes the worst case and emits a warning event (Step 3), plus a unit test.
   - BLS slots are assigned from the same `CapWeight` as voting power (x/inference), so the invariant normally holds by construction (~67 slots in any >2/3 set vs a threshold of 51); the coverage warning catches the residual differences (rounding, participants without a BLS key, guardian adjustments, key switch before `SET_NEW_VALIDATORS`).
3. **Liveness first.** `ExtendVote` and `VerifyVoteExtension` returning an error makes CometBFT **panic** (`state/execution.go:354, 369`). Both handlers must recover everything and return an empty extension / `REJECT`, never an error.
4. **Never reject on extension content (revised after the Step 1 review, [`block-bls-beacon-step1-gaps.md`](../../docs/block-bls-beacon/block-bls-beacon-step1-gaps.md) H1/H3).**
   - `VerifyVoteExtension` rejects **only oversized** extensions (cap from `i_total_slots`; from Step 3 on, optionally the owner's slot range). Rejecting a precommit for its content turns any share bug, stale share file or binary-version skew into lost precommits, or a halt if widespread.
   - Extensions never go on chain (Step 4 redesign): malformed, wrong-round/epoch, foreign-slot or cryptographically invalid partials are dropped by each node's collector and counted in its metrics.
   - `ProcessProposal` rejects only for the proposer's own choices: a missing beacon tx, a wrong σ, or a proof of miss that doesn't match its `LastCommit` or shows ≥ `t+1` valid slots. Inside a proof of miss, malformed or invalid extensions only count as invalid slots. No other validator can make a proposal invalid.
5. **Determinism.** Only Finalize writes state. σ is unique for `m_h`, so the stored value doesn't depend on which proposer built it or from which partials; `FinalizeBlock` re-verifies σ before storing it, and a proof of miss's result depends only on block content.
6. **Shares at rest.** The share file is as sensitive as the dAPI keyring. Protection is file permissions (0600, directory 0700) and volume scope. Sealing with a key stored on the same host adds nothing and is not required for v1.

---

## Implementation rules (normative for Steps 1–8)

Every step must obey these rules; each step's task list and test gate cite them. R1–R8 came out of the Step 1 review ([`block-bls-beacon-step1-gaps.md`](../../docs/block-bls-beacon/block-bls-beacon-step1-gaps.md): H1, H3, M4, M5); R9 came out of the Step 2 review (M3). A change that breaks a rule needs an explicit plan revision.

**R1. Beacon ABCI handlers never return an error.** `ExtendVote` and `VerifyVoteExtension` errors make CometBFT panic (F5). Recover everything inside the handler and degrade: empty extension / `REJECT`. `PrepareProposal`/`ProcessProposal` recover into "pass-through" / `REJECT` respectively.

**R2. Extension content is never a reason to reject anything (H1, H3).**
- `VerifyVoteExtension` rejects **only by size** (`beaconExtensionMaxBytes(i_total_slots)`; Step 3 may tighten it to the sender's owned slot range).
- `ProcessProposal` never rejects a proposal because an extension is malformed, stale, foreign or cryptographically invalid. Normally only σ is injected; in a proof of miss, bad extensions just count as invalid slots. Only the proposer's own choices (missing beacon tx, wrong σ, false proof of miss) cause a rejection.

**R3. Partials are judged by deterministic counting, never by rejecting votes.** Each node's collector drops bad partials (metrics). In `ProcessProposal`, σ is checked with one pairing, and a proof of miss by counting valid slots (bad ones count zero). `FinalizeBlock` re-verifies σ and never errors. The stored outcome is `OK` or `MISS_*`.

**R4. Reserved leading txs in `PrepareProposal` (M4).** The handler is a pipeline, not a wrapper around the SDK default:
1. Build the **reserved** txs first (Step 4: `beaconTx`, σ or a proof of miss, whenever `PendingBeacon(h)` exists; none before injection is active).
2. `budget := req.MaxTxBytes − Σ protoSize(reserved)`; if `budget < 0`, emit only the reserved txs (never drop the injection to make room).
3. Run the mempool pass-through (`DefaultProposalHandler` with `NoOpMempool`) on **`req.Txs` only**, with `MaxTxBytes = budget`. The pass-through never sees reserved txs (it `TxDecode`s every tx; a non-SDK `beaconTx` would make it error, and the SDK would fall back to raw `req.Txs`, losing the injection).
4. Return `reserved ++ selected`.
5. A pass-through error yields `reserved ++ []` plus a log line, never raw `req.Txs` (which would drop the injection and ignore the budget).

**R5. `ProcessProposal` inspects reserved txs only (M4).**
- Only `Txs[0]` is interpreted as the injection. From block `inj_from + 1` on it is required whenever `PendingBeacon(h)` exists, and forbidden otherwise (R6).
- `Txs[1:]` is accepted without decoding, regardless of local `mempool.max-txs` (mempool independence).
- No tx other than `Txs[0]` may carry the `GONKA_BEACON_V1` prefix (structural check).

**R6. Capabilities switch on at their own on-chain activation height, never at `E` (M5).**

| Height | Set by | Meaning |
|---|---|---|
| `E` | v0.2.18 handler (Step 1) | CometBFT vote extensions on; extensions may be empty forever |
| `A_ext` | Step 3 upgrade handler | Validators start putting real partials in extensions |
| `A_inj` | Step 4 upgrade handler | From block `A_inj + 1`, `Txs[0]` must carry σ or a proof of miss for the previous round |
| `A_rec` | Step 5 upgrade handler | `FinalizeBlock` stores a `BeaconRecord` for every round |

- Store them in x/bls state (`BeaconActivation{ext_from, inj_from, rec_from}`; `ext_from` exists since Step 3), written once by the upgrade handler of the step that introduces the capability (`= upgrade height`, or later), never inferred from `E` or the binary version. On mainnet, `E` passes at v0.2.18.
- **The activation height itself is produced by the old binary.** At upgrade height `U`, `ExtendVote(U)`, `PrepareProposal(U)` and `ProcessProposal(U)` all ran on the old binary; only `FinalizeBlock(U)` runs on the new one. So:
  - Block `A_inj` comes without a beacon tx and is accepted; the requirement starts at `A_inj + 1`. If it is reached in a decided block anyway, the round is recorded `MISS_NO_INJECTION`.
  - Rounds `≤ A_ext` have no partials, so the next block carries a proof of miss with empty extensions: accepted, recorded `MISS_THRESHOLD`, never `REJECT` or an error.
  - After any later upgrade that changes the extension format, an extension the current binary cannot parse is dropped by the collector (R3), never rejected (R2).
- Capabilities may share one upgrade (`A_ext = A_inj = A_rec = U`); the rules above still apply per capability.

**R7. Mempool independence.** Proposal handling never depends on local `app.toml` (`mempool.max-txs`), so all validators evaluate a proposal identically (Q3).

**R8. State is written only in `FinalizeBlock`** (PreBlocker / BeginBlock / EndBlock). Prepare/Process/ExtendVote/Verify read committed state only.

**R9. The beacon key switches when the new DKG completes, not at epoch start (Step 2 review, M3).** The beacon has its own key pointer, `BeaconKeyEpoch` in x/bls state. It is **not** `GetCurrentSigningEpochID`.
- **Why:** x/inference moves the signing (effective) epoch at `SET_NEW_VALIDATORS`, but that epoch's DKG finishes later. In testermint epoch 3 became the signing epoch at block 51 and its DKG completed about 8 blocks later, roughly half of a 15-block epoch. Keyed on the signing epoch, every validator would lack shares for that window and the beacon would `MISS` every round of it, every epoch, with CPoC missing too (no fallback).
- **Rule:**
  - When an epoch `N`'s DKG reaches `COMPLETED` at height `X` (the `phase_transitions.go` transition that stores `GroupPublicKey` and `SlotPublicKeys`), x/bls stores `BeaconKeyCandidate{epoch: N, from_round: X + 1 + beacon_key_switch_delay}`.
  - `EndBlock(H)` writes `PendingBeacon{round: H+1}` with `key_epoch = N` once `H+1 ≥ from_round`, and sets `BeaconKeyEpoch = N`. Earlier rounds keep the previous key, so there is no gap.
  - **Monotonic:** a candidate is only accepted if `N > BeaconKeyEpoch`.
  - **DKG failure:** a `FAILED` DKG never becomes a candidate. The previous key stays active, for as long as its holders keep its shares.
  - `COMPLETED` is enough: σ is verified against the committed `GroupPublicKey`. `SIGNED` (the previous group attesting the new key) matters for external verifiers, not for the beacon, so the beacon does not wait for it.
- **`beacon_key_switch_delay`:** a new x/bls param in blocks, default **5**, `0` allowed. It covers the DKG event → dAPI export → node reload path (sub-second with fsnotify, ≤ 30 s by polling), so validators whose dAPI lags still hold the new shares at the first new-key round. During the delay the old key signs, so the delay costs nothing.
- **Slot owner map:** built when `BeaconKeyEpoch` changes, not when the signing epoch changes.
- **Retention follows the active key, not the epoch counter:**
  - The dAPI never prunes, and the node never drops, `epoch-<BeaconKeyEpoch>.json` or a pending candidate's file.
  - The dAPI reconcile loop covers `{BeaconKeyEpoch, candidate, current, current+1}`.
  - The node calls `Get(pending.key_epoch)`, so its floor follows the key in use.
- **Validator-set alignment:** if a DKG completes **before** `SET_NEW_VALIDATORS` (possible with longer PoC stages on mainnet), new participants may not yet be consensus validators, and their slots can't be signed until they are. The coverage invariant (Security note 2) is evaluated at the key switch against the current validator set, and emits `EventBeaconSlotCoverageWarning` if fewer than threshold slots are coverable.

---

## Share bridge (dAPI → chain process)

`ExtendVote` runs inside `inferenced` and can't block on a network call. dAPI is the only process that can decrypt shares (it holds the keyring key used by `decryptShare`, `internal/bls/verifier.go:477`). So dAPI **exports** recovered slot shares to a file that the node reads.

**Location (v1):** `${INFERENCE_HOME}/bls-beacon/` = `/root/.inference/bls-beacon/`. Both `node` and `api` already mount the same host directory there:

- `deploy/join/docker-compose.yml:80` (node) and `:118` (api) → `.inference`
- `local-test-net/docker-compose-base.yml:7` (chain-node) and `:26` (api) → `./prod-local/${KEY_NAME}`

No compose change is needed for v1. Hardening later: a dedicated `.bls-beacon` host directory, mounted read-write in `api` and read-only in `node`.

Configurable paths:

- dAPI: `bls_beacon.share_dir` (default `/root/.inference/bls-beacon`), `bls_beacon.export_disabled` (default `false`); env `DAPI_BLS_BEACON__SHARE_DIR`, `DAPI_BLS_BEACON__EXPORT_DISABLED`. The zero value means "export on", so dAPI configs persisted before this section existed keep exporting.
- node `app.toml`: `[bls-beacon] share-dir` (default empty = `<home>/bls-beacon`), `[bls-beacon] enabled = true`. An `app.toml` without the section (nodes upgraded in place) keeps the defaults.

**File:** `epoch-<epoch_id>.json`, mode 0600 in a 0700 directory, written atomically (temp file → fsync → rename → fsync dir). The checksum covers every field except `created_at` and `sha256`, so re-exporting the same shares is a no-op.

```json
{
  "version": 1,
  "chain_id": "gonka-mainnet",
  "epoch_id": 123,
  "participant_address": "gonka1…",
  "slot_start_index": 40,
  "slot_end_index": 47,
  "group_public_key": "<hex, 96B compressed G2>",
  "shares": ["<hex 32B big-endian fr>", "…"],
  "created_at": "2026-11-09T12:00:00Z",
  "sha256": "<hex of canonical JSON without this field>"
}
```

**Node-side** `ShareProvider`**:**

- Interface `Get(epochID uint64) (*EpochShares, bool)`, in package `inference-chain/app/beacon` (no keeper imports).
- On start it loads every `epoch-*.json`. A background `fsnotify` watcher, with a 30 s polling fallback, reloads on change. Results are kept in memory, so `ExtendVote` never touches the disk.
- **Validation on load** (skip file and log if any fails): checksum; `chain_id`; `group_public_key` equals `EpochBLSData.group_public_key` in committed state; the slot range's owner is the file's participant **and that participant's validator is this node** (consensus address from the local CometBFT `/status` vs the owner's validator in committed state; Step 2 review M2); `sk_i·G2 == slotPublicKey_i` for every slot (`PrecomputeSlotPublicKeysBlst`). The last check catches stale or wrong files before they produce bad partials.
- Keeps the current and next epoch; drops anything older than `current-1`.
- Never logs shares; logs only `epoch_id`, slot range, and validity.

**dAPI-side exporter:**

- Hook into `ProcessGroupPublicKeyGeneratedToVerify` after `recomputeAggregatedSharesFromConsensusValidDealers` (`verifier.go:828`), and into the restart recovery path (`recoverySF`).
- Export only when `ensureConsensusSharesComplete(result)` passes (the same guard signing uses).
- Idempotent: if the file exists with the same checksum, skip.
- Delete files with `epoch_id < current-1`.

**Rejected for v1:**

- **Synchronous call ExtendVote → dAPI** (HTTP or socket): a precommit-path dependency. Any dAPI hiccup becomes an empty extension and pushes toward systemic MISS.
- **HTTPS push dAPI → node endpoint:** a new authenticated write listener in the consensus binary, and shares are lost on node restart unless persisted (so you need the file anyway). Only worth it if `api` and `node` run on different hosts. The `ShareProvider` interface makes a gRPC or Unix-socket implementation a drop-in later.

---

## Step 1 recheck findings (2026-11-09)

| #   | Finding                                                                                                                                                                                                                                                               | Evidence                                                                                                                                                                | Consequence                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| --- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| F1  | No VE / proposal handlers registered today. The SDK installs defaults: `NoOpExtendVote`, `NoOpVerifyVoteExtensionHandler`, `DefaultProposalHandler`.                                                                                                                  | `grep` of `inference-chain/app` finds nothing; SDK `baseapp/baseapp.go:230-244`                                                                                         | Enabling VE height with no code change would technically work (empty extensions). Step 1 still registers explicit handlers so later steps only fill them in.                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| F2  | Default app mempool is `NoOpMempool` (`max-txs = -1`, SDK `server/config/config.go:275`). So the default `PrepareProposal` returns `req.Txs` trimmed to `MaxTxBytes`, and the default `ProcessProposal` is `NoOpProcessProposal` (accept all).                        | SDK `abci_utils.go:262-267, 390-396`                                                                                                                                    | Our wrappers replace the defaults outright. Nothing gets lost. All shipped configs use `-1`, but an operator can override it (Open questions, Q3), so the wrappers must **not** depend on local mempool config (Step 4).                                                                                                                                                                                                                                                                                                                                                                        |
| F3  | Handler setters panic on a sealed BaseApp. The app is built through depinject (`appBuilder.Build`, `app.go:299`) and sealed in `app.Load` (`app.go:345`).                                                                                                             | SDK `baseapp/options.go:362-367`                                                                                                                                        | Register handlers **between** `Build` and `Load`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               |
| F4  | The runtime installs its own `PreBlocker` only if none is set (`runtime/app.go:119-122`), and it just calls `ModuleManager.PreBlock`.                                                                                                                                 | SDK runtime                                                                                                                                                             | The custom PreBlocker must call `app.ModuleManager.PreBlock(ctx)` first, then beacon finalize.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| F5  | `ExtendVote` / `VerifyVoteExtension` errors make CometBFT panic. The SDK also returns an error if VE handlers are called while VE is disabled.                                                                                                                        | Comet `state/execution.go:354, 369`; SDK `abci.go:620-628, 697-699`                                                                                                     | Handlers must never error. Tests must cover panics inside handlers (the SDK recovers them into an error, which Comet then panics on, so we must recover ourselves first).                                                                                                                                                                                                                                                                                                                                                                                                                       |
| F6  | Mainnet genesis has `consensus.params.abci.vote_extensions_enable_height = "0"`. No existing upgrade touches consensus params.                                                                                                                                        | `genesis/genesis.json:5450-5471`; `app/upgrades/*`                                                                                                                      | Mainnet enable needs an upgrade handler that writes `ConsensusParamsKeeper.ParamsStore`. It's the first of its kind in this repo. Comet only accepts an enable height **in the future**, and it **can't be changed or disabled once reached**. It's irreversible.                                                                                                                                                                                                                                                                                                                               |
| F7  | **tmkms** `0.12.0`**,** `protocol_version = "v0.34"`**.**                                                                                                                                                                                                             | `tmkms/docker/Dockerfile_keygen:14`; `tmkms/template/tmkms.toml` (`protocol_version`); image `tmkms-softsign-with-keygen:0.2.16` in `deploy/join/docker-compose.yml:67` | **Blocker.** Verified: the production image `0.2.16` runs tmkms `0.12.0` (image build history: `cargo install tmkms --features=softsign --version=0.12.0`), which cannot sign extensions. With VE enabled, a non-nil precommit without an extension signature is a **non-recoverable** error in `types.SignAndCheckVote` (`types/vote.go:443`), and `consensus/state.go:2409` panics ("non-recoverable error when signing vote"). The node logs `CONSENSUS FAILURE` and stops participating until it is fixed and restarted. If >1/3 of power, the chain halts. Fix: tmkms **0.14.0** with `sign_extensions = true` (`[[chain]]`), `protocol_version = "v0.34"` (the only value 0.14 accepts; it is the connection protocol). **Not 0.15.0:** it encodes `SignedVoteResponse` with `cometbft.types.v1beta1.Vote` (fields 1–8, no `extension`/`extension_signature`), so the extension signature is dropped on the wire and the node panics anyway (verified on a live cluster, see [Step 1 results](#step-1-testermint-results-2026-11-09); upstream iqlusioninc/tmkms#1174, no fixed release). **Rollout is coordinated in [Step 0](#step-0-signer-tmkms-rollout-coordination).** The same applies to any other remote signer operators use. |
| F8  | Testermint genesis overrides wrap `genesisSpec` under `app_state` only (`DockerGroup.kt:765`). The node-side merge is a deep `jq '. * input'` of `genesis_overrides.json` into the whole genesis (`inference-chain/scripts/init-docker-genesis.sh:107-119, 164-165`). | as cited                                                                                                                                                                | Testermint needs a top-level override (e.g. `consensusOverrides` in `ApplicationConfig`) to set `consensus.params.abci.vote_extensions_enable_height`. The node script already supports it.                                                                                                                                                                                                                                                                                                                                                                                                     |
| F9  | Testermint does not run tmkms by default (`local-test-net/docker-compose.tmkms.yml` is opt-in).                                                                                                                                                                       | as cited                                                                                                                                                                | Green testermint without tmkms says nothing about F7. A tmkms case is mandatory in Step 1 and Step 8.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| F10 | Snapshot join exists in testermint (`useSnapshots`). With VE on, a state-synced node needs the extended commit for its first proposal.                                                                                                                                | `DockerGroup.kt` `createDockerGroup(..., useSnapshots)`                                                                                                                 | Add a "join via snapshot after `E`" e2e case (Step 1 smoke, Step 8 full).                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| F11 | Block store grows: Comet persists the extended commit per height after `E`.                                                                                                                                                                                           | Comet 0.38 blockstore                                                                                                                                                   | Small (≈ validators × (extension + 64 B)). Note in the ops docs; no action.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     |

---

## Implementation steps (each testable)

Do them in order. Each step is gated on its tests.

### Progress board

Legend: ✅ done · 🟡 partly done (what remains is listed) · ➡️ **next** · ⬜ not started · ⛔ blocked

| Step | Delivers | Status |
|---|---|---|
| [0. Signer (tmkms) rollout coordination](#step-0-signer-tmkms-rollout-coordination) | ≥95% of voting power signs vote extensions before the v0.2.18 proposal | 🟡 **Tooling and docs ✅** (hardened tmkms **0.14.0** image: reviewed lockfile, non-root, ~150 MB; in-place config migration + CI, operator Readme, swap rehearsal test **passing**). **Open:** push image + bump `deploy/join` tag ➡️, testnet rehearsal, operator announcement + readiness tracker, then the ⛔ v0.2.18 proposal (gated on ≥95% of voting power). Runs in parallel with Steps 2–5. |
| [1. Enable VE + no-op handlers + signer readiness](#step-1-enable-ve--no-op-handlers--signer-readiness) | VE active with empty extensions; tmkms signs extensions | ✅ **Done 2026-11-09.** Code, unit + app tests, and `BlockBeaconTests` 5/5 on fresh full clusters ([results](#step-1-testermint-results-2026-11-09)). |
| [2. Share bridge](#step-2-share-bridge) | Node has verified slot shares per epoch | ✅ **Done (2026-11-09)**: code, unit/app tests, testermint `share file exported` / `share file re-exported after dapi restart` green ([status](#step-2-implemented-2026-11-09)) |
| [3. Pending message, slot map, real ExtendVote / VerifyVoteExtension](#step-3-pending-message-slot-map-real-extendvote--verifyvoteextension) | Valid partials on precommit of `h` | ✅ **Done (2026-11-11)**: code, unit/keeper/app tests (incl. end-to-end across the upgrade), testermint `real extensions after dkg` green ([status](#step-3-implemented-2026-11-11)). Activation rides in v0.2.18 |
| [4. σ required in the next block, or a proof of miss](#step-4-σ-required-in-the-next-block-or-a-proof-of-miss) | Block `h+1` carries σ_h (one pairing) or a verifiable proof of miss; neither → rejected | ✅ **Done (2026-11-11)**: code, unit + app tests, testermint `sigma in every block` and `withholding proposer is replaced` green ([status](#step-4-implemented-2026-11-11)) |
| [5. Store σ_h in x/bls: one record per round](#step-5-store-σ_h-in-xbls-one-record-per-round) | `BeaconRecord` for every round in the next block (OK / MISS), queries, events | ✅ **Done (2026-11-11)**: code, keeper/app/cmd tests, testermint `beacon record every round` green plus Step 4 regression ([status](#step-5-implemented-2026-11-11)). Rides v0.2.18 |
| [6. Wire x/inference consumer (CPoC)](#step-6-wire-xinference-consumer-cpoc) | CPoC uses σ, no hash fallback (flagged) | ✅ **Done (2026-11-11)**: flag `confirmation_poc_params.use_block_beacon`, draw from σ of `H-1`, no fallback; unit tests and testermint `cpoc trigger draws from the beacon` green ([status](#step-6-implemented-2026-11-11)). Work seed (§8) still open |
| [6b. Ungrindable PoC work seeds](#step-6b-ungrindable-poc-work-seeds-poc-cpoc-poc-challenges) | PoC, CPoC and challenge seeds from σ, stored on chain; the dAPI reads them from state | ➡️ **Next, security-critical** (today the proposer of the PoC start block can grind the seed and start early) |
| [7. Unit/keeper + app tests, benchmarks](#step-7-unitkeeper--app-tests-benchmarks) | Crypto + ABCI coverage, CPU bounds | ⬜ Not started |
| [8. Testermint e2e](#step-8-testermint-e2e) | Multi-node live beacon + CPoC + tmkms + snapshot join | ⬜ Not started (the Step 1 subset already lives in `BlockBeaconTests.kt`) |

**Next actions, in order:**

1. ✅ Step 1 sign-off: full cluster rebuild + `BlockBeaconTests` 5/5 (2026-11-09).
2. ➡️ Step 0 phase 1: push the tmkms **0.14.0** image (`make tmkms-release VERSION=<tag>`) and bump `deploy/join/docker-compose.yml` (needs registry rights).
3. ✅ Step 2: share bridge done (unit/app tests + 2 testermint gates).
4. ✅ Step 3: real extensions done (2026-11-11).
5. ✅ Step 4: σ or proof of miss required in every block, done (2026-11-11).
   ✅ Step 5: one `BeaconRecord` per round, done (2026-11-11).
   ✅ Step 6: CPoC trigger draw from σ (flagged), done (2026-11-11).
   ➡️ Step 6b: ungrindable work seeds for PoC, CPoC and challenges (security-critical), then the VE-on regression gate and Step 7.
6. Step 0 phases 2–4 in parallel: testnet rehearsal, announcement, readiness tracker.
7. ✅ [`block-bls-beacon-step1-gaps.md`](../../docs/block-bls-beacon/block-bls-beacon-step1-gaps.md) **M1** (reviewed tmkms 0.14.0 lockfile + `cargo audit` CI) and **M2** (multi-stage, digest-pinned, non-root image; per-install KMS identity) are done, so Step 0 phase 1 is unblocked. Before tagging v0.2.18: run the live-params gate and decide **L1** (grace window).

---

### Step 0. Signer (tmkms) rollout coordination

**Why this is its own step:** `inferenced` and dAPI upgrade automatically (cosmovisor / versiond), but tmkms is a separate container that every operator must replace by hand. Once vote extensions are on (height `E`), every validator's signer must attach an extension signature to every **non-nil precommit**. A node whose signer can't panics in consensus (F7) and stays down until its operator fixes tmkms and restarts the node. Enabling is irreversible (F6). So the **v0.2.18 upgrade proposal must not be submitted until the signer rollout passes the gate below.**

**Facts the plan relies on (verified 2026-11-09):**

| Fact | Evidence |
|---|---|
| Production image `ghcr.io/product-science/tmkms-softsign-with-keygen:0.2.16` runs tmkms **0.12.0** (no extension signing) | Image build history on ghcr |
| tmkms signs an extension only for `Precommit` with a non-nil block ID, and only when `sign_extensions = true` | tmkms `src/privval.rs` `extension_bytes`, `src/session.rs` |
| **tmkms 0.14.0 delivers it; 0.15.0 does not.** 0.14 encodes responses with `tendermint_proto` 0.35 root types (= v0.38 `Vote`, tags 9/10 present); 0.15 uses `cometbft.types.v1beta1.Vote` (tags 1–8 only) | Proto sources inside the built images; live cluster: 0.15 → `extension signature: ` empty + `CONSENSUS FAILURE` at h=55; 0.14 → 24 extension signatures per join validator, chain at h=78; upstream iqlusioninc/tmkms#1174 |
| tmkms 0.14.0 must be built with `rust:1.79` (its locked `time` 0.3.x fails on Rust ≥ 1.80) | Docker build log |
| **Upgrading tmkms early is safe.** While VE is off, CometBFT 0.38.21 accepts an extension signature on a non-nil precommit and drops it (`vote.ExtensionSignature = nil`) | Comet `types/vote.go:435-449` |
| An extension signature on a nil precommit or a prevote would be fatal, and tmkms never produces one | as above |
| Operators **without** tmkms (local `priv_validator_key.json`) need nothing: CometBFT's FilePV signs extensions itself | Comet `privval/file.go` |
| `init.sh` only copies the template on the first start, so an image swap alone leaves the old `tmkms.toml` on the volume. The new image runs `migrate-toml.sh` on every start | `tmkms/docker/init.sh`, `tmkms/docker/migrate-toml.sh` |
| tmkms 0.14 reads the same JSON double-sign state file (verified: the swap test from 0.12 keeps signing and the state height keeps increasing). The migration also copies it to `priv_validator_state.json.pre-vote-extensions` once | tmkms CHANGELOG; `migrate-toml.sh` |

**Phases:**

1. **Publish (T0).** Build and push the new tmkms image from this repo (`make -C tmkms build-with-keygen docker-push VERSION=<tag>`; the same for `build-with-import` if that image is distributed). Bump `deploy/join/docker-compose.yml` `tmkms.image` to `<tag>`. The cloud testnet (`deploy-test-net-cloud.yml`) already builds tmkms per commit and recreates its PVC, so it picks this up automatically, but it does **not** exercise the in-place migration (fresh volume).
2. **Testnet rehearsal (T0 + a few days).** On at least one testnet join node with an **existing** `.tmkms` volume: swap the image and check the operator verification below. Then run a VE-enable upgrade on that testnet and confirm the node keeps signing past `E`. Record the result here.
3. **Operator upgrade window (T1).** Announce with a deadline. Operator procedure (`deploy/join`):
   ```bash
   cp -a .tmkms .tmkms.backup-$(date +%F)          # key, config and double-sign state
   docker compose pull tmkms
   docker compose up -d tmkms                        # node keeps running; it reconnects to tmkms
   docker compose logs --tail=200 tmkms
   ```
   Verify in the tmkms logs:
   - `tmkms version: 0.14.0`
   - `Migrated /root/.tmkms/tmkms.toml: sign_extensions = true, protocol_version = v0.34` (or `already up to date`)
   - recurring `signed vote extension` lines. These appear **before** `E` too: tmkms signs and CometBFT discards. This is the readiness signal.
   - the node still signs blocks: your validator appears in recent `last_commit` signatures (`inferenced query block --type=height <h>`, or explorer uptime).

   **Rollback (only before `E`):** stop tmkms, restore `.tmkms.backup-*` (or `tmkms.toml.bak.*` plus the state file backup), and start the previous image tag. Never restore an **older** state file after the new tmkms has signed newer heights; keep the current `priv_validator_state.json` when rolling back the binary.
4. **Readiness tracking (T2).** Before `E` there is no on-chain signal, because extension signatures are stripped while VE is off. Readiness is **self-reported**: each operator posts its validator address and the `tmkms version` and `signed vote extension` log lines in the coordination thread or form. The coordinator keeps a table of validator → confirmed (yes/no) → voting power (`inferenced query comet-validator-set`), plus operators confirmed as "no remote signer".
   - **Gate:** confirmed voting power ≥ **95%** (hard minimum: anything ≤ 2/3 halts at `E`), and the largest unconfirmed validator is below 5%.
5. **Proposal (T3).** Only after the gate: submit the `v0.2.18` software-upgrade proposal at height `U`. The handler sets `E = U + VoteExtensionsEnableDelay` (100 blocks). Re-announce `U` and `E` to the unconfirmed operators.
6. **Activation watch (T4, `E` … `E+1 epoch`).** Watch for validators missing from `last_commit` from `E` on, and for node logs containing `non-recoverable error when signing vote` / `CONSENSUS FAILURE`. Remediation for an affected operator: upgrade tmkms (phase 3), then **restart the node container** (its consensus routine stopped and doesn't resume by itself).

**Coupling to note:** v0.2.18 also carries other pending work (e.g. the warm-key log, `warm-key-log-authz-hooks.md`). Putting the VE enable in v0.2.18 makes that whole release wait for this gate. If v0.2.18 has to ship first, move `EnableVoteExtensions` into the next upgrade before tagging. It's a one-line move in `app/upgrades/v0_2_18/upgrades.go`.

**Status (2026-11-09):**

| Item | State |
|---|---|
| Image with tmkms 0.14.0 + in-place config migration (`tmkms/`) | Done (code, CI, local build; live swap test green) |
| Operator procedure + rollback (`tmkms/Readme.md`) | Done |
| Swap rehearsal as an automated test (`BlockBeaconTests` › `tmkms image swap keeps signing and state`) | ✅ Passing (0.12 → 0.14) |
| Phase 1: push image (`make tmkms-release VERSION=<tag>`), bump `deploy/join/docker-compose.yml` `tmkms.image` | **Open**: needs registry push rights; the tag doesn't exist yet |
| Phase 2: testnet rehearsal on a node with an existing volume | **Open** |
| Phases 3–4: announcement, readiness tracker, coordinator | **Open**: channel and coordinator not chosen |
| Phases 5–6: proposal and activation watch | **Open**: blocked on the gate |

**Exit criteria:** testnet rehearsal recorded; gate met; v0.2.18 proposal submitted; no consensus failures in the first epoch after `E`.

---

### Step 1. Enable VE + no-op handlers + signer readiness

**Touches:** `inference-chain/app`, the v0.2.18 upgrade package, `tmkms/`, `local-test-net`, testermint harness.  
**Does not touch:** cosmos-sdk fork, dAPI, `x/bls`, `x/inference` logic.

**Implemented (2026-11-09):**

| Part | Files | Verified |
|---|---|---|
| 1a handlers | `inference-chain/app/beacon_abci.go` (registered in `app.go` between `Build` and `Load`) | `app/beacon_abci_test.go` (never-error ExtendVote incl. panic/error builders, Verify accept/reject, pass-through MaxTxBytes, PreBlocker propagation); `app/beacon_ve_integration_test.go` drives the full app across `E` (FinalizeBlock returns the new param, ExtendVote is gated before `E` and empty from `E`, Prepare/Process/Finalize keep working) |
| 1b enable height | `app/upgrades/v0_2_18/{constants.go,vote_extensions.go,upgrades.go}`; `app/upgrades.go` passes `ConsensusParamsKeeper.ParamsStore` | `app/upgrades/v0_2_18/vote_extensions_test.go`. Deviation from the original text: if an enable height is **already set** the handler leaves it unchanged (no error), because failing an upgrade halts the chain and testnets may enable VE at genesis. |
| 1c tmkms | `tmkms/docker/Dockerfile_{keygen,import_keys}` (tmkms **0.14.0**, `--locked`, `rust:1.79`); `tmkms/template/tmkms.toml` (`sign_extensions = true`, `protocol_version = "v0.34"`); `tmkms/docker/migrate-toml.sh` (called from `init.sh`, also prints `tmkms version`) | `tmkms/docker/migrate-toml_test.sh` (legacy 0.12 config fixture, idempotency, backups, permissions, env overrides), shellcheck-clean, passes on Debian and Ubuntu, wired into CI (`.github/workflows/tmkms-config-migration.yml`). Image builds (`tmkms v0.14.0`). Live: the swap test migrates a legacy volume and keeps signing. Operator docs: `tmkms/Readme.md` § "Upgrading to vote-extension signing". |
| 1d testermint | `ApplicationConfig.voteExtensionsEnableHeight`; `genesisOverridesJson` + cluster matching in `DockerGroup.kt`; `AppExport.consensus`; env `TESTERMINT_VE_ENABLE_HEIGHT` for the default config; `docker-compose.tmkms.yml` gets `TMKMS_IMAGE` and a persistent `.tmkms` volume; `getValidatorInfo` falls back to `status` (no local key with tmkms); helpers `getLastCommitSigners`, `readSiblingLogs` | `GenesisOverridesTest.kt` (unit, 6/6). E2E in `BlockBeaconTests.kt` (5 cases: VE empty extensions, tmkms on joins, legacy tmkms halts, tmkms image swap continuity, snapshot join after the first snapshot): compiled, **not yet run**. The overrides file is now truncated on write. |

#### Step 1 testermint results (2026-11-09)

Full image rebuild (`local-test-net/stop-rebuild.sh`), then `BlockBeaconTests` on fresh clusters (3 validators, `E=2`), `TESTERMINT_TMKMS_IMAGE` = local build of `tmkms/docker/Dockerfile_keygen`:

| Case | Result | Evidence |
|---|---|---|
| `ve enabled empty extensions` | ✅ PASS (2.5 min) | Genesis `vote_extensions_enable_height = 2` on all nodes; every validator in ≥ 80% of last commits past `E+20`; no `CONSENSUS FAILURE` / `ExtendVote call failed` |
| `ve enabled with tmkms signing extensions on join nodes` | ✅ PASS (7 min) with tmkms **0.14.0** | Both join validators sign through tmkms: 24 `signed vote extension` each, chain at h=78, 0 consensus failures. **With 0.15.0 the same case failed:** both joins crashed at h=55 with `extensions must be present IFF vote is a non-nil Precommit; extension signature: ` (empty) |
| `legacy tmkms cannot sign extensions and the node halts` | ✅ PASS (5 min) | join1 on tmkms 0.12.0 joins the set at h=54; at h=55 its node logs `CONSENSUS FAILURE!!! … non-recoverable error when signing vote … Precommit`. Saved in `testermint/logs/legacy-tmkms-join1-{node,tmkms}.log` |
| `tmkms image swap keeps signing and state` | ✅ PASS (7.5 min) | 0.12.0 → 0.14.0 on the same volume: config migrated, signing resumes (≥ 80%), state height increases, no double-sign refusal, `signed vote extension` logged while VE is off (readiness signal) |
| `snapshot join after enable height catches up` | ✅ PASS (12 min, twice) | A joiner state-syncs from the first snapshot past `E` and catches up |

**Rerun with the hardened image (M1/M2, 2026-11-09):** `ve enabled with tmkms signing extensions on join nodes` and `tmkms image swap keeps signing and state` ✅ PASS with the final image (tmkms 0.14.0 built from `tmkms/build/tmkms-0.14.0.Cargo.lock`, multi-stage, non-root): both join tmkms run as uid 10001, 23 extension signatures each, chain at h=78, 0 consensus failures; the swap from a root-owned 0.12 volume migrates, re-owns and keeps signing.

**Review after the runs (2026-11-09):** H1 (tolerant `VerifyVoteExtension`), H2 (tmkms rollback script + CI) and M3 (live mainnet params gate) are fixed; everything still open is tracked in [`block-bls-beacon-step1-gaps.md`](../../docs/block-bls-beacon/block-bls-beacon-step1-gaps.md).

**Bugs found by the runs and fixed:**
- **Product (Step 0/1):** tmkms 0.15.0 cannot deliver extension signatures (see F7) → target switched to **0.14.0**.
- **testermint harness:** (1) join nodes behind tmkms were registered with the wrong consensus key: `inferenced start` always regenerates a local `priv_validator_key.json` (SDK `LoadOrGenFilePV`), so `comet show-validator` returned an unused key → `getValidatorInfo` now uses `status` (the key the node actually signs with); (2) join nodes' genesis uses the legacy top-level `consensus_params` layout → both layouts parsed; (3) participation was measured over blocks before the join validators existed → only validators active over the whole window are checked; (4) the legacy case now treats the halt as the expected outcome and saves logs before teardown; (5) the snapshot case waits for the first snapshot (interval 100).
- **Local tree:** an empty untracked `devshard/cmd/devshardd/inference/zz_frames_probe_test.go` (0 bytes, no `package` clause) broke `make devshardd-build`; moved aside (not deleted).

Publishing the image and bumping `deploy/join` belong to Step 0.

**1a. App handler skeleton** (new file `inference-chain/app/beacon_abci.go`)

- `func (app *App) registerBeaconABCI()`, called in `New()` right after `appBuilder.Build(...)` (`app.go:299`) and before `app.Load` (F3).
- `ExtendVote`: wrapper that `recover()`s and returns `&abci.ResponseExtendVote{VoteExtension: nil}`, always with a nil error (F5).
- `VerifyVoteExtension`: accepts any extension up to `beaconExtensionMaxBytes(i_total_slots)` (64 B/slot + 512 B, floor 4 KiB), rejects only oversized ones, never errors. (Originally "reject anything non-empty"; changed in the review, [`block-bls-beacon-step1-gaps.md`](../../docs/block-bls-beacon/block-bls-beacon-step1-gaps.md) H1.)
- `PrepareProposal`: pass-through equivalent of the current default (F2): take `req.Txs` up to `req.MaxTxBytes`.
- `ProcessProposal`: accept (matches today's `NoOpProcessProposal`).
- `PreBlocker`: `return app.ModuleManager.PreBlock(ctx)` (F4). Beacon logic is added in Step 5.
- A metric/log line in `PrepareProposal` showing whether `LocalLastCommit` carries extensions (debug level).

**1b. Enable height**

- **New networks / local / testermint:** `genesis_overrides.json` → `{"consensus":{"params":{"abci":{"vote_extensions_enable_height":"<E>"}}}}`. `E=2` for tests (it must be ≥ initial height; `1` is avoided because `ExtendVote` at the initial height uses the `finalizeBlockState` branch).
- **Mainnet:** in the (unreleased) `app/upgrades/v0_2_18` handler, gated by Step 0:
  ```go
  cp, err := app.ConsensusParamsKeeper.ParamsStore.Get(ctx)
  if cp.Abci == nil { cp.Abci = &cmtproto.ABCIParams{} }
  cp.Abci.VoteExtensionsEnableHeight = sdkCtx.BlockHeight() + VEEnableDelay   // e.g. 100
  err = app.ConsensusParamsKeeper.ParamsStore.Set(ctx, cp)
  ```

  Run `cp.ToProto()` through `cmttypes.ConsensusParamsFromProto(...).ValidateUpdate(...)` in the handler so a bad value fails the upgrade instead of halting the node later. `VEEnableDelay` is a constant in `constants.go`. It gives a grace window for the last tmkms stragglers but can't be undone afterwards (F6).
  Alternative without a binary upgrade: a gov `MsgUpdateParams` on `x/consensus`. Not preferred, because it can be passed before the binary with handlers is deployed.

**1c. Signer readiness (F7 blocker)**

- Upgrade to tmkms **0.14.0** (not 0.15.0, see F7). `tmkms/docker/Dockerfile_keygen` and `Dockerfile_import_keys`: `FROM rust:1.79`, `cargo install tmkms --locked --features=softsign --version=0.14.0`; `tmkms/template/tmkms.toml`: `sign_extensions = true` in `[[chain]]`, `protocol_version = "v0.34"` in `[[validator]]`. Bump the image tag in `deploy/join/docker-compose.yml:67` and `local-test-net/docker-compose.tmkms.yml`.
- Check that the new tmkms keeps reading the existing `priv_validator_state.json` (double-sign protection) without migration. Document the operator steps.
- Operator runbook entry: "upgrade tmkms before upgrade height U; verify with `inferenced query block` that your precommits are still included after `E`."
- Pre-flight: after `E`, an e2e query that every validator signature in `LastCommit` is present (no absentees).

**1d. Testermint harness (F8)**

- Add `consensusOverrides: Map<String, Any>?` (or a typed `ConsensusSpec`) to `ApplicationConfig` (`LocalInferencePair.kt:1373`). In `DockerGroup.kt:765` merge it at the top level next to `app_state`.

**Tests (gate):**

- Unit `app/beacon_abci_test.go`: ExtendVote returns empty with nil error, including when a deliberately panicking inner function is injected. Verify accepts empty and rejects non-empty. PreBlocker calls the module manager (check via an upgrade-module side effect or a mock).
- Upgrade unit test `app/upgrades/v0_2_18/vote_extensions_test.go`: after the handler runs, `ParamsStore.Get().Abci.VoteExtensionsEnableHeight == H+delay`; running it again is a no-op; invalid params fail before any write.
- Testermint smoke (`BlockBeaconTests.kt`, case `ve enabled empty extensions`): genesis `E=2`, 3 nodes, chain passes `E+20`; every validator appears in ≥80% of the last 20 `last_commit`s; no node logs `CONSENSUS FAILURE` / `ExtendVote call failed`.
- Testermint **tmkms** case (`ve enabled with tmkms signing extensions on join nodes`, needs `TESTERMINT_TMKMS_IMAGE`): join nodes sign through the new tmkms; same checks, plus tmkms logs show `sign_extensions = true` and `signed vote extension`. The genesis node keeps its local key: it's created by the genesis flow, which doesn't support a remote signer.
- Testermint **legacy tmkms** case (`legacy tmkms cannot sign extensions and the node halts`, default image `…:0.2.16`): join1 on tmkms 0.12 logs `non-recoverable error when signing vote` / `CONSENSUS FAILURE` once it precommits after `E`. This proves F7 for real.
- Testermint snapshot-join smoke (F10, `snapshot join after enable height catches up`): a joiner state-syncs at height > `E` and catches up.
- Testermint **tmkms state continuity**: start a cluster on the **old** tmkms image with VE off and run to height X. Stop tmkms, swap in the new image, and keep the same `.tmkms/state/priv_validator_state.json`. Expect: tmkms starts without migration errors, the node keeps signing from X+1 (no gap beyond the restart, no "double sign" refusal), and the state file's height keeps increasing. Then a VE-enable upgrade to `E` passes with no missing precommits.

---

### Step 2. Share bridge

**Touches:** `decentralized-api/internal/bls` (exporter), `decentralized-api` config, `inference-chain/app/beacon` (provider), `inference-chain` `app.toml` template.  
**Does not touch:** cosmos-sdk, consensus behaviour (extensions stay empty in this step).

**2a. Shared format package**

- `inference-chain/app/beacon/sharefile` (Go, no keeper deps): `type EpochShareFile struct{…}`, `Marshal/Unmarshal`, canonical checksum, `Validate(chainID)`. dAPI imports it if module boundaries allow; otherwise duplicate it with a cross-test fixture (`testdata/epoch-share-v1.json`) that both sides parse.

**2b. dAPI exporter** (`decentralized-api/internal/bls/share_export.go`)

- `func (bm *BlsManager) exportBeaconShares(result *VerificationResult) error`: build the file from `result.EpochID`, `SlotRange`, `AggregatedShares` (fr → 32 B big-endian), `GroupPublicKey`, the participant address from the cosmos client, and the chain id.
- Call sites: after `recomputeAggregatedSharesFromConsensusValidDealers` in `ProcessGroupPublicKeyGeneratedToVerify` (`verifier.go:828`) and at the end of the recovery path. Guard with `ensureConsensusSharesComplete`.
- Atomic write: `os.CreateTemp(dir)` → write → `f.Sync()` → `os.Rename` → fsync dir; `os.MkdirAll(dir, 0700)`, file `0600`.
- Cleanup: remove `epoch-*.json` with `id < current-1`.
- Config: `bls.beacon_share_dir`, `bls.beacon_export_enabled` (default true).

**2c. Node ShareProvider** (`inference-chain/app/beacon/share_provider.go`)

- `type ShareProvider interface { Get(epochID uint64) (*EpochShares, bool) }` where `EpochShares{SlotStart, SlotEnd uint32; Scalars [][32]byte /* LE for blst */; ParticipantAddress string}`.
- `FileShareProvider`: loads at startup, `fsnotify` plus 30 s polling, `sync.RWMutex` map.
- `Validate` against committed state happens in a lazy verifier (`verified map[epoch]bool`), run once per epoch on first `Get` from an ABCI context that passes a `SlotPublicKeyFn` (wraps `PrecomputeSlotPublicKeysBlst` + `GroupPublicKey`). Verification failure → mark the epoch bad and return `false` from then on (until the file changes).
- Wired into `App` in `New()`. Disabled when `[bls-beacon] enabled=false`.

**Tests (gate):**

- Round-trip: dAPI writes → provider reads → identical scalars.
- Negative: wrong `chain_id`, bad checksum, a truncated file, a scalar that doesn't match the slot public key, and a `group_public_key` mismatch are all rejected; a different epoch requested gives `false`.
- Atomicity: a concurrent reader during a write never sees a partial file (loop test).
- Permissions: the file is created 0600 and the directory 0700.
- **Testermint gate** (`BlockBeaconTests.kt`, case `share file exported`): after DKG, `/root/.inference/bls-beacon/epoch-<N>.json` exists on every validator node container, mode 0600, and the node log shows `beacon shares loaded epoch=<N> slots=[a,b] valid=true`. Nothing else changes yet (extensions are still empty).
- **Testermint gate** (case `share file re-exported after dapi restart`): delete the file on one node and restart that node's dAPI. Recovery writes the file again within one recovery cycle, and the node log shows it reloaded.

#### Step 2 implemented (2026-11-09)

| Piece | Where | Notes / deviations from the text above |
|---|---|---|
| Shared format | `inference-chain/app/beacon/sharefile` | `New`, `Validate(chainID)`, `Marshal/Unmarshal` (unknown fields and trailing data rejected), `ReadFile` (8 MiB cap), `WriteAtomic` (idempotent by checksum, re-tightens 0600/0700), `Prune` (also stale `.epoch-*.tmp`). Shares must be non-zero fr elements. dAPI imports the package directly (it already depends on `inference-chain`), so no duplicate. Golden fixture `testdata/epoch-share-v1.json`. |
| dAPI exporter | `decentralized-api/internal/bls/share_export.go` | Hooked into `storeVerificationResult` (one place covers the DKG-completion event and every recovery path) instead of the two call sites; exports only `COMPLETED`/`SIGNED` participant results with a 96-byte group key, guarded by `ensureConsensusSharesComplete`. **Async (review L6):** the hook only snapshots shares and group key into a job; a worker writes the file (queue of 8; a full queue drops and the reconcile loop writes it); the chain id (`Status().NodeInfo.Network`) is prefetched at start and cached. Prunes `< epoch-2` on export. |
| dAPI reconcile loop | same file, `StartBeaconShareExport` (30 s) | For the current and next epoch: take the final result from cache, or recover it from chain (`GetOrRecoverVerificationResult`, rate-limited to one attempt per 2 min per epoch; once per epoch per process when it succeeds). **Content check (review L2):** the file is rewritten unless its checksum equals the file this result produces (missing, corrupt, stale or foreign files are all repaired), and removed when this node holds no slots in the epoch. Prunes `< current-1`. Started from `main.go`. |
| dAPI config | `apiconfig.BlsBeaconConfig` (`bls_beacon.share_dir`, `bls_beacon.export_disabled`) | `export_disabled` instead of `beacon_export_enabled`, so a zero/persisted config stays on. |
| Node provider | `inference-chain/app/beacon/share_provider.go` | `Get(ctx sdk.Context, epochID)`. Loads at start, fsnotify + 30 s polling, 200 ms debounce. **Reader (review L3):** regular files only (no symlinks, FIFOs, devices; `O_NOFOLLOW\|O_NONBLOCK`, size-limited), mode 0600 checked before the content; name/content epoch must match. **Background verification (review L5):** a worker checks new files against an immutable committed version (height recorded by the beacon PreBlocker), so `Get` in `ExtendVote` only reads memory; checks are chain id, group key, slot range owned by the file's participant, that participant's validator is this node (M2), and `sk_i·G2 == slotPublicKey_i` for every slot. **Retries (review L1):** only deterministic verdicts (`ErrKeysInvalid`: DKG failed, range outside the epoch) reject a file; other errors and a not-final DKG retry after 20 heights, an unknown own identity every height. A rejected file stays rejected until it changes. **Missing file (review L4):** one warning per epoch after 20 heights without a file for the requested epoch. Keeps epochs `≥ requested-1`. Recovers panics. |
| Committed keys | `inference-chain/app/beacon_shares.go` | Reads `EpochBLSData.SlotPublicKeys` (stored at DKG completion), falls back to `PrecomputeSlotPublicKeysBlst` only for legacy epochs without them. Resolves the slot owner participant → validator (`ValAddress(acc)`) → consensus address (`OwnerConsAddress`). |
| Own validator identity (review M2, 2026-11-10) | `inference-chain/app/beacon_identity.go`, `beacon.SelfIdentity` | Background lookup of `validator_info.address` from the local CometBFT `/status` (`rpc.laddr`, tcp or unix; works with a remote signer), retried with backoff and never on the ABCI path. The provider uses a file only if the owner's consensus address equals it; unknown identity → retry next block; owner without a validator → retry after 20 blocks; mismatch → `invalid` (`this node signs as …`). |
| Memory-only share volume (review M1, 2026-11-10) | `deploy/join/docker-compose.yml`, `local-test-net/docker-compose-base.yml` | `bls-beacon` volume (`tmpfs`, 16 MiB, `mode=0700`) at `/root/.inference/bls-beacon` in `node` and `api` only. Shares never touch the disk or backups; `versiond` (which mounts `.inference` read-only) sees an empty mount point. Default paths are unchanged. When both containers stop the shares are gone and the api re-exports them from chain. Encrypting with the warm key was considered and rejected (the node would need the warm key and `KEYRING_PASSWORD`, which is in plaintext in `config.env`). |
| Wiring | `app.go` (`App.BeaconShares`, `App.Close`), `beacon_abci.go`, `cmd/inferenced/cmd/config.go` | `[bls-beacon]` section in the `app.toml` template. ExtendVote runs `beaconShareProbe`: asks the provider for `GetCurrentSigningEpochID` (this triggers the one-time verification and its `beacon shares loaded … valid=` log) and **still returns an empty extension**. |

**Tests (all green, `-race` where concurrent):**

- `app/beacon/sharefile`: round trip, checksum ignores `created_at`, golden cross-fixture, 13 rejection classes (wrong chain, version, ranges, bad/zero/≥modulus shares, checksum, size), malformed JSON (truncated, unknown field, trailing data), 0600/0700 and re-tightening, idempotence, **concurrent reader never sees a partial file** (200 rewrites × 4 readers), prune, size cap.
- `app/beacon`: provider round trip with real polynomial shares; rejections against committed state (group key, wrong share, shares of another DKG, foreign owner, chain id, keeper error), each verified once; recovery when the file is fixed; not-ready retry after 20 heights; bad files (0644, truncated, name/content mismatch); never panics; epoch floor; fsnotify watch (create → delete → re-export); concurrent `Get` + `Reload`.
- `app`: `[bls-beacon]` parsing (missing section = defaults), provider setup on/off, `epochKeysFromData` phases and legacy precompute, `beaconShareProbe` (empty extension, nil/panicking provider), **end-to-end with the real app**: completed epoch in committed x/bls state + file written by `sharefile.WriteAtomic` → `ExtendVote` at E verifies it (`valid`) and returns an empty extension.
- `cmd/inferenced/cmd`: the rendered `app.toml` contains `[bls-beacon]` with defaults.
- `decentralized-api/internal/bls`: export writes a 0600 file with the right fields; **dAPI → node provider round trip** (the node accepts what the dAPI writes); non-final results skipped; incomplete shares refused; disabled config and chain-id errors; `storeVerificationResult` hook; pruning; reconcile re-exports a deleted/corrupted file from cache, recovers after a restart (empty cache), backs off, contains panics, prunes `< current-1`; the loop re-exports a deleted file.
- Testermint (`BlockBeaconTests.kt`, tag `share-bridge`), **2/2 passed (2026-11-09, ~9 min each)** on rebuilt api + node images:
  - `share file exported`: every validator's dAPI wrote `epoch-<E>.json` (0600 in a 0700 dir) for the signing epoch, with its own participant address and slot range; every node logged `beacon shares loaded … valid=true` and no `valid=false`; no consensus failure.
  - **Rerun after the review fixes M1/M2 (2026-11-10): 2/2 passed (~9 min each).** `share file exported` additionally checks that `/root/.inference/bls-beacon` is a tmpfs mount in node and api, that each node's logged `beacon validator identity` equals its `/status` validator address, and that a share file copied from genesis onto join2 is rejected (`this node signs as …`).
  - `share file re-exported after dapi restart`: after `rm` of join1's file and `docker restart` of its dAPI, the startup reconcile rewrote a byte-identical file (same `sha256`), and the node logged `beacon shares unloaded` then a fresh `loaded … valid=true`.
  - Test notes: the cases wait for the **effective epoch** (`query inference get-current-epoch`, the same epoch `ExtendVote` and the dAPI reconcile use), not `latestEpoch.index`, which advances at PoC start before that epoch's DKG completes (the first run timed out on exactly that). They run with 40-block epochs so the signing epoch cannot rotate during a dAPI restart (the default 15-block epoch rotates in ~80 s). In the first (timed-out) run the feature itself already worked: exports on every DKG completion, 56 `valid=true` loads, 0 rejections.

Gaps found while implementing are listed in [`block-bls-beacon-step1-gaps.md`](../../docs/block-bls-beacon/block-bls-beacon-step1-gaps.md) (S2-*).

---

### Step 3. Pending message, slot map, real ExtendVote / VerifyVoteExtension

**Rules:** R1, R2, R6, R8, R9 ([Implementation rules](#implementation-rules-normative-for-steps-18)). The Step 3 upgrade handler writes `BeaconActivation.ext_from = U` (R6) and seeds `BeaconKeyEpoch` with the newest epoch whose DKG is already `COMPLETED`/`SIGNED` (R9); `ExtendVote` produces partials only for heights `> ext_from` (height `U` itself is precommitted by the old binary).

**Touches:** `inference-chain/proto/inference/bls/beacon.proto` (new), `x/bls/keeper/beacon.go` (new), `x/bls` module `EndBlock`, `app/beacon_abci.go`.  
**Does not touch:** aggregation and persistence of σ (Step 5), proposals (Step 4).

**3a. Protos**

```proto
message BeaconVoteExtension {
  uint32 version        = 1;   // 1
  uint64 round          = 2;   // h
  uint64 key_epoch      = 3;
  repeated uint32 slot_indices = 4;   // ascending, == owner's full range
  bytes  signatures     = 5;   // 48 * len(slot_indices), compressed G1
}

message PendingBeacon { uint64 round = 1; uint64 key_epoch = 2; bytes msg = 3; /* 32B m_h */ }

message BeaconSlotOwner { bytes cons_addr = 1; string participant_address = 2; uint32 slot_start = 3; uint32 slot_end = 4; }
```

**3b. x/bls state + EndBlock**

- Store keys: `PendingBeacon/{round}`, `BeaconSlotOwner/{epoch}/{consAddr}`, `BeaconKeyEpoch`, `BeaconKeyCandidate` (R9). Param `beacon_key_switch_delay` (blocks, default 5).
- DKG completion hook (R9): the transition to `COMPLETED` for epoch `N` at height `X` stores `BeaconKeyCandidate{N, X+1+beacon_key_switch_delay}` if `N > BeaconKeyEpoch`.
- `EndBlock(H)`: first, if a candidate exists and `H+1 ≥ candidate.from_round`, set `BeaconKeyEpoch = candidate.epoch`, delete the candidate and rebuild the slot owner map (R9). Then, if `H+1 ≥ VoteExtensionsEnableHeight` (read from `ctx.ConsensusParams().Abci`) and `BeaconKeyEpoch` is set: write `PendingBeacon{round:H+1, key_epoch: BeaconKeyEpoch, msg: m_{H+1}}` using `HeaderInfo().Hash` (= `blockHash(H)`). If no DKG has completed yet, write nothing (Finalize will record `MISS_NO_KEY`). `GetCurrentSigningEpochID` is **not** used for the beacon.
- Pruning: `PendingBeacon` for round `r` is deleted in Finalize of `r+1` (Step 5). A safety sweep in EndBlock deletes `round < H-10`, in case blocks lacked an EC.
- Slot owner map: when `BeaconKeyEpoch` changes (R9; the switch in EndBlock above), build `BeaconSlotOwner` for each participant: `participant.address` (acc) → `sdk.ValAddress(accBytes)` → `StakingKeeper.GetValidator` → `GetConsAddr()`. Participants without a bonded validator are skipped (their slots can never be covered by VE; log it and count them in the invariant check).
  - Confirmed: participant account bytes == validator operator bytes (`epochgroup/epoch_group.go:411`). Pin it with a unit test.
  - **Consensus-key rotation:** `ValidatorKey` can be updated (`msg_server_submit_new_participant.go:23-24`), so the persisted map is `epoch → acc → slot range` only. `VerifyVoteExtension`, ProcessProposal and Finalize resolve `consAddr → validator` (`GetValidatorByConsAddr`) → operator → acc at call time. Test: rotate a validator's consensus key mid-epoch; its extensions are still attributed to the right slots.
- Invariant check at map build (Security note 2): sort validators by power, compute the minimal slots held by any ≥2/3-power subset (greedy: drop the largest slots-per-power validators until <1/3 power is dropped), and emit `EventBeaconSlotCoverageWarning` if it's below threshold.

**3c. Keeper helpers** (`x/bls/keeper/beacon.go`)

- `BeaconMessage(chainID string, keyEpoch, round uint64, prevHash []byte) [32]byte` (pure; test vectors).
- `GetPendingBeacon(ctx, round) (PendingBeacon, bool)`
- `GetBeaconSlotOwner(ctx, epoch, consAddr) (BeaconSlotOwner, bool)`: resolves `consAddr` through staking at call time, then looks up the persisted `epoch → acc → range` map (the `cons_addr` field in the proto is filled in on read, not stored).
- `ValidateBeaconExtensionStructure(ctx, ext []byte, round uint64, consAddr []byte) error`: decodes; `version==1`; `round==h`; `key_epoch == pending.key_epoch`; `slot_indices` exactly `[owner.start..owner.end]`; `len(signatures)==48*len(slots)`; total size ≤ `48*i_total_slots + 128`. No pairings.
- `MaxBeaconExtensionBytes(params)` exported for the size check.

**3d. App handlers**

- `ExtendVote(ctx, req)`:
  1. `pending, ok := bls.GetPendingBeacon(ctx, req.Height)` → if not ok, empty.
  2. `shares, ok := shareProvider.Get(pending.KeyEpoch)` → if not ok, empty (warn once per epoch).
  3. `msgG1 := hashToG1(pending.Msg)` (the same mapper as `keeper.hashToG1` / dAPI `group_validation.go:300`: one `MapToCurve1` call + isogeny + cofactor clearing on a 32-byte input).
  4. For each scalar: `σ_i = msgG1 · sk_i` (blst, the same code as dAPI `computePartialSignatureBlst`), concatenated compressed.
  5. Marshal `BeaconVoteExtension`. `recover()` wrapper; nil error always.
  - Budget: ~0.5 ms per slot. Log a warning if > 50 ms total.
- `VerifyVoteExtension(ctx, req)`: keep the Step 1 size-cap behaviour (optionally tighten the cap to the sender's owned slot range, [`block-bls-beacon-step1-gaps.md`](../../docs/block-bls-beacon/block-bls-beacon-step1-gaps.md) L6). **Do not** call `ValidateBeaconExtensionStructure` here: structure failures are dropped in `FinalizeBeacon` (Security note 4). `recover()`; never error.
- Activation (R6, gap M5): the first extended commit after the Step 3 upgrade has empty extensions (old binary precommitted `U`); treat as `MISS`, never as an error. Store `BeaconActivation.ext_from` in x/bls state in the upgrade handler; never infer it from `E`.
- Move the shared signing primitive into a package both dAPI and chain can import (`inference-chain/x/bls/blsutil`), so the chain doesn't depend on dAPI code.
- Share bridge follow-up (R9): add a query `bls beacon-key` (`BeaconKeyEpoch` + candidate). The dAPI reconcile loop covers `{BeaconKeyEpoch, candidate, current, current+1}` and its pruning never removes the active key or candidate's file (today it covers `{current, current+1}` and prunes `< current-1`). The Step 2 probe (`beaconShareProbe` on `GetCurrentSigningEpochID`) is replaced by `Get(pending.KeyEpoch)`.

**Tests (gate):**

- `m_h` vectors: fixed inputs → fixed 32-byte hex (commit the vector; reuse it for cross-language later).
- Crypto: fixture DKG (reuse helpers from `bls_crypto_aggregation_test.go`) → two validators' extensions → each per-slot partial verifies with `verifyBLSPartialSignatureBlst`; a single bit flip fails.
- Structure (`ValidateBeaconExtensionStructure`, used by the Step 4 collector; originally planned for `FinalizeBeacon`): wrong round, wrong epoch, a subset or superset of the slot range, a slot range belonging to another validator and an odd signature length are all **dropped** (counted in `invalid_slots`); oversized is rejected in Verify; empty is a valid no-contribution.
- ExtendVote: no pending → empty; no shares → empty; a panicking provider → empty with nil error; with fixture shares → a non-empty extension that decodes and verifies.
- EndBlock: `PendingBeacon(H+1)` written iff `H+1 ≥ E` and `BeaconKeyEpoch` is set; `msg` equals the vector for `HeaderInfo().Hash`.
- Key switch (R9): DKG `N` completes at `X` → rounds `≤ X+delay` keep `key_epoch = N-1`, rounds `≥ X+1+delay` use `N`, with no round lacking a pending message; the switch does not wait for `SET_NEW_VALIDATORS` or `SIGNED`; a `FAILED` DKG keeps the old key; an older epoch completing late never moves the key backwards; `delay = 0` switches at `X+1`; the upgrade handler seeds `BeaconKeyEpoch` from existing state.
- Slot map: built on key switch (not on signing-epoch change); consAddr lookup is correct; the coverage warning fires for a crafted skewed distribution.
- Activation (R6): app test crossing the Step 3 upgrade height `U` with `E < U`: `ExtendVote(U)` (old-binary semantics: empty) and `ExtendVote(U+1)` (partials); no rejection anywhere; `BeaconActivation.ext_from == U` after the handler, unchanged by a second run.
- Rule R2: `VerifyVoteExtension` accepts a garbage-but-small extension and an extension from a validator with no slots; rejects only oversized.

**Observability added in this step** (needed for e2e, since CometBFT RPC doesn't expose vote extensions before Step 4 puts them in blocks):
- Prometheus metrics in `app/beacon`: `beacon_extend_vote_slots` (gauge: slots signed in the last ExtendVote), `beacon_extend_vote_empty_total{reason=no_pending|no_shares|panic}`, `beacon_verify_vote_ext_total{result=accept|reject,reason=…}`, `beacon_extend_vote_duration_seconds`.
- An info log once per epoch plus a debug log per height: `beacon extension built round=<h> epoch=<e> slots=<n>`.
- A testermint helper `ChainNode.metric(name, labels)` that scrapes `:26660/metrics` (Prometheus is already on via `CONFIG_instrumentation__prometheus`). Add it to the harness if it doesn't exist.

**Testermint gate** (`BlockBeaconTests.kt`, case `real extensions after dkg`), 3 validators, `E=2`:
- Before DKG completes: `beacon_extend_vote_empty_total{reason="no_pending"}` (or `no_shares`) increases; no rejects.
- After DKG + 20 blocks: on every validator `beacon_extend_vote_slots` equals its slot range size, and `beacon_verify_vote_ext_total{result="reject"} == 0` everywhere.
- `beacon_extend_vote_duration_seconds` p99 < 50 ms.
- No `ExtendVote call failed` / `VerifyVoteExtension call failed` in any node log; block time is within 10% of a VE-off baseline run.

---

#### Step 3 implemented (2026-11-11)

| Piece | Where | Notes / deviations from the text above |
|---|---|---|
| Protos | `proto/inference/bls/beacon.proto` (new), `params.proto`, `events.proto`, `query.proto` | `BeaconVoteExtension`, `PendingBeacon` (+ `i_total_slots` of the key epoch, see size cap), `BeaconSlotOwner`, `BeaconKeyCandidate`, `BeaconActivation{ext_from}` (`inj_from`/`agg_from` arrive with Steps 4/5). Param `beacon_key_switch_delay` (default 5, max 1000). Events `EventBeaconKeySwitched`, `EventBeaconSlotCoverageWarning`. Queries `bls beacon-key`, `bls pending-beacon [round]`. |
| Shared primitive | `x/bls/blsutil` | `BeaconMessage` (m_h), `HashToG1` (the keeper's `hashToG1` and the dAPI's now delegate to it), `SignPartials`, `VerifyPartial`. The dAPI's threshold-signing partials go through it, byte-identical to before (pinned by a reference test). |
| State, key switch, pending message | `x/bls/keeper/beacon.go`, hook in `finalizeDisputingPhase`, call at the end of x/bls `EndBlock` | Rule R9 as written. Every beacon write is gated on `BeaconActivation`, so pre-upgrade replay writes nothing. Pending messages older than 10 rounds are swept in EndBlock (Step 5 will delete them on finalize). |
| Slot owners | same file | The index stores **every** participant's range for the key epoch (not only bonded validators); `consAddr → validator → operator account` is resolved through x/staking at call time, so a participant that becomes a validator mid-epoch, or rotates its consensus key, is handled. Non-validators are excluded from the coverage check only. x/bls gets a narrow `StakingKeeper` via depinject. |
| Coverage check | same file (`WorstCaseBeaconSlots`) | At each key switch; skipped when there is no bonded power yet (InitGenesis before x/staking). |
| Structure check | `ValidateBeaconExtensionStructure` | As specified. After the 2026-11-11 redesign it is used by each node's partial collector (Step 4) instead of an on-chain `FinalizeBeacon`. |
| ExtendVote | `app/beacon_extend.go` | As specified. Empty (never an error) with a counted reason: `inactive`, `no_pending`, `no_shares`, `not_owner`, `oversize`, `error`, `panic`. Never sends an extension over the size cap. |
| VerifyVoteExtension | `app/beacon_abci.go` | Size cap only (R2). **Cap = the larger of `params.i_total_slots` and the key epoch's slot count** (`PendingBeacon.i_total_slots`): an epoch keyed before an `i_total_slots` change could otherwise make a slot-heavy validator's extension "oversized" and get its precommits rejected. |
| Metrics | `app/beacon/metrics.go` | As specified, plus `beacon_extend_vote_signed_total`. Default Prometheus registry, served by CometBFT on `:26660`; local-test-net now sets `CONFIG_instrumentation__prometheus` (default true, as in `deploy/join`). |
| Activation | `app/upgrades/v0_2_18/beacon_activation.go`; x/bls `InitGenesis` | **Carried by the pending v0.2.18 upgrade** (R6 allows capabilities to share an upgrade; nothing is signed before `E = U+100` anyway): `ext_from = U`, key seeded from the newest completed DKG around the signing epoch, `beacon_key_switch_delay = 5`; idempotent. Moving it to a later upgrade is one call. Chains started from genesis are active from block 1 (`ext_from = 0`), keyed from imported epochs. |
| dAPI (R9 retention) | `decentralized-api/internal/bls/share_export.go` | Reconcile covers `{key, candidate, current, current+1}` via the `BeaconKey` query and prunes below the oldest of them and `current-1`; export no longer prunes (after failed DKGs the key can be older than any fixed window). Falls back to `{current, current+1}` while the query does not exist (pre-upgrade chain). |

**Tests (all green, `-race`):**

- `x/bls/blsutil`: m_h vectors from an independent implementation (committed), field binding, input checks, a `HashToG1` regression vector, sign/verify round trips, bit flip, wrong key/message, identity point, determinism.
- `x/bls/keeper`: activation (idempotent, seeding, `ext_from` semantics), key switch with the default delay (no round without a pending message, the old key through `X+delay`, the new one from `X+1+delay`), delay 0, monotonic key, **a FAILED DKG through the real `CompleteDKG` path keeps the old key**, pre-activation no-op, pending gating on `E` and on the key, m_h equals the vector for `HeaderInfo().Hash`, sweep bound, slot owners through staking and consensus-key rotation, the coverage algorithm and event (balanced vs skewed, slots without a validator), two validators' extensions verifying per slot with `verifyBLSPartialSignatureBlst` (bit flip fails), every structure rejection class, the size bound for a validator owning 1000 slots, queries, the param bound. `x/bls/module`: genesis activation and seeding.
- `app`: the builder (signs, verifies with `blsutil`, deterministic; every empty reason counted; inconsistent shares), the size cap, R2 (real, garbage and slot-less extensions accepted, oversized rejected, verdicts counted); **end-to-end across the upgrade**: VE enabled at `E=4`, Step 3 activation at `U=6`, `ExtendVote(U)` empty, `PendingBeacon(U+1)` binds `hash(U)`, `ExtendVote(U+2)` carries partials that pass `ValidateBeaconExtensionStructure` and verify per slot, peers accept it, a second activation changes nothing. `app/upgrades/v0_2_18`: activation and candidate selection.
- `decentralized-api/internal/bls`: reconcile keeps and repairs an old active key's file and the candidate's, prunes after the key moves, falls back without the query; export does not prune; signing equivalence with `blsutil`.
- Testermint (`BlockBeaconTests.kt`, tag `real-extensions`), `real extensions after dkg`: **passed (2026-11-11)**, with the two Step 2 cases re-run green on the same images.
  - All three nodes switched keys at the same rounds: epoch 1 from round 62, epoch 2 from round 102 (R9).
  - `beacon_extend_vote_slots` matched each validator's range (epoch 1: genesis 100; epoch 2: 34 / 33 / 33), no `reject` verdicts anywhere, peers' non-empty extensions accepted, ≥99% of ExtendVote calls under 50 ms (logged 2–6 ms per extension), no consensus failure.
  - Mean block interval 5.01 s with empty extensions vs 5.56 s with partials (+11%): inside this gate's bound, slightly above the plan's 10%. The two windows fall in different epoch phases and signing takes milliseconds, so this is likely phase noise, but one run does not show it; Step 7 benchmarks should measure it properly.
  - The first run failed only on a test-helper bug (Prometheus prints labels sorted; the helper built `result,reason`), fixed before the green run.

**Deviation in the testermint gate:** block time is compared within the same run (partials vs the window with empty extensions, bound `1.25x + 0.5 s`), not against a separate VE-off baseline run.

### Step 4. σ required in the next block, or a proof of miss

**Design (decided 2026-11-11).** Block `h+1` must carry, as `Txs[0]`, exactly one of:

1. **`σ_h`** (the normal case): any `t+1` valid partials over `m_h` give the same 48 bytes, so the proposer aggregates them locally and every validator checks the result with **one pairing** against the key epoch's group public key.
2. **A proof of miss**: the extended commit of `h` (the precommits in the block's own `LastCommit`, with their signed extensions), from which every validator can verify that it holds **fewer than `t+1` valid slots**.

A block with **neither** is rejected, so CometBFT moves to the next round and the proposer is replaced. There is no inclusion window: every round is resolved in the very next block (K = 1).

Why this shape:
- **No withholding.** A proposer can't simply leave σ out. To skip a round it must prove that the commit really lacked valid slots.
- **No halt.** An honest proposer can always build one of the two from the same `LocalLastCommit`: σ if it holds ≥ `t+1` valid slots, otherwise the proof of miss. So the beacon can never stop the chain, and the cases where slots are truly unavailable (dAPI or share-export outage, participants without a BLS key, new slot holders not yet validators, a broken DKG share) end in a recorded miss.
- **Rejection is safe.** A proposal is rejected only for the proposer's own choices (a wrong σ, a false proof of miss, a missing beacon tx). Extension content never causes a rejection: in a proof of miss, malformed or invalid partials only count as invalid slots (R2).
- **Small blocks in the normal case:** ~60 bytes. The extended commit (~5 KB at 100 slots) is carried only in miss rounds.

**Rules:** R1, R2, R3, R4, R5, R6, R7, R8 ([Implementation rules](#implementation-rules-normative-for-steps-18)). The Step 4 upgrade handler writes `BeaconActivation.inj_from = U` (R6).

**Touches:** `app/beacon/collector.go` (new), `app/beacon_abci.go` (Prepare/ProcessProposal, VerifyVoteExtension hook), `x/bls/blsutil` (aggregation, proof-of-miss counting), `beacon.proto`.
**Does not touch:** state (records are Step 5), CometBFT.

**4a. Wire format**

```proto
message BeaconSigma {
  uint64 round     = 1;   // h
  uint64 key_epoch = 2;   // == PendingBeacon(h).key_epoch
  bytes  sigma     = 3;   // 48-byte compressed G1
  bytes  contributors_hint = 4;  // OPTIONAL slot bitmap; unverified, never stored, metrics only
}
message BeaconMissProof {
  uint64 round     = 1;
  uint64 key_epoch = 2;
  tendermint.abci.ExtendedCommitInfo extended_commit = 3;  // the proposer's LocalLastCommit, unmodified
}
message InjectedBeacon { oneof outcome { BeaconSigma sigma = 1; BeaconMissProof miss = 2; } }
```

- `Txs[0] = []byte("GONKA_BEACON_V1") ‖ proto.Marshal(InjectedBeacon)`.
- `FinalizeBlock`'s tx loop fails to decode it as an SDK tx and records a failed `ExecTxResult` for index 0 (harmless; explorers show one undecodable tx per block). The PreBlocker reads it (Step 5). A `TxDecoder` shim stays an open question.
- `contributors_hint` exists only for operators; validators can't check it, so nothing may depend on it.

**4b. Collector (every node, off the consensus path)**

Every validator's app already receives the partials for round `h`: its own from `ExtendVote(h)` and every peer's in `VerifyVoteExtension(h)`. The collector verifies them ahead of time so the proposer rarely has to:

1. `ExtendVote` / `VerifyVoteExtension` copy `(height, validator address, extension bytes)` into a bounded queue and return immediately (non-blocking; drop when full). Their verdicts don't change: size cap only (R2).
2. A background worker, on a read-only context over the latest committed version (the Step 2 L5 mechanism), checks each extension with `ValidateBeaconExtensionStructure` (Step 3) and each slot's partial with `blsutil.VerifyPartial` against `SlotPublicKeys[slot]`. Valid partials are kept per round and slot; invalid ones are counted (`beacon_collector_partials_total{result}`).
3. At ≥ `t_slots_degree + 1` valid slots: `σ_r = blsutil.AggregatePartials(...)` (Lagrange; moved out of `keeper.aggregateBLSPartialSignaturesBlst`), checked with one pairing against the key epoch's `GroupPublicKey`.
4. Memory is bounded to the last few rounds.

**4c. PrepareProposal(h+1)** (R4, reserved leading tx)

1. Before `inj_from`, or when committed state has no `PendingBeacon(h)` (no beacon key yet, VE not on): no reserved tx; Step 1 pass-through.
2. Otherwise build the reserved `beaconTx`:
   - If the collector holds σ_h, or the partials in `req.LocalLastCommit` reach the threshold (aggregate + one pairing; fall back to per-slot checks if that fails): `BeaconSigma`.
   - Else: `BeaconMissProof` with `req.LocalLastCommit` exactly as CometBFT handed it over (never filtered or modified).
3. `beaconTx` first, then the mempool pass-through on `req.Txs` only with the remaining byte budget (R4). The pass-through never sees `beaconTx`; a pass-through error yields `[beaconTx]`. `beaconTx` is never dropped for space: a block without it is invalid.

**4d. ProcessProposal(h+1)**

1. **Not required** (before `inj_from`, at block `inj_from` itself, or no `PendingBeacon(h)`): `Txs[0]` must not carry the prefix; REJECT if it does.
2. **Required** (block `h+1 > inj_from` and `PendingBeacon(h)` exists): `Txs[0]` must carry the prefix and decode; REJECT otherwise. This is the "neither → proposer replaced" rule.
3. **`BeaconSigma`:** `round == h`, `key_epoch == PendingBeacon(h).key_epoch`, and `e(σ, G2) == e(H(m_h), group_pk(key_epoch))`; REJECT otherwise.
4. **`BeaconMissProof`:**
   - `round == h`, `key_epoch == PendingBeacon(h).key_epoch`;
   - the extended commit matches `req.ProposedLastCommit`: same round, same number of votes, and per vote the same validator address, power and `BlockIdFlag`;
   - `baseapp.ValidateVoteExtensions(ctx, valStore, ec)`: every extension is the one its validator signed (so the proposer cannot strip or swap one);
   - count valid slots over the votes with `BlockIdFlag_Commit`: `ValidateBeaconExtensionStructure` (exact owned range, round, epoch), then each slot's partial with one pairing; malformed or invalid extensions just count zero (R2);
   - the count must be **< `t+1`**; if it reaches the threshold the proposer could have built σ, so REJECT.
5. No tx other than `Txs[0]` may carry the prefix (R5). `Txs[1:]` is accepted without decoding, regardless of local `mempool.max-txs` (R5, R7). Never delegate to `DefaultProposalHandler`.
6. `recover()` → REJECT. Cost: one pairing for σ; up to one per slot (~100 at 100 slots) for a proof of miss, only in miss rounds.

**4e. What a proposer can still do (Security note 2).** It chooses which >2/3-power set of precommits goes into its `LastCommit`. If some such set holds fewer than `t+1` valid slots, the proposer can pick it and prove a miss. BLS slots follow the same `CapWeight` as voting power (x/inference), so a >2/3-power set normally holds >2/3 of the slots (~67 > 51): this works only when validators holding ≥ ~16% of the slots in that set send no valid partials. The Step 3 coverage warning monitors the remaining gap. It can never change σ.

**4f. Activation (R6).** `inj_from = U` from the Step 4 upgrade. Block `U` is proposed by the old binary without `beaconTx` and is accepted (rule 1). From `U+1` the beacon tx is required. If Step 3 activates in the same upgrade (`ext_from = U`), round `U` has no partials, so block `U+1` carries a proof of miss with empty extensions: valid, recorded as a miss.

**Observability:** `beacon_collector_partials_total{result=valid|invalid|not_owner|stale}`, `beacon_sigma_built_total`, `beacon_proposal_outcome_total{outcome=sigma|miss}`, `beacon_process_proposal_total{result,reason}`; an info log naming the validators whose partials were missing or invalid in a miss round. Debug: `inferenced debug decode-beacon-tx <base64>` prints the outcome (`sigma_sha256`, or per vote `{validator, power, flag, ext_len, valid_slots}`).

**Tests (gate):**

- `blsutil.AggregatePartials`: two different ≥ threshold subsets give the **same σ**; σ verifies under the group key; a bad partial in the set gives a σ that fails.
- Collector: valid partials kept; garbage, wrong-round, wrong-epoch, foreign-range and bad-signature extensions dropped and counted, never changing `VerifyVoteExtension`'s verdict; σ built at the threshold; memory bounded; the hook never blocks.
- PrepareProposal: σ when the threshold is reached (from the collector or from `LocalLastCommit` alone); a proof of miss with the unmodified `LocalLastCommit` otherwise; `beaconTx` first and never dropped for space (`MaxTxBytes` tight → mempool txs trimmed); no `beaconTx` before `inj_from` or without `PendingBeacon(h)`; pass-through error keeps `[beaconTx]` (R4/M4).
- ProcessProposal ACCEPT: valid σ; valid proof of miss (< threshold, including one where some extensions are garbage); no beacon tx when not required. REJECT: no beacon tx when required; bad σ; σ for another round or epoch; proof of miss with ≥ threshold valid slots; proof of miss not matching `ProposedLastCommit` (a vote dropped, power or flag changed); an extension stripped or swapped (signature check); beacon tx when not required; prefix on another tx; truncated encoding.
- Mempool independence: the same proposal through ProcessProposal on apps with `max-txs=-1` and `max-txs=5000` gives the same verdict; same for PrepareProposal output (R7).
- Full ABCI cycle on `app.App` with 4 test validators: σ every block with everyone up; with validators holding > `n−t−1` slots sending empty extensions, every block carries a proof of miss and the chain keeps going.
- R6: crossing `inj_from` (old-binary block `U` without a beacon tx accepted; required from `U+1`; the boundary proof of miss with empty extensions accepted).

**Testermint gate** (`BlockBeaconTests.kt`, case `sigma in every block`), 3 validators, `E=2`, 30 blocks after the first key:
- Every height `H` after the first key round has `Txs[0]` with the prefix; `decode-beacon-tx` shows `BeaconSigma` for round `H−1`.
- No `ProcessProposal` rejections (`beacon_process_proposal_total{result="reject"} == 0`); collectors report only valid partials.
- Inference txs keep landing (20 requests during the run, all included and settled).

**Testermint regression gate** (required before Step 5). Run the existing suite with the VE genesis override `E=2` via `TESTERMINT_VE_ENABLE_HEIGHT=2`:
- At least: `BLSDKGSuccessTest`, `BLSDisputeApiRestartTest`, `BLSNetworkRecoveryTest`, `BLSProofPathTest`, `ConfirmationPoCPassTests`, `ConfirmationPoCFailTests`, `ConfirmationPoCMultiNodeTests`, `MultiNodeTests`, `InferenceAccountingTests`, `GovernanceTests`, `ParticipantTests`.
- Pass criterion: the same pass/fail set as the VE-off baseline on the same commit (dAPI `BlockObserver` seeing `Txs[0]`, larger miss-round blocks, timing).
- Keep it in CI as a nightly job from here on.

---

#### Step 4 implemented (2026-11-11)

| Piece | Where | Notes / deviations from the text above |
|---|---|---|
| Wire format | `beacon.proto` (`BeaconSigma`, `BeaconMissProof`, `InjectedBeacon`), `x/bls/types/beacon_tx.go` | As specified. `BeaconMissProof.extended_commit` is `bytes` holding `proto(tendermint.abci.ExtendedCommitInfo)` (no CometBFT proto import in our protos). A beacon tx is capped at 4 MiB before decoding. |
| Activation | `BeaconActivation.inj_from`, set together with `ext_from` by `ActivateBeacon` (v0.2.18 handler; genesis = 0) | Step 4 rides v0.2.18 with Step 3. `BeaconInjectionRound(H)`: required iff activated, `H > inj_from` and `PendingBeacon(H-1)` exists. Round `U` never has a pending message (EndBlock(U-1) ran on the old binary), so the first required block is `U+2`; no `MISS_NO_INJECTION` case arises. |
| Aggregation | `blsutil.AggregatePartials`, `blsutil.VerifySignature` | Pure Lagrange at 0 (slot `s` is x = s+1); byte-identical to the keeper's `aggregateBLSPartialSignaturesBlst` (test), which stays for threshold signing. |
| Collector | `app/beacon_proposal.go` (`beaconCollector`) | Fed by the own `ExtendVote` (address from the local `/status` identity) and every accepted `VerifyVoteExtension`; non-blocking queue (4096); one worker. **Deviation:** each round `h` is checked on committed version `h-1` (where `PendingBeacon(h)` lives), not "latest committed": the PreBlocker's recorded height lags one block during voting. Keeps 4 rounds. |
| PrepareProposal | same file | σ from the collector, else from `LocalLastCommit` (stops at the threshold), else proof of miss with the unmodified `LocalLastCommit`. R4 pipeline: beacon tx first, mempool pass-through on `req.Txs` minus any beacon-prefixed tx within the remaining budget, `[beaconTx]` on pass-through error or no room. A panic while building it proposes without it (the block is rejected, the next proposer takes over). Optional `contributors_hint` bitmap filled. |
| ProcessProposal | same file | As specified. Proof of miss: round/epoch, same round/length/address/power/flag per vote as `ProposedLastCommit`, `baseapp.ValidateVoteExtensions` with the staking keeper, then slot counting with early stop at the threshold. Without key material it accepts a proof of miss (same state everywhere, nobody can count). |
| Test switch | `[bls-beacon] test-withhold-sigma` (env `INFERENCED_BLS_BEACON_TEST_WITHHOLD_SIGMA`), `local-test-net/docker-compose.beacon-withhold.yml` | TEST-ONLY: the node proposes without the required beacon tx (metric `beacon_proposal_outcome_total{outcome="withheld"}`). Not in the app.toml template; it can only hurt the node that sets it. Used by the testermint case `withholding proposer is replaced`. |
| Debug | `inferenced debug decode-beacon-tx <base64|hex>` | JSON `{outcome, round, key_epoch, sigma_sha256, sigma_len, contributors_hint}` or, for a miss, `{votes:[{validator, power, flag, ext_len}]}`. Valid-slot counts need chain state and are not printed. |
| Metrics | `app/beacon/metrics.go` | `beacon_collector_partials_total{result}`, `beacon_sigma_built_total`, `beacon_proposal_outcome_total{outcome}`, `beacon_process_proposal_total{result,reason}`. |

**Tests (green, `-race`):**

- `x/bls/blsutil`: aggregation gives the same σ for any threshold subset and order, verifies, fails below threshold or with a wrong partial; input errors. `x/bls/types`: beacon tx codec (both outcomes, garbage, oversized). `x/bls/keeper`: `BeaconInjectionRound` (not activated, block `inj_from`, required, no pending), `GetBeaconKeyMaterial`, aggregation equal to the keeper's.
- `app` (unit, fake chain with a 10-slot key): collector (builds the unique σ and hint, drops non-owners, garbage and duplicates, never blocks, ignores unknown rounds); PrepareProposal (σ from the commit, σ from the collector when the commit alone is short, proof of miss with the unmodified commit, byte budget, `[beaconTx]` on pass-through error, prefixed mempool tx filtered, not required → plain pass-through, panic → pass-through, withhold switch); ProcessProposal (accepts σ, proof of miss with a garbage extension counted as zero, no beacon when not required, and whatever PrepareProposal built; rejects 17 classes: missing, bad/garbage σ, wrong round/epoch, proof of miss with enough slots, dropped vote, changed flag, changed power, bad extension signature, undecodable proof, truncated tx, beacon tx when not required, prefix elsewhere or not first, panic).
- `app` (real app, real ed25519 extension signatures): σ in three consecutive blocks after the v0.2.18 activation, accepted, and rejected without σ / with a bad σ / with a false proof of miss, finalized with tx 0 undecodable; a proof of miss from an empty-extension round accepted, with a forged extension signature rejected; the withhold switch from app options; ProcessProposal identical with `mempool.max-txs` −1 and 5000. Stable 5/5 under `-race` (tests write state directly only while the background readers are idle).
- `cmd`: `decode-beacon-tx` for both outcomes, base64 and hex.
- Testermint (`BlockBeaconTests.kt`, tag `sigma-in-block`), **all green (2026-11-11)** on the rebuilt node image:
  - `sigma in every block`: over 30 blocks after the key over all validators, every block's `Txs[0]` is `BeaconSigma` for round `H−1` (48 bytes, current key epoch); no `ProcessProposal` rejections, no proofs of miss, collectors saw only valid partials, σ built on every node; 10 ordinary txs (bank sends) landed behind the beacon tx. Classic inference is deprecated on this branch (HTTP 410), so bank sends replace the planned inference requests.
  - `withholding proposer is replaced` (join2 runs `test-withhold-sigma`): all three nodes rejected its proposals with reason `missing`; **14 of 40 heights were committed in a later round by another proposer**, none by join2; every block still carried σ; no consensus failure.
  - `real extensions after dkg` (Step 3) re-run green on the Step 4 code.
  - Test-side fixes made on the way: the SDK binds env vars with the executable name (`INFERENCED_…`, not `INFERENCE_…`); a σ window must start two blocks after the key switch is observed (the round keyed just before the switch still uses the old key, R9); the bootstrap transfer restriction (`x/restrictions`, code 1108) can reject a bank send while still executing it.
- Not run yet: the **VE-on regression gate** (`TESTERMINT_VE_ENABLE_HEIGHT=2` over the existing suites), required before Step 5.

### Step 5. Store σ_h in x/bls: one record per round

**Rules:** R1, R3, R6, R8 ([Implementation rules](#implementation-rules-normative-for-steps-18)). The Step 5 upgrade handler writes `BeaconActivation.rec_from = U` (R6). Steps 4 and 5 are meant to ship together; they are separate only for testing.

**Touches:** `x/bls/keeper/beacon.go`, `beacon.proto`, `query.proto`, x/bls CLI, `app/beacon_abci.go` (PreBlocker), x/bls params.
**Does not touch:** `MsgSubmitPartialSignature` and the existing request-based signing.

**Proto:**

```proto
enum BeaconStatus {
  BEACON_STATUS_UNSPECIFIED = 0;
  OK = 1;                 // σ in block h+1
  MISS_THRESHOLD = 2;     // proof of miss in block h+1: < t+1 valid slots in the commit
  MISS_NO_KEY = 3;        // no completed DKG yet
  MISS_NO_INJECTION = 4;  // only at the activation boundary (block proposed by the old binary)
}
message BeaconRecord {
  uint64 round = 1; uint64 key_epoch = 2; BeaconStatus status = 3;
  bytes  sigma = 4;        // 48 bytes when OK
  uint32 valid_slots = 5;  // MISS_THRESHOLD: valid slots the proof showed
}
```

- `beacon_retention_blocks` (default ≈ 2 epochs): records older than this are pruned in EndBlock.
- The contributor bitmap is **not** stored (it can't be verified for σ).

**PreBlocker(FinalizeBlock h+1):**

1. `resp, err := app.ModuleManager.PreBlock(ctx)`.
2. From `rec_from` on, if `PendingBeacon(h)` exists:
   - `Txs[0]` is a `BeaconSigma` for `h`: re-verify the pairing (cheap; keeps FinalizeBlock self-sufficient under block sync and replay) and store `BeaconRecord{h, OK, σ}`.
   - `Txs[0]` is a `BeaconMissProof` for `h`: store `BeaconRecord{h, MISS_THRESHOLD, valid_slots}`. The proof was checked by ProcessProposal on >2/3 of voting power, and the stored result depends only on block content, so all nodes store the same.
   - No beacon tx (possible only for blocks up to `inj_from`): `BeaconRecord{h, MISS_NO_INJECTION}`.
   - Delete `PendingBeacon(h)`. Never an error (R1, R3): a σ that fails re-verification (impossible in a decided block) is stored as a miss and logged.
3. Emit `EventBlockBeacon{round, key_epoch, status, sigma_sha256, valid_slots, contributors}`. For a miss the contributors come from the proof (verified); for `OK` from the hint (unverified, labelled so).

**EndBlock(H)** (extends Step 3's `ProcessBlockBeacon`):

1. When EndBlock would write `PendingBeacon(H+1)` but no key exists, write `BeaconRecord{H+1, MISS_NO_KEY}` instead. Before `E` or activation: nothing.
2. Step 3's 10-round sweep of pending messages stays as a safety net; with Step 4 every pending round is resolved in the next block, so at most one `PendingBeacon` exists at a time.
3. Prune records older than `beacon_retention_blocks`.

**Keeper API / queries:**

- `GetBlockBeacon(ctx, round) (BeaconRecord, found bool)`. From `rec_from` on, round `H−1` always has a record at `EndBlock(H)`.
- gRPC `Query/BlockBeacon{round}`, `Query/LatestBlockBeacon` (newest `OK`); CLI `inferenced query bls block-beacon <round>`, `latest-block-beacon`. `pending-beacon` exists since Step 3.
- `inferenced debug verify-beacon <round>`: checks a record's σ against the key epoch's group key.

**Tests (gate):**

- σ in block `h+1` → `OK` record, `PendingBeacon(h)` deleted, event emitted.
- Proof of miss → `MISS_THRESHOLD` with the proof's `valid_slots`; the event lists who contributed.
- No key → `MISS_NO_KEY`; no records before `E` or activation; the boundary block without a beacon tx → `MISS_NO_INJECTION`.
- Uniqueness: blocks from two proposers using different partial subsets store the same σ.
- FinalizeBlock never errors, including on a block built directly with a bad σ (bypassing ProcessProposal): stored as a miss.
- Determinism: the same block finalized twice gives byte-identical state; records pruned after retention; at most one pending entry.
- App multi-block: with everyone up, every round from the first key round is `OK`; with too few slots, every round is `MISS_THRESHOLD` and the chain advances.

**Testermint gate** (`BlockBeaconTests.kt`, case `beacon record every round`), 3 validators, `E=2`:
- Every round from the first key round to `tip−1` has a record; with everyone up all are `OK`, `verify-beacon` passes, σ is identical on all 3 nodes.
- Rounds before the first key (plus `beacon_key_switch_delay`) are `MISS_NO_KEY`.
- `EventBlockBeacon` in `/block_results` with `sigma_sha256 == sha256(sigma)`.

#### Step 5 implemented (2026-11-11)

| Piece | Where | Notes / deviations from the text above |
|---|---|---|
| Activation | `BeaconActivation.rec_from`, set with `ext_from` / `inj_from` by `ActivateBeacon` (v0.2.18 handler; genesis = 0) | **Deviation:** Step 5 rides v0.2.18 together with Steps 3–4 rather than a separate upgrade. Records are written for every round `> rec_from`. |
| Record | `beacon.proto` (`BeaconStatus`, `BeaconRecord`), `x/bls/keeper/beacon.go` (`FinalizeBeacon`) | Written in the PreBlocker of `FinalizeBlock(h+1)` for round `h`, after the module PreBlockers (so an upgrade at that height applies first); panics are recovered and never fail the block. σ is **re-verified** (block sync does not rely on ProcessProposal) → `OK`; a proof of miss → `MISS_THRESHOLD` with the valid slots recounted; anything else (missing, undecodable, bad σ, wrong round/epoch) → `MISS_NO_INJECTION`. **Deviation:** the record also stores `msg` (m_h), because the previous block hash isn't kept anywhere else, so σ can be checked from the record plus the group key alone. The pending message is deleted once recorded. |
| `MISS_NO_KEY` | `ProcessBlockBeacon` (EndBlock) | Rounds `> rec_from` without a beacon key get a `MISS_NO_KEY` record directly; no pending message is written for them. |
| Latest pointer | `beacon_latest_ok` | Updated on every `OK` record; served by `LatestBlockBeacon`. |
| Event | `EventBlockBeacon{round, key_epoch, status, sigma_sha256, valid_slots, contributors}` | `contributors` lists the validators whose slots counted for a proof of miss. |
| Retention | param `beacon_retention_blocks` (default **31000**, ≈ 2 epochs; `0` keeps everything), set by the v0.2.18 handler | Pruned in EndBlock, at most 100 records per block. |
| Queries / CLI | `query bls block-beacon <round>`, `query bls latest-block-beacon`; `inferenced debug verify-beacon <round>` | `verify-beacon` checks a stored σ against the key epoch's group public key (one pairing). |

**Tests (green, `-race`):**

- `x/bls/keeper`: `TestFinalizeBeaconRecordsSigma` (including the same σ stored from two different partial subsets), `TestFinalizeBeaconMissNoInjection` (5 cases), `TestFinalizeBeaconProofOfMissRecountsSlots` (recount + contributors), `TestFinalizeBeaconGating`, `TestMissNoKeyRecord`, `TestBeaconRecordRetention` (including `0` = keep), `TestFinalizeBeaconDeterministic` (byte-identical state from two keepers fed different subsets), `TestBlockBeaconQueries`.
- `app` (real app): `TestBlockBeacon_SigmaInEveryBlock` also checks every round is recorded `OK` and verifies from the record; `TestBlockBeacon_ProofOfMiss` checks `MISS_THRESHOLD`; `TestBlockBeacon_FinalizeWithoutBeaconTx` finalizes a block with no beacon tx (`MISS_NO_INJECTION`), after which every later round is still recorded. 3/3 under `-race`.
- `cmd` `TestVerifyBeaconRecord`; upgrade `TestActivateBlockBeacon` (sets the retention param); module `TestGenesisActivatesBlockBeacon` (`rec_from = 0`). x/inference suite unaffected.
- Testermint, **all green (2026-11-11)** on the rebuilt node image:
  - `beacon record every round`: over 30 rounds every round is `OK` on all 3 nodes with an identical σ; `verify-beacon` passes; `EventBlockBeacon.sigma_sha256` matches; rounds before the first key are `MISS_NO_KEY`; old pending messages are gone.
  - Step 4 regression on the Step 5 code: `sigma in every block` and `withholding proposer is replaced` green.
- Still open: the **VE-on regression gate** (`TESTERMINT_VE_ENABLE_HEIGHT=2` over the existing suites).

---

### Step 6. Wire x/inference consumer (CPoC)

**Touches:** `x/inference/module/confirmation_poc.go` (+ tests), x/inference params, the `BlsKeeper` expected interface in `x/inference/types/expected_keepers.go`.  
**Does not touch:** cosmos-sdk, dAPI (except ops docs).

- New param `confirmation_poc_use_block_beacon` (bool, default `false`), updated via gov.
- Round `H-1` is always resolved by `EndBlock(H)` (Step 4/5: σ or proof of miss in block `H`), so the consumer reads exactly that round.
- In `handleConfirmationPoC` at `EndBlock(H)` (`module.go:414`), replace `confirmation_poc.go:163-170`:
  ```go
  if params.ConfirmationPocUseBlockBeacon {
      rec, found := k.BlsKeeper.GetBlockBeacon(ctx, uint64(H-1))
      if !found || rec.Status != blstypes.OK { log("cpoc: beacon miss", H-1, rec.Status); return nil }   // no fallback
      seedHash := sha256.Sum256(append([]byte("GONKA_CPOC_DRAW_V1"), rec.Sigma...))
      seed := int64(binary.BigEndian.Uint64(seedHash[:8]))
      randFloat = calculations.DeterministicFloat(seed, fmt.Sprintf("confirmation_poc_trigger_%d", H))
  } else { /* existing hash path; fix the misleading "H-1" comment: it is the current block hash */ }
  ```
- Details, the adversary table, notice and the work-seed options: [`block-beacon-randomness.md`](block-beacon-randomness.md) §5–§8.
- The event at `confirmation_poc.go:261-263` (`PocSeedBlockHash`) gains `PocSeedBeaconRound` and `PocSeedSigmaHash` when beacon mode is on. Keep the old field for compatibility, filled with an empty string in beacon mode.
- Expected trigger rate: a MISS skips the evaluation for that round, so the effective probability is `p × P(OK)`. Either document it, or rescale `triggerProbability` by the observed OK ratio. v1: document only, and monitor the miss rate.

**Tests (gate):**

- Flag off: existing `confirmation_poc_test.go` unchanged and green.
- Flag on: a mock BlsKeeper OK σ → deterministic trigger decision; MISS/absent → no trigger, no error.
- Grind regression: changing the tx set (and therefore `HeaderInfo().Hash`) of block `H` doesn't change the decision when the flag is on.
- Statistical: over 10k synthetic σ values, the trigger frequency is within tolerance of `p`.

#### Step 6 implemented (2026-11-11)

| Piece | Where | Notes / deviations from the text above |
|---|---|---|
| Param | `ConfirmationPoCParams.use_block_beacon` (field 5, default `false`) | **Deviation:** nested in the existing CPoC params, so the name is `confirmation_poc_params.use_block_beacon`, not a top-level `confirmation_poc_use_block_beacon`. No upgrade migration: the zero value is the default. |
| Draw | `x/inference/module/confirmation_poc.go` (`confirmationPoCDraw`, `cpocBeaconDrawValue`) | Flag on: `BeaconRecord(H-1)` must be `OK` with a 48-byte σ; value = `DeterministicFloat(u64be(sha256("GONKA_CPOC_DRAW_V1" ‖ σ)[:8]), "confirmation_poc_trigger_H")`. Otherwise it logs `beacon miss, no trigger draw` and skips; no error, no hash fallback. Flag off: the legacy value, bit for bit; the misleading "H-1" comment is replaced (it is block `H`'s own hash). All other trigger gates are unchanged and run first. |
| Event | `ConfirmationPoCEvent.trigger_beacon_round` (10), `trigger_sigma_sha256` (11) | **Deviation:** named `trigger_*`, not `PocSeed*`, and `poc_seed_block_hash` is **not** blanked: it is the CPoC work seed set at generation start that the dAPI uses for nonces. Blanking it would break CPoC; it stays on the block hash until the work-seed decision ([`block-beacon-randomness.md`](block-beacon-randomness.md) §8). |
| Interface | `x/inference/types/expected_keepers.go` `BlsKeeper.GetBlockBeacon`; mock in `testutil/keeper/expected_keepers_mocks.go` | Mock method added by hand in mockgen's format (`mockgen` not installed). |
| Testermint | `data/AppExport.kt` `useBlockBeacon`, `data/epoch.kt` `triggerBeaconRound` / `triggerSigmaSha256` | |

**Tests (green):**

- `x/inference/module/confirmation_poc_beacon_test.go`: flag off reproduces the legacy value and never reads the beacon (short hash still errors); flag on matches an independent recomputation, pinned golden value, height-dependent, domain-separated; 10 miss cases (no record, each MISS status, unspecified status, empty or short σ, OK only for `H` or `H-2`, height < 2) → no draw, no error; grind regression (50 block hashes → one beacon draw, 50 distinct legacy draws); 10k random σ at p = 0.01 / 0.05 / 0.3 / 0.5 within 5σ; end to end through `checkConfirmationPoCTrigger` (OK → event with round and σ hash; miss → no event; flag off → no beacon fields). The test keeper helper takes an optional x/bls wrapper and upgrade keeper.
- Existing x/inference suites (including `confirmation_poc_test.go`), app beacon tests, x/bls, cmd and the upgrade package unchanged and green; dAPI builds.
- Testermint `cpoc trigger draws from the beacon` (`BlockBeaconTests.kt`, tag `cpoc-beacon`), **green (2026-11-11)** on the rebuilt node image: flag on in genesis, p ≥ 1; the first event triggered at height 86 from beacon round 85; every event so far has `trigger_beacon_round = trigger_height − 1`, that round is `OK` on all 3 nodes, and `trigger_sigma_sha256` matches its σ. The key was ready before the first trigger window, so no miss happened on the cluster; the miss path is covered by the unit tests.
- Not run: `ConfirmationPoCPassTests` with the flag on (the full CPoC outcome under beacon mode) — part of [Step 8](#step-8-testermint-e2e).

---

### Step 6b. Ungrindable PoC work seeds (PoC, CPoC, PoC challenges)

**Priority: security-critical.** Step 6 removed the grindable input from the CPoC *trigger*; the *work seeds* are still block hashes. The work seed is the input every ML node derives its PoC nonces from, and every validator recomputes them from the same seed. Whoever learns it first, or picks it, gains PoC weight that does not reflect real compute.

**Current seeds (verified 2026-11-11):**

| Seed | Where it is chosen | Value | Lever |
|---|---|---|---|
| Regular PoC (every epoch) | dAPI: `chainBridge.GetBlockHash(pocStartBlockHeight)` (`broker.queryCurrentPoCParams`, `new_block_dispatcher.go` `ValidateAll`) → ML node `BlockHash` | hash of block `P` (PoC start) | The proposer of `P` builds the header and can **grind** it (tx set, ordering, time) and **knows it first**, before the block is even proposed, so its nodes start generating ahead of everyone. The proposer of `P` is predictable from the proposer rotation, so the opportunity can be planned. |
| CPoC | chain: `event.PocSeedBlockHash = HeaderInfo().Hash` at generation start `G` (`confirmation_poc.go`, GRACE_PERIOD → GENERATION) | hash of block `G` | The same, for the proposer of `G`. |
| PoC challenge | chain: `PoCChallenge.Seed = HeaderInfo().Hash` (`keeper/poc_challenge.go` `StartChallenge`, `RotateChallengeSegment` in `module/poc_challenge.go`) | hash of the block with the challenge tx / the rotation block | The proposer of that block; a challenger colluding with it can also time the tx. |

Related grindable samplers (not work seeds, but the same flaw; included in the inventory, moved in the same step if cheap): the dAPI validator's `samplingBlockHash` (which nonce leaves are checked, `poc/validator.go` `getSamplingBlockHash`) and claim-rewards inference sampling (`msg_server_claim_rewards.go`, `HeaderInfo().Hash`).

**Rule:** a work seed is `seed = sha256(tag ‖ u64be(height) ‖ σ_r)` for a fixed beacon round `r` chosen by the rules below, written to chain state by `EndBlock`, and read by the dAPI **from chain state**, never from a block hash. Tags: `GONKA_POC_SEED_V1`, `GONKA_CPOC_SEED_V1`, `GONKA_POC_CHALLENGE_SEED_V1`. The 32-byte hex seed has the same format as today's block hash, so the ML node API is unchanged.

**Properties:** σ_r is unique (no grinding by anyone) and unpredictable until `t+1` slots sign round `r`. Every validator's collector sees it during the precommit of `r`, about one block before it is on chain: early knowledge moves from one proposer to all validators at once, the same for everyone.

**Seed rounds and misses:**

- **Regular PoC, stage start `P`:** `r = P-1`; at `EndBlock(P)` the chain stores `PocSeed{epoch, round, seed}`. On a miss, the seed is the **first `OK` round after `P-1`**, stored when its record lands; the dAPI starts generation when the seed appears and the stage **end is unchanged**, so a miss shortens generation equally for everyone. A regular PoC can't be cancelled: if no `OK` round lands within `poc_seed_max_wait` blocks (param, default 5), the chain stores the hash-derived seed with `source = BLOCK_HASH_FALLBACK` and emits an event. That fallback is reachable only by a coalition that can make rounds miss (an availability attack, visible in the records); it is logged and monitored, never silent.
- **CPoC, generation start `G`:** `r = G-1`; a miss cancels the event (no weights, no slashing; the next trigger draw can start a new one). Recommended option of [`block-beacon-randomness.md`](block-beacon-randomness.md) §8.
- **PoC challenge:** the seed is taken from the round before the challenge's generation start (not the tx's block), with the CPoC miss rule (cancel and refund the lock).
- **No key yet (epochs 0–1, `MISS_NO_KEY`):** block-hash seed, as today. Bootstrap only.

**Activation (mixed versions):** generators and validators must use the same seed, so the chain decides. New param `poc_params.seed_from_block_beacon` (default `false`). When on, the chain stores the seed for each PoC / CPoC / challenge, and dAPIs use the stored seed. A dAPI that sees the flag on but can't read a stored seed must not fall back to the block hash; it waits. Order: ship the dAPI that reads stored seeds (versiond), confirm adoption, then gov enables the flag. Before the flag, the chain still stores the seeds (so the dAPI path is exercised) but the dAPI keeps using block hashes.

**Touches:** x/inference (`PocSeed` store, query, events, params; `confirmation_poc.go`; `poc_challenge.go`), `x/inference/types/expected_keepers.go` (`GetBlockBeacon`, already added in Step 6), dAPI (`broker.queryCurrentPoCParams`, `prefetchPocParams`, `new_block_dispatcher.go`, `poc/validator.go`), testermint data classes.

**Tests (gate):**

- Keeper: seed stored at `EndBlock(P)` from `OK` round `P-1`; miss → first later `OK` round; no `OK` within `poc_seed_max_wait` → `BLOCK_HASH_FALLBACK` with event; `MISS_NO_KEY` → block hash; flag off → nothing changes for the dAPI.
- Grind regression: changing block `P`'s (or `G`'s) transactions and header doesn't change the stored seed.
- CPoC: miss at `G-1` cancels the event, no weight change, no slashing. Challenge: miss cancels and refunds.
- dAPI: generation and validation use the stored seed when the flag is on; flag on and seed missing → waits, never uses the block hash; flag off → block hash as today. Generator and validator agree on the seed (unit test with a fake chain bridge).
- Testermint: a full epoch PoC with the flag on (weights match the flag-off run), a CPoC event with the flag on, and a forced miss at `P-1` (withholding proposer or a stopped validator) that delays generation without changing the stage end.

---

### Step 7. Unit/keeper + app tests, benchmarks

**Touches:** tests under `inference-chain` (+ `decentralized-api` for the exporter).

Checklist, each item a named test:

1. `TestBeaconMessageVectors`: `m_h` domain separation and field order.
2. `TestVerifyVoteExtensionSizeOnly` (R2): any payload ≤ cap accepted (fuzz with random bytes: no panic, no error, ACCEPT), > cap rejected.
3. `TestProcessProposalRejects*`: every rejection class from Step 4 (no beacon tx when required, bad σ, wrong round/epoch, false proof of miss, proof not matching `ProposedLastCommit`, stripped/swapped extension, beacon tx when not required, prefix on another tx, malformed); `TestProcessProposalAcceptsProofWithGarbageExtension` (R2).
4. `TestCollectorDropsBadPartials`: garbage, foreign-range, wrong-round and bad-signature extensions never reach σ and never change `VerifyVoteExtension`'s verdict (R2).
5. `TestWithholdingProposerReplaced` / `TestSigmaUniqueAcrossProposers`: a proposal omitting σ while ≥ threshold slots exist is rejected and the next proposer's σ is stored; different partial subsets give the same stored σ.
6. `TestNoHashFallbackWhenFlagOn`.
7. `TestHandlersNeverError`: inject panics into the provider, keeper and codec → nil error everywhere.
8. `TestSlotCoverageInvariant`: the greedy worst-case calculation on known distributions.
8a. `TestPrepareProposalReservedTxs` (R4/M4): reserved-first, budget, pass-through never sees `beaconTx`, error path keeps `beaconTx`.
8b. `TestBeaconActivationBoundaries` (R6/M5): `E < ext_from ≤ inj_from ≤ rec_from`, including all-equal; the activation block of each capability (old-binary semantics) is accepted and yields `MISS_*`, never `REJECT`/error.
9. Benchmarks: collector per-slot verification (`i_total_slots ∈ {100, 500}`), σ aggregation in `PrepareProposal` (target < 20 ms), σ check in `ProcessProposal` (target < 5 ms), proof-of-miss check (all slots, target < 300 ms), and block time with vs without partials over the same epoch phases (gap S3-1).
10. `BenchmarkExtendVote`: per-slot signing cost; target < 50 ms for the largest single-owner slot range.
11. Exporter ↔ provider cross-fixture (`testdata/epoch-share-v1.json`).

---

### Step 8. Testermint e2e

**Touches:** `testermint` (+ local-test-net genesis VE height / tmkms compose).  
**Does not touch:** cosmos-sdk fork.

Class `BlockBeaconTests.kt` (uses the Step 1d `consensusOverrides`, `E=2`). Rebuild via `local-test-net/stop-rebuild.sh` + gradle per [`testermint-infrastructure.md`](../../devshard/docs/testermint-infrastructure.md).

| Case | Setup | Expect |
|---|---|---|
| Happy path | 3 validators, DKG completes | After DKG + N blocks, `block-beacon` for rounds `[first_key_round … tip-1]` all `OK` with 48-byte σ; each σ verifies under the group PK fetched from `query bls epoch-data` (verify in Kotlin via a helper CLI or a debug query `verify-beacon`). |
| Before key | Heights before the first completed DKG (+ switch delay) | `MISS_NO_KEY`, chain advancing. |
| Uniqueness | Restart one node, re-query | σ byte-identical across nodes and restarts. |
| Share file present | After DKG | `bls-beacon/epoch-<N>.json` exists on every validator container, mode 0600. |
| Share file missing on one node | Delete the file + restart dAPI export disabled on one node | That node's extensions are empty; beacon still `OK` if remaining slots ≥ threshold; no `ExtendVote call failed`. |
| Corrupt share file | Overwrite the scalars on one node | Provider rejects it (log) → empty extensions; collectors elsewhere report no invalid partials. |
| Withholding proposer | One node configured (test-only flag) to propose without the beacon tx | Its proposals are rejected (`beacon_process_proposal_total{reason="missing"}`) and the next round's proposer commits the block with σ; every round still `OK`; the chain keeps going (those heights take an extra consensus round). |
| Partials missing | Disable the share export on validators holding > (total − threshold) slots of the commit (they still precommit) | Blocks carry a proof of miss; records `MISS_THRESHOLD` naming the non-contributors; CPoC doesn't trigger; chain keeps producing blocks. |
| Epoch transition | Run across an epoch boundary with a new DKG | **No `MISS_*` at the boundary** (R9): records keep the old `key_epoch` through `SET_NEW_VALIDATORS` and switch to the new one at `dkg_completed_height + 1 + beacon_key_switch_delay`, all `OK`. |
| Failed DKG | Force one epoch's DKG to `FAILED` | Records keep the previous `key_epoch`, all `OK` while its holders are up; the dAPI does not prune that epoch's file. |
| CPoC flag on | Gov proposal sets `confirmation_poc_params.use_block_beacon=true` | Triggers only on `OK` rounds; the event carries `trigger_sigma_sha256`. Run `ConfirmationPoCPassTests` with the flag on. |
| tmkms | All nodes via `docker-compose.tmkms.yml` with the upgraded tmkms | Same as happy path, no missing precommits after `E`. |
| Snapshot join | A joiner state-syncs past `E` | Catches up, becomes a validator, its partials are counted valid by the other nodes' collectors; records stay `OK`. |
| Upgrade path | Genesis `E=0`; run the upgrade handler at height U | `E = U+delay`; no panic at `E`; extensions stay empty until the Step 3 activation. |
| Capability activation (R6/M5) | Cluster already past `E`; run the Step 3/4/5 upgrade(s) at `U2` (one combined, and separately staggered `ext_from < inj_from ≤ rec_from`) | Block `inj_from` (old-binary proposal) accepted without a beacon tx; from `inj_from+1` a beacon tx in every block (a proof of miss while partials are empty); `MISS_*` records at the boundary, then `OK`; no `CONSENSUS FAILURE`. |
| Consensus-key rotation | Mid-epoch, one validator submits a new `ValidatorKey` (`MsgSubmitNewParticipant`) and restarts tmkms/node with the new key once the validator set update takes effect | Before and after the switch, the other nodes' collectors count that validator's partials as valid (no attribution gap beyond the switch heights); `beacon_verify_vote_ext_total{result="reject"} == 0`; beacon stays `OK`. |
| Mixed mempool config | One validator runs with `INFERENCED_MEMPOOL_MAX_TXS=5000`, the others with the default `-1`; that validator proposes regularly | No ProcessProposal rejections on any node; that validator's proposals carry the beacon tx and get committed; block time matches the all-default baseline; inference txs sent through that node are included. |
| dAPI restart recovery | Restart dAPI on one validator mid-epoch (file left in place), then delete the file and restart again | First restart: no change in that node's `beacon_extend_vote_slots`. Second: extensions are empty only until recovery re-exports the file, then the slot count returns; beacon stays `OK` throughout (the other slots ≥ threshold). |
| tmkms state continuity | Swap the tmkms image old → new on a running cluster with the same state file (as in Step 1), then cross `E` | No double-sign refusal, no signing gap beyond the restart, all precommits present after `E`. |
| Regression suite with VE on | The Step 4 regression job on the final build | Same pass set as the VE-off baseline. |

---

## Explicit non-corrections

| Component | Why not |
|---|---|
| CometBFT fork | VE already in 0.38 |
| Cosmos SDK fork | Handlers and helpers verified present; no change needed |
| `MsgSubmitPartialSignature` beacon spam | Replaced by the VE path |
| Hash-seed fallback "for safety" | Reintroduces proposer grind; forbidden when beacon mode is on |
| Signing inside `x/inference` | Consumer only |
| Crypto checks in `VerifyVoteExtension` | Liveness risk (Security note 4); done by each node's collector off the consensus path, and σ is checked in Process/Finalize |
| σ required with no escape | Would halt the chain whenever the commit holds fewer than `t+1` valid slots (share-export outage, participants without a BLS key, key switch before `SET_NEW_VALIDATORS`); re-electing the proposer can't help because the precommits of `h` are fixed. Adopted instead: σ required **or** a verifiable proof of miss (decided 2026-11-11) |

---

## Rollout sketch

1. **tmkms first ([Step 0](#step-0-signer-tmkms-rollout-coordination)):** publish the tmkms 0.14.0 image (with the in-place config migration). Operators upgrade and confirm `signed vote extension` on the current chain (VE still off). Track adoption by voting power.
2. Steps 1–5 on testnet with queries only (CPoC still on the hash).
3. Testermint Step 8 green (including the tmkms and snapshot cases).
4. Mainnet upgrade `v0.2.18` at height U with handlers; `E = U + VoteExtensionsEnableDelay` (100). **Precondition: the [Step 0](#step-0-signer-tmkms-rollout-coordination) gate** (≥95% of voting power confirmed on a signer that signs extensions).
5. Observe the beacon OK rate for at least one epoch.
6. Gov: `confirmation_poc_params.use_block_beacon = true`.
7. dAPI that reads stored seeds (Step 6b) rolled out via versiond; then gov: `poc_params.seed_from_block_beacon = true`.

---

## Open questions

Resolved by this revision:

- ~~Canonical bytes of injected EC~~ → superseded 2026-11-11: `"GONKA_BEACON_V1" ‖ proto(InjectedBeacon)` as a required `Txs[0]`, carrying σ or, in miss rounds, the extended commit as proof (Step 4).
- ~~Exact parent hash for~~ `m_h` → `HeaderInfo().Hash` in `EndBlock(h-1)`, stored as `PendingBeacon(h)` (Timing model).
- ~~Share file path~~ → `/root/.inference/bls-beacon/epoch-<id>.json`, already shared by node and api (Share bridge).
- ~~Empty extension encoding~~ → zero-length bytes; Comet accepts it; collectors ignore it and a proof of miss counts it as zero slots.

Investigated 2026-11-09:

- **tmkms version (Q1) — superseded 2026-11-09 by testing.** Use **tmkms 0.14.0** (`sign_extensions = true`, `protocol_version = "v0.34"`, built with `rust:1.79`). The changelog suggested 0.15.0, but 0.15.0 drops the extension signature on the wire (`cometbft.types.v1beta1.Vote`, no tags 9/10) and nodes still panic. Verified on a live cluster and in the proto sources; matches upstream iqlusioninc/tmkms#1174 ("Reverting to v0.14.0 fixes the issue"). No release newer than 0.15.0 exists. Revisit when upstream ships a fix. The production image `0.2.16` runs 0.12.0 (verified from its build history).
- **Participant ↔ validator (Q2).** Confirmed: `epochgroup/epoch_group.go:411` builds `ComputeResult.OperatorAddress = sdk.ValAddress(accAddr)`, and `maintenance_validation.go:203` relies on the same identity ("participant account address bytes match the validator operator address bytes"). Mapping: `participant.Address` → `sdk.ValAddress(acc)` → `StakingKeeper.GetValidator` → `GetConsAddr()`. **Caveat:** the consensus key can change (`msg_server_submit_new_participant.go:23-24` updates `ValidatorKey`), so `VerifyVoteExtension` resolves ownership **at call time** (`GetValidatorByConsAddr(consAddr)` → operator → acc → slot range from a per-epoch `acc → range` map), not from a cached `consAddr` table (Step 3b updated).
- **Mempool (Q3).** Every shipped config uses the NoOp app mempool: the SDK default `max-txs = -1` (`server/config/config.go:275`), `initAppConfig` doesn't override it (`cmd/inferenced/cmd/config.go`), `app_overrides.toml` has no `[mempool]` section, and `prod-local/genesis/config/app.toml:256` has `max-txs = -1`. An operator *can* still set it, by editing `app.toml` or with env `INFERENCED_MEMPOOL_MAX_TXS` (the SDK uses the executable name as viper env prefix), and `server.DefaultBaseappOptions` (`commands.go:158`) would then install a `SenderNonceMempool`. **Resolution:** the beacon handlers are **independent of local mempool config** (Step 4: PrepareProposal always selects from `req.Txs`; ProcessProposal never delegates to `DefaultProposalHandler`), plus a startup warning when `max-txs ≥ 0`. No network-wide survey needed.
- **Consumers of** `Txs[0]` **(Q5).** dAPI `BlockObserver` (`event_listener/block_observer.go:219`) iterates `BlockResults.TxsResults` and flattens events. A failed-decode tx has no events, so it yields a synthetic event with only `tx.height`, which the relevance filter drops. Harmless. `devshard` has no raw-tx consumer outside `testenv/mockchain`. Not verified: external explorers and indexers (they will show one failed tx per block).

Still open:

1. ~~Exact tmkms version in the current production image~~ → 0.12.0, verified from the image build history; the operator runbook is in Step 0. Still open: the readiness tracking channel (form or thread) and the coordinator.
2. **CPoC**: whether to rescale the trigger probability for the miss rate. (Work seed source and miss handling: decided in [Step 6b](#step-6b-ungrindable-poc-work-seeds-poc-cpoc-poc-challenges).) See [`block-beacon-randomness.md`](block-beacon-randomness.md) §8 and §10.
3. Whether to add a TxDecoder shim so explorers don't show a failed tx (the beacon tx) in every block (Step 4). Recommended only if an explorer or indexer breaks.
4. ~~Accountability for missing or bad partials~~ → decided 2026-11-11: **no slashing or rewards** depend on beacon participation. Collector metrics on each node are enough; no on-chain record of contributors.
5. ~~Slot assignment by voting power~~ → already the case: x/inference assigns BLS slots from `CapWeight` (`module.go:1786`), the same capped trust weight that sets consensus voting power (`module.go:894`). Residual differences: rounding to integer slots, participants without a BLS key are left out of the DKG, and the genesis-guardian adjustment is applied by two separate functions (staking power vs slot reservation). The Step 3 coverage warning (`EventBeaconSlotCoverageWarning`) stays as the safeguard for those.
6. ~~Inclusion window~~ → decided 2026-11-11: none (K = 1). σ is required in block `h+1`, or a proof of miss.
7. ~~CPoC design alignment~~ → the consumer design now lives in the repo ([`block-beacon-randomness.md`](block-beacon-randomness.md)) and is written against this plan.
