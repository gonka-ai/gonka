# Height sync dataflow (summary)

Short companion to
[`proposals/HEIGHT_SYNC_HEADERS_PROPOSAL.md`](proposals/HEIGHT_SYNC_HEADERS_PROPOSAL.md).
It describes **only** how attestation data moves: **Omit** vs **Anchor (height sync)** vs **Strong (full proof)**.

---

## 1. Two attestation shapes

| | **Height sync (Anchor)** | **Strong sync** |
|---|--------------------------|-----------------|
| **Carries** | Signed **`mainnet_height`** + **`mainnet_height_hash`** (CometBFT `BlockID.Hash` for height `H`) | Same scalars **plus** non-empty **`light_block`** (`tendermint.types.LightBlock`) |
| **`light_block`** | **Empty** (`proof_type == height-anchor-v1`) | **Non-empty**, verified (`proof_type == cometbft-light-block-v1`) |
| **Receiver trust** | If block `H` is local: check hash vs canonical. If not: **deferred** check when the follower reaches `H`. | **Full CometBFT light verification** (`VerifyCommit`, validator set, etc.) + `sender_signature` |
| **Cost** | Small; no large proof on the wire | Large; full consensus proof |

So: **height sync** = “I assert we agree on **(height, block hash)**” with a **sender signature**, without shipping a `LightBlock`. **Strong sync** = the same claim **backed by a verifiable `LightBlock`**.

---

## 2. Every *K* mainnet blocks: periodic Anchor (“round” alignment)

On a fixed schedule (e.g. **`K = 10`**, TBD), parties perform an **Anchor** message: the next applicable user↔host envelope **SHOULD** include section 1 in **Anchor** mode — signed **`H`** and **hash**, **empty** `light_block`. That is the **periodic height sync** tick: a **full turn** in the sense of “both sides refresh the **signed** mainnet **(H, hash)** contract on the wire,” **not** a `LightBlock` on every *K*.

**Between** those Anchor points, section 1 **MUST NOT** carry `mainnet_height`, `mainnet_height_hash`, or `light_block` (**Omit** mode): inference traffic does not restate mainnet height/hash; both sides rely on the **last Anchor** and their **own mainnet listener** to stay in the same block-clock regime.

```text
Mainnet time →
  … Omit, Omit, Omit …  →  Anchor@H (H+hash, signed, no LightBlock)  →  … Omit …
                            ↑
                    every K blocks (+ session/escrow start if policy says so)
```

---

## 3. When to use which mode (from the proposal)

| Situation | Mode | Rationale |
|-----------|------|-----------|
| **Normal** messages **between** Anchors | **Omit** | No per-message mainnet attestation; avoids proof overhead and “trusted window” on every request. |
| **Scheduled** sync every **K** blocks (and **escrow/session start** when policy requires) | **Anchor** | Cheap, signed **(H, hash)** alignment; hash checked immediately or **deferred** until the block is available locally. |
| **Peer’s claimed height vs your aligned height differs by more than `D`** (default **`D = 2`**) | **Strong** | Disagreement too large to trust a bare hash; must show a **verifiable `LightBlock`**. |
| **Finalization, disputes, or policy** requiring consensus-grade evidence | **Strong** | Need full CometBFT proof, not just hash + signature. |

**Rule of thumb:** default path is **Omit** + **Anchor every *K***. **Strong** is the **escalation** for **large skew** (`> D`) or **evidence-grade** needs — **not** the default on every *K* block.

---

## 4. One-line dataflow

```text
Local follower advances H_local  ───────────────────────────────────┐
                                                                      │
Periodic clock (every K blocks) ──► Anchor: send signed (H, hash) ◄──┼──► peer updates anchor state (verify/defer hash)
                                                                      │
Between anchors ───────────────────► Omit: no height in section 1 ─────┘  (both use last Anchor + local follower)

If |H_peer − H_local_aligned| > D  ──► Strong: LightBlock + verify ──► reconcile or reject
```

---

## 5. Pointer

Normative fields, signing input, validation pipeline, timeouts, and escrow/epoch details remain in
[`proposals/HEIGHT_SYNC_HEADERS_PROPOSAL.md`](proposals/HEIGHT_SYNC_HEADERS_PROPOSAL.md).
