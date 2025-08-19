# Overview & Motivation

`ValidateBasic` provides **pure, deterministic, stateless** validation for incoming data (primarily transaction messages) before they touch state. Strong, consistent `ValidateBasic`:

* **Rejects obviously invalid payloads early**, reducing mempool spam and wasted CPU.
* **Improves UX** with fast, deterministic errors that clients/frontends can interpret.
* **Raises safety margins** by preventing malformed inputs from reaching message handlers/keepers.

# Scope & Entities

These are the entities in your chain that MUST define and enforce rigorous `ValidateBasic`-style checks:

1. **All `sdk.Msg` types** (every `Msg*` you expose in modules).
2. **Governance proposal content**

    * Legacy: types implementing `govtypes.Content`.
    * Gov v1/v1beta1: proposal messages (e.g., `MsgSubmitProposal`, module-specific proposal messages) should perform equivalent stateless checks on their payloads.
3. **IBC packet data structs** for any custom IBC apps (e.g., ICS‑20-like packets or bespoke channels) that cross chains.
4. **Evidence and other core types** you introduce that flow through consensus pathways.
5. **Parameters & Genesis**: While typically named `Validate()` (not `ValidateBasic`), they belong in the spec for completeness since they gate initialization and on‑chain updates. They must follow the same **stateless** principles.

> Out of scope for `ValidateBasic`: anything that needs `sdk.Context` or store access (account existence, balances, sequences, fee sufficiency, current block time/height comparisons, param/state reads).

# Validation Requirements (What should be validated)

Apply these requirements **consistently** across all in‑scope entities. Each rule is stateless and deterministic.

## 1) Addresses & Signers

* **Bech32 format**: every address field must be a valid bech32 account/validator address for your chain’s HRP.
* **Required signers present**: any field intended to be a signer must be non‑empty and correctly formatted.
* **No duplicates** where uniqueness is implied (e.g., sender ≠ recipient when protocol forbids self‑sends).

## 2) Identifiers & Numeric Fields

* **IDs (`uint64`/`int`)**: required IDs must be **non‑zero**; enforce upper bounds if your protocol defines them.
* **Amounts (`sdk.Int`, `sdk.Dec`)**: must be non‑nil and satisfy sign/interval constraints:

    * Integers representing quantities must be **positive** (or non‑negative if zero is meaningful).
    * Decimals used as **rates/percentages** must fall within **\[min, max]** (commonly `[0, 1]`).
* **Monotonic or relational constraints** that don’t require state: e.g., `min ≤ max`, `start < end`.

## 3) Coins & Denominations

* **Denom format**: each denom passes `sdk`’s canonical validation rules.
* **Amounts**: each `Coin.Amount` must be **positive** (or ≥ 0 if zero is explicitly allowed).
* **`sdk.Coins` invariants**: sorted, unique denoms, no zero/negative amounts.
* **Denom policy**: if the message only permits certain denoms (e.g., staking token), enforce **allow‑lists** here.

## 4) Strings & Bytes

* **Required strings** are **trim‑non‑empty**.
* **Length caps**: every free‑form string (name, description, memo, metadata, label, memo-like fields) has a documented max length.
* **UTF‑8 validity** for user‑visible text.
* **Character set / regex** where applicable (e.g., resource keys, tag names).
* **Bytes**: explicit **max sizes**; if representing hex/base64/content‑hashes, verify **encoding** and **expected length** (e.g., 32‑byte hashes).

## 5) URIs / URLs / Identifiers (non‑bech32)

* If a field is a URI/URL, require syntactic validity and a maximum length.
* If your protocol limits schemes (e.g., `https:` only) or host patterns, assert that here.

## 6) Temporal / Height Hints

* **Timeouts/epochs/heights** provided in a message must be **> 0** when required.
* Validate **internal relationships** (e.g., `start_time < end_time`), but **do not** compare to current block time/height.

## 7) Enums, Oneof, and Mutually Exclusive Fields

* Enforce that the value is one of the supported enum cases.
* For `oneof`/mode switches, ensure **exactly one** path is set and conflicting fields aren’t simultaneously provided.

## 8) Cross‑Field Stateless Relationships

* Fields that must match in shape/length (e.g., arrays of keys/values) have equal sizes.
* Composite constraints that are purely structural (e.g., sum of weight fractions equals 1 with a tolerance) are enforced if definitional.

## 9) Cryptographic Material

* **Public keys**: expected type, non‑empty, well‑formed bytes.
* **Signatures** are generally verified elsewhere, but if a message embeds static cryptographic commitments or proofs, check **lengths, encodings, and domain tags** as applicable.

## 10) Size & DOS Guards

* Hard **upper bounds** for every variably sized field (strings, bytes, lists).
* Bounded counts: lists/maps must specify **max elements**; empty lists disallowed where nonsensical.

## 11) Governance‑Specific Content

* **Title/description**: non‑empty, UTF‑8, length caps.
* **Payloads/metadata** (e.g., JSON blobs): maximum size; if schema‑constrained, validate schema shape (types/presence) statelessly.
* Any referenced denoms/addresses/percentages obey the same rules above.

## 12) IBC Packet Data (Custom Apps)

* **Addresses, denoms, amounts, memos** follow the same rules.
* **Timeout fields** (height/timestamp): must be non‑zero if required by your app’s semantics.
* **Channel/port identifiers**: format and length according to ICS constraints (stateless).

## 13) Params & Genesis (Validate/ValidateBasic‑equivalent)

* **Parameter bounds**: rates in ranges, durations positive, lists non‑empty where required, denoms valid.
* **Genesis**: no duplicates, IDs start from valid ranges, cross‑object structural consistency without state lookups.

# Error Semantics & Consistency Requirements

* **Deterministic errors**: same input → same error. No panics.
* **Granular error causes**: return precise, human‑readable messages and stable error codes so clients can map them.
* **Canonical limits**: publish module‑wide constants for max lengths, max list sizes, allowed denoms, and numeric bounds so all messages apply the same constraints.
* **Statelessness is mandatory**: no store/context access, no time/height reads, no network calls, no randomness.

# Non‑Goals (Explicitly Not in `ValidateBasic`)

* Account existence, balances, spend limits, sequence/nonce checks.
* Fee sufficiency, signature verification, gas accounting.
* Authorization/permissions tied to on‑chain state or params.
* Comparisons to current block height/time or reading module parameters.

---

If you want, I can turn this spec into a per‑module checklist template next.
