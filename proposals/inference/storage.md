# Inference Subnet: Storage Design

Persistent storage for subnet sessions. Covers schema, write path, state reconstruction, warm keys, lifecycle, and config consolidation.


## Single SQLite File

All sessions share one `subnet.db` file. No per-session files, no per-escrow sharding.

Rationale: 1000 concurrent sessions with separate DBs = 3000 file descriptors (each SQLite file uses db + wal + shm). Competes with network sockets under the same ulimit. A single DB = 3 file descriptors total, regardless of session count.

WAL mode: concurrent readers, single writer. The writer serializes all mutations through a goroutine (see Write Path below).

Sessions are logically separated by `escrow_id` in all tables.


## Schema

Three tables: sessions, diffs, signatures.

```sql
CREATE TABLE sessions (
    escrow_id       TEXT PRIMARY KEY,
    creator_addr    TEXT NOT NULL,
    config_json     TEXT NOT NULL,      -- SessionConfig as JSON
    group_proto     BLOB NOT NULL,      -- []SlotAssignment serialized (proto)
    initial_balance INTEGER NOT NULL,
    latest_nonce    INTEGER NOT NULL DEFAULT 0,
    last_finalized  INTEGER NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT 'active',  -- active | settled | pruned
    created_at      INTEGER NOT NULL,
    settled_at      INTEGER            -- NULL until settled
);

CREATE TABLE diffs (
    escrow_id       TEXT NOT NULL,
    nonce           INTEGER NOT NULL,
    txs_proto       BLOB NOT NULL,     -- []SubnetTx serialized (proto, same as wire format)
    user_sig        BLOB NOT NULL,
    state_hash      BLOB NOT NULL,     -- state root after applying this diff
    warm_keys_json  TEXT,              -- warm key bindings from diff-contained sigs, NULL if none
    created_at      INTEGER NOT NULL,
    PRIMARY KEY (escrow_id, nonce)
);

CREATE TABLE signatures (
    escrow_id       TEXT NOT NULL,
    nonce           INTEGER NOT NULL,
    slot_id         INTEGER NOT NULL,
    sig             BLOB NOT NULL,
    PRIMARY KEY (escrow_id, nonce, slot_id)
);

CREATE INDEX idx_diffs_escrow ON diffs(escrow_id);
CREATE INDEX idx_signatures_escrow_nonce ON signatures(escrow_id, nonce);
```

Signatures are a separate table because they arrive asynchronously (lag 1+ rounds). Separate table = simple INSERT, no contention with diff rows.

`state_hash` serves double duty: it is the computed state root after applying the diff, and it equals `Diff.PostStateRoot` for that diff. The state machine rejects any diff where computed root != `PostStateRoot`. The user signs `DiffContent{nonce, txs, escrow_id, post_state_root}`. To reconstruct the signed diff payload for equivocation proofs: `BuildDiffContent(escrow_id, nonce, txs, state_hash)`.

`warm_keys_json` stores `{"slot_id": "address"}` for warm key bindings introduced by diff-contained signatures at this nonce. Most diffs have no new bindings (NULL). See Warm Keys below.


## State vs Diffs

Two layers:

1. State machine: in-memory `EscrowState`. Source of truth during a session.
2. Storage: persisted diffs (append-only log) + session metadata. Source of truth across restarts.

Full state = `replay(diffs[1..N])` through the state machine.

No periodic state snapshots in Phase 1. Sessions are bounded (~16k diffs in stress tests). Replay from nonce 1 takes milliseconds. If this becomes a bottleneck, add a `state_snapshots` table with periodic checkpoints.


## Warm Keys

The warm key map (`state.WarmKeys`) is part of the state root via `computeWarmKeysHash`. A binding (slot S -> warm address W) enters the state root at the nonce of the diff whose application first resolved it. Correct replay requires the same bindings at the same nonces.

### Binding rule

Only diff-contained signatures can introduce a warm key binding into state. Specifically:

- `proposer_sig` on host-proposed txs: MsgFinishInference, MsgValidation, MsgValidationVote, MsgRevealSeed
- `executor_sig` in MsgConfirmStart

These are verified inside `applyTx` -> `verifyProposerSig` / `applyConfirmStart`, which is the consensus path (`applyCore`). All participants process the same diffs in the same order, so the same signatures trigger the same `ResolveWarmKey` calls at the same nonces. Once bound, the binding is permanent for the session -- a different key from the same validator is rejected.

State signatures, gossip nonce messages, and discovery endpoints must NOT mutate `state.WarmKeys`. These are out-of-state metadata that different participants may observe at different times. They should verify the warm key against the binding (if one exists) or use `CheckWarmKey` (non-mutating), but never create new bindings.

Required code fix: `user.go:173` calls `sm.ResolveWarmKey` from a state signature (host response). This mutates `state.WarmKeys` from an out-of-state event. Must switch to `CheckWarmKey` or verify against an existing binding.

### Storage

Store warm key deltas per diff. During normal operation, the host snapshots `WarmKeys` before and after applying a diff and records any new bindings:

```go
warmBefore := sm.WarmKeys()
root, err := sm.ApplyDiff(diff)
warmAfter := sm.WarmKeys()
delta := computeWarmKeyDelta(warmBefore, warmAfter)
rec := types.DiffRecord{..., WarmKeyDelta: delta}
store.AppendDiff(escrowID, rec)
```

Most diffs have no new bindings (delta is nil). Typical session: each host signs its first tx with its warm key in the first round (slots 0..N-1). After that, all bindings are cached and deltas stay nil for the rest of the session.

### Replay

Before applying each diff, inject its stored delta:

```go
for _, rec := range diffs {
    sm.InjectWarmKeys(rec.WarmKeyDelta)
    root, err := sm.ApplyLocal(rec.Nonce, rec.Txs)
    // verify root == rec.StateHash
}
```

`InjectWarmKeys` writes entries to `state.WarmKeys` without overwriting existing ones. A conflicting entry (different key for an already-bound slot) indicates storage corruption; replay fails and the session is marked corrupt. When `applyTx` -> `verifyProposerSig` -> `ResolveWarmKey` runs, the warm key is already cached. No bridge call needed. State root at each nonce matches the stored `state_hash`.

Why not pin at session creation: that would place all warm keys in the state root from nonce 0, changing state roots at every nonce compared to the current lazy approach. It also requires a discovery step before any diffs (contacting every host or querying all grants). The per-diff delta approach keeps discovery lazy, requires no coordinated session setup, and produces identical state roots.


## Restart Recovery

On process restart:

1. Load active sessions from `sessions` table (`status = 'active'`)
2. For each session, recreate the state machine: `NewStateMachine(escrowID, config, group, initial_balance, creator_addr, verifier)`
3. Load all diffs from `diffs` table, ordered by nonce
4. Replay each diff:
   a. Inject `warm_keys_json` delta into state machine
   b. Apply diff via `ApplyLocal` (skips user signature verification; proposer sigs in `applyTx` still run but warm keys are already cached from step a)
   c. Verify computed state root == stored `state_hash`
5. Load signatures from `signatures` table, populate tracking
6. Resume from `latest_nonce`

User signature verification is skipped because persisted diffs were already verified on first receipt. Proposer sig verification still runs during replay, but replay never re-queries the bridge because warm keys are already cached from injected deltas. Recovery relies only on persisted session metadata, persisted diffs, and persisted warm-key deltas.

If any replayed state root mismatches the stored hash, the session is corrupt. Log error, skip session.


## Storage Interface

The current `storage.Storage` interface needs expansion for restart recovery and settlement tracking. New methods marked below.

```go
type Storage interface {
    // Session lifecycle
    CreateSession(params CreateSessionParams) error
    MarkSettled(escrowID string) error
    ListActiveSessions() ([]string, error)

    // Diff log
    AppendDiff(escrowID string, rec types.DiffRecord) error    // also updates sessions.latest_nonce
    GetDiffs(escrowID string, fromNonce, toNonce uint64) ([]types.DiffRecord, error)

    // Signatures
    AddSignature(escrowID string, nonce uint64, slotID uint32, sig []byte) error
    GetSignatures(escrowID string, nonce uint64) (map[uint32][]byte, error)

    // Queries
    GetSessionMeta(escrowID string) (*SessionMeta, error)

    // Finalization tracking
    MarkFinalized(escrowID string, nonce uint64) error
    LastFinalized(escrowID string) (uint64, error)
}

type CreateSessionParams struct {
    EscrowID       string
    CreatorAddr    string
    Config         types.SessionConfig
    Group          []types.SlotAssignment
    InitialBalance uint64
}

type SessionMeta struct {
    EscrowID       string
    CreatorAddr    string
    Config         types.SessionConfig
    Group          []types.SlotAssignment
    InitialBalance uint64
    LatestNonce    uint64
    LastFinalized  uint64
    Status         string
}
```

`DiffRecord` gains a `WarmKeyDelta` field:

```go
type DiffRecord struct {
    Diff
    StateHash    []byte
    Signatures   map[uint32][]byte
    WarmKeyDelta map[uint32]string    // bindings from diff-contained signatures at this nonce
    CreatedAt    int64
}
```

No `GetState`. The old `GetState` returned `*types.EscrowState` with `Balance` meaning initial escrow, which invites confusion with live balance (the meaning of `Balance` everywhere else in the protocol). `GetSessionMeta` replaces it with a dedicated type where `InitialBalance` is unambiguous. Any caller that needs live state must replay diffs through the state machine.


## Write Path

Single writer goroutine fed by a buffered channel. All mutations go through it:

- `CreateSession`
- `AppendDiff` (atomically: INSERT into diffs + UPDATE sessions.latest_nonce)
- `AddSignature`
- `MarkFinalized` (UPDATE sessions.last_finalized)
- `MarkSettled` (UPDATE sessions.status + sessions.settled_at)

Reads hit the DB directly (WAL allows concurrent readers):

- `GetSessionMeta`
- `GetDiffs`
- `GetSignatures`
- `LastFinalized`
- `ListActiveSessions`

```go
type writeOp struct {
    fn     func(tx *sql.Tx) error
    result chan error
}

type SQLite struct {
    db      *sql.DB
    writeCh chan writeOp
}
```

Callers enqueue a `writeOp` and block on the result channel. The writer goroutine dequeues, executes within a transaction, and sends the error back.

`AppendDiff` must update `sessions.latest_nonce` in the same transaction as the diff INSERT. If the process crashes between the two, the diff would be orphaned. Single transaction prevents this.

Buffer size: 256. If the buffer fills, callers block. Natural backpressure without dropping writes.


## Session Lifecycle

Status values:

| Status | Meaning |
|--------|---------|
| `active` | Normal operation, accepting diffs |
| `settled` | MsgSettleEscrow confirmed on chain, `settled_at` recorded |
| `pruned` | Diffs and signatures deleted, session row retained for accounting |

Transitions:

```
active -> settled   (MarkSettled, on settlement confirmation)
settled -> pruned   (after prune_after_epochs from settled_at)
```

No transition back to `active`. A settled escrow cannot be reopened.


## Pruning

Background goroutine runs periodically (every epoch or configurable interval):

1. Find sessions where `status = 'settled'` AND `settled_at < now - prune_threshold`
2. Delete rows from `diffs` and `signatures` for those sessions
3. Update `status = 'pruned'`

Session row is never deleted. It serves as an audit record that the escrow existed and was settled.

Pruning keys off `settled_at`, not `created_at`. A long-lived session that just settled keeps its data for the full prune window.

Pruning is idempotent. Safe to run multiple times or crash mid-pruning.

## Protocol Invariants

1. `state_hash == post_state_root` for every stored diff.
2. Only diff-contained signatures may mutate `state.WarmKeys`.
3. Replay must reproduce the same warm-key bindings at the same nonces. Otherwise the session is corrupt.


## Config Consolidation

All subnet parameters in a single `SubnetConfig` struct. Two categories in one file:

- Session/protocol params. Must be identical for all participants in a session. Persisted in `sessions.config_json`.
- Local node params. Affect only this node's storage, gossip, transport, and pruning behavior. Not part of the replicated session state.

Currently scattered across 6 files:

| Parameter | Current location |
|-----------|------------------|
| RefusalTimeout, ExecutionTimeout, TokenPrice, ValidationRate | `subnet/types/config.go` |
| Gossip K, StaleTTL, RecoveryDelay, RecoveryTick | `subnet/gossip/gossip.go` |
| InferenceTimeout, GossipTimeout, VerifyTimeout, QueryTimeout | `subnet/transport/client.go` |
| MaxIdleConnsPerHost, IdleConnTimeout, TLSHandshakeTimeout | `subnet/transport/client.go` |
| penaltyValidationRate | `subnet/state/machine.go` |

Consolidated struct:

```go
type SubnetConfig struct {
    DataDir   string          `koanf:"data_dir"`
    Session   SessionParams   `koanf:"session"`
    Gossip    GossipParams    `koanf:"gossip"`
    Transport TransportParams `koanf:"transport"`
    Lifecycle LifecycleParams `koanf:"lifecycle"`
}

type SessionParams struct {
    RefusalTimeout   int64  `koanf:"refusal_timeout"`   // seconds (default: 60)
    ExecutionTimeout int64  `koanf:"execution_timeout"` // seconds (default: 1200)
    TokenPrice       uint64 `koanf:"token_price"`       // price per unit (default: 1)
    ValidationRate   uint32 `koanf:"validation_rate"`   // basis points (default: 5000 = 50%)
}

type GossipParams struct {
    Fanout        int           `koanf:"fanout"`         // K peers (default: 10)
    StaleTTL      time.Duration `koanf:"stale_ttl"`      // default: 120s
    RecoveryDelay time.Duration `koanf:"recovery_delay"` // default: 60s
    RecoveryTick  time.Duration `koanf:"recovery_tick"`  // default: 60s
}

type TransportParams struct {
    InferenceTimeout  time.Duration `koanf:"inference_timeout"`   // default: 20m
    GossipTimeout     time.Duration `koanf:"gossip_timeout"`      // default: 10s
    VerifyTimeout     time.Duration `koanf:"verify_timeout"`      // default: 3m
    QueryTimeout      time.Duration `koanf:"query_timeout"`       // default: 30s
    MaxIdlePerHost    int           `koanf:"max_idle_per_host"`   // default: 4
    IdleConnTimeout   time.Duration `koanf:"idle_conn_timeout"`   // default: 120s
}

type LifecycleParams struct {
    PruneAfterEpochs int `koanf:"prune_after_epochs"` // default: 2
}
```

`VoteThreshold` and finalization quorum are derived from group size at runtime, not configurable.

### Integration with dapi config

Add `Subnet SubnetConfig` field to `apiconfig.Config`:

```go
type Config struct {
    // ... existing fields ...
    Subnet SubnetConfig `koanf:"subnet" json:"subnet"`
}
```

Loadable from YAML:

```yaml
subnet:
  data_dir: /var/lib/gonka/subnet.db
  session:
    refusal_timeout: 60
    execution_timeout: 1200
    token_price: 1
    validation_rate: 5000
  gossip:
    fanout: 10
    stale_ttl: 120s
  transport:
    inference_timeout: 20m
  lifecycle:
    prune_after_epochs: 2
```

Environment variables: `DAPI_SUBNET__DATA_DIR`, `DAPI_SUBNET__SESSION__REFUSAL_TIMEOUT`, etc. (koanf uses `__` for nesting).

`DefaultSubnetConfig()` returns the current hardcoded values. Tests use defaults directly without YAML.


## Implementation Order

1. Add `WarmKeyDelta` to `DiffRecord`, add `InjectWarmKeys` to state machine
2. Expand `storage.Storage` interface (`CreateSessionParams`, `GetSessionMeta`, `ListActiveSessions`, `MarkSettled`)
3. Update `Memory` implementation to match new interface
4. SQLite implementation of `storage.Storage`
5. Add `SubnetConfig` to `subnet/types/` with `DefaultSubnetConfig()`
6. Wire config through `HostManager` and `NewHTTPSession`
7. Add restart recovery logic to `HostManager`
8. Add pruning goroutine
9. Integration tests: restart recovery, concurrent sessions, warm key replay, pruning


----

## State of Implementation

Steps 1-4 are complete.

### Step 1: WarmKeyDelta + InjectWarmKeys

Done. `DiffRecord.WarmKeyDelta` field added to `subnet/types/domain.go`. `InjectWarmKeys` method added to state machine (`subnet/state/machine.go`). Tests cover round-trip through storage and replay.

### Step 2: Expanded Storage Interface

Done. `subnet/storage/interface.go` defines `Storage` with `CreateSessionParams`, `SessionMeta`, `GetSessionMeta`, `ListActiveSessions`, `MarkSettled`, `MarkFinalized`, `LastFinalized`.

### Step 3: Memory Implementation

Done. `subnet/storage/memory.go` implements the full interface. Shared conformance tests in `subnet/storage/shared_test.go` run against both Memory and SQLite.

### Step 4: SQLite Implementation

Done. `subnet/storage/sqlite.go` uses modernc.org/sqlite (pure Go, no CGO). Uses separate read/write connection pools on the same WAL-mode file: `writeDB` (MaxOpenConns=1) serializes writes, `readDB` (MaxOpenConns=10) allows parallel reads. Schema matches the design doc (sessions, diffs, signatures tables). Diffs and signatures loaded via LEFT JOIN to avoid nested queries. Concurrency tests verify no SQLITE_BUSY errors under parallel read/write load.

Deviation from design: the write path uses two `sql.DB` pools instead of a background writer goroutine with a channel. All writes are already serialized at the application level by `host.mu`, so MaxOpenConns=1 on the write pool is sufficient. The channel-based writer would add complexity without benefit.

Another deviation: `group_json TEXT` instead of `group_proto BLOB` in the sessions table. Group is stored as JSON for debuggability. No functional difference since the interface returns the same Go types.

### Steps 5-9: Not Started

Config consolidation, restart recovery, pruning, and integration tests remain.
