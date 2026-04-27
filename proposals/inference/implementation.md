# Inference Subnet: Implementation Plan

Phased implementation of the subnet described in [README.md](./README.md) and [design.md](./design.md). Each phase is self-contained: define scope, write tests first, implement, verify. Later phases build on earlier ones.

General approach: plan one phase at a time, implement, then plan the next. Test design is the priority -- tests define the contract before any implementation exists.

Implementation status: the codebase is ahead of this phased plan in some areas (warm key handling exists in the state machine) and behind in others (SQLite persistence, restart recovery, and warm-key delta replay from [storage.md](./storage.md) are not yet implemented; current code uses in-memory storage and the older storage interface).

Test levels:
- Unit tests: per-package, in-process, no I/O
- Subnet integration tests: multi-node, mock MainnetBridge, stub InferenceEngine/ValidationEngine
- Testermint: full system (chain + dapi + subnet + mock ML nodes)

| Phase | Unit | Subnet integration | Testermint |
|-------|------|--------------------|------------|
| 1     | x    |                    |            |
| 2     | x    | in-process         |            |
| 3     | x    | multi-node (HTTP)  |            |
| 4     | x    | multi-node         |            |
| 5     | x    |                    |            |
| 6     | x    | multi-node         |            |
| 7     | x    | + real chain query |            |
| 8     | x    | dapi-hosted        |            |
| 9     |      | user lib           |            |
| 10    |      |                    | x          |


## Phase 1: Foundation and State Machine [COMMITTED]

Goal: standalone `subnet/` Go module that can apply diffs, track state, compute hashes, and verify signatures. No networking, no mainnet integration, no gossip. Everything runs in-process, driven by Go tests.

Deliverables:
1. Project structure and Go module
2. Proto definitions for all 8 subnet transaction types
3. Domain types (EscrowState, InferenceRecord, HostStats, Diff)
4. State machine: apply diffs, verify nonces, update balances/stats
5. State hashing (two-level Merkle)
6. Signing: secp256k1 sign/verify via go-ethereum/crypto
7. Storage interface + in-memory implementation
8. Tests covering the full inference lifecycle

### 1.1 Project and Module Setup

Create `subnet/` at repo root with its own `go.mod`. Dependencies: `go-ethereum/crypto` (secp256k1), `google.golang.org/protobuf` (proto). No cosmos-sdk.

Package layout: see [design.md Package Structure](./design.md#package-structure). Phase 1 additions: `engine.go` and `types.go` are stubs. `bridge/interface.go` and `gossip/interface.go` define interfaces with no implementation. `storage/` uses in-memory implementation only.

### 1.2 Proto Definitions

`proto/subnet/v1/tx.proto` -- all 8 subnet transaction types plus TimeoutVote:

```protobuf
syntax = "proto3";
package subnet.v1;
option go_package = "subnet/types";

message MsgStartInference {
  uint64 inference_id = 1;
  bytes  prompt_hash = 2;
  string model = 3;
  uint64 input_length = 4;          // prompt length in characters
  uint64 max_tokens = 5;            // max output tokens (matches request body)
  int64  started_at = 6;            // user's timestamp
  bytes  executor_sig = 7;          // optional: receipt from executor, skips pending if present
}

message MsgConfirmStart {
  uint64 inference_id = 1;
  bytes  executor_sig = 2;       // receipt: sign(inference_id || prompt_hash || model || input_length || max_tokens || started_at)
}

message MsgFinishInference {
  uint64 inference_id = 1;
  bytes  response_hash = 2;
  uint64 input_tokens = 3;          // actual input tokens from execution
  uint64 output_tokens = 4;         // actual output tokens from execution
  uint32 executor_slot = 5;
  bytes  proposer_sig = 6;          // host signature, verified on apply then discarded
}

message MsgTimeoutInference {
  uint64 inference_id = 1;
  string reason = 2;             // "refused" or "execution"
  repeated TimeoutVote votes = 3;
}

message TimeoutVote {
  uint32 voter_slot = 1;
  bool   accept = 2;
  bytes  signature = 3;          // sign(escrow_id || inference_id || reason || accept)
}

message MsgValidation {
  uint64 inference_id = 1;
  uint32 validator_slot = 2;
  bool   valid = 3;
  bytes  proposer_sig = 4;
}

message MsgValidationVote {
  uint64 inference_id = 1;
  uint32 voter_slot = 2;
  bool   vote_valid = 3;
  bytes  proposer_sig = 4;
}

message MsgRevealSeed {
  uint32 slot_id = 1;
  bytes  signature = 2;          // sign(escrow_id_bytes) with host's key
  bytes  proposer_sig = 3;
}

message MsgFinalizeRound {
  // no fields -- presence is the signal. user-proposed.
}
```

`proto/subnet/v1/state.proto` -- for deterministic serialization in hash computation:

```protobuf
syntax = "proto3";
package subnet.v1;
option go_package = "subnet/types";

// Used for deterministic serialization when computing state hashes.
// Not the runtime state representation (that's domain types in Go).

message HostStatsProto {
  uint32 slot_id = 1;
  uint32 missed = 2;
  uint32 invalid = 3;
  uint64 cost = 4;
  uint32 required_validations = 5;
  uint32 completed_validations = 6;
}

message HostStatsMapProto {
  repeated HostStatsProto entries = 1;  // sorted by slot_id
}

// Field count can be smaller: raw token counts are recoverable from stored diffs.
// Keeping all for debuggability.
message InferenceRecordProto {
  uint64 inference_id = 1;
  uint32 status = 2;
  uint32 executor_slot = 3;
  string model = 4;
  bytes  prompt_hash = 5;
  bytes  response_hash = 6;
  uint64 input_length = 7;          // chars, from MsgStartInference
  uint64 max_tokens = 8;            // from MsgStartInference
  uint64 input_tokens = 9;          // actual, from MsgFinishInference
  uint64 output_tokens = 10;        // actual, from MsgFinishInference
  uint64 reserved_cost = 11;        // computed at start from (input_length, max_tokens, config)
  uint64 actual_cost = 12;          // computed at finish from (input_tokens, output_tokens, config)
  int64  started_at = 13;
  uint32 votes_valid = 14;
  uint32 votes_invalid = 15;
}

message InferencesMapProto {
  repeated InferenceRecordProto entries = 1;  // sorted by inference_id
}
```

Generate Go code from protos using `protoc` (not ignite -- subnet is standalone).

### 1.3 Domain Types

Field semantics: see [design.md State Machine](./design.md#state-machine) and [Session State](./design.md#session-state).

`types/domain.go`:

```go
type InferenceStatus uint8

const (
  StatusPending     InferenceStatus = iota
  StatusStarted
  StatusFinished
  StatusChallenged
  StatusValidated
  StatusInvalidated
  StatusTimedOut
)

type InferenceRecord struct {
  Status        InferenceStatus
  ExecutorSlot  uint32
  Model         string
  PromptHash    []byte
  ResponseHash  []byte
  InputLength   uint64
  MaxTokens     uint64
  InputTokens   uint64
  OutputTokens  uint64
  ReservedCost  uint64
  ActualCost    uint64
  StartedAt     int64
  VotesValid    uint32
  VotesInvalid  uint32
  VotedSlots    map[uint32]bool
}

type HostStats struct {
  Missed               uint32
  Invalid              uint32
  Cost                 uint64
  RequiredValidations  uint32
  CompletedValidations uint32
}

type SessionConfig struct {
  RefusalTimeout   int64   // seconds before reason=refused timeout (test default: 60)
  ExecutionTimeout int64   // seconds before reason=execution timeout (test default: 1200)
  TokenPrice       uint64  // price per unit (test default: 1 = 1 ngonka)
  VoteThreshold    uint32  // minimum slot-weighted accept votes for timeout (total_slots / 2)
}
// Q: How to have dynamic pricing? TokenPrice is flat per session.
// Per-model pricing needs further design. Flat price sufficient for Phase 1.

type EscrowState struct {
  EscrowID      string
  Config        SessionConfig
  Group         []SlotAssignment              // slot assignments, immutable for the session
  Balance       uint64                        // remaining escrow
  Phase         SessionPhase                  // Active | Finalizing | Settlement
  FinalizeNonce uint64                        // nonce at which finalization started
  Inferences    map[uint64]*InferenceRecord   // keyed by inference_id (= nonce of MsgStartInference)
  HostStats     map[uint32]*HostStats         // keyed by slot_id
  RevealedSeeds map[uint32]int64              // keyed by slot_id, from MsgRevealSeed
  WarmKeys      map[uint32]string             // slot_id -> warm key address, lazily populated
  LatestNonce   uint64
}

type SubnetTx struct {
  // one-of: each tx type
  StartInference   *MsgStartInference
  ConfirmStart     *MsgConfirmStart
  FinishInference  *MsgFinishInference
  Validation       *MsgValidation
  ValidationVote   *MsgValidationVote
  TimeoutInference *MsgTimeoutInference
  RevealSeed       *MsgRevealSeed
  FinalizeRound    *MsgFinalizeRound
}

// Diff is the protocol primitive: what the user creates and signs.
// UserSig covers hash(serialize(DiffContent{nonce, txs, escrow_id, post_state_root})).
type Diff struct {
  Nonce         uint64
  Txs           []SubnetTx
  UserSig       []byte
  PostStateRoot []byte     // claimed state root after applying this diff's txs
}

// DiffRecord is the storage representation: Diff + computed metadata.
type DiffRecord struct {
  Diff
  StateHash    []byte               // state_root after applying this diff
  Signatures   map[uint32][]byte    // slot_id -> state signature (accumulated over time)
  WarmKeyDelta map[uint32]string    // warm key bindings introduced at this nonce (nil if none)
  CreatedAt    int64
}

type SlotAssignment struct {
  SlotID           uint32
  ValidatorAddress string   // cold key / validator identity, used for settlement
}
```

### 1.4 State Machine

`state/interface.go`:

```go
// NewStateMachine creates a state machine for a session.
// group and balance come from mainnet escrow data.
// verifier is used to check all signatures (user, executor, host-proposed txs).
func NewStateMachine(escrowID string, config SessionConfig, group []SlotAssignment, balance uint64, userAddress string, verifier Verifier) StateMachine

type StateMachine interface {
  // ApplyDiff validates and applies a diff at the next expected nonce.
  // Verifies user signature. Diff may contain 0 or 1 MsgStartInference.
  // Returns the computed state root after application.
  ApplyDiff(diff Diff) (stateRoot []byte, err error)

  // ApplyLocal replays a previously verified diff (skip user signature verification).
  // Used during restart recovery. Warm keys must be injected before calling this.
  ApplyLocal(nonce uint64, txs []*SubnetTx) (stateRoot []byte, err error)

  // InjectWarmKeys writes warm key bindings into state without overwriting existing ones.
  // Used during replay to restore bindings before ApplyLocal.
  InjectWarmKeys(delta map[uint32]string)

  // ComputeStateRoot returns the current state root without modifying state.
  ComputeStateRoot() []byte
}
```

Diff application:
1. Verify user signature on the diff (`UserSig` over `hash(serialize(DiffContent{nonce, txs, escrow_id, post_state_root}))`)
2. Validate nonce is sequential (latest_nonce + 1)
3. Validate at most one MsgStartInference per diff. Reject MsgStartInference if `state.Phase != Active`.
4. For each tx: verify proposer_sig on host-proposed txs (MsgFinishInference, MsgValidation, MsgValidationVote, MsgRevealSeed), validate well-formed, check preconditions, apply to EscrowState. Warm key bindings are lazily populated via `ResolveWarmKey` during proposer sig verification.
5. Compute state_root: `sha256(host_stats_hash || rest_hash || phase_byte)`, where rest_hash includes `warm_keys_hash`
6. Verify computed root == `diff.PostStateRoot`. Reject if mismatch.
7. Return state_root

Transitions follow [design.md Inference Lifecycle](./design.md#inference-lifecycle) and [State Machine](./design.md#state-machine). Phase 1 deviation: any non-executor slot can validate (ShouldValidate deferred to Phase 4).

### 1.5 State Hashing

Hash computation follows [design.md State Hash](./design.md#state-hash). Proto serialization ensures determinism.

### 1.6 Signing

`signing/interface.go`:

```go
type Signer interface {
  // Sign signs the message and returns the signature.
  Sign(message []byte) ([]byte, error)

  // Address returns the signer's address derived from its public key.
  Address() string
}

type Verifier interface {
  // RecoverAddress recovers the signer's address from message and signature.
  RecoverAddress(message []byte, signature []byte) (string, error)
}
```

Message formats: see [design.md What Gets Signed](./design.md#what-gets-signed).

### 1.7 Storage

Storage interface: see [storage.md](./storage.md#storage-interface) for the full interface and [design.md](./design.md#storage-interface) for a summary.

No `GetState` on the storage interface. Live state only exists in the state machine's memory. `GetSessionMeta` returns immutable session metadata (creator address, config, group, initial balance). Any caller that needs live state must replay diffs.

Phase 1 implementation: in-memory map protected by mutex (persistence semantics hold -- state is never stale within a process lifetime). SQLite implementation is Phase 2 (see [storage.md](./storage.md)).

### 1.8 Test Plan

Tests are the primary deliverable. They define the contract and validate the state machine before anything else exists.

**State machine tests** (`state/machine_test.go`):

```
TestApplyDiff_UserSigVerification
  - Diff with invalid UserSig, verify rejection
  - Diff with valid UserSig, verify acceptance

TestApplyDiff_StartInference
  - Apply MsgStartInference, verify record created with status=pending, balance decremented by reserved_cost

TestApplyDiff_ConfirmStart
  - Apply Start then ConfirmStart with valid executor receipt, verify status=started

TestApplyDiff_ConfirmStart_InvalidReceipt
  - Apply Start then ConfirmStart with bad executor_sig, verify rejection

TestApplyDiff_StartInference_FastPath
  - MsgStartInference with executor_sig present, verify status=started immediately

TestApplyDiff_FinishInference
  - Start -> ConfirmStart -> Finish, verify status=finished, balance += reserved_cost - actual_cost, host_stats.cost updated

TestApplyDiff_FinishInference_WrongExecutorSlot
  - MsgFinishInference with executor_slot != assigned executor, verify rejection

TestApplyDiff_FinishInference_InvalidProposerSig
  - MsgFinishInference with bad proposer_sig, verify rejection

TestApplyDiff_Validation_Valid
  - Start -> ConfirmStart -> Finish -> Validation(valid=true), verify status=validated

TestApplyDiff_Validation_SelfValidation
  - MsgValidation where validator_slot == executor_slot, verify rejection

TestApplyDiff_Validation_Invalid_ChallengeVoting
  - Finish -> Validation(valid=false), verify status=challenged
  - Apply votes, verify transition to validated or invalidated based on majority (total_slots/2)
  - Verify host_stats.invalid incremented on invalidation
  - Verify cost refunded on invalidation (host_stats.cost decremented, balance restored)

TestApplyDiff_Timeout_Refused
  - Start (pending, no receipt) -> Timeout(reason=refused, with accept votes)
  - Verify status=timed_out, host_stats.missed += 1, reserved_cost released

TestApplyDiff_Timeout_Execution
  - Start -> ConfirmStart (started) -> Timeout(reason=execution, with accept votes)
  - Verify status=timed_out, host_stats.missed += 1, reserved cost released

TestApplyDiff_Timeout_WrongReason
  - Timeout(reason=execution) on pending inference, verify rejection
  - Timeout(reason=refused) on started inference, verify rejection

TestApplyDiff_Timeout_InsufficientVotes
  - Timeout with fewer accept votes than total_slots/2, verify rejection

TestApplyDiff_Timeout_AfterFinish
  - Finish -> Timeout, verify rejection (finished cannot transition to timed_out)

TestApplyDiff_NonceSequential
  - Diff with wrong nonce, verify error

TestApplyDiff_MultipleMsgStartInference
  - Diff with 2 MsgStartInference, verify rejection

TestApplyDiff_FinalizeRound
  - MsgFinalizeRound sets state.Phase = Finalizing, records FinalizeNonce
  - MsgStartInference after MsgFinalizeRound, verify rejection
  - Second MsgFinalizeRound, verify rejection (already finalizing)
  - Host-proposed txs (MsgFinishInference, MsgRevealSeed) still accepted after finalization

TestApplyDiff_DuplicateTimeout
  - Two timeouts for same inference_id, second rejected (already timed_out)

TestApplyDiff_FullLifecycle
  - 10 inferences through various paths (finished, timed_out, validated, invalidated)
  - Verify final host_stats match expectations
  - Verify final balance = initial_balance - sum(host_stats[*].cost)

TestApplyDiff_EscrowBalanceCheck
  - Start inference when balance < reserved_cost, verify rejection
```

**State hash tests** (`state/hash_test.go`):

```
TestComputeStateRoot_Deterministic
  - Same state produces same root across multiple calls

TestComputeStateRoot_DifferentState
  - Different balance produces different root

TestStateRoot_MerkleStructure
  - Verify hash(host_stats_hash || rest_hash) == state_root
  - Verify rest_hash = hash(balance_bytes || inferences_hash)

TestStateRoot_SortedKeys
  - host_stats with slot_ids [5, 2, 8] produces same hash regardless of insertion order
```

**Signing tests** (`signing/secp256k1_test.go`):

```
TestSign_RecoverAddress
  - Sign message, recover address, verify match

TestSign_DifferentKeys
  - Two keys produce different signatures, recover different addresses

TestVerify_TamperedMessage
  - Modify message after signing, verify recovered address differs
```

**Storage tests** (`storage/memory_test.go`):

```
TestCreateSession_GetSessionMeta
  - Create session, retrieve via GetSessionMeta, verify fields

TestAppendDiff_GetDiffs
  - Append 5 diffs, retrieve range, verify order and content

TestAddSignature
  - Append diff, add signature later, verify it appears in GetDiffs result
```

**Integration test** (`state/integration_test.go`):

```
TestFullSession_HappyPath
  - Create state machine with 5-slot group
  - Apply 15 diffs (3 full rounds of round-robin)
  - Each diff: MsgStartInference + accumulated MsgConfirmStart/MsgFinishInference from previous
  - Sign each state root with the receiving host's key
  - Verify 2/3+ signatures exist for the final state
  - Verify host_stats reflect correct cost distribution
  - Compute settlement data (state_root, rest_hash, host_stats, signatures)
```

### 1.9 Scope Boundaries

Phase 1 INCLUDES:
- User signature verification on diffs
- Proposer signature verification on host-proposed txs
- Executor receipt signature verification (MsgConfirmStart)
- executor_slot match verification (MsgFinishInference)
- validator_slot != executor_slot check (MsgValidation)
- At most one MsgStartInference per diff enforcement
- MsgFinalizeRound: sets Phase = Finalizing, records FinalizeNonce, blocks further MsgStartInference

Phase 1 does NOT include:
- Networking, HTTP handlers, gossip
- MainnetBridge implementation (interface only)
- InferenceEngine/ValidationEngine implementation (interface stubs only)
- Round-robin enforcement (host role logic)
- Inclusion enforcement (K-round grace period)
- Timeout verification flow (vote collection from hosts)
- Host/User role separation
- Equivocation detection
- Validation scheduling (ShouldValidate, seed reveal)
- Real storage (SQLite/PostgreSQL)
- Proto generation CI pipeline (manual protoc for now)

These belong to later phases.


## Phase 2: Host and User Roles

Goal: protocol logic for both participants. Full session runnable in-process without networking.

Scope:
- Host role (execution loop):
  - Validate incoming diffs (nonce, signatures, well-formedness)
  - Execute inference when assigned as executor (via stub InferenceEngine)
  - Sign executor receipt (MsgConfirmStart data)
  - Sign state root (or withhold based on acceptance rules)
  - Propose MsgFinishInference after execution completes
  - Mempool management: track own proposed txs, include in response
  - Inclusion enforcement: withhold signature if mempool txs not included after K rounds
- User role:
  - Compose diffs with correct tx ordering
  - Round-robin host selection: `slot_at_position(nonce % group_size)`
  - MsgConfirmStart pipelining (receipt from host response -> included in next diff)
  - Signature collection and tracking
- InferenceEngine/ValidationEngine: stub implementations returning fixed results

Host responsibilities deferred to later phases:
- Validation (ShouldValidate, MsgValidation, MsgValidationVote) -> Phase 4
- Timeout verification participation (vote on timeout requests) -> Phase 3
- Gossip (nonce propagation, lazy tx gossip) -> Phase 3
- Seed reveal (MsgRevealSeed) -> Phase 4

Tests: in-process integration test with 5 hosts + 1 user. Full happy-path session (3 rounds, 15 inferences). Timeout detection (user-side). Signature withholding when mempool txs are not included. Inclusion enforcement unblocks when missing txs are added.

Testable deliverable: user composes correct diffs in round-robin order, hosts execute and propose MsgFinishInference, signatures accumulate to 2/3+, session completes with correct host_stats.


## Phase 3: Networking and Gossip

Goal: real HTTP transport between nodes. Gossip protocol. Timeout verification flow.

Scope:
- HTTP handlers for all subnet endpoints (see design.md API Surface):
  - POST /subnet/v1/sessions/{id}/chat/completions (inference with diffs)
  - POST /subnet/v1/sessions/{id}/verify-timeout (timeout verification)
  - POST /subnet/v1/sessions/{id}/gossip/nonce (nonce propagation)
  - POST /subnet/v1/sessions/{id}/gossip/txs (lazy tx gossip)
  - GET /subnet/v1/sessions/{id}/diffs (state recovery)
  - GET /subnet/v1/sessions/{id}/mempool (unsettled txs)
- Request authentication: X-Subnet-Signature header (see design.md Request Authentication)
- Gossip: nonce propagation to K=10 random peers, lazy tx gossip after K rounds, re-propagation on gap detection (120s)
- Timeout verification: user contacts non-executor hosts, hosts contact executor, return signed votes
- Equivocation detection: conflicting state hashes at same nonce via gossip

Multi-node test infrastructure: nodes as goroutines with real HTTP listeners on localhost. Mock MainnetBridge, stub InferenceEngine.

Tests: multi-node integration tests. Happy path over HTTP. Host-down + timeout verification (reason=refused and reason=execution). Equivocation detection and session termination. Lazy tx gossip triggers when user withholds host txs.

Testable deliverable: subnet cluster of N nodes communicates over HTTP, gossip detects gaps and equivocation, timeout verification works end-to-end.


## Phase 4: Validation and Settlement

Goal: probabilistic validation protocol, seed reveal, settlement data construction.

Scope:
- Seed derivation: `first_8_bytes(sign(escrow_id_bytes))` per host per session. Pure function, ~10 lines.
- ShouldValidate: pure function using DeterministicFloat (sha256-based, same approach as chain). Flat probability per session via `SessionConfig.ValidationRate`. No reputation or traffic basis -- subnet has no cross-session state. All hosts compute the same result from the same inputs. ~20 lines.
- MsgRevealSeed state effect: derive seed from revealed signature, run ShouldValidate for all finished inferences, update required_validations and completed_validations in HostStats.
- Host: produce MsgRevealSeed during finalization (sign escrow_id_bytes, add to mempool).
- Compliance computation: deterministic from revealed seeds + existing MsgValidation txs.
- Settlement payload construction: extract (state_root, rest_hash, host_stats, signatures) for MsgSettleEscrow.

Existing code reference (not imported, reimplemented):
- `inference-chain/x/inference/calculations/should_validate.go`: `DeterministicFloat()` pattern reused (sha256 + first 8 bytes -> float). `ShouldValidate()` simplified -- subnet uses flat rate instead of reputation-weighted probability.
- `decentralized-api/internal/seed/seed.go`: `CreateSeedForEpoch()` signs bytes and takes first 8 bytes -- same pattern as subnet seed derivation (signs escrow_id instead of epoch index).

Not in Phase 4:
- Dispute detection (requires MainnetBridge `OnSettlementProposed` notification -> Phase 7).
- Host validation execution (calling `ValidationEngine.Validate()` to produce MsgValidation -> Phase 8, needs ML engine). Phase 4 tests use manually composed MsgValidation txs.
- Pinned signing key verification (first-sig pinning for seed reveal). Adds complexity, deferred.

Tests: full session with seed reveal, finalizing rounds, compliance verification, settlement data construction. Both in-process and multi-node.

Max group size is 128 slots. Enforced by Bitmap128 in ValidatedBy and VotedSlots. Bitmap operations are no-op/false for bit >= 128, preventing out-of-bounds corruption.

Testable deliverable: session ends with correct settlement payload. Seed reveal produces valid ShouldValidate expectations. Compliance numbers match. Settlement Merkle proof is verifiable.


## Phase 5: Mainnet Modifications

Goal: MsgCreateEscrow and MsgSettleEscrow in the inference chain module.

Scope:
- Proto definitions in `inference-chain/proto/` for MsgCreateEscrow and MsgSettleEscrow
- Keeper logic: escrow creation (lock funds, store escrow info, record app_hash), settlement verification (recompute host_stats_hash, verify Merkle proof `hash(host_stats_hash || rest_hash) == state_root`, check 2/3+ slot-weighted signatures)
- Escrow distribution: pay each host from host_stats[slot].cost, refund remaining balance to user
- Record host_stats on chain (for reputation, future punishment logic)
- ~~Dispute window: X blocks after settlement proposal. Competing state with higher nonce and valid signatures overrides.~~ Replaced by finalization round in the subnet protocol. The user proposes MsgFinalizeRound, hosts reveal seeds and complete pending validations, then the user settles with a single MsgSettleSubnetEscrow that includes 2/3+ slot signatures on the finalized state root. No on-chain dispute window needed because settlement requires quorum agreement on the final state before submission.
- Unsettled escrow pruning: escrows older than 2 epochs are pruned by the chain. Unsettled escrows split funds equally among unique validators in the group.

Tests: keeper unit tests in `inference-chain/x/inference/keeper/`. Settlement verification using test vectors from Phase 4 (known state_root, host_stats, signatures from subnet integration tests).

Testable deliverable: chain correctly creates escrows, verifies settlement Merkle proofs and signatures, distributes funds.


## Phase 6: Warm Key Support

Goal: support warm keys (operator keys authorized via authz grants) so hosts can sign with a key different from their validator identity.

### Problem

On mainnet, hosts sign with warm keys -- operator keys authorized via on-chain authz grants. The dapi config separates `account_public_key` (cold/validator key) from `signer_key_name` (warm key loaded from keyring). The warm key address differs from the validator address.

The subnet needs to accept signatures from both cold keys (validator's own) and warm keys (authorized operator keys). The validator address identifies the slot for settlement; the signing address proves the host controls an authorized key.

### Design

Warm key bindings are lazily discovered and permanently cached in `state.WarmKeys` (part of `EscrowState`). No eager pinning at session start -- `SlotAssignment` contains only `SlotID` and `ValidatorAddress`.

Binding rule: only diff-contained signatures (proposer_sig on host-proposed txs, executor_sig in MsgConfirmStart) can introduce a warm key binding into state. The first diff-contained signature from a slot binds that warm key permanently for the session. A different key from the same slot is rejected.

When `verifyProposerSig` or `applyConfirmStart` processes a signature, `ResolveWarmKey` is called. On first encounter it queries the bridge to verify the authz grant, then caches the binding in `state.WarmKeys`. Subsequent calls hit the cache. All participants process the same diffs in the same order, so the same bindings appear at the same nonces across all nodes.

`state.WarmKeys` is part of the state root via `computeWarmKeysHash`. See [storage.md Warm Keys](./storage.md#warm-keys) for the full binding rule, storage format, and replay implications.

State signatures, gossip messages, and discovery endpoints verify the warm key against an existing binding (or use `CheckWarmKey`, non-mutating) but never create new bindings.

### State Machine Changes

Signature verification accepts either the validator's cold key or an authorized warm key:
- `verifyProposerSig()` recovers address, checks against `ValidatorAddress` or `WarmKeys[slot]`
- Seed reveal: verify signature against `WarmKeys[slot]` (or cold key if no warm key was ever used)
- Settlement: use `ValidatorAddress` to identify the recipient on chain

`WarmKeys` is part of the state root. The state hash formula includes `warm_keys_hash`.

### Storage Changes

Warm key deltas are persisted per diff in `DiffRecord.WarmKeyDelta`. During replay, deltas are injected via `InjectWarmKeys` before `ApplyLocal` so warm keys are cached without bridge calls. See [storage.md Replay](./storage.md#replay).

### Test Plan

```
TestWarmKey_LazyBinding
  - Host signs first tx with warm key
  - Verify WarmKeys[slot] is populated after ApplyDiff
  - Verify state root includes the binding

TestWarmKey_RejectConflictingKey
  - Host signs first tx with warm key A, then a later tx with warm key B
  - Verify second signature is rejected

TestWarmKey_ColdKeyAccepted
  - Host signs with cold key (ValidatorAddress)
  - Verify signature accepted, no warm key binding created

TestWarmKey_ReplayWithDeltas
  - Apply several diffs with warm key bindings
  - Record WarmKeyDeltas
  - Replay via InjectWarmKeys + ApplyLocal
  - Verify state roots match at every nonce

TestWarmKey_SeedRevealWithWarmKey
  - Host reveals seed signed with warm key
  - Verify seed derivation and ShouldValidate use the warm key signature
  - Verify MsgRevealSeed is checked against the bound warm key

TestWarmKey_SettlementUsesValidatorAddress
  - Session with warm keys settles
  - Settlement payload maps costs to ValidatorAddress, not the warm key
```

### Scope Boundary

Phase 6 does NOT include:
- Mid-session key rotation (warm key is permanently bound for the session)
- Discovery of warm key addresses (that is Phase 7 via /v1/identity)
- State signature or gossip-based warm key binding (these are out-of-state events)

Bridge-based warm key verification (`VerifyWarmKey`) is called lazily on first diff-contained signature from each slot. Subsequent signatures use the cached binding.


## Phase 7: Chain Adapter (MainnetBridge) [PARTIAL]

Goal: real mainnet communication via REST. Host discovery.

Implemented:
- RESTBridge implementing MainnetBridge query methods via HTTP (GetEscrow, GetValidatorInfo, VerifyWarmKey)
- BuildGroup helper: escrow -> []SlotAssignment using chain-stored slots (no re-derivation)
- Unit tests (httptest) for all query methods and BuildGroup
- Integration test (build tag: integration, env: CHAIN_REST_URL)
- Stub methods (OnEscrowCreated, OnSettlementProposed, OnSettlementFinalized, SubmitDisputeState) return ErrNotImplemented

Deferred:
- /v1/identity endpoint and DelegateTAs discovery (needs dapi endpoint extension)
- Warm key verification on first contact (VerifyWarmKey bridge call, depends on Phase 6)
- Event subscription for escrow creation/settlement (OnEscrowCreated, OnSettlementProposed, OnSettlementFinalized)
- SubmitDisputeState (needs tx submission)
- GetSlotsFromSorted port (unnecessary -- chain stores pre-computed slots in SubnetEscrow)
- Slot assignment derivation from (app_hash, escrow_id, validator_weights) -- chain does this at escrow creation

### Scope Boundary

- Grant revocation mid-session does not invalidate an active session. The warm key was valid at setup time. Revocation takes effect on next session creation.
- Warm key cache is per-session, not global. Different sessions may see different warm keys for the same validator.
- /v1/identity provides discovery only. The chain grant check (VerifyWarmKey) is the authorization source of truth.


## Phase 8: dapi Integration and ML Node Adapters

Goal: subnet runs as part of decentralized-api. Real ML node interaction via existing infrastructure.

Scope:
- InferenceEngine adapter wrapping broker + completionapi (execute inference on vLLM node, stream response, extract hashes and token counts)
- ValidationEngine adapter wrapping broker + validation logic (re-execute with enforced tokens, compareLogits)
- Router: mount `/subnet/v1/` echo group on existing dapi public server
- MainnetBridge adapter using dapi's chain client (alternative to REST bridge from Phase 7)
- SQLite storage (WAL mode, single writer goroutine, as specified in design.md)

Tests: subnet running inside dapi, mock ML node (WireMock), real signing, real SQLite, real gossip over localhost. Full session with actual inference execution and streaming.

Testable deliverable: dapi serves subnet inference requests, executes on ML node via existing broker, returns streaming results with subnet protocol (diffs, signatures, mempool).


## Phase 9: User Client Library

Goal: Go library for the user side of the protocol. Callable from code, usable in integration tests and testermint.

Scope:
- User SDK: open session (escrow_id, bridge), send inference requests (OpenAI-compatible), handle receipts and pipelining, collect signatures, trigger finalizing rounds, construct settlement payload
- Go library API: no HTTP server, direct function calls
- Later iteration: proxy mode wrapping the library behind an OpenAI-compatible HTTP server (user sends normal /chat/completions, proxy handles all subnet protocol transparently)

Tests: Go test creates user client, sends inference requests to a subnet cluster, gets streaming responses, verifies session settles correctly.

Testable deliverable: user client library drives a full session against real subnet nodes. No manual diff construction or protocol handling from test code.


## Phase 10: End-to-End (Testermint)

Goal: full system tested through testermint integration framework.

Scope:
- Testermint test: MsgCreateEscrow -> inference requests via user client library -> MsgSettleEscrow
- Verify mainnet state after settlement: host balances increased, user refund correct, host_stats recorded
- Edge cases: host down during session (timeout recovery), user disappears (host-initiated settlement)
- Verify integration with existing epoch/PoC system (subnet sessions coexist with current inference flow)

Tests: testermint integration tests using user client library from Phase 9 against full local cluster (chain nodes + dapi + mock ML nodes).

Testable deliverable: complete system works end-to-end. Settlement produces correct on-chain state.


## Further Work

Items identified during review that are not blockers for current phases but should be addressed before production.

### Equivocation response

Equivocation detection (same nonce, different state hash from different slots) currently logs a debug message and returns HTTP 409 to the gossip sender. No further action is taken. `checkStateConflict()` in `gossip/gossip.go` is a stub.

Needed:
- Submit slashing evidence to chain (requires on-chain slashing msg)
- Escalate to operator (structured log at error/warn level, optional webhook)
- Consider session abort if conflict involves the local host's own state
