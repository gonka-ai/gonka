# Inference Scaling

## Problem

Per inference, the following transactions are recorded on-chain:
- MsgStartInference
- MsgFinishInference
- MsgValidation (0 to N_hosts per inference; let's consider case when it's 1 tx for simplicity)

3 txs per inference. Max capacity per block is ~5000
=> 5000 / 3 = 1666 inferences per block
=> 1666 / 6 = 277 inferences per sec

Consider 4xH100 with Qwen3-235B deployed.
For 5000/1000 input/output tokens, such a setup can process 3.5-4 RPS (TODO: confirm)
=> 277 / 3.5 * 4 = 316 H100 GPUs to saturate the chain

Requests could be batched into a single transaction, but the computation and state growth per request makes this not scalable to hundreds of thousands of inferences.

The bottleneck is better with longer requests (more compute per tx) and worse with smaller models (more RPS per GPU, more txs per GPU).

> Note: the per-block computation cost of inference state transitions is a separate bottleneck that hits before the raw tx count limit. Even a 100x optimization there only postpones the problem -- it does not remove the throughput ceiling. This proposal addresses both by moving all per-inference processing off-chain.


## Proposal

This proposal describes an approach that moves all per-inference communication off-chain.
The chain processes only two transactions: one to put coins in escrow and assign a subgroup of hosts, one to settle at the end.
All inference communication and validations happen inside the subgroup directly, over a long session (e.g. one epoch).
To close the session, the user submits the final usage state signed by a supermajority of hosts (threshold: 2/3 slot-weighted).
Both sides have a clear incentive to settle: the user recovers the unused escrow balance, and the subgroup gets paid from it.

Effectively, as each subgroup would have to achieve consensus for the final state, the architecture will consist of:
- main blockchain
- many sub-chains / shards with extremely lightweight architecture

Sub-chains will be able to process only the inference related transactions and their decision might affect only the escrows, assigned to such sub-chains

> Note: "sub-chain" does not have to mean a real blockchain. Because the group carries no state outside of its assigned user, groups can be dynamic: formed per session, with large overlaps between them. The only thing they share is the mainnet escrow as anchor.

## Architecture

```
+-----------+     +-------------------+     +----------------------------+
|   User    |     |      Mainnet      |     |  Subnet (one per session)  |
+-----------+     +-------------------+     +----------------------------+
      |                    |                             |
      | 1. MsgCreateEscrow |                             |
      |    (100GNK)        |                             |
      | -----------------> |                             |
      | <- escrow_id,      |                             |
      |    group=[h1..hN]  |                             |
      |                    |                             |
      | 2. POST /chat (req1) --------------------------> |
      | 3. POST /chat (req2) --------------------------> |
      | 4. POST /chat (reqN) --------------------------> |
      |    ...             |                             |
      |                    |                             |
      | 5. MsgSettleEscrow |                             |
      |   (finalState,     |                             |
      |    signatures, ..) |                             |
      | -----------------> |                             |
      | <- user refund +   |                             |
      |    hosts paid      |                             |
+-----------+     +-------------------+     +----------------------------+
```

User sends exactly 2 transactions to mainnet: `MsgCreateEscrow` to open the session, `MsgSettleEscrow` to close it.
All inference requests happen directly with the assigned subnet group; mainnet never sees individual requests.

## User Flow

- [mainchain]: user creates `MsgCreateEscrow(100GNK)`
- [subchain]: user interacts with hosts in subgroup in pre-defined order
- [mainnet]: at the end of session, user creates `MsgSettleEscrow(state_root, nonce, signatures, usage, host_stats, ...)`

Q: Who decides host punishments, the subchain or mainnet?
A: Mainnet. The subchain records raw per-session stats (missed/invalid counts) in MsgSettleEscrow. Subchain-level punishment would require shared persistent state per group, forcing fixed groups instead of dynamic ones.

Q: Do hosts maintain per-group state or per-user state?
A: Per-user. Each host tracks only what happened inside each user session. No shared state between users in the same group. This is what makes dynamic per-session groups possible.


## Main Network Protocol

```
MsgCreateEscrow(
  creatorAddr
  amount,
)
```
1. move money to escrow via `MsgCreateEscrow`
2. return id to sample N(64?) slots-hosts using weighted random sampling (see [proposals/poc/optimize.md](../poc/optimize.md) for the slot idea)
3. interact in sub-chain during session
4. settle on-chain via `MsgSettleEscrow`

`MsgSettleEscrow` carries the final Merkle state root, host-level usage stats, and 2/3+ slot-weighted signatures. Field definitions and verification steps are in [design.md Settlement](./design.md#settlement).

Settlement does not require individual inference records. The mandatory finalizing round ensures all seeds are revealed and validation compliance is computed before settlement. Once verified, each host is paid from escrow proportionally to compute delivered; the remaining balance is refunded to the user.


## Subnet Protocol

The subnet is a lightweight shard with voting weight provided by mainnet. It settles back to mainnet when the session ends.

Design goals: lightweight, parallelizable, enforce that the user uses all hosts from the group.

What does the user want?
Send OpenAPI-compatible REST requests (`/chat/completions`, `/embeddings`, etc.) and know as little as possible about the blockchain.

What does the chain want?
Same properties we tried to achieve on mainnet:
- Know when each request starts and finishes. Other hosts measure executor performance against expected throughput and punish underperformance (missed rate).
- Know the hash of prompt (signed by user) and hash of response payload (signed by executor). Prompt signature authorizes payment. Payload signature enables probabilistic inference validation (invalid rate).
- Enforce distribution of requests across executors proportionally to their weight.

The chain needs these properties but does not want to process this data on mainnet.

### Transaction Types

The subnet uses 8 off-chain transaction types (see [design.md Transaction List](./design.md#transaction-list)).

Only transactions change state. Host state signatures (over state_root at each nonce) remain outside state -- they are metadata accumulated alongside diffs, not processed by the state machine.

**Inference identity.** Each inference is identified by the nonce of the diff that contains its MsgStartInference. inference_id = nonce. This guarantees uniqueness (at most one MsgStartInference per diff), gives the executor slot for free (`slot_at_position(nonce % group_size)`), and requires no separate ID allocation. During finalizing rounds (after MsgFinalizeRound), nonces advance without MsgStartInference -- those nonces have no associated inference.

### Inference Lifecycle

Inference lifecycle transitions are defined in [design.md Inference Lifecycle](./design.md#inference-lifecycle). No reverse transitions.

### User-Driven Resolution

The user reserves max_cost at MsgStartInference. Both MsgFinishInference and MsgTimeoutInference return money to the user (difference between max_cost and actual cost, or full refund on timeout). Only the user is incentivized to resolve inferences:

- During session: unresolved inferences lock escrow balance, limiting further requests.
- At settlement: hosts are paid sum(host_stats[*].cost). User receives escrow_amount - sum(host_costs). Unresolved reservations aren't in host_stats, so the reserved amount flows back to the user as part of the refund. The user overpaid for nothing.

Settlement does not require all inferences to be in terminal state. Unresolved inferences waste the user's escrow capacity but don't block settlement.

### Receipts and Signing

The executor signs a receipt attesting to request content and timestamp. Other hosts verify the receipt when processing MsgConfirmStart. Signing formats for receipts and timeout votes are defined in [design.md What Gets Signed](./design.md#what-gets-signed).

Executor receipts and host state signatures are different signed messages. `receipt_sig_h1` is the executor receipt; `state_sig_h1` is the state signature over `(state_root, escrow_id, nonce)`.

MsgStartInference has an optional `executor_sig` field. If the user already has the receipt, it can include it directly -- the inference skips pending and enters started immediately. Otherwise, the inference enters pending and the receipt is delivered later via MsgConfirmStart.

Pipelining: the user does not block on receiving the receipt before sending the next request. The user sends MsgStartInference (pending) at nonce N, gets the receipt in the executor's HTTP response, and includes MsgConfirmStart at nonce N+1 or later. Receipts lag by 1+ rounds depending on how fast the user sends requests.

### Timeout Verification

MsgTimeoutInference requires votes from other hosts as evidence. Proofs must be collected on time (hosts need to contact the executor and verify timestamps against their own clocks while the event is fresh), but can be recorded into state later via the normal propagation model. The state machine verifies the votes deterministically.

> Currently proofs are collected from the full group. Later this can be optimized with a random subgroup.

Two reasons, each with different preconditions and verification:

**reason=refused** (pending -> timed_out): executor never signed the receipt.

1. User contacts other hosts via timeout verification endpoint, provides prompt data
2. Each host contacts executor, forwards prompt data, and independently assesses request validity (max_cost sufficiency, timestamp)
3. If executor responds and signs receipt -> vote: reject timeout (executor got the data, should compute)
4. If request is invalid (e.g., max_cost too low for prompt + model) -> vote: reject timeout (executor was right to refuse)
5. If executor unreachable and request is valid -> vote: accept timeout
6. User collects enough accept votes, includes MsgTimeoutInference(votes) in diff
7. State machine verifies votes, applies timeout. host_stats[executor].missed += 1, reserved max_cost released back to escrow

If hosts repeatedly reject timeouts due to invalid requests from the user, they withhold future state signatures. The session effectively terminates -- the user cannot reach 2/3+ signatures for settlement.

During verification, the prompt data is forwarded to the executor via other hosts. This serves as the recovery mechanism: even if the user's initial request didn't reach the executor, the verification phase propagates the data. If the executor receives it and responds with a receipt, the user should use MsgConfirmStart instead of timeout.

**reason=execution** (started -> timed_out): executor signed receipt but didn't finish within deadline.

1. User contacts other hosts after deadline (started_at + T, where started_at was attested by executor's receipt)
2. Each host contacts executor and checks for MsgFinishInference
3. If executor has result -> vote: reject timeout (MsgFinishInference should be included instead)
4. If executor unreachable or no result + host's own clock confirms deadline passed -> vote: accept timeout
5. User collects enough accept votes, includes MsgTimeoutInference(votes) in diff
6. State machine verifies votes, applies timeout. host_stats[executor].missed += 1, reserved cost released back to escrow

Timeout votes are signed statements (format in [design.md What Gets Signed](./design.md#what-gets-signed)). State machine verifies signatures and counts. Threshold: total_slots/2 (slot-weighted).

----

**Per-user state.** State is saved per user independently. Each user's history is a chain of diffs. Each diff is essentially a block. Since there is no cross-user state, a node operator can shard its database and resources per user. Each node can participate in any number of subnets simultaneously. Subnet processing scales linearly with user count. Only escrow creation and settlement on mainnet do not.

**User-driven propagation.** The user is responsible for sequencing and propagating transactions. User attaches accumulated diffs to each inference request. This piggybacks propagation on normal API usage.

**Round-robin host ordering.** The user must iterate hosts in the group in a predefined order. This naturally distributes requests across hosts (not real work amount, but request count). Each diff carries a nonce that determines the expected recipient: `slot_at_position(nonce % group_size)`. The receiving host verifies it is the expected recipient for the nonce before processing. If it is not, the request is rejected. This enforces round-robin and prevents skipping.

**Propagation model.** During normal operation, every nonce has a MsgStartInference. All other transactions (MsgConfirmStart, MsgFinishInference, timeout votes, etc.) piggyback on inference requests. Proofs and receipts are collected out-of-band when the event happens, but recorded into state with the next inference request. There is no urgency to flush pending txs -- delaying only hurts the user (locked balance, unresolved inferences). Hosts can also sync state from each other via the public endpoint (see Host-proposed transactions) without advancing nonces.

The only non-inference round is the finalizing round before settlement (see Settlement). During finalization, the user visits every host in order without MsgStartInference to reveal seeds and flush remaining txs.

**Escrow accounting.** Each MsgStartInference reserves cost from escrow; MsgFinishInference releases the difference between reserved and actual cost. MsgTimeoutInference releases the full reserved amount. Users cannot overspend -- each inference is individually checked against available balance at creation time. Formula in [design.md State Machine](./design.md#state-machine).

**Finalized state.** A host considers state at nonce N finalized when it has collected 2/3+ slot-weighted signatures for that nonce. This is an operational concept tracked locally per host, not part of the state machine. Hosts use the latest finalized nonce as the safe settlement point (e.g., after equivocation or if the user disappears). Signatures arrive via nonce gossip and user-propagated diffs.

**Nonce propagation.** After processing each user request, the receiving host gossips (nonce, state_hash, state_signature) to the group. Small constant-overhead message. Each host tracks the highest nonce seen. If host_i sees that nonce has advanced past its assigned position but was never contacted, it detects a gap. Equivocation detection: if two hosts see different state_hashes for the same nonce, the user submitted conflicting diffs. The included state_signature provides an additional signature collection path -- hosts accumulate signatures from each other directly, reducing dependency on the user as sole signature aggregator.

**Host-proposed transactions.** Hosts produce transactions (MsgFinishInference, MsgValidation, etc.) that must be included in the state. The user is the sequencer, but cannot be trusted to include them. Each host maintains a mempool of unsettled transactions -- its own proposed txs plus txs received from other hosts via lazy gossip. Propagation channels:
- Response body: host returns its proposed transactions to the user alongside the inference result.
- Lazy gossip: host pushes proposed transactions to other hosts only if the user hasn't included them after K rounds. Zero overhead in the happy path. Once received, other hosts add them to their own mempool and can independently enforce inclusion.
- Public endpoint: each host exposes its unsettled transactions per session. Fallback if lazy gossip fails.

**Inclusion enforcement.** Hosts process every diff and update local state, but refuse to sign until all blocking conditions are resolved:
- Host-proposed txs in mempool not included after K rounds grace period (accounts for async lag).
- Equivocation detected (permanent -- host stops signing, any host can submit equivocation proof to mainnet, user loses full escrow).

The user continues to future nonces normally. Once the missing txs are included, the host resumes signing. For settlement, only 2/3+ signatures on the final state are needed, so temporarily withheld signatures don't block progress. Each host response includes its mempool so the user always knows what's pending.


### Scenarios

#### Happy path

Group = [h1, h2, h3, h4, h5], user sends 3 requests.

```
User -> h1: POST /chat/completions (nonce 1)
  diff: [MsgStartInference(1)]
  h1: validates diff, signs state(nonce=1), starts executing
  returns: (state_sig_h1, receipt_sig_h1, mempool=[])
  inference 1: pending

User -> h2: POST /chat/completions (nonce 2)
  diff: [MsgConfirmStart(1, executor_sig=receipt_sig_h1),
         MsgStartInference(2)]
  h2: verifies receipt_sig_h1 is valid executor receipt for inference 1
  inference 1: pending -> started
  inference 2: pending
  h2: signs state(nonce=2), starts executing
  returns: (state_sig_h2, receipt_sig_h2, mempool=[])

  (meanwhile h1 finishes, creates MsgFinishInference(1))

User -> h3: POST /chat/completions (nonce 3)
  diff: [MsgConfirmStart(2, executor_sig=receipt_sig_h2),
         MsgFinishInference(1),
         MsgStartInference(3)]
  inference 2: pending -> started
  inference 1: started -> finished
  inference 3: pending
  h3: signs state(nonce=3), starts executing
  returns: (state_sig_h3, receipt_sig_h3, mempool=[])
```

State after 3 requests:
- inference 1: finished (cost finalized, host_stats[h1].cost updated)
- inference 2: started (receipt confirmed, executing)
- inference 3: pending (waiting for h3's receipt)

Executor receipts lag by 1+ rounds: receipt_sig_h1 is included at nonce 2, receipt_sig_h2 at nonce 3. Normal pipelining.


#### Executor doesn't respond (reason=refused)

h1 is down. User sends request but gets no response.

```
User -> h1: POST /chat/completions (nonce 1)
  diff: [MsgStartInference(1)]
  h1: no response (down)
  inference 1: pending (no receipt)

User -> h2: POST /chat/completions (nonce 2)
  diff: [MsgStartInference(2)]
  Note: no MsgConfirmStart(1) -- no receipt from h1
  inference 2: pending
  h2: signs state(nonce=2), returns sig_h2

User detects: inference 1 stuck in pending, no receipt from h1.
User initiates timeout verification (out-of-band):
  POST /subnet/v1/sessions/{id}/verify-timeout to h2..h5
  User provides prompt data for inference 1
  Each host contacts h1, forwards prompt data
  h1 unreachable -> hosts return signed votes (accept)

User -> h3: POST /chat/completions (nonce 3)
  diff: [MsgConfirmStart(2, sig_h2),
         MsgTimeoutInference(1, reason=refused,
           votes=[vote_h2, vote_h3, vote_h4, vote_h5]),
         MsgStartInference(3)]
  inference 2: pending -> started
  inference 1: pending -> timed_out
  inference 3: pending
  host_stats[h1].missed += 1, balance += max_cost
  h3: verifies votes, signs state(nonce=3)
```

If h1 was reachable during verification: hosts forwarded the prompt data. h1 could sign a receipt. Votes would reject the timeout. User should use MsgConfirmStart instead, wait for MsgFinishInference.


#### Executor signs but doesn't finish (reason=execution)

h1 accepts the request but never delivers a result.

```
User -> h1: POST /chat/completions (nonce 1)
  diff: [MsgStartInference(1)]
  h1: signs state(nonce=1), returns (state_sig_h1, receipt_sig_h1)
  h1: starts executing but crashes / hangs
  inference 1: pending

User -> h2: POST /chat/completions (nonce 2)
  diff: [MsgConfirmStart(1, receipt_sig_h1),
         MsgStartInference(2)]
  inference 1: pending -> started (receipt verified, timestamp attested)
  h2: signs state(nonce=2), returns (state_sig_h2, receipt_sig_h2)

  ... session continues, deadline passes (started_at + T) ...

User detects: inference 1 still started, no MsgFinishInference from h1.
Deadline passed (started_at attested by h1's receipt, T >= 20 min).
User initiates timeout verification (out-of-band):
  POST /subnet/v1/sessions/{id}/verify-timeout to h2..h5
  Each host contacts h1, checks for result
  h1 unreachable or no result + deadline passed -> signed votes (accept)

User -> h_next: POST /chat/completions (nonce N)
  diff: [MsgTimeoutInference(1, reason=execution,
           votes=[vote_h2, vote_h3, vote_h4, vote_h5]),
         MsgStartInference(N)]
  inference 1: started -> timed_out
  host_stats[h1].missed += 1, balance += reserved_cost
```


#### User withholds data from executor

User creates MsgStartInference but never sends prompt data to h1.

```
User -> h1: POST /chat/completions (nonce 1)
  diff: [MsgStartInference(1)]
  But: user sends malformed or empty prompt to h1
  h1: rejects or can't compute, doesn't sign
  inference 1: pending

User wants to timeout h1 unfairly.
User initiates timeout verification:
  POST /subnet/v1/sessions/{id}/verify-timeout to h2..h5
  User must provide prompt data (prompt_hash is in MsgStartInference)
  Hosts forward data to h1
  h1 receives valid data via hosts, signs receipt, starts computing
  -> votes reject timeout (executor responded)

User must include MsgConfirmStart(1) and wait for MsgFinishInference(1).
Attack fails: the verification phase propagated the data to h1.
```


Sending a request without recording MsgStartInference is not possible -- the executor rejects requests without a corresponding MsgStartInference (no payment authorization = no reason to compute).

#### User submits insufficient max_cost

User creates MsgStartInference with max_cost too low for the actual prompt (wrong token estimation).

```
User -> h1: POST /chat/completions (nonce 1)
  diff: [MsgStartInference(1, max_cost=10)]
  h1: receives prompt, determines max_cost is insufficient for this prompt + model
  h1: signs state (MsgStartInference is protocol-valid), refuses executor receipt
  inference 1: pending (no receipt, executor won't compute)

User wants to timeout h1 unfairly.
User initiates timeout verification:
  POST /subnet/v1/sessions/{id}/verify-timeout to h2..h5
  User provides prompt data
  Hosts forward data to h1, independently assess max_cost sufficiency
  Hosts determine max_cost is too low for the prompt -> votes reject timeout

User cannot timeout the executor. Escrow remains locked for this inference.
If hosts determine the user deliberately submitted invalid estimations,
they withhold future signatures -- session effectively terminates.
```

#### Equivocation

User sends two different diffs at the same nonce to different hosts.

```
User -> h1: nonce 5, diff_A
User -> h2: nonce 5, diff_B (different content)

h1 gossips (nonce=5, state_hash_A)
h2 gossips (nonce=5, state_hash_B)

h3 sees conflicting state_hashes for nonce 5.
h3 requests diffs from both h1 and h2.
Two different user-signed diffs at nonce 5 = equivocation proof.
h3 gossips evidence, stops signing.

Session terminates. Any host submits equivocation proof to mainnet
(two user-signed diffs at the same nonce). User loses full escrow.
```


### Inference Validation

Validation is probabilistic, same as on mainnet. Each host independently decides which inferences to validate using a deterministic seed and the same `ShouldValidate` logic.

On mainnet, hosts commit a seed at epoch start and reveal it at epoch end. The subnet has no epochs. Instead, the seed is derived deterministically from the host's private key and the escrow_id: `seed_i = first_8_bytes(sign(escrow_id_bytes))`. One seed per host per session. The host has no freedom to choose a different seed since signing is deterministic and the public key is known.

The signing key is pinned by the host's first diff-contained signature in the session (`proposer_sig` or `executor_sig`). This binding enters `state.WarmKeys` and becomes part of the state root. At reveal time, `MsgRevealSeed` is verified against the same session binding, not against a separately learned state-signature key. A validator cannot try different keys at reveal time to influence which inferences it must validate. See [storage.md Warm Keys](./storage.md#warm-keys) for the binding rule and replay implications.

During the session, each host uses its seed to decide which finished inferences to validate. If selected, host_i re-executes the inference, compares logits, and submits MsgValidation into subnet state.

Seed reveal happens during the mandatory finalizing round (see Settlement). Each host submits MsgRevealSeed(signature). Other hosts derive the seed from the signature, verify it against the known public key, re-run ShouldValidate for all finished inferences, and count misses. Compliance results go into host_stats before settlement.

> We considered deriving the seed from the host's state signature at each nonce (no commit-reveal, no finalizing round). This avoids the extra round but requires signatures to be part of state for compliance verification. Signatures are deliberately not in state because they arrive asynchronously and would break deterministic state hashing. The finalizing round could potentially be eliminated if seeds are derived from data already in state, avoiding commit-reveal entirely. Requires further refinement of the validation protocol.


## Settlement

Before submitting settlement to mainnet, the user must complete two finalizing rounds without MsgStartInference:

- Round 1: collect MsgRevealSeed, pending MsgFinishInference, and any remaining MsgValidation from each host. After this round, all seeds and txs are in state, but hosts visited early in the round haven't seen seeds from hosts visited later.
- Round 2: propagate the complete state to everyone. Each host applies all seeds -- the state machine computes required_validations and completed_validations per host deterministically from the revealed seeds and existing MsgValidation txs. Hosts sign the final state.

> Round 1 could potentially be replaced with a dedicated requestSeed endpoint (requires developer signature, blocks new MsgStartInference). This would avoid a full diff round for seed collection. Optimization for later.

User then submits `MsgSettleEscrow` to mainnet. Verification steps and escrow distribution are defined in [design.md Settlement](./design.md#settlement).

> Note: the list of individual signatures can be replaced with an aggregated BLS signature in the future to reduce tx size.

Settlement includes a dispute window allowing hosts to submit competing state with higher nonces. Details in [design.md Dispute Window](./design.md#dispute-window).

**User disappears.** Any group member can submit MsgSettleEscrow after a timeout. All hosts have full state within one round (propagated via diffs). If a host is missing recent state, it can request it from other hosts via the public API endpoint. Same 2/3+ signature requirement, same dispute window. TODO: define timeout trigger (wall-clock from last nonce vs escrow expiry height at creation).

**Inflated state.** User claims less usage than actually happened (to get a larger refund). Requires 2/3+ host signatures over the false state. Reduces to BFT assumption: safe as long as <1/3 of slot-weighted hosts are malicious.


## Example Requests

Third request in the happy path (sent to h3):

```
POST /chat/completions
Host: h3

{
  "model": "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8",
  "stream": true,
  "messages": [
    {"role": "user", "content": "Write a haiku about Seattle."}
  ],
  "diffs": [
    {"nonce": 1, "txs": ["MsgStartInference(1)"], "state_sigs": ["state_sig_h1"]},
    {"nonce": 2, "txs": ["MsgConfirmStart(1, receipt_sig_h1)", "MsgStartInference(2)"], "state_sigs": ["state_sig_h2"]},
    {"nonce": 3, "txs": ["MsgConfirmStart(2, receipt_sig_h2)", "MsgFinishInference(1)", "MsgStartInference(3)"], "state_sigs": ["state_sig_h3"]}
  ],
  "state_hash": "<SHA256>"
}
```

First request (to h1):

```
{
  ...
  "diffs": [
    {"nonce": 1, "txs": ["MsgStartInference(1)"], "state_sigs": []}
  ],
  "state_hash": "<SHA256>"
}
```


## Weights in Subnet

Subnet group formation reuses the slot sampling mechanism from PoC validation (see [proposals/poc/optimize.md](../poc/optimize.md)).

Slot assignment is a deterministic function of (app_hash after escrow creation, escrow_id, validator_weights) using the same `GetSlotsFromSorted` algorithm as in PoC. The chain does not need to compute it at escrow creation. Anyone can derive the group independently. The chain only verifies the group was correct at settlement time (MsgSettleEscrow).

Each slot maps to a host. If a host is sampled into 3 slots, it has weight 3 in the subnet. Each slot carries weight 1. This preserves the mainnet weight distribution inside the subnet without requiring any additional weight tracking.

The slot sequence also defines the round-robin order for user requests.

Requirements for slot count are less strict than in PoC. In PoC, slots protect against adversarial validation (fake participant attacks). In the subnet, the group only needs enough redundancy for availability and settlement signatures. The exact slot count (64 vs 128) is TBD.

TODO: define settlement signature threshold relative to slot count
