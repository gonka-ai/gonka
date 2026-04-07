# Inference Subnet: Technical Design

Working document. Captures design decisions and open questions for the subnet implementation described in [README.md](./README.md).

## No Cosmos SDK in Subnet

The subnet does not depend on Cosmos SDK. Cosmos SDK is slow, heavyweight, and the subnet is not a blockchain.

Mainnet keeps Cosmos SDK for `MsgCreateEscrow` and `MsgSettleEscrow` in `inference-chain/x/inference/`.

Crypto: `go-ethereum/crypto` for secp256k1 signing/ecrecover, `crypto/sha256` for hashing. Proto definitions are self-contained within the subnet package.

Keys are secp256k1, same as mainnet. A host signs with its warm key (authz grant) or cold key (validator account key).


## Transaction List

### Mainnet (2 txs)

Defined in `inference-chain/proto/` alongside existing 43 tx types.

| Tx | Proposer | Purpose |
|----|----------|---------|
| MsgCreateEscrow | user | Lock funds, source of data for group sampling |
| MsgSettleEscrow | user or host | Finalize session, initiate escrow distribution |

### Subnet (8 txs)

Defined in the subnet package's own proto files. No shared types with mainnet protos.

| Tx | Proposer | Purpose |
|----|----------|---------|
| MsgStartInference | user | Authorize inference, reserve max cost |
| MsgConfirmStart | user | Deliver executor receipt (executor_sig). pending -> started |
| MsgFinishInference | host | Record completion, response hash, token counts |
| MsgTimeoutInference | user | Declare timeout with votes as evidence |
| MsgValidation | host | Validation result. valid=false opens challenge voting |
| MsgValidationVote | host | Vote during challenge |
| MsgRevealSeed | host | Reveal validation seed during finalizing round |
| MsgFinalizeRound | user | Enter finalization. Irreversible, blocks further MsgStartInference |

10 total (2 mainnet + 8 subnet). No governance, staking, or other chain overhead in the subnet.

No separate `MsgInvalidateInference`. Invalidation is the result of a challenge voting round: `MsgValidation(valid=false)` opens the vote, `MsgValidationVote` collects votes, majority decides.

MsgTimeoutInference is user-proposed with votes from other hosts as evidence. The user collects votes out-of-band (hosts contact the executor to verify), then includes them in MsgTimeoutInference. The state machine verifies votes deterministically. If the inference is already in a terminal state (timed_out, finished, etc.), the diff is rejected as a protocol violation.


## Package Structure

Top-level Go module: `subnet/` at repo root. Imported by `decentralized-api` as a library.

### Both Roles in One Library

The library implements both the host and user flows. The state machine is shared -- diffs, signature verification, nonce tracking, state hashing. The difference is who proposes transactions and who sequences.

Host-specific: validate incoming requests, sign state, propose MsgFinishInference/MsgValidation, gossip nonces, enforce inclusion rules.

User-specific: create MsgStartInference, sequence diffs, round-robin host selection, collect signatures, submit settlement.

A single Go test can create 30 host nodes and 1 user node, drive a full session, and verify everything in-process. When we need a JS/Python client later, the Go library is the reference and the wire protocol is the contract.

```
subnet/
  go.mod                    # standalone module, deps: go-ethereum/crypto, protobuf. no cosmos-sdk
  engine.go                 # InferenceEngine, ValidationEngine interfaces (contract with dapi)
  types.go                  # ExecuteRequest, ExecuteResult, ValidateRequest, ValidateResult
  proto/                    # subnet-specific proto definitions
  types/                    # generated proto types + domain types
  state/                    # state machine: apply diffs, verify nonces, track balances
  host/                     # host role: request handling, signing, gossip, inclusion enforcement
  user/                     # user role: sequencing, round-robin, signature collection, settlement
  signing/                  # signature creation and verification
  storage/                  # storage interface + implementations
  gossip/                   # gossip client and handlers
  bridge/                   # MainnetBridge interface

decentralized-api/
  internal/
    subnet/
      engine_adapter.go     # implements subnet.InferenceEngine using broker + completionapi
      validation_adapter.go # implements subnet.ValidationEngine using broker + compareLogits
      router.go             # mounts /subnet/v1/ routes, wires adapters to subnet host
```

The engine interfaces at the subnet root are the contract between subnet and dapi. They define what the subnet needs from the ML node infrastructure without importing any dapi or cosmos-sdk types. dapi adapters implement them by wrapping existing broker, completionapi, and validation logic.

Module boundary enforces no cosmos-sdk at compile time. During development, dapi uses a replace directive:

```
// decentralized-api/go.mod
replace subnet => ../subnet
```

First release: `decentralized-api` imports `subnet/` and mounts a new echo router group on the existing public server port, e.g. `/subnet/v1/`. The user flow is available as a Go client library (`subnet/user`) for integration tests and future standalone client tooling.


## State Machine

### Session State

Current state after applying all diffs up to latest_nonce. History lives in diffs (storage).

```
EscrowState:
  escrow_id            string
  config               SessionConfig
  group                []SlotAssignment                # from mainnet at creation, immutable
  balance              uint64                          # remaining escrow
  phase                SessionPhase                    # Active | Finalizing | Settlement
  finalize_nonce       uint64                          # nonce at which finalization started
  inferences           map[uint64]InferenceRecord      # keyed by inference_id
  host_stats           map[uint32]HostStats            # keyed by slot_id
  revealed_seeds       map[uint32]int64                # keyed by slot_id, from MsgRevealSeed
  warm_keys            map[uint32]string               # slot_id -> warm key address, lazily populated
  latest_nonce         uint64
```

```
# Field count can be smaller: raw token counts are recoverable from stored diffs.
# reserved_cost and actual_cost are the only fields the state machine uses for accounting.
# Keeping all fields for now for debuggability.
InferenceRecord:
  status               enum {pending, started, finished, challenged, validated, invalidated, timed_out}
  executor_slot        uint32
  model                string
  prompt_hash          []byte
  response_hash        []byte              # set on MsgFinishInference
  input_length         uint64              # chars, from MsgStartInference
  max_tokens           uint64              # from MsgStartInference
  input_tokens         uint64              # actual, set on MsgFinishInference
  output_tokens        uint64              # actual, set on MsgFinishInference
  reserved_cost        uint64              # computed at start: (input_length + max_tokens) * price
  actual_cost          uint64              # computed at finish: (input_tokens + output_tokens) * price
  started_at           int64               # user's timestamp from MsgStartInference
  votes_valid          uint32              # count during challenge
  votes_invalid        uint32              # count during challenge
  voted_slots          map[uint32]bool     # prevent double vote
```

```
HostStats:
  missed               uint32
  invalid              uint32
  cost                 uint64              # total cost of inferences executed by this host
  required_validations uint32              # inferences ShouldValidate selected for this host
  completed_validations uint32             # MsgValidation txs actually submitted
```

```
SessionConfig:
  refusal_timeout      int64               # seconds before reason=refused timeout
  execution_timeout    int64               # seconds before reason=execution timeout
  token_price          uint64              # price per unit (flat per session; per-model pricing TBD)
  vote_threshold       uint32              # minimum slot-weighted accept votes for timeout (total_slots / 2)
```

Signatures are not part of EscrowState. They are stored alongside diffs in storage.

### Inference Lifecycle

```
pending -> started        (MsgConfirmStart: executor receipt verified)
pending -> timed_out      (MsgTimeoutInference reason=refused, with votes)
started -> finished       (MsgFinishInference)
started -> timed_out      (MsgTimeoutInference reason=execution, with votes)
finished -> validated     (MsgValidation valid=true)
finished -> challenged    (MsgValidation valid=false)
challenged -> validated   (MsgValidationVote: majority valid)
challenged -> invalidated (MsgValidationVote: majority invalid)
```

No reverse transitions. Once timed_out, MsgFinishInference and MsgConfirmStart are rejected. Once finished, MsgTimeoutInference is rejected. The sequencing order in diffs determines which lands first.

Validation is probabilistic. Most inferences follow `pending -> started -> finished`.

Transitions and state updates:

- MsgStartInference: creates record with status=pending, reserves max_cost from available balance. `max_cost = (input_length + max_tokens) * per_token_price` where input_length is prompt length in characters (caveat: overestimates for Latin, may underestimate for CJK; acceptable for now). inference_id must equal the diff's nonce. executor_slot is derived: `group[inference_id % len(group)].SlotID`. At most one MsgStartInference per diff. Rejected if state.finalizing == true.- MsgFinalizeRound: sets state.finalizing = true. Irreversible. After this, MsgStartInference is rejected. At most one per session. Diffs after this contain only cleanup txs (MsgRevealSeed, MsgFinishInference, MsgValidation, etc.).
- MsgConfirmStart: verifies executor receipt signature against executor's public key, pending->started.
- MsgFinishInference: verifies executor_slot matches inference's assigned executor. started->finished. `actual_cost = (input_tokens + output_tokens) * per_token_price`. Rejects if actual_cost > reserved_cost. Releases reserved_cost - actual_cost to balance. Updates host_stats[executor_slot].cost += actual_cost.
- MsgValidation: verifies validator_slot != executor_slot. finished->validated (valid=true) or finished->challenged (valid=false). Q: how to verify no unauthorized validation? Deferred to ShouldValidate (Phase 4).
- MsgValidationVote: increments votes_valid or votes_invalid. Votes are slot-weighted (one slot = one vote). When votes_invalid > total_slots/2: status=invalidated, host_stats[executor].invalid += 1, host_stats[executor].cost -= actual_cost, actual_cost released to escrow. When votes_valid > total_slots/2: status=validated. No time-based expiry.
- MsgTimeoutInference: requires slot-weighted votes (threshold: total_slots/2). Vote signature covers `(escrow_id, inference_id, reason, accept)`. reason=refused requires pending. reason=execution requires started. host_stats[executor].missed += 1, reserved cost released to escrow.

### State Hash

```
state_root = sha256(host_stats_hash || rest_hash || phase_byte)
```

Where:

- `host_stats_hash = sha256(proto(sorted host stats))` -- 32 bytes
- `rest_hash = sha256(balance_be || inferences_hash || warm_keys_hash)` -- 32 bytes
- `warm_keys_hash = sha256(sorted slot_id_be || addr_bytes)` -- lazy warm key bindings
- `inferences_hash = sha256(proto(sorted inference records))`
- `phase_byte = uint8(phase)`: 0x00=Active, 0x01=Finalizing, 0x02=Settlement

All components have fixed, known lengths (32 + 32 + 1), so the concatenation is unambiguous without length prefixes. Serialization is deterministic (protobuf with sorted map keys, fixed field order).

escrow_id and latest_nonce are already bound by the host signature (`sign(state_root || escrow_id || nonce)`) and don't need separate inclusion in the hash.

At settlement, mainnet receives host_stats and rest_hash (the sibling). It recomputes host_stats_hash, combines with rest_hash, hardcodes `phase_byte=0x02`, and checks against the signed state_root. Mainnet doesn't need to interpret rest_hash.

Every host applying the same diffs to the same nonce produces the same state_root. This is what gets signed.

### What Gets Signed

User signs the diff content: `sign(hash(serialize(DiffContent{nonce, txs, escrow_id, post_state_root})))`. `post_state_root` is the state root after applying the diff's txs. This binds the user signature to the exact post-state claimed for that nonce. Host signatures remain outside the signed diff payload.

Host signs the state: `sign(state_root || escrow_id || nonce)`. escrow_id prevents cross-session replay, nonce prevents cross-nonce replay, state_root binds to a specific state. Signing happens before execution.

Executor signs the receipt: `sign(inference_id || prompt_hash || model || input_length || max_tokens || started_at)`. Attests to request content and timestamp. Delivered to other hosts via MsgConfirmStart.

Host signs proposed txs: each host-proposed transaction (MsgFinishInference, MsgValidation, MsgValidationVote, MsgRevealSeed) carries the proposer's signature over the serialized tx content. Verification accepts either the validator's cold key or an authorized warm key. Once the first diff-contained warm-key signature from a slot is accepted, that warm-key binding is fixed for the session and later verified against `state.WarmKeys`. This prevents the user from forging host-proposed txs when including them in diffs.

### Diff Application

When a host receives a request with diffs:

1. For each diff from local_latest_nonce+1 to received_latest_nonce:
   a. Verify user signature on the diff
   b. Validate: nonce is sequential, txs are well-formed, proposer is authorized
   c. Apply each tx to EscrowState (update balance, inferences, host_stats, usage)
   d. Compute state_root, store diff + state_root
2. Verify included host signatures against stored state_roots at their respective nonces
3. Append new diff (current nonce) with the user's new txs
4. Acceptance check: sign state_root if all blocking conditions are satisfied. Otherwise process the diff but withhold signature. Both cases include host mempool in response.

If any diff fails validation (bad signature, bad nonce, malformed tx), reject the entire request.

### Timestamp and Request Validation

Nonce is the subnet's block height -- the authoritative ordering. The state machine is deterministic: same diffs at same nonces produce the same state on every host.

Wall-clock time is a local observable, not consensus truth. Different hosts have different clocks.

Two layers:

State machine (deterministic): applies diffs, computes state. Same diffs at same nonces produce the same result on every host. No local clocks.

Acceptance (local): each host decides independently whether to sign state and whether to accept the inference request. Based on local clock, request validity, and estimation checks.

Diff application is unconditional -- the state machine always applies diffs to keep state in sync. Signing and receipt issuance are separate decisions.

**State signing vs receipt signing.** When the executor receives a request it considers invalid (suspicious timestamp, insufficient max_cost for the given prompt and model), it separates two actions:

1. Process the diff and sign the state root. MsgStartInference is protocol-valid (well-formed, sequential nonce, sufficient balance). State moves forward.
2. Refuse the executor receipt. No receipt means the inference stays pending -- the executor won't compute.

Resolution follows Timeout(refused): the user collects votes from other hosts. During verification, hosts forward prompt data to the executor and independently assess request validity (timestamp, max_cost sufficiency). If the request is malformed, hosts reject the timeout. If hosts determine the user deliberately submitted an invalid request (e.g., wrong cost estimation), they withhold future signatures -- the session effectively terminates for the user.

**Gossip on rejection.** When the executor withholds a receipt, it gossips the rejection reason and signed evidence to the group. Each group member forms its own opinion based on its own clock and local assessment. No single host's clock is authoritative.


## Sequencing Model

### Primitives

Log: ordered sequence of diffs, indexed by nonce. Single writer (user). Append-only.

Diff: list of txs at a nonce, signed by the user. Immutable once in the log.

State: deterministic function of log[1..N]. Hosts compute state from diffs.

Q: Can we avoid maintaining a state machine on the user side?
A: Possibly, but it would require comparing state_hashes from multiple hosts to detect a malicious one returning a fake hash. Adds complexity and latency. Keep local state machine on the user side for this iteration.

Round: one pass through all hosts in the group in slot order.

### Tx Sources

User-proposed: MsgStartInference, MsgConfirmStart, MsgTimeoutInference.

Host-proposed: MsgFinishInference, MsgValidation, MsgValidationVote, MsgRevealSeed. Returned in host response mempool. User includes in future diffs.

The user is the sequencer for both.

### Flow

The user composes diffs and sends requests in round-robin order. Each request carries all accumulated diffs since the receiving host's last sync point. The user signs each diff (see What Gets Signed). Within a round, the user sends to the next host without waiting for the previous host's response.

Each host on receiving a request:
1. Verify user signature on each new diff
2. Apply all diffs through the state machine (deterministic, always runs)
3. For its assigned nonce: acceptance layer decides whether to sign
4. Gossip (nonce, state_hash, state_signature) to K peers
5. Return: host signature + mempool (signed) or mempool only (signature withheld)

### Signature Withholding

A host withholds its signature for two reasons:
- Inclusion: host-proposed txs in mempool not included after K rounds.
- Acceptance: a tx violates the host's local judgment (e.g., suspicious timestamp).

The host still processes diffs and updates local state. The user continues to future nonces and includes missing txs to unblock signing. For settlement, only 2/3+ signatures on the final state are needed.

### Equivocation

Hosts gossip (nonce, state_hash) after processing each request. If a host sees a different state_hash for the same nonce from another host, it requests the diff at that nonce. Two different user-signed diffs at the same nonce = equivocation proof. The detecting host gossips the evidence and stops signing. Any host submits equivocation proof to mainnet. User loses full escrow.

### Session Structure

```
session:     rounds with new inferences (pipelined)
finalize 1:  round to collect MsgRevealSeed and remaining txs
finalize 2:  round to propagate complete state, hosts sign final state
settle:      MsgSettleEscrow to mainnet
```

Each step uses the same primitive: diffs in round-robin order.


## Interface Boundaries

Every subnet subpackage exposes a minimal interface in a dedicated `interface.go` file. The full subnet must be testable without mainnet, dapi, or containers.

### Mainnet Boundary

`MainnetBridge` (7 methods, full definition with types in Chain Data Requirements below). Both host and user use it, but all calls are expensive by design. The subnet derives everything it can locally (slot assignment from deterministic function of app_hash/escrow_id/validator_weights, signature verification from cached public keys) and only queries the bridge for what it cannot compute (escrow existence, validator info, warm key authorization).

Bridge calls are batched at session start. During the session, the only bridge call is warm key verification on cache miss (host rotates warm key mid-session). This is rare but possible. The bridge adapter can target any mainnet data source: local chain client (dapi), public REST endpoint, or dedicated RPC. REST is preferred for the bridge adapter -- the data is small and infrequent, and it avoids proto coupling between subnet and chain. The chain already exposes REST via grpc-gateway (standard Cosmos SDK). A JS/Python user SDK implements the bridge adapter with plain HTTP + JSON.

In production, `decentralized-api` provides an implementation that talks to the local chain node. In tests, a struct literal with preset return values drives the full subnet through any scenario.

### Per-Package Interfaces

Each subpackage defines its own interface file. The package never imports concrete implementations from sibling packages directly. Wiring happens at the top level.

```
subnet/
  bridge/interface.go        # MainnetBridge
  state/interface.go         # StateMachine (apply diffs, verify nonces)
  storage/interface.go       # Storage (already defined above)
  signing/interface.go       # Signer, Verifier
  gossip/interface.go        # GossipClient (notify peers)
```

This means any component can be replaced with a test double. A test can run the full state machine with in-memory storage, a no-op gossip client, and a fake mainnet bridge. No network, no disk, no containers.

### In-Process Unit Tests

The target: a Go test file that creates a subnet session, sends inference requests, collects signatures, and settles -- all in-process, all deterministic. All nodes run in the same process, no network I/O. The test constructs the dependency graph manually:

```go
bridge := &FakeBridge{escrows: map[string]EscrowInfo{...}}
store  := storage.NewMemory()
signer := signing.NewSecp256k1(privateKey)
gossip := &NoOpGossip{}

node := subnet.New(bridge, store, signer, gossip)
// now drive the full protocol with real data
```

No docker-compose, no chain binary, no dapi binary. Every scenario from README.md (happy path, host down, user withholds data, timeout verification) is testable this way. Simulation speed is limited only by CPU, not by block times or network latency.

### Multi-Node Integration Tests

Unit tests cover one node in-process. Integration tests cover a real subnet cluster: multiple nodes running as separate processes, communicating over real HTTP, with real gossip, real storage, real signing. The only mock is `MainnetBridge`.

Each node is a standalone binary (or a Go test spawning goroutines with real listeners). A test harness spins up N nodes, injects escrow info through the fake bridge, then drives user traffic against the cluster. Nodes gossip to each other over localhost. Storage is real SQLite (or PostgreSQL). Signatures are real secp256k1.

This is the level where stress testing happens. Scenarios:

- 1000 concurrent sessions across 30 nodes, measure throughput and latency
- Kill nodes mid-session, verify timeout verification works end-to-end
- Inject malicious user behavior (withhold diffs, skip hosts, submit stale state)
- Race conditions: concurrent writes, signature arrival ordering, nonce conflicts

The fake bridge is trivial -- a shared in-memory map protected by a mutex. It returns preset escrow data and records settlement calls. No chain, no blocks, no Cosmos SDK, but the rest of the system is production code running under production conditions.

This is the key payoff of the narrow mainnet boundary: the entire subnet is real, only the 7-method bridge is fake. Stress tests hit real concurrency, real network, real disk I/O.


## Chain Data Requirements

The subnet needs a small set of data from mainnet. All of it flows through the `MainnetBridge` interface.

### What the Subnet Needs

1. Escrow info: amount, creator address, creation height, app_hash at creation.
2. Validator list and weights for the current epoch (to derive slot assignment locally).
3. Validator account addresses, public keys, and primary URLs (from `participant.inference_url` on chain). These are loaded at session start via `GetValidatorInfo` and serve as the cold key reference for signature verification -- no bridge call needed to verify a validator signing with its own key.
4. Warm key verification: given a (warm_address, validator_address) pair, confirm the authz grant exists. Called only on cache miss.

Slot assignment is derived locally from items 1+2 using the same `GetSlotsFromSorted` algorithm as PoC. The bridge never provides slot assignment directly.

### Signing and Verification

Mainnet uses Cosmos SDK's secp256k1 module for signature verification. That module is heavyweight and depends on the full SDK. The subnet cannot import it (no Cosmos SDK dependency).

The subnet uses `go-ethereum/crypto` for secp256k1 operations. This is the same library Ethereum has used since its initial version -- well-tested, standalone, no chain dependencies. The key operation is `ecrecover`: given a message hash and signature, recover the signer's public key and derive their address. No public key lookup needed.

Verification flow:

1. Message includes `validator_address` (the validator the signer claims to act for).
2. Receiver runs `ecrecover(message_hash, signature)` -> recovers signer address.
3. Receiver checks: is the recovered address the validator itself (cold key) or an authorized warm key?
4. For cold key: the recovered address matches `ValidatorInfo.Address` loaded at session start. No bridge call, no cache lookup.
5. For warm key: check the local warm key cache first. If found, done. If not found (first contact or new grant mid-session), call `bridge.VerifyWarmKey(recoveredAddress, validatorAddress)` to confirm the authz grant exists, then cache the result for the session. Bridge is only called on cache miss.

A host can sign with either its cold key (validator's own account key) or its warm key (operational key authorized via authz grant on mainnet). Both are secp256k1. Cold key signing requires no grant -- the validator is acting directly.

Only diff-contained signatures (proposer_sig on host-proposed txs, executor_sig in MsgConfirmStart) introduce warm key bindings into the state root. The first diff-contained signature from a slot binds that warm key permanently for the session. A different key from the same slot is rejected. These signatures are verified inside `applyTx`, which all participants execute identically. State signatures and gossip messages verify the key against the binding but never create new ones. See [storage.md Warm Keys](./storage.md#warm-keys) for the full binding rule and replay implications.

The finalized storage design persists warm-key deltas per diff so replay can reproduce the same binding decisions at the same nonces without re-querying the bridge.

```go
type WarmKeyInfo struct {
    ValidatorAddress string
}
```

### Host Discovery: Multi-URL Identity

Each validator has a primary URL recorded on mainnet as `participant.inference_url`. This is the entrypoint. All initial discovery goes through it.

A validator may run multiple dapi instances behind this entrypoint, each capable of serving different subnets. The `/v1/identity` endpoint advertises which instances are available:

```go
type IdentityData struct {
    Address        string            `json:"address"`
    WarmKeyAddress string            `json:"warm_key_address"`
    Block          int64             `json:"block"`
    Timestamp      string            `json:"timestamp"`
    DelegateTAs    []DelegateGroup   `json:"delegate_tas,omitempty"`
}

type DelegateGroup struct {
    URL            string `json:"url"`
    WarmKeyAddress string `json:"warm_key_address"`
}
```

`DelegateTAs` is an indexed list of (URL, warm key) pairs. Selection for a given subnet is deterministic:

```
groupIndex = hash(escrow_id, app_hash) % len(DelegateTAs)
```

All subnet participants compute the same index for each host, so everyone agrees on which URL and warm key to use. If `DelegateTAs` is empty or has one entry, the primary URL is used (the common case at launch).

- Phase 1: single dapi, `DelegateTAs` has one entry or is omitted. No behavioral change.
- Phase N: validator runs 4 dapi instances. Each advertises itself as a delegate group. Subnets get distributed across instances deterministically.

### Discovery Flow

When a subnet session starts:

1. The node has escrow info (from `OnEscrowCreated` notification or `GetEscrow` query).
2. The node derives the slot assignment locally from (app_hash, escrow_id, validator weights).
3. For each validator in the assignment, the node fetches `/v1/identity` from the validator's primary URL (from `participant.inference_url` on chain, provided by `GetValidatorInfo`).
4. The response includes `DelegateTAs`. The node selects the entry at `hash(escrow_id, app_hash) % len(DelegateTAs)`.
5. That entry's URL becomes the communication endpoint and its warm key becomes the expected signer for that host in this subnet.

Step 3 happens once per session start, not per request. The result is cached for the session lifetime.

### MainnetBridge Interface

Minimal. The subnet derives everything it can locally and only asks the bridge what it cannot compute.

```go
type MainnetBridge interface {
    // Notifications: mainnet -> subnet
    OnEscrowCreated(escrow EscrowInfo) error
    OnSettlementProposed(escrowID string, stateRoot []byte, nonce uint64) error
    OnSettlementFinalized(escrowID string) error

    // Queries: subnet -> mainnet
    GetEscrow(escrowID string) (*EscrowInfo, error)
    GetValidatorInfo(validatorAddress string) (*ValidatorInfo, error)
    VerifyWarmKey(warmAddress, validatorAddress string) (*WarmKeyInfo, error)

    // Actions: subnet -> mainnet
    SubmitDisputeState(escrowID string, stateRoot []byte, nonce uint64, sigs map[uint32][]byte) error
}

type ValidatorInfo struct {
    Address   string
    PublicKey []byte
    URL       string    // participant.inference_url from chain
    Weight    uint64
}

type EscrowInfo struct {
    EscrowID       string
    Amount         uint64
    CreatorAddress string
    CreationHeight int64
    AppHash        []byte
}

type WarmKeyInfo struct {
    ValidatorAddress string
}
```

7 methods. `GetEscrow` and `GetValidatorInfo` are called at session start. `VerifyWarmKey` is called lazily on first contact with an unknown warm key, then cached. `OnEscrowCreated`, `OnSettlementProposed`, and `OnSettlementFinalized` are push notifications from mainnet events. `OnSettlementProposed` triggers dispute checks; `OnSettlementFinalized` triggers local state cleanup. `SubmitDisputeState` is called by a host that detects stale settlement during the dispute window. In tests, all seven are trivial struct methods returning preset data.


## Storage

Full storage design is in [storage.md](./storage.md). Summary of key decisions:

Single SQLite file, WAL mode. All sessions share one `subnet.db`. Sessions are logically separated by `escrow_id`. Three tables: `sessions` (metadata), `diffs` (append-only log), `signatures` (async arrival). Write serialization via goroutine + buffered channel. Reads are concurrent.

EscrowState is a deterministic function of the ordered sequence of diffs applied from nonce 1 to latest_nonce. The state machine owns the in-memory state; storage persists diffs. On restart, diffs are replayed through the state machine to reconstruct state.

DiffRecord embeds Diff plus computed metadata: StateHash, Signatures (accumulated over time), WarmKeyDelta (warm key bindings from diff-contained signatures at this nonce), CreatedAt.

### Storage Interface

```go
type Storage interface {
    CreateSession(params CreateSessionParams) error
    MarkSettled(escrowID string) error
    ListActiveSessions() ([]string, error)
    AppendDiff(escrowID string, rec DiffRecord) error
    GetDiffs(escrowID string, fromNonce, toNonce uint64) ([]DiffRecord, error)
    AddSignature(escrowID string, nonce uint64, slotID uint32, sig []byte) error
    GetSignatures(escrowID string, nonce uint64) (map[uint32][]byte, error)
    GetSessionMeta(escrowID string) (*SessionMeta, error)
    MarkFinalized(escrowID string, nonce uint64) error
    LastFinalized(escrowID string) (uint64, error)
}
```

Phase 1: in-memory (`storage.NewMemory()`). Phase 2: SQLite. Later: PostgreSQL for multi-instance dapi.


## Gossip

### What Needs Gossiping

Two things with different frequency:

1. Nonce propagation (every request). After processing a user request, the host pushes the current nonce to K random peers. A skipped host has zero visibility into the session because the user carries diffs only to hosts it contacts. A skipped host never receives them. Nonce gossip is the only detection mechanism.

2. Lazy tx inclusion (failure-path only). When a host-proposed tx (MsgFinishInference, MsgValidation) is not included by the user after K rounds, the host pushes it to peers so they can refuse to sign until it's included.

### Pattern

After processing a user request, the host gossips the current nonce to K=10 random group members.

Detection probability for a skipped host_i (N=30, K=10). Each request causes one host to gossip to 10 random peers. Probability host_i is NOT among them: ~65%. Cumulative:

- After 5 requests: ~12% still unaware (88% detection)
- After 10 requests: ~1.4% still unaware (98.6% detection)
- After 15 requests: ~0.2% still unaware (99.8% detection)

As long as majority of hosts are honest and gossiping, propagation is reliable. A few malicious hosts refusing to gossip only slightly reduces the rate.

**Re-propagation.** If a host receives a gossiped nonce but never receives the actual user request within 120 seconds, it re-propagates to K=10 random peers (same as initial gossip, random selection, no tracking of who already received it). This amplifies coverage and signals to the group that a gap was detected.

Total cost per inference request: K=10 outbound HTTP calls with ~100 byte body. No persistent connections, no new ports. Group members are known from mainnet slot assignment, each already has a public URL.

### Endpoints

See [API Surface](#api-surface) for all subnet endpoints including gossip routes.

No new ports. No peer discovery (group is deterministic from mainnet). Connection pooling with short idle timeout handles the 10 calls per request without accumulation.

### Future Optimization

K=10 random peers over REST is the simplest correct approach. Gossip can be optimized independently of the rest of the system since the interface is just "notify peers about nonce N." Possible future directions: libp2p gossipsub, QUIC transport, adaptive K based on group size, or persistent WebSocket connections within a session. None of these affect the subnet state machine or storage layer.


## API Surface

All routes mounted on the existing dapi public server under `/subnet/v1/`. No new ports. Group members are known from mainnet slot assignment, each has a public URL.

### Request Authentication

All POST endpoints include sender authentication in HTTP headers:

```
X-Subnet-Signature: <hex>
X-Subnet-Timestamp: <unix>
```

Signature covers `sha256(escrow_id || request_body || timestamp_bytes)`. Receiver recovers the sender's address via ecrecover, verifies it belongs to a group member (host-to-host endpoints) or the escrow creator (user-to-host endpoints), and checks the timestamp is within bounds (+-30s). Requests failing authentication are rejected before any body processing.

For `/chat/completions`, diff signatures already authenticate the user. The header signature provides fast rejection before parsing potentially large request bodies.

### Inference Request

```
POST /subnet/v1/sessions/{escrow_id}/chat/completions
```

Main endpoint. User sends OpenAI-compatible request body with subnet extensions:

```json
{
  "model": "...",
  "stream": true,
  "messages": [...],
  "diffs": [
    {"nonce": 1, "txs": [...], "sigs": {...}}
  ],
  "state_hash": "<SHA256>"
}
```

Host applies diffs, validates nonce ordering and round-robin assignment, executes inference via InferenceEngine. Response is a streaming SSE stream identical to OpenAI format. After the stream completes, host returns state signature and mempool in a final SSE event:

```json
{"type": "subnet_meta", "signature": "<hex>", "mempool": [...]}
```

During finalizing rounds: same endpoint, same format, but no `messages` field and no MsgStartInference in diffs. Host processes diffs (MsgRevealSeed, remaining txs), returns signature + mempool.

### Timeout Verification

```
POST /subnet/v1/sessions/{escrow_id}/verify-timeout
```

User requests timeout verification from non-executor hosts. Authenticated via `X-Subnet-Signature` (see Request Authentication). Host verifies the sender is the escrow creator before contacting executor.

Host contacts executor, assesses validity, returns signed vote.

```json
{
  "inference_id": 123,
  "reason": "refused",
  "prompt_data": "<base64>"
}
```

```json
{
  "vote": "accept",
  "inference_id": 123,
  "reason": "refused",
  "timestamp": 1234567890,
  "signature": "<hex>"
}
```

Vote signature covers: `(escrow_id, inference_id, reason, vote, host_timestamp)`. See README.md Timeout Verification for the full flow per reason type.

During verification, the verifying host contacts the executor to forward prompt data (reason=refused) or check for MsgFinishInference (reason=execution). This uses existing subnet endpoints -- the host sends diffs to the executor via `/chat/completions` or fetches state via `/diffs`.

### Gossip

```
POST /subnet/v1/sessions/{escrow_id}/gossip/nonce
```

Nonce propagation. Sent to K=10 random group members after processing a user request.

```json
{
  "nonce": 42,
  "state_hash": "<hex>",
  "state_signature": "<hex>",
  "sender_slot": 3
}
```

`state_hash` and `state_signature` enable equivocation detection. If a host sees different state hashes for the same nonce, it requests diffs from both sources.

```
POST /subnet/v1/sessions/{escrow_id}/gossip/txs
```

Lazy tx propagation. Sent only when host-proposed txs are not included by the user after K rounds.

```json
{
  "txs": [...],
  "sender_slot": 3
}
```

Each tx in `txs` carries `proposer_sig` -- the proposer's signature over the serialized tx content (see design.md What Gets Signed). Receiving hosts verify the signature before adding to their mempool.

### State Recovery

```
GET /subnet/v1/sessions/{escrow_id}/diffs?from_nonce=N&to_nonce=M
```

Fetch diffs for state recovery. Used by hosts that detected a gap via nonce gossip, by hosts preparing for host-initiated settlement, and by users reconnecting after disconnect.

```
GET /subnet/v1/sessions/{escrow_id}/mempool
```

Fetch host's unsettled proposed transactions for this session. Fallback when lazy gossip fails. Returns all transactions in the host's mempool: both the host's own proposed txs and txs received from other hosts via gossip. Each tx carries `proposer_sig` from its original proposer.


## Settlement

### What Mainnet Needs to Verify

Mainnet receives MsgSettleEscrow and must verify that the claimed usage and host_stats are correct. The mandatory finalizing rounds (see README.md Settlement) ensures all inferences are resolved and host_stats are final before settlement.

```
MsgSettleEscrow:
  escrow_id          string
  state_root         []byte                       # Merkle root after finalizing round
  nonce              uint64                       # latest nonce
  signatures         map[uint32][]byte            # slot_id -> sig over (state_root, escrow_id, nonce)
  rest_hash          []byte                       # Merkle sibling: hash(balance_bytes || inferences_hash)
  host_stats         map[uint32]HostStats
```

Mainnet verification:
1. Compute host_stats_hash from the submitted host_stats
2. Verify Merkle proof: hash(host_stats_hash || rest_hash) == state_root
3. Verify 2/3+ slot-weighted signatures over (state_root || escrow_id || nonce)
4. Settle: pay each host from escrow according to host_stats[slot].cost, refund remaining balance (escrow_amount - sum of all host costs) to user, record host_stats

The Merkle proof is constant size: one sibling hash (rest_hash). Mainnet never sees individual inference records or balance.

No balance field in the payload. Mainnet knows the escrow amount and computes the refund from the sum of host_stats[*].cost.

### Finalizing Rounds

Before settling, the user completes two rounds in round-robin order without MsgStartInference:

- Round 1: collect MsgRevealSeed, pending MsgFinishInference, and any remaining MsgValidation from each host. After this round, all seeds and txs are in state, but hosts visited early haven't seen seeds from hosts visited later.
- Round 2: propagate the complete state to everyone. Each host applies all seeds -- the state machine computes required_validations and completed_validations per host deterministically from the revealed seeds and existing MsgValidation txs. Hosts sign the final state.

Both rounds use the same diff format -- no special request type, just no new inferences.

> Round 1 could be replaced by a dedicated off-chain seed collection endpoint where each host signs and returns its seed directly. Simpler but requires a new endpoint outside the diff protocol. Optimization for later.

### Dispute Window

When mainnet receives MsgSettleEscrow, settlement enters a dispute window of X blocks (TBD). Bridge notifies each host via `OnSettlementProposed(escrowID, stateRoot, nonce)`.

Host compares proposed nonce against its local latest_nonce:
- If proposed nonce < local latest_nonce AND the host has 2/3+ signatures for a higher nonce: host calls `SubmitDisputeState` with the newer state. User submitted stale state and is penalized (forfeits remaining escrow to hosts).
- If proposed nonce >= local latest_nonce: no dispute. Settlement finalizes after X blocks.

The host is passive -- it reacts to the bridge notification, doesn't poll. One new notification method plus one action method on the bridge.

### Host-Initiated Settlement

If the user disappears, any group member can submit MsgSettleEscrow after a timeout (TBD: wall-clock from last nonce or escrow expiry height set at creation). All hosts have full state within one round (propagated via diffs). If a host is missing recent state, it requests from other hosts via the public API endpoint. Same 2/3+ signature requirement, same dispute window.


## ML Node Integration

The subnet reuses the existing dapi infrastructure for ML node interaction. Two interfaces defined in the subnet package, implemented by dapi as thin adapters over existing code. Zero cosmos-sdk in subnet, minimal changes in dapi.

### What dapi Already Has

Inference execution and validation re-execution share the same core: send an OpenAI-compatible request to a vLLM node, collect response, extract logits and token counts. This logic lives in:

- `completionapi/` -- request modification (`ModifyRequestBody`), response parsing (`CompletionResponse` interface), streaming processor (`ExecutorResponseProcessor`), logit extraction. Pure HTTP + JSON, zero chain dependencies.
- `internal/server/public/proxy.go` -- streaming response handler. Pure HTTP.
- `broker/` -- ML node locking, retry on transport/5xx errors, model-based node selection (`DoWithLockedNodeHTTPRetry`, `LockNode`).
- `internal/validation/inference_validation.go` -- `validateWithPayloads()` re-executes inference with enforced tokens, `compareLogits()` computes similarity score. Core logic is pure compute, only depends on node URL and payloads.

Chain-coupled parts that the subnet does NOT need: MsgStartInference/MsgFinishInference/MsgValidation chain transactions (subnet tracks its own state), transfer agent logic, escrow validation. Authz verification is still needed for warm key grants.

### Subnet Interfaces

Defined at the subnet module root (`subnet/engine.go`, `subnet/types.go`). These are the contract between subnet and dapi.

```go
// subnet/engine.go

// InferenceEngine executes inference on an ML node.
// Implemented by dapi using existing broker + completionapi.
type InferenceEngine interface {
    Execute(ctx context.Context, req ExecuteRequest) (*ExecuteResult, error)
}

// ValidationEngine re-executes inference and compares logits.
// Implemented by dapi using existing broker + completionapi.
type ValidationEngine interface {
    Validate(ctx context.Context, req ValidateRequest) (*ValidateResult, error)
}
```

```go
// subnet/types.go

type ExecuteRequest struct {
    Model       string
    RequestBody []byte              // original OpenAI-compatible JSON
    Seed        int32
    Writer      http.ResponseWriter // receives streaming response
}

type ExecuteResult struct {
    ResponsePayload  []byte
    PromptPayload    []byte  // canonicalized request
    PromptHash       string
    ResponseHash     string
    PromptTokens     uint64
    CompletionTokens uint64
}

type ValidateRequest struct {
    Model           string
    PromptPayload   []byte
    ResponsePayload []byte
}

type ValidateResult struct {
    Similarity   float64 // 1.0 = identical, <0.99 = invalid
    ResponseHash string
}
```

### dapi Adapters

Adapters live in dapi, not in the subnet package. Each wraps existing functions with no modifications to the originals.

```
decentralized-api/
  internal/
    subnet/
      engine_adapter.go       # implements InferenceEngine
      validation_adapter.go   # implements ValidationEngine
      router.go               # mounts /subnet/v1/ routes, wires adapters
```

InferenceEngine adapter (~50-80 lines):
1. `completionapi.ModifyRequestBody(requestBody, seed)` -- existing
2. `broker.DoWithLockedNodeHTTPRetry(broker, model, ...)` -- existing, broker used as-is
3. `http.Post(completionsUrl, body)` -- existing pattern
4. `completionapi.NewExecutorResponseProcessor` + `proxyResponse` -- existing, streams to writer
5. Extract hash + usage from `responseProcessor.GetResponse()` -- existing
6. Return `ExecuteResult`

ValidationEngine adapter (~40-60 lines):
1. `broker.LockNode(broker, model, ...)` -- existing
2. Parse payloads, extract enforced tokens via `completionapi` -- existing
3. POST to mlnode with enforced tokens -- existing pattern from `validateWithPayloads`
4. Compare logits via `compareLogits()` -- existing
5. Return `ValidateResult`

### Integration Point

The subnet mounts on the existing dapi server as a new echo router group:

```go
// decentralized-api/internal/subnet/router.go

func Mount(group *echo.Group, engine InferenceEngine, validator ValidationEngine, ...) {
    host := subnet.NewHost(engine, validator, storage, signer, ...)
    group.POST("/sessions/:escrow_id/chat/completions", host.HandleInference)
    group.POST("/sessions/:escrow_id/gossip/nonce", host.HandleNonceGossip)
    group.POST("/sessions/:escrow_id/gossip/txs", host.HandleTxGossip)
}
```

The dapi server startup wires the adapters and mounts the group at `/subnet/v1/`. The subnet handler receives user requests with diffs, applies them to subnet state, calls `InferenceEngine.Execute()`, creates MsgFinishInference for subnet state, signs the new state, returns signature + streaming response.


## Attack Vectors

Documented in `subnet/docs/attacks.md`. Each attack gets one section:
header names the attack, body lists mitigations as a numbered list.
Add new entries there when identifying new vectors.


## Performance Tracking

`make stress-test` runs a stress test: 16 hosts, 1 user, 1000 rounds (16,000 inferences), full pipeline through finalization and settlement. Measures per-diff timing (percentiles), seed reveal cost, state/diff memory footprint, and signature collection completeness. Build tag `stress` isolates it from normal test runs.


## Future Work

- Compliance ordering: if MsgFinishInference arrives after seed reveal, the inference is not counted in RequiredValidations for that validator.
- Seed grinding: validator could try different keys to minimize RequiredValidations via ShouldValidate. Enforce key pinning at escrow creation.

## Open Questions

1. Re-propagation timeout: 120s is a starting point. Should it scale with model latency or be fixed?
