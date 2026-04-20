# cPoC skip protocol (devshard) — proposal

## Summary

In a **devshard**, hosts that run **confirmation PoC (cPoC)** must **not** serve normal inference for the duration of their PoC obligation. Other hosts must be able to tell whether a skip is **legitimate** (the skipping host is on the **cPoC schedule** at the relevant height) or **abusive** (lying, or refusing work). This document specifies the **data flow** and the **cases** that the cPoC protocol must handle.

**Out of scope for this document:**

- **How each host obtains / agrees on mainnet height.** That is solved by **[HEIGHT_SYNC_HEADERS_PROPOSAL.md](./HEIGHT_SYNC_HEADERS_PROPOSAL.md)** (Omit / Anchor / Strong, deferred checks, etc.). Here we **assume** each host has a scalar `**H(host)`** equal to the **height known to the majority of validators / devshard hosts** (its own follower + height-sync rules have converged on that value). Discrepancies at the level handled by the height-sync spec are **that spec’s problem**; this document only distinguishes the cases where such a discrepancy **affects a cPoC verdict** and defers the discrepancy itself to height sync.
- **Selection of `POC_SLOT` hosts** (inference-exempt role) — policy/RNG, see Scope table.
- **Mainnet settlement / slashing math** — out of scope; this doc emits **verdicts** (`Valid` / `Invalid` / `Inconclusive`) and hands evidence to [FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md](./FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md).

**Status:** draft — **data flow + cases** specified below; wire schemas, chain hooks, and slashing predicates still TBD.

---

## Scope


| Part  | Content                                                                                                                                                                                                         | Depth in this doc                                                                                                         |
| ----- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------- |
| **1** | On escrow start, random (or policy-driven) host assignment so that roughly **~20%** of devshard hosts have `POC_SLOT = true` (keep serving inference during cPoC windows) and the rest run cPoC when scheduled. | **Out of scope for deep specification** — operational/policy; implementers can fix exact ratio and RNG source separately. |
| **2** | Protocol for **proving** that a host was entitled to skip inference because of cPoC at a given **mainnet height**, under **height disagreement** and **Byzantine** developers/hosts.                            | **In scope** — normative intent below; formalization pending.                                                             |


---

## Shared assumptions (informative)

1. **Height oracle (provided by height sync, treated as black box here):** Each host `**V`** exposes a scalar `**H(V)**` — the mainnet height **known to the majority of validators / devshard hosts** as of `**V`**’s latest convergence with the height-sync layer. This doc **does not** re-specify how `H(V)` is computed, trusted, or refreshed; see [HEIGHT_SYNC_HEADERS_PROPOSAL.md](./HEIGHT_SYNC_HEADERS_PROPOSAL.md). When this doc says “height `**H`**” without qualification, read it as `**H(V)` at the moment `V` evaluates the case**.
2. **cPoC schedule:** Given a host `**H_i`** and a mainnet height `**H**`, there exists a deterministic predicate `**Schedule(H_i, H) ∈ {idle, prepare, active}**` derivable from chain / epoch state by anyone with that height. Semantics of `prepare` vs `active` are defined in the chain-side cPoC spec and out of scope here.
3. `**POC_SLOT` roster:** For the epoch in force, a set `**PoC_slot_set`** of hosts with `POC_SLOT = true` is available to every honest host (exact provenance — escrow init vs post-init query — is Open question **1**).
4. **Executor schedule:** Requests in a session are ordered by a **monotonic nonce** (linear increment). With `**N_slots`** slots and fixed mapping `**executor(nonce) = hosts[nonce mod N_slots]**`, the same logical slot recurs at `**nonce + N_slots**` (one **round**).
5. **Asynchronous developer traffic:** The developer **does not** wait for a host response before sending the next request. A response to a request at `**R_req`** is merged into the session's **linearized diff** at some later nonce `**R_req + x`**, `**x ≥ 0**` — not necessarily the same nonce. Any nonce-bound rule must work on **the nonce at which a message appears in `Diff`**, not on wall-clock pairing with the outbound request.

### Notation (nonces used throughout)

All nonces below are monotonic indices into `Diff` (Data flow § Per-session local state). They are defined here once so later sections can reference them without re-introducing each.


| Symbol    | Definition                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 | Introduced by                         |
| --------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------- |
| `n`       | Generic `Diff` nonce.                                                                                                                                                                                                                                                                                                                                                                                                                                                                      | —                                     |
| `R_req`   | **Request nonce.** The nonce at which `MsgStartInference` is appended to `Diff` (Path A) — the inference request a cPoC skip answers.                                                                                                                                                                                                                                                                                                                                                      | `D → devshard` (`MsgStartInference`). |
| `N_SP`    | **Probe nonce.** The nonce at which `MsgSkipProbe` is appended to `Diff` (Path B) — the lightweight probe that plays `R_req`'s role when no prompt is submitted.                                                                                                                                                                                                                                                                                                                           | `D → devshard` (`MsgSkipProbe`).      |
| `N_carry` | **Carry nonce.** The nonce at which the `CarrySkip` envelope (embedding either the signed `CPoCSkipResponse` or `CPoCProbeResponse`) is appended to `Diff`. This is the only verdict-bearing artifact in `Diff`; every verifier computes `Verdict` from `Diff[N_carry]`. Causality: `R_req < N_carry` in Path A (distinct proto messages ⇒ distinct nonces, and the dev cannot sign the carry until after the host's p2p response exists); `N_SP < N_carry` in Path B for the same reason. | `D → devshard` (`CarrySkip`).         |
| `X`       | **Witness nonce.** The latest nonce `≤ R_req` (or `≤ N_SP`) whose executor is `V` itself; `height_at[X]` is V's local height observed no later than `R_req` and is the lower endpoint of the freshness interval `I = [h_X, h_carry]`. Formula and derivation in § Verdict predicate, step 1.                                                                                                                                                                                               | Computed locally by each `V`.         |
| `N_slots` | Number of executor slots per round; `executor(n) = hosts[n mod N_slots]` (assumption **4**).                                                                                                                                                                                                                                                                                                                                                                                               | Chain / epoch parameter.              |


---

## Problem statement

### 1. Skip correctness

A host that returns **“skipping because of cPoC”** may be:

- **Honest** — `Schedule(H_i, H) ∈ {prepare, active}` and `H_i ∉ PoC_slot_set`, or
- **Malicious** — returning `CPoC_SKIP` while **not** scheduled / while in `PoC_slot_set` (avoids work).

The protocol must let every honest verifier `**V`** reach the same verdict from the **same diff**, using `**H(V)`** as the height oracle (assumption **1**).

### 2. Developer replay / withholding

A developer could **hold** a host's cPoC skip response and later attach it via `CarrySkip`. Mitigation is layered:

- **At the cPoC verdict layer (this doc):** freshness is bounded **in mainnet heights**, not rounds, via the interval `I = [h_X, h_carry]` each verifier personally witnesses (§ Nonce binding). A late carry of a **genuine** skip blob remains `Valid` — it was truthful at a height in `I`; a late reveal does not retroactively make it a lie. Only skips that were **never** legitimate at any height in `I` produce `Invalid`.
- **At the settlement layer (out of scope here):** the remaining harm from late carries — inference records kept open, stale evidence used to stall settlement — is handled by `MsgTimeoutInference{…CPOC}` timeouts and finalization deadlines.

### 3. Gossip volume

Under high inference rate, if most hosts skip during cPoC, per-skip gossip is unacceptable:

- **No** gossip inside a normal round if diffs already propagate the evidence.
- **Dispute-grade** evidence rides on **[finalization / state sharing](./FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md)** rather than a parallel flood channel.

---

## Design principles (high level)

The formalization in § Data flow and the cases in § Cases to handle are chosen to satisfy the following principles. Nonce symbols (`R_req`, `N_SP`, `N_carry`, `X`, `N_slots`) are defined in **Shared assumptions → Notation**; `H(V)`, `Schedule`, and `PoC_slot_set` in **Shared assumptions** items 1–3; `timeout_skip_gossip` under § Gossip minimization.

### Two request paths

The developer chooses **one of two shapes** when opening a request; both converge on the same `Verdict` predicate.

**Path A — inference with possible cPoC refusal (full payload).** Developer submits a real inference request; the host either confirms and runs it, or refuses because of cPoC.

```
D → devshard : MsgStartInference(R_req, prompt_hash, …)           [into Diff at R_req]

  happy path → H_i → devshard : MsgConfirmStart(R_req)             [into Diff]
                               → … → MsgFinishInference

  cPoC   path → H_i → D       : CPoCSkipResponse(R_req, reason)    [p2p; NOT in Diff]
               D  → devshard  : CarrySkip(N_carry, <embedded CPoCSkipResponse>)
                                                                    [into Diff at N_carry]
```

**Path B — lightweight skip probe (no prompt, no inference cost).** Developer asks `H_i` to report its cPoC state without paying a prompt. The host **does not execute inference**; it just returns a signed status. The response has **two possible outcomes**:

- *Refusal* — `H_i` is still on cPoC (`cpoc_active` or `cpoc_prepare`); behaves like a Path-A skip for verdict purposes.
- *Ready* — `H_i` has finished cPoC and is `READY_INFERENCE`; the developer should resume sending real `MsgStartInference` to `H_i`.

```
D  → devshard  : MsgSkipProbe(N_SP)                                 [into Diff at N_SP]
H_i → D        : CPoCProbeResponse(N_SP, outcome ∈ {cpoc_active,
                                                    cpoc_prepare,
                                                    ready})         [p2p; NOT in Diff]
D  → devshard  : CarrySkip(N_carry, <embedded CPoCProbeResponse>)   [into Diff at N_carry]
                  # N_SP < N_carry strictly (two distinct Diff entries)
```

A `ready` outcome carried into `Diff` is **not** an `Invalid` skip — the verdict predicate simply does not apply (no refusal to validate). It is instead a **scheduling receipt**: V records that `H_i` signalled `ready` at a height in `[h_X, h_carry]`. Subsequent developer behaviour is checked against that receipt by **C13** (developer keeps probing / skipping a ready host — see § Cases).

> **Future optimization (out of scope for this release).** The Path-B flow above is a full developer↔host roundtrip: `MsgSkipProbe` → p2p `CPoCProbeResponse` → `CarrySkip`. A cheaper variant is possible once hosts are able to publish their own cPoC state directly into `Diff` (or into a chain-signed witness that verifiers already consume): the developer would not need to probe at all, and the `ready` transition could be observed by every `V` without a p2p roundtrip. This would eliminate the `MsgSkipProbe` / `CPoCProbeResponse` / `CarrySkip` triple for status-only queries and reduce Path B to a passive read of host-published state. Designing this path requires (i) a signed host-status channel (either new `SubnetTx` entries or a chain-level heartbeat), (ii) a staleness bound so a missing update isn't silently treated as `ready`, and (iii) reconciling the witness with the existing `ready_at[H_i]` semantics used by C13. **Deferred to a future release** — the current doc specifies only the explicit-probe flow.

Key invariants shared by both paths:

- **Host responses are p2p and not directly observable by verifiers.** Whether `CPoCSkipResponse` (Path A refusal) or `CPoCProbeResponse` (Path B status), the host's signed statement only enters the verifier's field of view when the developer echoes it via `**CarrySkip`** into `Diff`.
- `**CarrySkip` is the only verdict-bearing artifact in `Diff`.** Every `V` computes `Verdict` from `Diff[N_carry]` (plus, in Path A, the presence / absence of `MsgConfirmStart` for the same `R_req`).
- **Two distinct `Diff` entries per path.** Both paths have `R_req < N_carry` (resp. `N_SP < N_carry`) strictly: different proto messages occupy different nonces, and the developer signature on `CarrySkip` binds bytes that only exist *after* the host's p2p response arrives.
- **Settlement is decoupled.** Once `CarrySkip` has reached a final `Verdict`, closing the inference record at chain level uses the existing `MsgTimeoutInference{reason = TIMEOUT_REASON_CPOC}` path (new enum value); this settlement step is **not** what the verdict depends on.

Everything below — nonce binding, gossip minimization, data flow, cases — applies to both paths uniformly; the only Path-B specialization is the additional `ready` outcome (and its follow-on case C13).

### Nonce binding (height-interval freshness)

Because of **asynchronous developer traffic** (Shared assumptions, item **5**), the response to a request sent at nonce `**R_req`** may appear in `Diff` only at nonce `**R_req + x**`, `**x ≥ 0**`. The delay `x` is **not bounded in rounds** — rounds can be far faster than mainnet blocks or host response, so many rounds may legitimately elapse between `R_req` and `N_carry`. Verdicts therefore bind to a **height interval** that each verifier constructs **locally** from its own observations of `Diff`:

- **Reference nonce** of a skip attestation = `**R_req`** (the request it answers), stated inside the signed `CPoCSkipResponse`. (Term chosen to avoid collision with the height-sync "Anchor", which is out of scope here.)
- **Carry nonce** = `**N_carry`** (the nonce at which `CarrySkip` is appended to `Diff` and becomes visible to verifiers).
- **Witness nonce `X`** = the **latest nonce `≤ R_req`** whose executor is `V` itself — a nonce V personally handled, so `height_at[X]` is a height V actually observed no later than `R_req`. The exact formula (same round vs. previous round, depending on `SP_v` vs. `SP_e`) and its derivation are given in § Verdict predicate, step 1.
- **Height interval `I = [h_X, h_carry]`** where `h_X := height_at[X]` and `h_carry := height_at[N_carry] = H(V)` at ingest of `Diff[N_carry]`. This interval **bounds the set of mainnet heights** at which the host's skip could physically have been produced, as seen through **this** verifier's local clock.
- **Legitimacy test (anti-cheat, not anti-replay).** The skip is legitimate iff **∃ H ∈ I : `Schedule(H_i, H) ∈ {prepare, active}`**. If no height in `I` places `H_i` on the cPoC schedule, the skip could not have been truthful at any moment `V` witnessed → `Invalid` against `H_i`. A stale but genuine skip blob replayed well after the host returned to `READY_INFERENCE` is **still `Valid`** — the host was legitimately refusing at some height in `I`; a late carry does not retroactively make it a lie. (Replay / withholding harms settlement, not the cPoC verdict — see § Consensus / voting and the settlement-only row in the primitives table.)
- **Height attribution is local only.** Each verifier computes `h_X` and `h_carry` from its own `height_at[·]` map; the developer's or host's claimed height in `CPoCSkipResponse` is informational and is **not** input to the verdict.

### Gossip minimization

1. **Round-based elision (high load):** If within `timeout_skip_gossip` after `N_carry` the session advances to `N_carry + N_slots` (one full round), every honest verifier has seen the evidence via the diff. No dedicated gossip is emitted.
2. **Timeout-based gossip (low load):** Otherwise, any `V` with a non-`Valid` verdict **MAY** emit a compact `SkipEvidenceGossip` pointing into `Diff`. Peers re-run the verdict predicate locally.
3. **Finalization alignment:** Global, dispute-grade evidence rides with [FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md](./FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md) rather than a parallel flood channel.

Parameter `**timeout_skip_gossip`** (proposal: **≈ 2** mainnet blocks) is **chain-parametrized**; its exact value is out of scope here.

---

## Data flow (formalized)

### Parties


| Symbol    | Role                                                                          |
| --------- | ----------------------------------------------------------------------------- |
| `**D`**   | Developer / client.                                                           |
| `**H_i**` | Host at slot `**i**` (`i = nonce mod N_slots`).                               |
| `**V**`   | Any verifier (a host that observes the session diff and must form a verdict). |


### Per-session local state (at each `**V**`)


| Symbol                  | Meaning                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| ----------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `**Diff**`              | Append-only linearized diff of session messages, indexed by monotonic nonce `**n**`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| `**H(V)**`              | Height oracle (out of scope — supplied by [HEIGHT_SYNC_HEADERS_PROPOSAL.md](./HEIGHT_SYNC_HEADERS_PROPOSAL.md)): mainnet height known to the majority of validators as of `**V**`’s latest height-sync convergence.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| `**height_at[n]**`      | Local map: when `V` ingests diff entry at nonce `**n**`, it records `**H(V)**` at that moment. **Not shared**; local only.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          |
| `**PoC_slot_set`**      | See assumption **3**.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               |
| `**pending_verdicts`**  | Buffer of skip attestations ingested from `Diff` whose `Verdict` is not yet final, keyed by `(R_req, N_carry)`. Two reasons an entry sits here: (a) `**Inconclusive**` — `I`'s endpoints (`h_X`, `h_carry`) are not yet strictly confirmed by the height-sync layer (resolution key: confirmation signal covering `I`, see C6); (b) `**Invalid**` awaiting the round-elision / gossip deadline (C9/C10). Note that `Diff[X]` is **always** present by the time `Diff[N_carry]` is ingested (because `X ≤ R_req ≤ N_carry` and `Diff` is append-only), so `h_X` is always immediately computable — no "wait for witness" deferral exists. Each entry holds: `N_carry`, `R_req`, skipping host `H_i`, raw signed host response (`CPoCSkipResponse` or refusal-outcome `CPoCProbeResponse`), current tentative verdict (if any), and the resolution key/deadline. Entries are removed on commit: `Valid` → drop; `Invalid` → hand to finalization. `ready`-outcome carries never enter this buffer — they are recorded directly in `ready_at` (below). |
| `**ready_at`**          | Map `host → (N_carry, h_carry, reset_height?)` recording the latest `CPoCProbeResponse(outcome = ready)` for each host, from the most recent `CarrySkip` in `Diff` with `payload_kind = probe_response` and `outcome = ready`. Consumed by case **C13** (developer withholding from a ready host). Cleared for `H_i` when V later observes either (a) `Schedule(H_i, H) ∈ {active, prepare}` strictly confirmed for some `H > h_carry`, or (b) a fresh non-`ready` `CPoCProbeResponse` / `CPoCSkipResponse` for `H_i` carried into `Diff`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          |
| `**withholding_alert`** | Per-`(D, H_i)` flag set by V when the C13 violation predicate fires on local `Diff` observations; cleared per C13 flow step 5. While set, V (if queued as a future executor for `D`) refuses to serve `D` until fairness is restored.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               |


### Primitives

Names in `subnet/proto/subnet/v1/{tx,diff}.proto` unless marked **(new)**. The **verdict-predicate input set** is `MsgStartInference`, `MsgConfirmStart`, `MsgSkipProbe`, and `CarrySkip`; the **verdict-settlement input set** is `CPoCVote`; the remaining messages are p2p carriers (`CPoCSkipResponse`, `CPoCProbeResponse`), delivery gossip (`SkipEvidenceGossip`), or final settlement (`MsgTimeoutInference{…CPOC}`).


| Object                                                                             | Kind / channel                         | Direction            | Carries (minimum)                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| ---------------------------------------------------------------------------------- | -------------------------------------- | -------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `**MsgStartInference`**                                                            | Diff (existing)                        | `D → devshard`       | Inference request at nonce `R_req`: `inference_id`, `prompt_hash`, `model`, `input_length`, `max_tokens`, `started_at`. Path A only; this is the request the cPoC verdict anchors on.                                                                                                                                                                                                                                                                 |
| `**MsgConfirmStart**`                                                              | Diff (existing)                        | `H_i → devshard`     | Happy-path executor confirmation: `inference_id`, `executor_sig`, `confirmed_at`. **Absent** when `H_i` is skipping for cPoC — its absence, together with a matching `CarrySkip`, is what flips the Path-A verdict.                                                                                                                                                                                                                                   |
| `**CPoCSkipResponse`** **(new)**                                                   | **p2p** (not in Diff)                  | `H_i → D`            | **Path A only.** Host's signed refusal to a real inference request: `inference_id`, `reference_nonce = R_req`, `reason ∈ {cpoc_active, cpoc_prepare}`, optional `claimed_height_h_i` (informational; verdict ignores it), host signature under domain `cPoCRefusalContent`.                                                                                                                                                                           |
| `**CPoCProbeResponse`** **(new)**                                                  | **p2p** (not in Diff)                  | `H_i → D`            | **Path B only.** Host's signed response to a skip probe: `probe_nonce`, `reference_nonce = N_SP`, `outcome ∈ {cpoc_active, cpoc_prepare, ready}`, optional `claimed_height_h_i` (informational), host signature under domain `cPoCProbeResponseContent`. `ready` means *H_i has exited cPoC and expects real inference requests*.                                                                                                                     |
| `**CarrySkip`** **(new)**                                                          | Diff (new message in `SubnetTx` oneof) | `D → devshard`       | Developer-signed envelope that embeds exactly one host response blob — either a `CPoCSkipResponse` (Path A) or a `CPoCProbeResponse` (Path B) — and places it at nonce `N_carry`: `nonce = N_carry`, `referenced_nonce = R_req` (or `N_SP`), `payload_kind ∈ {skip_response, probe_response}`, bytes `host_response`, developer signature under domain `CarrySkipContent`. **The only verdict-bearing / scheduling-bearing cPoC artifact in `Diff`.** |
| `**MsgSkipProbe`** **(new)**                                                       | Diff (new message in `SubnetTx` oneof) | `D → devshard`       | Path-B lightweight probe: `probe_nonce = N_SP`, `target_host_id`, `session/routing`, no prompt payload. Enters `Diff` at `N_SP`. The host's response (`CPoCProbeResponse`) is p2p and echoed into `Diff` via a subsequent `CarrySkip` at `N_carry > N_SP`.                                                                                                                                                                                            |
| **`CPoCVote`** **(new)**                                                           | p2p (→ collector), bundled into finalization | `V → collector` | Signed verdict vote emitted by each verifier with a non-`Valid` local verdict. Fields: `N_carry`, `referenced_nonce`, `target ∈ {host(H_i), carrier(D), developer(D)}`, `verdict`, `reason_code`, `schedule_witness`, signature under domain `cPoCVoteContent`. The collector (today: developer `D`; when `D` is the target: next executor host for the session; future: the finalization round itself) aggregates distinct signatures until `quorum_invalid` is reached, then hands the bundle to finalization. See § Consensus / voting. |
| **`MsgTimeoutInference` with `reason = TIMEOUT_REASON_CPOC`** **(new enum value)** | Diff (existing message + new enum)     | collector → devshard | **Settlement only.** After the `CPoCVote` quorum has decided a final `Verdict`, the inference record is closed through the existing timeout path with the new reason. Carries `inference_id`, `repeated TimeoutVote votes`. Verifiers do **not** need this to compute the verdict.                                                                                                                                                                                  |
| **`SkipEvidenceGossip`** **(new, optional)**                                       | Off-diff gossip                        | host ↔ hosts         | Used only when round-elision fails (§ Gossip minimization). References entries in `Diff` (`inference_id`, `N_carry`, vote indexes). **Delivery aid only** — makes the same `CarrySkip` visible to lagging peers so they can compute their local verdict and emit `CPoCVote`. Does not itself contribute to the verdict or the vote bundle.                                                                                                                                                                                                                      |


### End-to-end flow (happy path, host **actually** on cPoC)

```
                nonce R_req                                                  nonce R_req+1..N_carry-1
 D ─────────────── InferenceRequest(R_req) ─────────────▶ H_i                 (other requests to H_{i+1..})
                                                           │
                                                  H_i in cPoC
                                                           │
 D ◀─────────── CPoCSkipResponse(R_req, reason) ───────────┘        (arrives async, R_req + x in Diff)

 D ───── CarrySkip(N_carry) embeds CPoCSkipResponse(R_req, …) ──▶  any host  ──▶  Diff[N_carry]

 each V observing L:
   on ingest Diff[N_carry]:
     record height_at[N_carry] = H(V)
     evaluate Verdict(…)  using H(V) and nonce-window rules below
```

### Verdict predicate (normative shape)

`V` computes `**Verdict(skip_evidence) ∈ {Valid, Invalid, Inconclusive}**` as:

1. **Causality and height-interval construction.** Applied to the **first** `CarrySkip` in `Diff` that references `R_req` (see "First-carry rule" below):
  - **Causality:** `R_req ≤ N_carry`. A `CarrySkip` cannot reference a request that has not yet entered `Diff`. Failure → `Invalid` **against the carrier** (developer signature on `CarrySkip`), not against `H_i`.
  - **Witness nonce `X`** (per § Design principles → Nonce binding). Let `SP_e = R_req mod N_slots`, `SP_v = v_slot`, `round(R_req) = ⌊R_req / N_slots⌋`. Then:
    - If `SP_v ≤ SP_e` → `X = round(R_req) · N_slots + SP_v` (same round as `R_req`, `X ≤ R_req`).
    - If `SP_v > SP_e` → `X = (round(R_req) − 1) · N_slots + SP_v` (**previous** round, `X < R_req`). Taking V's slot in the current round would reference a nonce **after** `R_req`; its `height_at` would be observed *after* `R_req` and could not lower-bound `H_skip`. Stepping back one round gives the latest executor-of-`V` nonce ≤ `R_req`.
    - Closed form used in pseudocode: `X = R_req − ((SP_e − SP_v) mod N_slots)`.
  - **Interval endpoints:**
    - `h_X := height_at[X]` — V's local height when it ingested `Diff[X]`. By construction `X ≤ R_req`, so `h_X` was observed no later than the request itself.
    - `h_carry := height_at[N_carry] = H(V)` at ingest of `Diff[N_carry]`.
    - **Invariants** (sanity, not failure modes): `h_X ≤ h_carry` (heights are monotonic at ingest), and `h_carry ≤ H(V)_now` (trivially — V is ingesting `N_carry` right now and stamps `h_carry` from `H(V)_now`).
  - `**Diff[X]` is always available when `Diff[N_carry]` is being ingested.** By construction `X ≤ R_req`, and causality (checked first in this step) requires `R_req ≤ N_carry`, so `X ≤ N_carry`. Because `Diff` is append-only and ingested in order, every `Diff[k]` with `k ≤ N_carry` is already present when V processes `Diff[N_carry]`.
  - **Bootstrap edge case.** The only situation in which `X` does not identify a real prior executor-slot of V is `round(R_req) = 0 ∧ SP_v > SP_e`, where the closed form yields `X < 0` — V had no executor slot before `R_req` in this session. V then falls back to the implicit session-start anchor (the lowest nonce V has ingested, typically 0) as the lower endpoint `h_X`. This is a cold-start condition only; it does not recur once V has executed at least once.
  - **Output** of this step: the height interval `**I := [h_X, h_carry]`**, consumed by step (3).
   The correct bound is in mainnet heights, and each verifier derives it from heights **it personally observed** (`h_X` and `h_carry`) — no cross-host height assumption required.
   **First-carry rule.** If the developer publishes multiple `CarrySkip` entries for the same `R_req`, only the **earliest** `N_carry` in `Diff` is admitted as input to the verdict; later duplicates are **ignored** (they may still be recorded for developer-misbehavior accounting, out of scope for this predicate). This keeps `I` deterministic across verifiers.
   **Path B.** For `MsgSkipProbe` (case **C7**) the rule is identical, with `R_req := N_SP` (the probe nonce) and `N_SP < N_carry` strictly. `CarrySkip` may wrap either a `CPoCSkipResponse` (refusal) or a `CPoCProbeResponse` (status). If the carried outcome is `ready`, steps (2–3) of the Verdict predicate do not apply (no refusal to evaluate); the carry is instead recorded as a **scheduling receipt** consumed by case **C13**.
   **Worked examples** (let `N_slots = 4`, `V`'s slot `SP_v = v_slot = 2`):

  | `R_req`           | `SP_e` | branch                             | `N_carry` | `round(R_req)` | `X` | `h_X` | `h_carry` | Result                                                                                            |
  | ----------------- | ------ | ---------------------------------- | --------- | -------------- | --- | ----- | --------- | ------------------------------------------------------------------------------------------------- |
  | 10                | 2      | `SP_v = SP_e` (V **is** executor)  | 10        | 2              | 10  | 500   | 500       | pass; `I = {500}` (evaluate `Schedule(H_i, 500)` in step 3)                                       |
  | 10                | 2      | `SP_v = SP_e`                      | 13        | 2              | 10  | 500   | 500       | pass; same block ⇒ `I = {500}`                                                                    |
  | 10                | 2      | `SP_v = SP_e`                      | 40        | 2              | 10  | 500   | 520       | pass; `I = [500, 520]` — step 3 seeks any `H` in that interval on the cPoC schedule               |
  | 11                | 3      | `SP_v < SP_e` (same round)         | 40        | 2              | 10  | 500   | 520       | `X = 11 − 1 = 10` (same round as `R_req`); `I = [500, 520]`                                       |
  | 9                 | 1      | `SP_v > SP_e` (**previous** round) | 40        | 2              | 6   | 498   | 520       | `X = 9 − 3 = 6` (round 1, not round 2); `h_X = 498` observed **before** `R_req`; `I = [498, 520]` |
  | 1                 | 1      | `SP_v > SP_e`, `round = 0`         | 10        | 0              | —   | —     | —         | **bootstrap edge case**: no previous round ⇒ fall back to session-start anchor as `h_X`           |
  | 10                | 2      | —                                  | 9         | —              | —   | —     | —         | **fail (causality) → Invalid carrier**                                                            |
  | 10 (probe `N_SP`) | 2      | `SP_v = SP_e`                      | 13        | 2              | 10  | 500   | 500       | pass; `I = {500}` (Path B; `N_SP < N_carry` strictly)                                             |

2. **Role check.** `H_i ∉ PoC_slot_set`. Otherwise → `Invalid` (host had `POC_SLOT = true`, must not skip).
3. **Schedule check over interval `I`.**
  - `∃ H ∈ I : Schedule(H_i, H) ∈ {prepare, active}` → **candidate** `Valid` (subject to (4)). The host was legitimately on cPoC at **some** height V personally witnessed in `I`; that is sufficient.
  - `∀ H ∈ I : Schedule(H_i, H) == idle` → **candidate** `Invalid` (subject to (4)). The host claims cPoC refusal but is not on the schedule at any height in `I`.
4. **Height freshness at ingest.** If the endpoints of `I` (`h_X` and `h_carry`) are **strictly confirmed** by the height-sync layer (assumption **1**), commit to the candidate from (3). If the height-sync layer flags either endpoint as **not yet strictly confirmed**, and the schedule verdict is adversarial (`Invalid`), V **MUST** hold the verdict as `Inconclusive` until height sync reports confirmation covering `I` — then re-run step (3). **This could be scheduled for future releases**
5. **Signature / binding.** `CPoCSkipResponse` must be validly signed by `H_i` and reference `R_req` as it appears in `Diff`.

Outputs feed **Gossip minimization** (below) and, for disputes, **[FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md](./FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md)**.

---

## Cases to handle (case / dataflow)

Legend: `R_req` = Path-A inference-request nonce (or, in Path B, aliased to the probe nonce `N_SP`); `N_carry` = nonce at which `CarrySkip` is appended to `Diff`; both paths have `R_req < N_carry` strictly. `R` denotes the executor round of size `N_slots`.

### C1 — Honest skip, honest developer (happy path)

**Setup:** `Schedule(H_i, H(V)) = active`, `H_i ∉ PoC_slot_set`, dev behaves normally.

**Flow:**

```
D → H_i       : InferenceRequest(R_req)
H_i → D       : CPoCSkipResponse(R_req, active)
D → H_{i+1}   : next InferenceRequest at R_req+1 carrying skip blob
                (or separate CarrySkip at some N_carry ≥ R_req)
V (= any host): on Diff[N_carry] → Verdict = Valid (nonce window + schedule)
```

**Expected verdict:** `**Valid`**. No gossip, no finalization trigger.

### C2 — Malicious host, fake skip

**Setup:** `Schedule(H_i, H(V)) = idle`, `H_i ∉ PoC_slot_set`, but `H_i` replies `CPoCSkipResponse` to avoid work.

**Flow:** Same as C1 up to the point the developer publishes `CarrySkip`. Each host `V` then:

```
V on Diff[N_carry]:
  compute Verdict(...) = Invalid                         # Schedule check fails at I
  emit CPoCVote(N_carry, verdict = Invalid, signed_by=V) # p2p to D (and optionally gossip)
D collects CPoCVote messages from distinct hosts:
  if |votes(Invalid)| ≥ quorum_invalid:
    verdict is settled as Invalid
    D hands the vote bundle to finalization (today)
    — OR —
    hosts publish votes at the next finalization round (future release; see § Consensus / voting)
```

**Expected verdict:** **`Invalid`** (Schedule check fails on the height interval `I`). The `Invalid` outcome is not attached to finalization by one party; it is the **quorum of `CPoCVote`s** from hosts that observed `Diff[N_carry]` and independently reached the same verdict. See § Consensus / voting for the vote-collection protocol and the "developer today / self-finalization tomorrow" split.

### C3 — Developer late carry (genuine skip, late)

**Setup:** `H_i` returned a legitimate `CPoCSkipResponse` at `R_req` during its cPoC window (height `H_skip`). Developer holds the blob for arbitrarily many rounds and later emits `CarrySkip` at `N_carry ≫ R_req`.

**Flow:**

```
D → devshard     : MsgStartInference(R_req)              # during H_i's cPoC window
H_i → D          : CPoCSkipResponse(R_req, active)        # p2p, signed by H_i at H_skip
... time passes; Diff advances; mainnet advances past H_skip ...
D → devshard     : CarrySkip(N_carry, CPoCSkipResponse)   # late carry
V on Diff[N_carry]:
  SP_e = R_req mod N_slots; SP_v = v_slot
  X = R_req − ((SP_e − SP_v) mod N_slots)     # same round if SP_v ≤ SP_e, else previous round
  h_X    = height_at[X]   (≈ H_skip — V's height observed at or before R_req)
  h_carry = H(V) at ingest of Diff[N_carry]
  I = [H_skip, h_carry]; Schedule(H_i, H_skip) ∈ {prepare, active} ⇒ step 3 passes
  Verdict = Valid
```

**Expected verdict:** `**Valid`**. The host's attestation is truthful for a height in `I`; lateness does not retroactively make it a lie. Any residual harm (inference record kept open, stalled settlement) is handled at the **settlement layer** (`MsgTimeoutInference{…CPOC}` and finalization deadlines), **not** by the cPoC verdict predicate.

### C3' — Causality failure (forged carry)

**Setup:** Developer publishes a `CarrySkip` with `N_carry < R_req` (references a request that has not yet entered `Diff`).

**Flow:** Step (1) of the verdict predicate rejects the envelope on the causality inequality `R_req ≤ N_carry`.

**Expected verdict:** `**Invalid`** against the **carrier** (developer signature on `CarrySkip`), **not** against `H_i`. This is a pure forgery check, independent of any height interval.

### C4 — `POC_SLOT = true` host returns skip

**Setup:** `H_i ∈ PoC_slot_set` (inference-exempt during others’ cPoC), yet replies `CPoCSkipResponse`.

**Flow:** any normal request/response leading to a carried skip.

**Expected verdict:** **`Invalid`** (Role check fails). Verdict is settled by **vote quorum** (see C2 / § Consensus / voting): every host computes the same `Invalid` and emits `CPoCVote`; the collected bundle is the evidence handed to slashing (`H_i`).

### C5 — Skip during `prepare` window

**Setup:** `Schedule(H_i, H(V)) = prepare` (policy-dependent).

**Decision:** Same verdict rules as `active`.

### C6 — Inconclusive due to height uncertainty

**Setup:** `Schedule(H_i, H(V)) = idle`, but height-sync layer has not yet **strictly confirmed** `H(V)` for the nonce-window (out of scope for us — we only consume its signal).

**Flow:** Verdict step (4) returns `Inconclusive`.

**Expected action:** `V` **does not** emit a `CPoCVote` yet; it waits for the height layer to confirm. If confirmed **Invalid**, `V` emits `CPoCVote(Invalid)` and the standard vote-quorum flow (§ Consensus / voting) collects the bundle. If confirmed **Valid**, no vote is emitted and no action is taken.

### C7 — Skip probe (Path B), refusal outcome

**Setup:** `D` wants a cPoC status check from `H_i` without submitting a prompt. `Schedule(H_i, H) ∈ {active, prepare}` at the height the probe is answered.

**Flow:**

```
D  → devshard  : MsgSkipProbe(N_SP, target = H_i)          # into Diff at N_SP
H_i → D        : CPoCProbeResponse(N_SP, outcome ∈
                   {cpoc_active, cpoc_prepare})            # p2p, signed by H_i
D  → devshard  : CarrySkip(N_carry, CPoCProbeResponse)     # into Diff at N_carry > N_SP
V on Diff[N_carry]:
  R_req := N_SP
  run the Verdict predicate (steps 1–5) unchanged
```

**Expected verdict:** `**Valid`** (same predicate as Path A, applied with `R_req := N_SP`).

### C7' — Skip probe (Path B), ready outcome

**Setup:** `D` probes `H_i`. `H_i` has **finished** its cPoC window and is in `READY_INFERENCE` (`Schedule(H_i, H) = idle` at the answering height).

**Flow:**

```
D  → devshard  : MsgSkipProbe(N_SP, target = H_i)
H_i → D        : CPoCProbeResponse(N_SP, outcome = ready)  # p2p, signed by H_i
D  → devshard  : CarrySkip(N_carry, CPoCProbeResponse)     # into Diff at N_carry > N_SP
V on Diff[N_carry]:
  detect payload_kind = probe_response AND outcome = ready
  record scheduling receipt: ready_at[H_i] = (N_carry, h_carry)
  Verdict predicate steps (2–3) do NOT apply (no refusal to evaluate)
```

**Expected verdict:** **not applicable.** The carry is a **scheduling receipt**, not a skip attestation. It obliges the developer to resume routing real `MsgStartInference` to `H_i` at subsequent `H_i`-slot nonces. Persistent deviation after this receipt triggers **C13**.

### C8 — No response at all (timeout)

**Setup:** `H_i` returns nothing (neither inference nor skip).

**Expected action:** Out of scope of cPoC-skip verdict. Governed by `**USER_TIMEOUT`** in [FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md](./FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md). cPoC protocol contributes **no** verdict in this case.

### C9 — Low-load vote collection (explicit gossip)

**Setup:** After `timeout_skip_gossip` the diff has not advanced one full round, so not every `V` has necessarily seen the carried skip and the vote collector (see § Consensus / voting) has not yet reached `quorum_invalid`.

**Flow:**

```
V1 emits SkipEvidenceGossip(Diff-refs) to peers           # lagging peers catch up on Diff
peers reconstruct Diff-refs, compute Verdict locally,
  and emit CPoCVote if their verdict is non-Valid
collector (D today, or finalization round in future) aggregates votes
```

**Expected verdict:** whatever the vote quorum declares on the same `Diff` evidence. `SkipEvidenceGossip` is a **delivery** aid only; it does not compute a verdict, it just makes the same `CarrySkip` visible so lagging peers can vote.

### C10 — High-load round elision

**Setup:** High request rate; the diff naturally advances past `R_req + N_slots` within `timeout_skip_gossip`.

**Expected action:** No `SkipEvidenceGossip` emission needed; every `V` has the evidence by construction. Each `V` independently computes `Verdict` and, if non-`Valid`, emits `CPoCVote`. The collector aggregates votes as usual.

### C11 — Dispute-grade evidence bundle

**Setup:** A verdict is `Invalid` (C2, C4, C6-confirmed-invalid, C3', or C13).

**Flow:** Once `quorum_invalid` is reached, the collector assembles an **evidence bundle** consisting of: (i) the refs into `Diff` for `MsgStartInference` / `MsgSkipProbe`, `CarrySkip`, and (for C13) the `H_i`-slot window; (ii) the set of `CPoCVote` messages achieving quorum; (iii) the relevant schedule inputs (`PoC_slot_set`, `Schedule` at heights in `I`). This bundle is handed to [FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md](./FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md) for inclusion in the finalization bundle for mainnet — the bundle is the input to slashing.

### C12 — Executor / schedule desync (verifier bug)

**Setup:** `V` has stale `PoC_slot_set` or wrong epoch schedule (not the network majority view).

**Expected behavior:** `V` is at fault for mis-verdict; this is a **node-operator** / epoch-refresh issue, **not** host fault. Recovery belongs to the schedule/epoch layer (out of scope). The protocol must log the conflict so operators can detect it; it must **not** penalize `H_i` when only an outlier `V` disagrees.

### C13 — Developer withholds work from a ready host (routing misbehavior)

**Setup:** Some host `H_i` has signalled `ready` (either via `CPoCProbeResponse(outcome = ready)` carried in `Diff` at some nonce `N_ready`, or because `Schedule(H_i, H) = idle` across the last `W_ready` mainnet blocks that every verifier strictly confirms). The developer is nonetheless **not** routing real inference to `H_i`:

- at nonces where `executor(n) = H_i` (i.e. `n mod N_slots = i`), `D` keeps sending `MsgSkipProbe(target = H_i)` rather than `MsgStartInference`, **or**
- `D` stops emitting messages at `H_i`-slot nonces altogether while continuing to send to other slots.

**Observation (at each `V`).** `V` counts, over a trailing window of `W_fair` rounds ending at the current nonce:

- `n_inf(H_i)` = `MsgStartInference` entries with `executor(n) = H_i`,
- `n_probe(H_i)` = `MsgSkipProbe` entries targeted at `H_i`,
- whether `H_i` is `ready` (per `ready_at[H_i]` receipt **or** `Schedule(H_i, H) = idle` for every `H ∈ [h_start_window, H(V)]`).

**Violation predicate.** `ready(H_i)` ∧ `n_probe(H_i) + n_miss(H_i) ≥ θ_fair` ∧ `n_inf(H_i) < θ_min_inf` — i.e. over the window, `D` sent probes or left `H_i`-slots empty at least `θ_fair` times while sending fewer than `θ_min_inf` real inferences to `H_i`, despite `H_i` being ready. Exact values `(W_fair, θ_fair, θ_min_inf)` are **chain-parametrized** (TBD; see Open questions).

**Flow:**

```
1. Diff[N_ready] : CarrySkip wrapping CPoCProbeResponse(outcome=ready) for H_i
   → every V records ready_at[H_i] = (N_ready, h_ready)

2. Nonces N_ready+1 … N_ready+W_fair·N_slots advance:
   V tallies n_inf(H_i), n_probe(H_i) at H_i-slot nonces from Diff

3. Violation predicate fires at V:
   V enters "withholding-alert" state for (D, H_i)

4. Downstream enforcement: every V that is itself a future executor for D
   refuses to serve D's requests (returns a new p2p signal
   `RouteFairnessRefusal(D, H_i, evidence_refs)`) until:
     (a) D issues MsgStartInference(executor = H_i) AND H_i confirms it (MsgConfirmStart),
     OR
     (b) H_i re-enters cPoC (signals active/prepare via a fresh CPoCProbeResponse
         or via Schedule(H_i, H) transitioning back to {active, prepare}).

5. When (a) or (b) holds, V clears the withholding-alert and resumes serving D.
```

**Expected verdict:** `**Invalid` against the developer**, not against any host. Evidence: `ready_at[H_i]` receipt + the `H_i`-slot window of `Diff` showing probes / empty slots but no inference requests.

**Why enforcement sits with "next hosts".** The only actor that can credibly deny D further service is the host queued to execute D's next request. If those hosts refuse until D resumes fair routing, D has a direct economic incentive to stop withholding. No mainnet round-trip is required in the hot path; the decision is local at each `V` from the same `Diff` contents, so every honest host reaches the same alert.

**Open parameters (deferred to Open questions):**

- `W_fair`, `θ_fair`, `θ_min_inf` thresholds.
- Whether a `ready_at` receipt decays after the host re-enters cPoC (presumably yes — once `Schedule(H_i, H) = active` again, old receipts are cleared).
- Precise wire format of `RouteFairnessRefusal` and whether it also lands in `Diff` as evidence for slashing D's stake.

---

## Consensus / voting

Every verifier `V` computes the **Verdict predicate** (§ Data flow) independently against its local view of `Diff` and `H(V)`. When `Verdict ∈ {Invalid, Inconclusive-pending-confirmation}` (or a C13 developer-withholding alert fires), `V` signs and emits a **`CPoCVote`** addressed to the vote collector for that `N_carry`. A verdict is **settled** for finalization only after a **quorum** of independent votes has been collected; an individual verifier's opinion, by itself, slashes nobody.

### `CPoCVote` (new p2p message, then into finalization bundle)

| Field | Meaning |
| ----- | ------- |
| `N_carry` | Nonce of the `CarrySkip` this vote refers to (or, for C13, the earliest `Diff` reference in the evidence window). |
| `referenced_nonce` | `R_req` or `N_SP`, copied from the carry; lets the collector filter duplicates. |
| `target` | Kind-and-identity of the actor being voted against: `host(H_i)` for C2/C4/C6, `carrier(D)` for C3', `developer(D)` for C13. |
| `verdict` | `Invalid` (most common). `Valid` votes are implicit — honest verifiers simply don't emit a vote — so no `Valid` voting channel is required. |
| `reason_code` | Machine-readable pointer to which predicate step failed (`schedule_fail`, `role_fail`, `causality_fail`, `withholding`, `height_confirmed_invalid`, …). |
| `schedule_witness` | `(H*, Schedule(H_i, H*))` for the height in `I` the verifier consulted, so the bundle is self-contained for slashing. |
| `signature` | Host signature under domain `cPoCVoteContent` (binds all fields above). |

A single `CPoCVote` is cheap; the flood size is bounded because only verifiers with a non-`Valid` local verdict emit one, and every one is a pointer into existing `Diff` entries.

### Collector: today vs. future

**Today (this release)** — the **developer** `D` is the vote collector. This is natural because:

- `D` already owns the `CarrySkip` envelope and knows exactly which `N_carry` the vote refers to.
- `D` is the economically interested party in host-fault cases (C2 / C4 / C6): a malicious host means `D` didn't get served.
- The collector role for `D` is **not** in conflict with slashing — `D` never collects votes *against itself*; those (C3', C13) are collected by the **next executor host for `D`** (see below).

Collection procedure:

1. Each `V` with a non-`Valid` verdict sends `CPoCVote` to `D` via p2p (optionally piggy-backed on the same channel that carries `SkipEvidenceGossip`).
2. `D` aggregates distinct signatures until `|votes(Invalid)| ≥ quorum_invalid`.
3. `D` attaches the bundle to finalization per [FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md](./FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md). The vote bundle is the input to slashing.

**Collector when `D` is the target** (C3' forged carry, C13 developer withholding). `D` cannot be trusted to collect votes against itself. In these cases the collector is the **next executor host for the same session** — i.e. the host at `executor(N_next) = hosts[N_next mod N_slots]` that would serve `D`'s next request. That host is naturally available, already reads the same `Diff`, and is the party already enforcing C13 via `RouteFairnessRefusal`. Exact handoff to finalization is the same as in the developer-as-collector case.

**Future release (self-finalization).** When developer-independent self-finalization is specified (TBD, see finalization docs), the collector role moves to the **finalization round** itself:

- Each `V` still emits `CPoCVote` on the standard channel; no change at the host level.
- The finalization round aggregates votes across the session at a deterministic boundary, without requiring `D` to be online or honest.
- This removes the failure mode "`D` stops sending requests and therefore never submits the vote bundle" (which today would leave an `Invalid` verdict unclaimed).
- Migration is transparent to verifiers: the `CPoCVote` wire format is unchanged; only the collector location moves.

### Quorum, weighting, tie-breaks

Exact values — `quorum_invalid` (e.g. simple-majority vs. 2/3 stake-weighted), tie-break rules, stake weighting, and the mapping from votes to mainnet slashing amounts — must match the finalization / slashing layer. These are **chain-parametrized** and **deferred** to [FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md](./FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md) and the mainnet slashing spec. This doc only guarantees:

- Every honest `V` reaches the **same** verdict from the **same** `Diff` + strictly-confirmed height slice (by construction of the Verdict predicate).
- Dishonest minority votes cannot flip a correct quorum, because `CPoCVote` includes the `schedule_witness` and is auditable at finalization time (a dishonest vote is itself slashable).

---

## Next steps to formalize (roadmap)

Work should proceed in **dependency order** so implementers can parallelize after interfaces are fixed.


| Phase                                   | Goal                                                           | Primary output                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| --------------------------------------- | -------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **A — External contracts**              | Freeze how cPoC **consumes** other subsystems.                 | Pointer to a **height oracle** `H(V)` (provided by [HEIGHT_SYNC_HEADERS_PROPOSAL.md](./HEIGHT_SYNC_HEADERS_PROPOSAL.md) — treated as a black box here); on-chain / config **queries** for `PoC_slot_set` and **cPoC schedule** `Schedule(host, H) → {idle, prepare, active}` (exact module path TBD in chain spec).                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| **B — Wire formats**                    | Make bytes unambiguous, reusing existing proto where possible. | (i) Define **new p2p** `CPoCSkipResponse` (Path A refusal, reason ∈ {cpoc_active, cpoc_prepare}) + `cPoCRefusalContent` signing domain; (ii) define **new p2p** `CPoCProbeResponse` (Path B, outcome ∈ {cpoc_active, cpoc_prepare, ready}) + `cPoCProbeResponseContent` signing domain; (iii) define **new Diff** `CarrySkip` message with `payload_kind ∈ {skip_response, probe_response}` and `host_response` bytes, wire it into `SubnetTx` oneof (developer signature under `CarrySkipContent` binds `N_carry`, `referenced_nonce`, `payload_kind`, `host_response`); (iv) define **new Diff** `MsgSkipProbe` (probe_nonce, target_host_id, session/routing, no prompt) and wire it into `SubnetTx`; (v) extend `TimeoutReason` enum in `subnet/proto/subnet/v1/tx.proto` with `TIMEOUT_REASON_CPOC` (settlement only); (vi) define **new p2p** `RouteFairnessRefusal` (C13 enforcement signal; fields: developer_id, target_host = H_i, evidence_refs into `Diff`); (vii) define **new p2p** `CPoCVote` + `cPoCVoteContent` signing domain (fields per § Consensus / voting); (viii) optional off-diff `SkipEvidenceGossip` schema. Field-by-field signing rules and max sizes per message. |
| **C — Diff / height-interval calculus** | Lock the freshness rule.                                       | Prove correctness of the height-interval `I = [h_X, h_carry]` under Shared assumptions (item **5**) and independence across verifiers (two verifiers with different `X` and `h_X` must never produce contradictory `Valid`/`Invalid` on heights both have strictly confirmed); prove that a late carry of a genuine skip stays `Valid` and that no schedule-incompatible skip can pass; fix signing-input domain separators for `R_req` vs `N_carry`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| **D — Host state machine**              | Implementable control flow.                                    | Diagram + transitions: `READY_INFERENCE` → `PREPARE_cPoC` → `cPoC_ACTIVE` → `READY_INFERENCE`; guards; legal skip response windows; timeouts.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         |
| **E — Verdict + vote bundling + escalation** | Map local verdicts to settled outcomes.                   | Finalized `Verdict` predicate (§ Data flow); `CPoCVote` emission rules (§ Consensus / voting); collector role for developer (today) and next-executor-host (when developer is the target); mapping from vote quorum to the evidence bundle handed to [FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md](./FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md); `Inconclusive` handling (no vote emitted until height oracle confirms); deferred self-finalization collector path for future releases. |                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            |
| **F — Tests & adversarial catalog**     | Confidence.                                                    | Table-driven tests covering **C1…C13** (including `ready`-outcome probes **C7'** and developer-withholding **C13**) plus cryptographic edge cases (bad signature, wrong session, wrong epoch).                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |


**Immediate engineering order:** **A** (1-page chain-side stubs for `H(V)`, `Schedule`, `PoC_slot_set`) → **B** (proto draft) → **C** (nonce lemmas) → **D** → **E** → **F**.

---

## Protocol skeleton (v0.2 — height-sync-independent)

Normative names/state/messages/predicates for the cPoC protocol as specified above, with height sync explicitly abstracted behind `H(V)`.

### Predicates


| Name                                              | Meaning                                                                                                                                                |
| ------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `**Schedule(host, H) ∈ {idle, prepare, active}`** | Imported from chain / epoch state. `active` ⇒ must run cPoC; `idle` ⇒ must serve inference (absent `POC_SLOT`); `prepare` ⇒ policy-dependent (see C5). |
| `**HasPoCSlot(host)**`                            | `host ∈ PoC_slot_set`.                                                                                                                                 |
| `**Verdict(evidence)**`                           | Implements Data-flow § Verdict predicate; returns `Valid                                                                                               |


### Inputs to `Verdict` (closed set)

1. `(R_req, N_carry)` — nonces from `Diff`.
2. `H_i` — target host identity (derivable from `N_carry mod N_slots` or the signed response).
3. `H(V)` — verifier height oracle (black-box input; see Shared assumptions, item **1**).
4. `PoC_slot_set` and `Schedule(H_i, H(V))` — chain state at `H(V)`.
5. Signatures: host signature on the embedded host response (domain `cPoCRefusalContent` for `CPoCSkipResponse` / Path A, domain `cPoCProbeResponseContent` for `CPoCProbeResponse` / Path B) and developer signature on the enclosing `CarrySkip` (domain `CarrySkipContent`); for Path B, developer signature on `MsgSkipProbe` also binds the target host.

### Outputs

Each verifier `V` produces a **local verdict**; a verdict only becomes **settled** after the `CPoCVote` quorum is reached (§ Consensus / voting).

- `Valid` (local) — **no vote emitted**, no action. Applies when the carried host response is a refusal consistent with the schedule (happy C1 / refusal C7), or a plain late carry **C3**. Absence of an `Invalid` quorum is how the network records "nothing to settle".
- `Invalid` (local) — `V` emits `CPoCVote(Invalid, target, reason_code, …)` to the collector. Quorum of such votes settles the verdict and produces the evidence bundle for finalization. Applies to C2, C4, strictly-confirmed C6, the causality-fail variant **C3'** (Invalid against carrier), and **C13** (Invalid against developer for withholding — collector is the next executor host, not `D`).
- `Inconclusive` (local) — **do not** emit a vote yet; wait for the height oracle to stabilize (C6 unconfirmed). Re-run the predicate when confirmation arrives and, if it resolves to `Invalid`, emit `CPoCVote` at that point.
- **`NotASkip`** (scheduling receipt) — the `CarrySkip` wraps a `CPoCProbeResponse(outcome = ready)`. The verdict predicate does not fire; V records `ready_at[H_i]` and arms the C13 detector. No vote emitted. Case **C7'**.

---

## Open questions (for formalization)

1. `**PoC_slot_set` provenance:** set at escrow init (immutable) vs queried post-init and cached. Different failure modes.
2. `**prepare` policy:** is skip allowed while `Schedule = prepare` (treat like `active`) or forbidden (treat like `idle`)? Chain-spec flag `skip_allowed_during_prepare`.
3. **Signing input** domain separators: `cPoCRefusalContent` (host signature on `CPoCSkipResponse`, binds `inference_id` + `reference_nonce` + reason), `cPoCProbeResponseContent` (host signature on `CPoCProbeResponse`, binds `probe_nonce` + `reference_nonce` + outcome), `CarrySkipContent` (developer signature on `CarrySkip`, binds `N_carry` + `referenced_nonce` + `payload_kind` + `host_response` bytes), and the signing input for `MsgSkipProbe` (binds `probe_nonce = N_SP` + `target_host_id`).
4. **Evidence-object layout** for finalization (list of `Diff`-refs, signatures, schedule-witness); shared with [FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md](./FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md).
5. **C13 thresholds `(W_fair, θ_fair, θ_min_inf)`** for the developer-withholding predicate: how many `H_i`-slot nonces of probes / empty slots vs. real inferences, over how many rounds, qualify as misbehavior? Must be tuned so that legitimate brief probing (e.g. a single confirmation probe right after `ready` before resuming inference) does not trigger alerts.
6. `**ready_at` lifecycle.** When exactly does a `ready` receipt for `H_i` expire? Candidates: (a) on the first strictly-confirmed `Schedule(H_i, H) ∈ {active, prepare}` after the receipt; (b) on any subsequent non-`ready` `CPoCProbeResponse` / `CPoCSkipResponse` for `H_i` carried in `Diff`; (c) a hard TTL in mainnet heights. Likely all three with `(a) ∨ (b) ∨ (c)`.
7. `**RouteFairnessRefusal` surface.** Is this purely a p2p refusal signal between hosts, or must it also land in `Diff` as a signed artefact so mainnet can slash `D`? If the latter, it becomes another `SubnetTx` variant and needs its own signing domain.
8. **Roundtrip-free Path B (future release).** Can the `MsgSkipProbe` → p2p response → `CarrySkip` roundtrip be eliminated by having hosts publish their cPoC state directly — e.g. a signed host-status `SubnetTx` entry or a chain-level heartbeat — so verifiers read state without a developer probe? Requires (i) wire format for host-published status, (ii) a staleness / TTL bound to prevent silent "assume ready" failures, (iii) reconciling with `ready_at[H_i]` and the C13 detector. Explicitly **out of scope for the current release.**
9. **Vote quorum parameters.** `quorum_invalid` (simple majority vs. 2/3 stake-weighted), whether votes are counted per-host or stake-weighted, tie-break rules, and a liveness timeout for the collector to declare "no quorum reached, treat as `Valid`" are chain-parametrized and deferred to the finalization / slashing spec.
10. **Self-finalization collector (future release).** When developer-driven vote collection is replaced by a finalization-round collector, we need: (i) a deterministic boundary condition that triggers vote aggregation (block height, session sealing, etc.); (ii) handling for late-arriving votes across the boundary; (iii) a migration story so older nodes emitting `CPoCVote` to `D` still compose with newer collectors. The wire format of `CPoCVote` itself should not need to change — only the destination moves. Explicitly **out of scope for the current release**; see § Consensus / voting.
11. **Collector handoff when `D` is the target (C3' / C13).** The current design says "the next executor host for the session" collects votes against `D`. Is that host identity fixed ahead of time (next `n mod N_slots` after the triggering `N_carry`), chosen by stake, or rotated? And what happens if that host is itself offline — does the role pass to the one after it? The deterministic rule should be written down alongside C13.

---

## Related documents

- [HEIGHT_SYNC_HEADERS_PROPOSAL.md](./HEIGHT_SYNC_HEADERS_PROPOSAL.md) — **out of scope** for this doc; supplies `H(V)` as a black-box oracle.
- [FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md](./FINALIZATION_COLLECTOR_PROTOCOL_PROPOSAL.md) — consumes `Invalid` verdicts, decides inclusion in finalization bundles.

---

## Document history


| Version       | Note                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| ------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| draft         | Problem split, protocol intent, gossip optimization, height-sync binding.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| v0.1-skeleton | Added formalization **roadmap**, **protocol skeleton** (roles, state, messages, predicates), engineering order.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| v0.2          | **Height sync explicitly out of scope** (consumed as black-box `H(V)`). Added formal **Data flow** (parties, state, primitives, verdict predicate) and enumerated **cases C1…C12** with per-case flows.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| v0.2.1        | Aligned primitives with existing `subnet/proto/subnet/v1/{tx,diff}.proto`: skip attestation maps to `**MsgTimeoutInference{reason=TIMEOUT_REASON_CPOC}`** (new enum value) carrying a host `TimeoutVote`; probe maps to new `**MsgSkipProbe**`. Earlier illustrative names kept in diagrams with a mapping note.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               |
| v0.2.2        | Restored `**CPoCSkipResponse**` (p2p host → dev) and `**CarrySkip**` (Diff envelope signed by dev) as **first-class new messages**. High-level **two-path** section (Path A full inference vs Path B probe) added before Nonce binding. `**MsgTimeoutInference{…CPOC}`** demoted to **settlement-only** (not required for the verdict). Signing domains tightened: `cPoCRefusalContent` and `CarrySkipContent`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| v0.2.3        | **Replaced the nonce-window freshness rule `N_carry − R_req < N_slots` with a height-interval rule** `I = [h_X, h_carry]`, where `X` is verifier V's own executor nonce in the round of `R_req`. Rounds can be faster than mainnet blocks, so a nonce-count bound was incorrect. Step 3 now does `∃ H ∈ I : Schedule(H_i, H) ∈ {prepare, active}`. Case **C3** (late carry of a genuine skip) is now `**Valid`**; new **C3'** covers causality forgery `N_carry < R_req`. Problem statement §2 (developer replay) restated as a settlement concern, not a cPoC-verdict concern.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| v0.2.4        | **Corrected the witness-nonce formula `X`.** Earlier drafts used `X = round(R_req) · N_slots + v_slot` unconditionally, which fails when `SP_v > SP_e` (V's slot in `R_req`'s round is *after* `R_req`, so `height_at[X]` would be observed *after* `R_req` and cannot lower-bound `H_skip`). New rule: `X` is the **latest nonce ≤ `R_req`** whose executor is `V` — same round when `SP_v ≤ SP_e`, previous round when `SP_v > SP_e`. Closed form: `X = R_req − ((SP_e − SP_v) mod N_slots)`. Added bootstrap edge case (`round(R_req) = 0` with `SP_v > SP_e`). Updated Notation, Nonce-binding section, verdict-predicate step 1, worked-examples table (now covers both branches and the bootstrap case), and C3 pseudocode.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              |
| v0.2.5        | **Removed the "`X` not yet in `Diff`" deferral.** Because the new rule guarantees `X ≤ R_req`, and causality requires `R_req ≤ N_carry`, we have `X ≤ N_carry`. `Diff` is append-only and ingested in order, so `Diff[X]` is always already present when `Diff[N_carry]` is being processed. `h_X` is therefore always computable at step 1. `pending_verdicts` reasons reduced from three to two (`Inconclusive` height-confirmation, `Invalid` gossip-deadline). The only residual lower-bound fallback is the bootstrap case `round(R_req) = 0 ∧ SP_v > SP_e`, resolved via the session-start anchor rather than by waiting.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| v0.2.8        | **Replaced "evidence retained and attached to finalization" with an explicit `CPoCVote` quorum.** Every verifier with a non-`Valid` local verdict signs a `CPoCVote` (new p2p message, signing domain `cPoCVoteContent`) and sends it to the collector; only a quorum of votes settles the verdict for finalization. Collector today = developer `D` for host-fault cases (C2/C4/C6), and the **next executor host** for developer-fault cases (C3'/C13). Future release = finalization round aggregates votes independently of `D`'s liveness (prepares for self-finalization). § Consensus / voting rewritten from placeholder to a full protocol sketch. Cases C2, C4, C6, C9, C10, C11 rewritten to consume / produce `CPoCVote`. `SkipEvidenceGossip` clarified as a delivery aid only, not a verdict channel. Primitives table gained `CPoCVote`. Phase B of the roadmap added `CPoCVote` wire format; Phase E retitled "Verdict + vote bundling + escalation". Skeleton Outputs updated to distinguish local verdict from settled verdict. Open questions extended with vote-quorum parameters (§9), self-finalization migration (§10), and the `D`-is-target collector handoff rule (§11).
| v0.2.7        | Added a **future-optimization note** after the Path B flow: the explicit `MsgSkipProbe` → p2p `CPoCProbeResponse` → `CarrySkip` roundtrip can be replaced by a passive read of host-published cPoC state (signed `SubnetTx` entry or chain-level heartbeat) in a later release, eliminating the probe roundtrip entirely. Open questions extended with item 8 describing the prerequisites (wire format, staleness bound, reconciliation with `ready_at`). Explicitly out of scope for this release.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| v0.2.6        | **Path B is a proper two-entry flow.** Removed the "degenerate `N_SP = N_carry`" claim from every location (notation table, verdict predicate step 1, worked examples, case C7, invariants section). Both paths now strictly have `R_req < N_carry` (or `N_SP < N_carry`). **Host probe response has two outcomes** — introduced a distinct `**CPoCProbeResponse`** (p2p) for Path B with `outcome ∈ {cpoc_active, cpoc_prepare, ready}`, signed under a new `cPoCProbeResponseContent` domain; `CarrySkip` gains a `payload_kind` discriminator and can wrap either a Path-A `CPoCSkipResponse` or a Path-B `CPoCProbeResponse`. A `ready` outcome carried into `Diff` is **not** a skip — it is a **scheduling receipt** (`ready_at[H_i]`) that arms case **C13**. Old C7 split into **C7** (refusal) and new **C7'** (`ready` receipt). **New case C13 — developer withholding from a ready host**: developer keeps probing / skipping a host that has signalled `ready`. Verifiers detect via a local `Diff` tally over a trailing window (thresholds TBD) and future executors issue `RouteFairnessRefusal` to deny `D` further service until fairness is restored. Local state gained `ready_at` and `withholding_alert`. Roadmap Phase B expanded to include `CPoCProbeResponse`, `CarrySkip.payload_kind`, and `RouteFairnessRefusal`. Skeleton Outputs gained `NotASkip` (scheduling receipt). Open questions extended with C13 thresholds, `ready_at` lifecycle, and `RouteFairnessRefusal` surface. |


