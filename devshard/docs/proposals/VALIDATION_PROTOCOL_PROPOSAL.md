# Validation protocol: eligibility, in-place checks, and transparent randomness

## Summary

We should **replace** the current pattern of **per-host seeds + a finalization-time `MsgRevealSeed` phase** with a design where:

- **every `MsgValidation` (or equivalent) is checkable at arrival time** — receivers know whether the **sender is eligible** to validate that inference;
- validation runs **in place** (soon after finish), not only **post hoc** at settlement;
- **one protocol path** covers both **automatic / sampled** validation and **user-paid** validation (e.g. “validate every inference” for critical workloads);
- **randomness** for “who must validate” is **transparent** (everyone can recompute eligibility) and **hard to cheat** (user + executor + multi-identity hosts cannot pick favorable outcomes);
- **Very important:** the design must align with **private inference** — avoid **executor-wide** storage of plaintext prompts/responses for verifiers; prefer **user-held** data and **TEE-targeted** ciphertext for **executor** and **validator** ML nodes (see **Motivation §6**).
- **Transport (constraint):** **minimize** extra **interactions** and **message flooding**; **gossip**-style fan-out **only** in the **finalization** phase — see **Constraints** and [`FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md`](./FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md).

The **hard part** is defining **`R`** — the public seed or beacon — so that it is **binding after work is committed**, **not grindable** by the sequencer, and **efficient enough** for low-latency validation.

**Related:** [`FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md`](./FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md), [`HEIGHT_SYNC_PROTOCOL_PROPOSAL.md`](./HEIGHT_SYNC_PROTOCOL_PROPOSAL.md) (mainnet height and **`rand_seed`** sketch), [`../issues/validation-protocol-remove-seed-reveal.md`](../issues/validation-protocol-remove-seed-reveal.md), [`../attacks.md`](../attacks.md).

---

## Analysis: today’s subnet (short)

Today, each host derives **`ownSeed`** from signing **`escrow_id`** and uses **`ShouldValidate(ownSeed, inference_id, …)`** during **`PhaseActive`** to decide whether **it** should validate a finished inference (`subnet/state/validation.go`, `subnet/host/host.go`). At finalization, **`MsgRevealSeed`** publishes those seeds so **`recomputeCompliance`** can compare **expected** vs **actual** validations per address (`subnet/state/machine.go`). Documented mitigations include **warm keys** against seed-signature grinding ([`attacks.md`](../attacks.md)).

---

## Motivation

### 1. Seed reveal makes finalization heavier

Tying **honest accounting** to a **dedicated reveal round** grows **phase logic**, **gossip**, and **edge cases** (who revealed, duplicates, unrevealed penalties). Finalization should focus on **settlement** ([`FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md`](./FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md)), not on reproducing subnet validation dice **after the fact**.

### 2. No good pre-finalization check for “should have validated but didn’t”

With seeds revealed only at the end, the subnet **cannot** cheaply answer during the session: *“this inference was supposed to receive validation from set **S**, and we have evidence from **S**.”* Liveness and fraud detection want **immediate**, **verifiable** eligibility — not a reconciliation pass that runs only when everyone has revealed.

### 3. Open validation + multiple addresses → self-validation race

If **any** host may send **validate / invalidate** without a **cryptographic eligibility rule**, a participant with **several slots or linked identities** can **validate their own executor work** (or coordinate with a colluding validator) **before** honest nodes react. That undermines the point of sampling: the **first** message wins unless **every** receiver applies the **same** public rule **before** accepting the tx.

### 4. In-place, fast validation — not only postfactum

Operators and users want validations **right after** **`MsgFinishInference`** is committed, with **predictable** load. The protocol should support **low latency** while still binding randomness so the **executor** cannot know **who** must check until the rules say so (see **Threat model**).

### 5. One path for “system” and “user-paid” validation

Two cases should share **one message shape and verification pipeline**:

- **Sampled validation** — protocol chooses **who** must validate (from **`R`**, stake, slot set, etc.).
- **Optional extra validation** — user pays (escrow terms, per-inference flag, or premium tier) to require **additional** validators or **100%** check for critical operations.

The **only** difference is **how many** slots / which policy applies; **eligibility** for each validation message is still **computable from public inputs + `R` + payment policy**, not ad hoc.

### 6. Data locality and privacy (executor-held payloads vs user-held, TEE-bound) — **very important**

**Priority:** This constraint is **first-class**: any validation and eligibility design that **forces** long-lived **executor-hosted plaintext** (or world-readable fetch paths) for cross-host verification is **incompatible** with the product direction below.

The **current** validation flow assumes the **executor** keeps **prompt and response** material in a **database** (or otherwise makes it **fetchable**) so **other hosts** can re-run or compare work. That **centralizes sensitive content on the executor** and **conflicts** with a **private inference** direction: we want **inference processing** to stay **confidential** (minimal retention, minimal exposure).

A compatible direction is:

- **Only the user** holds **prompts and responses** (or ciphertext the user never decrypts on chain); the user supplies **ciphertext** **encrypted for a specific executor host’s Trusted Execution Environment (TEE)** so only that attested environment can run the job.
- **Validation** can follow the **same pattern**: the user (or a policy tool on the user’s side) produces **ciphertext for each required validator’s** attested **ML node / TEE**, so **re-execution** happens **inside** the validator’s boundary without the executor’s DB becoming the **system-of-record** for plaintext.

That implies the **validation protocol** must pair cleanly with **known-eligible validators** (so the user or tooling knows **which** public keys / attestations to target) and with **one** logical path for **sampled** vs **paid** extra checks — without requiring **world-readable** payload URLs on the executor.

---

## Constraints

These are **requirements** on any acceptable design, not reasons *why* we change the protocol (see **Motivation** for that).

### Interaction budget and gossip

The protocol must **limit** cross-host **traffic** and **fan-out**: prefer **direct** user–host rounds, **targeted** notifies to the selected validator, and **deterministic** recomputation over **broadcast storms**.

**Gossip** could solve many **state-sync** and **fan-out** problems cheaply in the abstract, but we **do not** rely on **gossip** as the **primary** synchronization mechanism during **active** inference and validation — it scales poorly, complicates **privacy** and **ordering** assumptions, and overlaps with abuse surfaces (see [`../issues/secure-gossip-propagation.md`](../issues/secure-gossip-propagation.md)). **Subnet-wide gossip–style fan-out** is **reserved** for the **finalization** phase (settlement, vote/commit, collector broadcasts) per [`FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md`](./FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md); ordinary validation uses **direct** messages and **deterministic** rules. **Validation** traffic stays **bounded** and **eligible-sender-only** where possible.

---

## Goals

1. **Eligibility at receive time:** On **`MsgValidation`**, every host rejects unless **sender ∈ EligibleValidators(inference_id, context)** under a **pure, specified function**.
2. **No finalization-time seed reveal** for validation assignment; settlement randomness stays separate from this problem where possible.
3. **Transparent randomness:** **`R`** (or VRF-based eligibility) is **reproducible** from agreed inputs so **no secret** round is needed for audit.
4. **Uncheatable binding:** **`R`** must not be controllable by **user + executor** (or grindable via diff layout); see design options below.
5. **Unified path** for default sampling and user-triggered / paid extra validation.
6. **Very important — private inference:** validation and randomness rules must **not** require the executor to be the **durable plaintext store** for prompts/responses consumed by other hosts; support **user-held** payloads and **TEE-targeted** encryption for executor and validator ML nodes (see **Motivation §6**).
7. **Low fan-out:** satisfy **Constraints — Interaction budget and gossip**.

---

## Threat model

Two major scenarios:

1. **Dishonest executor (without an accomplice on sequencing).** The executor would like to know **in advance** whether **their** inference will be subject to validation (e.g. to skip real work when they believe they will not be checked). That is **largely mitigated** if **who must validate** is fixed only from inputs available **once `MsgFinishInference` is committed** in the canonical session — in particular so that assignment is **bound to the user/sequencer-controlled ordering** (and any agreed mix-in such as mainnet entropy), not to secrets the executor holds alone. Before that commit, the executor should not be able to compute **`R`** or the eligible validator set for that inference.

2. **User and executor in the same attacking group.** Here the user can **shape or delay** diffs around **`MsgFinishInference`**. The main residual risk is **reputation / economics**: the group may try to steer outcomes so the executor **gains credit** for inferences that **never receive** honest validation. That is **not** treated as a **critical** safety or liveness break in the same class as consensus faults; it is still worth **bounding** with sampling, optional **user-paid full validation**, and clear settlement rules — but perfect prevention against a **malicious sequencer** colluding with the executor may be **out of scope** for the strongest guarantees.

Additional risks (orthogonal to the split above):

- If **`R`** depends on **grindable** fields, the colluding **user** may try to **tweak** diff contents to change sortition; beacon-style **`R`** (option A) reduces this.
- **Multi-slot / Sybil-style** operators may try to **occupy** the eligible set or **race** to **self-validate** unless **eligibility** is **narrow** and **publicly verifiable** at message receipt.
- **Honest validators** must not leak or infer **executor-advantageous** knowledge **before** the protocol fixes **`R`** for that inference (exact predicate TBD per option A/B/C).

---

## The core problem: seeding **`R`**

We need a value **`R_inf`** (or per-validator VRF inputs) such that:

- **Transparent:** any party recomputes the same **eligible set** / probability mass from **published data**.
- **Uncheatable:** the beacon is fixed from **mainnet** (or auditable chain state) so colluders cannot **forge** the **block hash** at the agreed height.
- **Compatible with speed:** **`validationSeedHeight`** is only a few blocks after the commit bundle’s **`finishInferenceHeight`**, at the cost of **waiting** for that block before validator identity is final.

A **concrete instantiation** is in **Design directions** below (three-party heights + **`block_hash(validationSeedHeight)`**).

---

## Protocol design

This section is a **concrete protocol sketch**. It **instantiates** the goals above: **transparent** randomness from **mainnet**, **no seed-reveal round**, and **third-party involvement** so the **user alone** cannot fix the beacon. Older abstract options (VRF-only, state-root-only) are **not** repeated here; they remain **alternatives** if this sketch is refined.

### Alignment with the current subnet (today’s code and behavior)

The **first segment** of the flow matches **`subnet/user/user.go`** and **`subnet/host/host.go`** today:

1. **User** builds a diff whose first tx is **`MsgStartInference`** (the code sets **`InferenceId`** to the **new diff nonce**; the **executor** for that inference is **`group[inference_id % len(group)]`**).
2. **Routing:** each diff has a monotonic **`nonce`**; the **HTTP request** for that diff is sent to **`hostIdx = nonce % len(group)`** (round-robin over the escrow participant list).
3. **Executor** for the inference is the host at **`inference_id % len(group)`** (same modulus); that host runs **`RunExecution`**, then queues **`MsgFinishInference`** (signed by the executor) in its **mempool** for later inclusion in a user diff.

So: **start → execute → finish queued** matches the **current** implementation. The **new** material below begins **after** **`MsgFinishInference`** is accepted into session state (included in a diff).

**Naming:** There is no **`MsgStartValidation`** in the current codebase; the user-facing start of work is **`MsgStartInference`**. Below, **“FinishInference commit”** is a **proposed** step (**`MsgFinishInferenceCommit`** or equivalent), not an existing tx type.

---

### Proposed protocol (after current start / execute / finish)

#### Step 1 — Same as today through **`MsgFinishInference`**

Unchanged: user-driven diffs, executor execution, **`MsgFinishInference`** with **`ResponseHash`**, token counts, **`ProposerSig`**, etc.

#### Step 2 — **`MsgFinishInferenceCommit`** on the **next** host (user → host B)

- User forms a **new** diff (next **`nonce`**). Routing uses **`hostIdx := int(nonce % uint64(len(group)))`** (`subnet/user/user.go`) — typically a **different** host than the previous diff’s receiver when **`len(group) > 1`**. That receiver is **host B**. **`len(group)==1`:** there is **no** distinct **B**; define a **fallback** (e.g. mainnet-only beacon without B/C, or **no** sampled validation for this escrow size).
- The diff includes a **proposed** message **`MsgFinishInferenceCommit`** (encoding TBD) that **binds** the finished inference (e.g. **`inference_id`**, **`response_hash`**, **`escrow_id`**) and carries a **height-sync section** as in [`HEIGHT_SYNC_PROTOCOL_PROPOSAL.md`](./HEIGHT_SYNC_PROTOCOL_PROPOSAL.md): **latest attested mainnet height**, **`LightBlock`**, **`sender_signature`**, etc.
- **Purpose:** introduce a **second honest party** (B) besides the user, with **verifiable** chain view, before fixing randomness.

#### Step 3 — Host B involves host C (height ping, deterministic third party)

- **B**, by protocol, selects **host C** **deterministically at random** from the set:  
  **{ hosts that did not execute this inference and are not B }**  
  (i.e. not the executor for this **`inference_id`**, and not B). Selection uses a **public** rule (e.g. **`H(escrow_id || inference_id || …) % |eligible|`**) so all implementers agree.
- **B** sends **C** a **minimal round-trip** (a “dummy” or **height-sync-only** request) that also carries **latest height** per **`HEIGHT_SYNC_PROTOCOL_PROPOSAL.md`**; **C** responds with a **signed** attestation of **their** latest aligned height (same envelope rules).
- **B** returns to the **user** (in the HTTP response path for the user’s request to B) the **evidence** needed so the user can attach **B’s** and **C’s** signed heights into the **canonical commit bundle** (exact wire format TBD).

After this step, **three parties** each hold a **verifiable** view of “latest height” **at the time of the commit round**: **user (A)**, **B**, **C**.

#### Step 4 — Deterministic **`finishInferenceHeight`**

All participants compute:

`finishInferenceHeight = max(height_A, height_B, height_C)`

using only **signed, verifiable** height fields from the commit bundle (each party’s section-1 style proofs as in [`HEIGHT_SYNC_PROTOCOL_PROPOSAL.md`](./HEIGHT_SYNC_PROTOCOL_PROPOSAL.md)). This value is **deterministic** given the same bundle.

#### Step 5 — **`validationSeedHeight` and unknowable block hash**

Even with three parties, **fresher** mainnet blocks may already exist than **`finishInferenceHeight`**. To reduce the chance that **any** party **picked** the beacon in advance, define:

`validationSeedHeight = finishInferenceHeight + VALIDATION_TRIGGER_OFFSET`

where **`VALIDATION_TRIGGER_OFFSET`** is a small **policy constant** (e.g. **2 or 3** mainnet blocks). **Semantics:** validation sampling for this inference is keyed to **mainnet height** **`validationSeedHeight`** — i.e. the **block** at that height whose hash is **not** knowable to anyone until that block is **finalized** (under the chain’s assumptions).

Let **`block_hash(H)`** be the canonical block ID hash at height **`H`**. Define e.g.:

`R_inf = H(escrow_id || inference_id || block_hash(validationSeedHeight) || …)`

and derive **eligible validator(s)** from **`R_inf`** with a **public** formula (e.g. weighted by **reputation** / stake as policy requires). **Everyone** agrees on **who** must validate once **`block_hash(validationSeedHeight)`** is available.

#### Step 6 — Notifying the selected validator; validation on the user’s nonce chain

- **Any** of **A, B, C** may send the **validator** a **proof package**: inference refs + **`MsgFinishInferenceCommit`** bundle + heights + **`validationSeedHeight`** + **`block_hash`** once known.
- The **validator** checks **`EligibleValidators`** using the same **`R_inf`** rule.
- **User** continues to advance **nonces** to other hosts in order; when the **validator** is picked by the round-robin, the **validator** may attach **validation results** (or “not yet ready”) to the response, so results can enter the **same** diff / gossip path as today.
- If the outcome is **invalid**, **gossip** (or equivalent) should surface that **early** so **validation voting** / challenge flows can start.

#### Step 7 — Finalization without waiting on the user

If the **user stops sending** messages but **finalization** starts, **validators** (and other hosts) **publish** whatever **proofs** and **partial results** they hold so the rest of the group can **verify** scheduled validations. **Finalization checks** should include: **all validations scheduled** under this protocol for **finished** inferences have **corresponding results** (or explicit timeout / slash per policy). This aligns with the **TODO** in [`FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md`](./FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md) on **unfinished** inferences and validations.

---

### Extensions (same path) — “validate every inference” / extra round-robin check

For workloads that require **every** inference to be checked (user-paid or policy), **do not** add a second randomness path. Use the **same** session rule as normal routing: **advance the nonce** and send the next diff to the **next host** in the round-robin (**`hostIdx = nonce % len(group)`**).

**Constraint:** the **validator** for this extra step **must not** be the **executor** for that inference. The executor is fixed by **`inference_id % len(group)`** (slot / host index). When incrementing **`nonce`**, if the next host index would equal the **executor’s index**, **skip** it once (increment again) so validation always lands on a **different** host.

**Multiple slots under one address:** if one **operator** owns several slots, the rule is still **host-index** (or primary slot index) based: **no** validation round may target the **same** executor **index** as the inference, even if another slot of the same address would receive the message — implementation maps **slot → host round-robin index** consistently so the executor cannot validate **their own** execution via a sibling slot.

---

### Threats and attack vectors (review of this sketch)

| Concern | Notes |
|--------|--------|
| **Stale or forged heights** | Mitigated by **`LightBlock`** verification and **section-1** rules in [`HEIGHT_SYNC_PROTOCOL_PROPOSAL.md`](./HEIGHT_SYNC_PROTOCOL_PROPOSAL.md); all three heights must be **attested**, not self-reported scalars. |
| **Delaying `MsgFinishInferenceCommit`** | **Not hash grinding in practice:** **`finishInferenceHeight`** is **`max(height_A, height_B, height_C)`** from **three** attested views (user + **B** + **C**); the user does **not** unilaterally pick that max. **`validationSeedHeight = finishInferenceHeight + VALIDATION_TRIGGER_OFFSET`** targets a block **several heights later**; the **hash** at that height is **unknowable** at commit time regardless of how “fast” the user’s RPC is — **`OFFSET`** exists precisely so the beacon is **not** the next block after the commit bundle. Delaying the commit only **shifts wall-clock** when heights are sampled; it does **not** let the user **select** **`block_hash(validationSeedHeight)`** like grinding a nonce. **Real residual:** **liveness / griefing** — the user can **withhold** **`MsgFinishInferenceCommit`** forever or past a **deadline** (mitigate with **max commit delay**, escrow policy, penalties). Optional edge cases: **`OFFSET = 0`**, or pathological **predictability** of the chain, could revive bias discussions — keep **`OFFSET`** at least **2–3** blocks as in the sketch. |
| **B and C collude with user** | Matches **Threat model §2**: mostly **reputation / economics**; three parties still **cannot forge** mainnet **`block_hash`** at **`validationSeedHeight`**. |
| **Omitting `MsgFinishInferenceCommit`** | If optional, **user+executor** could **never** trigger **`validationSeedHeight`** sampling — must be **mandatory** for sampled validation to count, or **treated as slash / no payout** for that inference. |
| **Steering host C (theoretical grind)** | **C** is not fully **user-independent** in every wiring: the user **chooses when** to send **`MsgFinishInferenceCommit`** and thus **which** host is **B** (round-robin by nonce), which can influence **which** host is drawn as **C** under the deterministic rule. So a motivated party could in principle **expend effort** to **nudge** **C**. The **upside** of doing so is **weak and expensive:** the only clear win is **inflating executor reputation** while avoiding real checks, and that scenario effectively requires **one operator** to control **the user**, **the executor**, **B**, and **C** — **four roles** spanning **the user plus three hosts**, i.e. a **large fraction** of a typical subnet. Even then, the attacker still pays **fees** and **inference cost**, **`MsgStartInference`** remains tied to the **monotonic nonce** and **round-robin** routing, and **all** committed **heights** and the **commit bundle** stay **on the record** for **later analysis**. So grinding **C** is **possible in theory** but usually **economically and operationally meaningless** compared to running the protocol honestly. |
| **Validator griefing / spam** | Proof packages must be **cheap to verify**; rate-limit **notify** traffic; **eligibility** checked before work. |
| **Finalization race** | If **finalization** runs before **`block_hash(validationSeedHeight)`** exists, define **wait**, **timeout**, or **penalty**; align with **unfinished validation** TODO on the finalization doc. |
| **Privacy (Motivation §6)** | This sketch does not by itself **encrypt** payloads; **TEE-bound** delivery of prompts/responses to the **selected validator** is a **separate** layer (user encrypts to validator attestation). |

---

## Relation to finalization collector protocol

[`FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md`](./FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md) addresses **settlement** (vote/commit, collectors). **Validation** eligibility and **`R_inf`** from **Design directions** are **orthogonal**: finalization should not depend on reproducing **`MsgRevealSeed`** semantics. **Finalization** must still **reconcile** scheduled validations (see **Design directions — Step 7** and the **TODO** in that doc on unfinished work).

---

## Recommended next steps

1. Specify **`MsgFinishInferenceCommit`** (fields, signatures, inclusion relative to **`MsgFinishInference`**).
2. Pin **`VALIDATION_TRIGGER_OFFSET`**, **`EligibleValidators(R_inf, …)`** (reputation weights), and **mandatory** vs **optional** commit step for payout.
3. Replace **`RevealedSeeds` / `recomputeCompliance` / `penalizeUnrevealedSeeds`** with rules that match this beacon + commit bundle (or interim bridge for legacy escrows).
4. Update [`../issues/validation-protocol-remove-seed-reveal.md`](../issues/validation-protocol-remove-seed-reveal.md) and extend [`PROTOCOL_TESTING_PROPOSAL.md`](./PROTOCOL_TESTING_PROPOSAL.md) with **three-party commit**, **timing grind**, **omit commit**, and **finalization-before-beacon** cases.

---

## Open questions

- Minimum **latency** acceptable between **finish commit** and **first allowed** validation.
- How **user-paid “validate all”** interacts with **pricing**, **DoS bounds**, and **[`shard-state-trim-inferences-by-height.md`](../issues/shard-state-trim-inferences-by-height.md)**.
- Whether **invalidation** messages require the **same** eligibility set as **validation** or a **stricter** quorum.

---

## Status

Draft proposal — for review before implementation.
