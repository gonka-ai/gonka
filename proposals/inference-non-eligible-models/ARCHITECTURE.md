# Minimal Contract-Backed DevShard Architecture

Status: Draft

## Decision

The architecture consists of two independent components:

1. One Gonka CosmWasm contract instance per model. All hosts serving that model
   store offers in the same contract.
2. A read-only DevShard `ContractBridge`. It reads one contract escrow and maps
   it to a single-host DevShard session.

External scripts deploy the contract and execute all writes. DevShard does not
create or settle contract escrows.

Contract-backed sessions use the DevShard protocol without format changes:

- one host and one slot;
- flat `TokenPrice`;
- the signed diff log and state root;
- the settlement JSON;
- no inference validation.

## DevShard Role

DevShard is not required by the contract. It is reused because it already
provides the off-chain accounting protocol that a direct broker-host design
would otherwise need to recreate:

- ordered owner-signed requests;
- balance reservation before inference;
- host-signed completion with token usage;
- monotonic nonces and replay protection;
- deterministic finalization and refund transitions;
- persistent recovery after restart;
- one final state root and host signature for contract settlement.

Individual inference events remain off-chain. Chain writes are limited to
escrow creation and final settlement. In this architecture, DevShard is a
single-host payment channel rather than a validation shard.

Settlement field semantics are:

- `missed`, `invalid`, `required_validations`, and `completed_validations`
  remain zero because a one-host session has no independent verifier;
- the contract includes all fields when recomputing the settlement root but
  uses only final `cost + fees` for payment.

The timeout-vote protocol cannot mark the only host as missed because executors
cannot verify their own timeout. During finalization, a pending request without
a host receipt is refunded without incrementing `missed`. A started request
with a host receipt is charged at its full reserved cost if no completion was
recorded.

The unused fields remain in the settlement schema to preserve protocol
compatibility. Removing them requires a new protocol version.

## Components

```text
  host transactions             broker transactions
          |                              |
          v                              v
  +--------------------------------------------------+
  | one model contract                               |
  |                                                  |
  | HOSTS     host key and collateral                |
  | OFFERS    URL, declared requests/minute, price  |
  | ESCROWS   immutable active session snapshots     |
  +-------------------------+------------------------+
                            ^
                            | Escrow smart query
                            |
                 +----------+-----------+
                 | devshardctl          |
                 | ContractBridge       |
                 +----------+-----------+
                            |
                            | DevShard signed protocol
                            |
                 +----------v-----------+
                 | DevShard host        |
                 | single-host process  |
                 +----------------------+

  settlement:

  DevShard -> settlement JSON -> broker script -> model contract
```

The contract code is stored once. For M models and H hosts, the number of
instances is O(M), not O(H). For example, 20 models and 10,000 hosts require 20
contract instances.

## Contract State

```rust
struct Config {
    model_id: String,
    denom: String,
    protocol_version: String,

    max_escrow_amount: Uint128,
    max_total_escrowed: Uint128,
    max_escrow_duration_seconds: u64,
}

struct Host {
    compressed_pubkey: Binary,
    collateral: Uint128,
    active_escrows: u32,
    next_offer_id: u64,
}

struct Offer {
    url: String,
    capacity_requests_per_minute: u32,
    token_price: u64,
    expires_at: u64,
    revision: u64,
    enabled: bool,
}

struct Escrow {
    owner: Addr,
    host: Addr,
    offer_id: u64,
    offer_revision: u64,

    host_pubkey: Binary,
    host_url: String,
    token_price: u64,

    protocol_version: String,
    amount: Uint128,
    epoch_id: u64,
    expires_at: u64,
}
```

```rust
CONFIG: Item<Config>
HOSTS: Map<Addr, Host>
OFFERS: Map<(Addr, u64), Offer>
ESCROWS: Map<u64, Escrow>
NEXT_ESCROW_ID: Item<u64>
TOTAL_ESCROWED: Item<Uint128>
```

The contract stores no hardware description. Capacity is a host declaration in
requests per minute for this model. It is returned for broker selection but is
not enforced by the contract. A value of 0 means unspecified.

## Contract Write Interface

The contract exposes the following complete write interface:

```rust
struct InstantiateMsg {
    model_id: String,
    denom: String,
    protocol_version: String,
    max_escrow_amount: Uint128,
    max_total_escrowed: Uint128,
    max_escrow_duration_seconds: u64,
}
```

```rust
enum ExecuteMsg {
    RegisterHost {
        compressed_pubkey: Binary,
    },

    DepositCollateral {},

    WithdrawCollateral {
        amount: Uint128,
    },

    CreateOffer {
        url: String,
        capacity_requests_per_minute: u32,
        token_price: u64,
        expires_at: u64,
    },

    UpdateOffer {
        offer_id: u64,
        url: String,
        capacity_requests_per_minute: u32,
        token_price: u64,
        expires_at: u64,
    },

    SetOfferEnabled {
        offer_id: u64,
        enabled: bool,
    },

    CreateEscrow {
        host: String,
        offer_id: u64,
        duration_seconds: u64,
        epoch_id: u64,
    },

    SettleEscrow {
        escrow_id: u64,
        settlement: SettlementPayload,
    },

    CancelExpiredEscrow {
        escrow_id: u64,
    },
}
```

Transaction rules:

- `RegisterHost` derives the sender address from `compressed_pubkey`. Attached
  `ngonka` becomes collateral.
- `DepositCollateral` and `WithdrawCollateral` only change the sender's host
  balance. Collateral cannot be withdrawn while the host has active escrows.
- `CreateOffer`, `UpdateOffer`, and `SetOfferEnabled` require host ownership.
- `UpdateOffer` overwrites the current offer and increments `revision`.
- `CreateEscrow` uses attached `ngonka`, explicitly selects `(host, offer_id)`,
  applies contract limits, increments `host.active_escrows`, and stores an
  immutable snapshot. Declared requests per minute are not enforced.
- `SettleEscrow` requires the escrow owner and the host-signed DevShard
  settlement.
- `CancelExpiredEscrow` refunds the owner after expiry and decrements
  `host.active_escrows`.

The settlement message uses the DevShard settlement fields:

```rust
struct SettlementPayload {
    version: String,
    state_root: Binary,
    nonce: u64,
    fees: u64,
    rest_hash: Binary,
    host_stats: Vec<HostStats>,
    signatures: Vec<SlotSignature>,
}

struct HostStats {
    slot_id: u32,
    missed: u32,
    invalid: u32,
    cost: u64,
    required_validations: u32,
    completed_validations: u32,
}

struct SlotSignature {
    slot_id: u32,
    signature: Binary,
}
```

Settlement requires one host-stat entry and one signature for slot 0. The
contract recomputes the canonical DevShard settlement root, verifies the host
signature, pays `cost + fees` to the host, refunds the remainder, releases the
host collateral lock when no escrows remain, emits the result, and deletes the
active escrow.

## Contract Read Interface

The contract exposes the following complete read interface:

```rust
enum QueryMsg {
    Config {},

    Host {
        address: String,
    },

    Offer {
        host: String,
        offer_id: u64,
    },

    Offers {
        start_after: Option<(String, u64)>,
        limit: Option<u32>,
    },

    OffersByHost {
        host: String,
        start_after_offer_id: Option<u64>,
        limit: Option<u32>,
    },

    Escrow {
        escrow_id: u64,
    },
}
```

- `Config` returns contract configuration and `TOTAL_ESCROWED`.
- `Host` returns the public key, collateral, active escrow count, and next
  offer ID.
- `Offer` returns one current offer.
- `Offers` and `OffersByHost` are paginated discovery queries.
- `Escrow` returns the complete immutable view used by DevShard.

The broker selects an offer off-chain. `CreateEscrow` never scans the offer
list, so its execution cost does not grow with the number of hosts.

## Escrow Query Consumed by DevShard

`QueryMsg::Escrow` returns:

```rust
struct EscrowResponse {
    contract_address: String,
    escrow_id: u64,

    amount: Uint128,
    owner: String,
    host: String,
    host_url: String,
    model_id: String,
    token_price: u64,
    protocol_version: String,
    epoch_id: u64,
    expires_at: u64,
}
```

`ContractBridge` maps this response to `bridge.EscrowInfo`:

```text
EscrowInfo.EscrowID
  <- contract_address + ":" + escrow_id

EscrowInfo.Amount
  <- amount

EscrowInfo.CreatorAddress
  <- owner

EscrowInfo.AppHash
  <- empty; the contract already selected the slot

EscrowInfo.Slots
  <- [host]

EscrowInfo.HostURLs
  <- {host: host_url}

EscrowInfo.ModelID
  <- model_id

EscrowInfo.TokenPrice
  <- token_price

EscrowInfo.CreateDevshardFee
  <- 0; DevShard applies its compiled default

EscrowInfo.FeePerNonce
  <- 0; DevShard applies its compiled default

EscrowInfo.InferenceSealGraceNonces
  <- 0; DevShard applies its compiled default

EscrowInfo.InferenceSealGraceSeconds
  <- 0; DevShard applies its compiled default

EscrowInfo.AutoSealEveryNNonces
  <- 0; DevShard applies its compiled default

EscrowInfo.ValidationRate
  <- 0; DevShard applies its compiled default

EscrowInfo.VoteThresholdFactor
  <- 0; DevShard applies its compiled single-host behavior

EscrowInfo.EpochID
  <- epoch_id

```

Fee, sealing, validation, and vote-threshold fields are not stored in the
contract. `ContractBridge` leaves them at zero, causing
`SessionConfigFromEscrow` to apply the compiled DevShard defaults.

`bridge.EscrowInfo` includes `HostURLs`. `user.NewHTTPSession` resolves a
contract-backed host from the URL snapshotted on the escrow. Eligible-model
escrows omit `HostURLs`, causing `user.NewHTTPSession` to resolve the URL
through `GetHostInfo`.

URL caching by host address is prohibited. A host may have several offers with
different URLs, and each concurrent escrow must use its immutable URL snapshot.

## Mainnet Data No Longer Queried

Contract-backed sessions do not query:

- `QueryGetDevshardEscrow`;
- `QueryParticipant` for the host URL;
- `QueryEpochGroupData` for validation thresholds;
- `QueryGranteesByMessageType` for warm keys;
- PoC model groups, weights, or slot sampling.

`ContractBridge.GetValidationThreshold` returns unsupported. It is not used
because a one-host session has no separate validator.

`ContractBridge.VerifyWarmKey` returns `false, nil`. The host signs directly
with the key registered in the contract.

Eligible-model sessions retain their mainnet queries and `GRPCBridge`.

## Offer Updates and State Growth

URL, declared requests per minute, price, and expiry updates overwrite one
offer in O(1). Existing escrows retain their snapshots. New escrows use the new
revision.

A host creates another offer only when it wants separately priced or addressed
capacity for the same model. Hardware changes do not create contracts or
historical offer records.

For 10,000 hosts:

- one update per host per day averages 0.12 transactions per second;
- one update per host per hour averages 2.78 transactions per second.

If prices change substantially faster, signed off-chain quotes can replace
on-chain price updates. Signed quotes are outside this architecture.

Current contract state is bounded by:

```text
hosts + current offers + active escrows
```

Settled and cancelled escrow bodies are deleted after complete events are
emitted. Escrow IDs increase monotonically and are never reused.

## DevShard Change Boundary

The DevShard patch is limited to:

1. add `devshard/bridge/contract.go`;
2. add `HostURLs` to `bridge.EscrowInfo`;
3. make `user.NewHTTPSession` prefer the escrow URL before `GetHostInfo`;
4. add configuration selecting `ContractBridge` instead of `GRPCBridge`;
5. add bridge query and mapping tests.

No changes are made to:

- the state machine or cost calculation;
- protobuf definitions;
- signed diffs or settlement JSON;
- gateway create or settle transactions;
- escrow rotation;
- DAPI or MLNode.

## Scope and Constraints

- Immediate host payout; no vesting.
- Collateral is locked but not objectively slashable.
- No inference validation or disputes.
- Capacity is declared as requests per minute and is not contract-enforced.
- No automatic host selection.
- No multi-host escrow.
- No separate input and output prices.
- External scripts perform all contract writes.
- Low per-escrow and total active TVL limits are mandatory.

Local and testnet genesis use `restriction_end_block = 0`, so contract funding
and payout work there. Networks with active bootstrap transfer restrictions
cannot use this path until restrictions expire or an exemption already exists.
