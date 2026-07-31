# Proposal: Inference on Non-Eligible Models

Draft.

## Goal / Problem

Current DevShard escrow creation requires an eligible PoC model group with
on-chain weights. This excludes models that should be available for inference
but should not contribute to consensus.

The existing path pays hosts from two sources:

- Work coins: GNK paid by users for completed inference.
- Reward coins: the Bitcoin-style minted subsidy distributed using consensus
  weight.

Hosts serving non-eligible models have no consensus weight. They receive only
work coins at a market price, and those payments must vest.

## Proposal

Keep the existing governance-controlled PoC model path unchanged. Add a
parallel, permissionless path for models that never contribute to consensus and
never receive reward coins.

Each model is represented by a contract instance. A host registers an offer:

- model and inference URL;
- declared capacity, for example nonces per minute;
- the period for which the capacity will be available;
- collateral that guarantees the capacity commitment.

Collateral is locked value, not a self-reported field. The host commits it
against the declared capacity and service period.

The contract defines market prices in GNK for input and output tokens.

A broker creates an escrow in the contract. The contract locks the broker's
funds and returns:

- escrow ID;
- host list selected from active offers;
- host URLs;
- fixed input and output token prices.

```
host                       model contract                   broker
 |                               |                            |
 | register URL, capacity, term  |                            |
 | lock collateral              |                            |
 | ----------------------------> |                            |
 |                               |                            |
 |                               | <------------------------- |
 |                               | create escrow, lock GNK    |
 |                               |                            |
 |                               | -------------------------> |
 |                               | escrow ID, hosts, prices   |
 |                               |                            |
 |                inference execution                         |
 | <========================================================> |
 |                               |                            |
 |                               | <------------------------- |
 |                               | settlement                 |
 | <--- vested work payment ---- | --- refund remainder ----> |
```

The host list comes from the contract, not from a mainnet epoch model group.
Hosts on this list do not gain consensus weight.

## Implementation

The expected implementation has only two new components:

1. Smart contracts for model registration, host offers, collateral, escrow,
   pricing, settlement, and vesting.
2. A DevShard version that reads those contracts and handles inference for the
   returned host group.

The existing PoC, consensus, and eligible-model inference paths remain
unchanged.

### Contract

The contract stores model information, host offers, locked collateral,
escrows, prices, and settlement state.

Host payments are work coins only. Settlement must put those payments on a
vesting schedule rather than transfer immediately.

### Inference execution

A separate DevShard version requires only the contract-facing changes:

- read escrow data and host URLs from the contract;
- accept host identities from the contract host list instead of the mainnet
  participant set;
- submit settlement to the contract instead of `x/inference`;
- charge input and output tokens using the two contract prices.

The current DevShard uses one `TokenPrice` for both input and output tokens, so
separate prices require a versioned cost calculation.

Whether this path should use DevShard at all is still open.

## Open Questions

### Q1: Should inference be validated inside the shard?

Current DevShard validation relies on host slots sampled from PoC-backed
weights. The open model list has no verified host weight: capacity is declared
and backed by collateral, but not measured by PoC. A host-weighted validation
quorum therefore cannot provide the same guarantee.

One option is validation on demand by the escrow owner, such as the user or
gateway. The owner decides whether a response is acceptable. If it marks the
response invalid, the executor is not paid and the reserved amount returns to
the escrow.

Should this owner-controlled validation be used, or should this path execute
inference without validation?

### Q2: Do we need DevShard?

The alternative is direct communication between the broker and each host. In
that design, the broker handles routing and depends on its direct interaction
with the host instead of a DevShard group.

A minimal implementation can still reuse DevShard as a single-host session:

- the contract returns one host for the session;
- the broker and host keep the existing signed diff log and state machine;
- there is no cross-host log replication or gossip;
- validation is disabled or controlled by the escrow owner;
- settlement requires the one host signature.

The current code already permits a group of one. Its settlement quorum is one,
and automatic validation is skipped because there is no non-executor host.

The signed diff log should remain. It records ordered requests, balance changes,
costs, and the settlement state root. Removing it would require a different
accounting and settlement protocol, not a minimal DevShard version.

If the broker uses several hosts, each can run an independent single-host
session. Sharing one escrow and one state across several hosts without a shared
log would require a new protocol.

Should this path use this single-host DevShard mode, or define a separate direct
broker-to-host protocol?
