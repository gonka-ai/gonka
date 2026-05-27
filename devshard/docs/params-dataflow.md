# Devshard params — data flow

**Motivation.** Decentralized API (dapi) already subscribes to chain events and keeps governance params in memory (`ConfigManager`). Devshard processes should not run their own periodic `QueryParams` / epoch polls. Instead, **dapi** is the single source of truth: its event listener refreshes params when blocks arrive, and **devshardd** pulls a snapshot over gRPC with **long-polling** so updates arrive as soon as dapi sees a param or epoch change—not on a fixed 30s (or 60s) timer.

See also: [session-config-flow-plan.md](./session-config-flow-plan.md) (implementation phases), [protocol-version.md](./protocol-version.md) (state-root vs runtime version).

---

## 1. Escrow create vs what devshard loads at bind

### `MsgCreateDevshardEscrow` (tx — creator only)

| In message | On chain after create |
|------------|------------------------|
| `creator`, `amount`, `model_id` | `id`, `slots`, `epoch_index`, `app_hash`, `token_price`, **`create_devshard_fee`**, **`fee_per_nonce`**, `settled`, … |

Governance defaults for fees (`DevshardEscrowParams.create_devshard_fee`, `fee_per_nonce`) are **copied onto the escrow row** at create. Zero on the row means “use compiled default” when building `SessionConfig`.

Tx response: **`escrow_id` only** — not the full record.

### Session bind (host / user — protocol uses escrow id)

HTTP/storage key is the id. First bind calls **`GetEscrow(escrowID)`** once per bind (group build reuses the result). `HostManager.create` / user bind then merge **three lanes** into `SessionConfig`:

#### Lane A — from `QueryGetDevshardEscrow` (per-escrow, frozen on chain)

| Field | Notes |
|-------|--------|
| `slots`, `epoch_index`, `app_hash` | Group + settlement context |
| `token_price` | Frozen for the life of the escrow |
| **`create_devshard_fee`**, **`fee_per_nonce`** | Snapshotted at escrow create; hashed into state root / settlement |
| `settled`, `model_id`, `amount` | Operational / display |

The bridge (`ChainBridge`, `RESTBridge`) is a **pure escrow query** — it does **not** call `QueryParams` or attach grace defaults to `EscrowInfo`.

#### Lane B — from dapi runtime cache / `GetRuntimeConfig` snapshot (**frozen at bind**)

Read once via `RuntimeParamsProvider` (`ConfigManagerRuntimeParams` embedded, `RuntimeConfigRuntimeParams` on standalone devshardd) and copied into `SessionConfig` in `HostManager.create` (`ApplyLiveSessionParams`):

| Field | Notes |
|-------|--------|
| `default_seal_grace_nonces` → `SealGraceNonces` | Consensus-sensitive |
| `default_inference_clear_grace_seconds` → `InferenceClearGraceSeconds` | Consensus-sensitive |
| `validation_rate` | Consensus-sensitive |
| `vote_threshold_factor` → `VoteThreshold` | Derived: `floor(groupSize * factor / 100)`; `factor == 0` → `groupSize / 2` |

**Open sessions do not hot-reload** lane B fields after governance changes (same rule as `token_price` and escrow fees). Mid-flight governance updates only affect **new** binds.

#### Lane C — from dapi runtime cache (**live**, not frozen in consensus)

| Field | Consumer | Notes |
|-------|----------|--------|
| `refusal_timeout`, `execution_timeout` | devshardctl proxy (`InferenceTimeouts` when wired) | Per inference attempt; not in state root |
| `max_nonce` | `MaxNonceProvider` | Host accept/reject gate |
| `devshard_requests_enabled` | `AvailabilityTracker` | 503 when disabled |
| `logprobs_mode` | Validation path | |
| `approved_versions` | versiond / routing | Child process policy |
| `current_epoch_id` | Prune, availability | Epoch transitions wake long-poll |

Proxy may still expose bound `RefusalTimeout` / `ExecutionTimeout` on `/status`; live inference uses the provider when configured.

**Protocol version** (`StateRootAndProtocolVersion` / `types.DevshardStateRootAndProtocolVersion`) is **not** `approved_versions`: it tags state-root and settlement hashing and is fixed per binary build. See [protocol-version.md](./protocol-version.md).

### Still per-escrow / per-address chain queries (not on long-poll)

| Query | Why |
|-------|-----|
| `QueryGetDevshardEscrow` | Per-escrow authoritative state (lane A only) |
| `QueryGetParticipant` | Per-validator inference URL |
| `QueryGetEpochGroupData` | Per epoch + model validation threshold |
| `QueryGranteesByMessageType` | Per validator warm-key grants |
| `QueryAccountByAddress` | Per address pubkey |

`bindGraceDefaults`, `DevshardDefaults`, and nested `QueryParams` on `GetEscrow` are **removed** — grace and governance session fields come from lane B at bind, not from the bridge.

---

## 2. Long-poll data flow

**Server: dapi.** NodeManager gRPC `GetRuntimeConfig` is implemented on the decentralized API process (`NODE_MANAGER_ADDR`, default `:9400`). Same port as ML `AcquireMLNode` / `ReleaseMLNode` — not HTTP `:9100/versions`.

**How dapi gets params (no extra chain poll for clients).** On each relevant block, dapi’s **chain event listener** updates `ConfigManager` and the phase tracker from subscribed events. When governance params or epoch change, dapi bumps `params_block_height` and calls `RuntimeConfigNotifier.Notify()`. The RPC handler reads the **in-memory cache** built by that listener — it does not query the chain per `GetRuntimeConfig` call.

**Client: devshardd.** One in-flight long-poll RPC per process; reconnects after each snapshot.

```
chain block
    → dapi event listener → ConfigManager (+ phase tracker)
    → param or epoch change → bump params_block_height, Notify()

devshardd:
    GetRuntimeConfig(client_height=H, max_wait≈60s)  →  dapi (server)
        → server_height > H: return RuntimeConfig from cache
        → else: dapi blocks until Notify() | max_wait | client cancel
    → apply snapshot locally (runtimeconfig.Provider)
    → re-issue RPC with new H
```

**`RuntimeConfig` snapshot fields (wire / cache):**

| Field | Bind lane |
|-------|-----------|
| `params_block_height`, `served_at_unix` | Metadata |
| `current_epoch_id` | C (live) |
| `logprobs_mode` | C |
| `devshard_requests_enabled` | C |
| `default_seal_grace_nonces`, `default_inference_clear_grace_seconds` | B (frozen) |
| `max_nonce` | C |
| `approved_versions` | C |
| `refusal_timeout`, `execution_timeout` | C (live at proxy; also copied at bind for `/status`) |
| `validation_rate`, `vote_threshold_factor` | B (frozen) |

**Consumers:** standalone **devshardd** (`runtimeconfig.Provider` + `RuntimeParamsProvider`); **embedded devshard inside dapi** uses the same `ConfigManager` in-process (no gRPC loop). **Epoch change** triggers `ManagedStorage.PruneOnce` (devshardd via `runtimeconfig.OnEpochChange`; embedded dapi via `ConfigManager.SetEpochChangeHandler` on the same publish path). No 30s storage prune ticker.

**Idle cost:** when the chain is quiet, devshardd gets at most one RPC per `max_wait` (~1/min with a 60s cap) — not a repeating `QueryParams` or epoch-db poll on the devshard side.

---

## 3. What else can move to this flow

| Param | Status | Consumer |
|-------|--------|----------|
| `approved_versions` | On long-poll | dapi cache; **versiond** may still poll `GET /versions` on `:9100` — candidate: versiond uses gRPC long-poll |
| `devshard_requests_enabled` | **Moved** | devshardd + `AvailabilityTracker` |
| `logprobs_mode` | **Moved** | Validation |
| Seal/clear grace defaults | **Moved** | Frozen at bind (lane B) |
| `max_nonce` | **Moved** | `MaxNonceProvider` |
| `refusal_timeout`, `execution_timeout` | **Moved** | Live proxy + long-poll snapshot |
| `validation_rate`, `vote_threshold_factor` | **Moved** | Frozen at bind (lane B) |
| Escrow fees (`create_devshard_fee`, `fee_per_nonce`) | On-chain escrow | Lane A — not on `RuntimeConfig` |

| Still direct chain | Why |
|--------------------|-----|
| `QueryGetDevshardEscrow` | Per-escrow authoritative state |
| `QueryGetParticipant`, `QueryGetEpochGroupData` | Per executor / epoch+model |
| `QueryGranteesByMessageType`, `QueryAccountByAddress` | Per validator / address |
| REST `GetEscrow` (devshardctl) | Escrow REST only — no `/params` for grace |

---

## 4. E2E test scenarios (Testermint)

Run: `cd testermint && ./gradlew :test --tests "<Class>" -DexcludeTags=unstable,exclude` after `local-test-net/./stop-rebuild.sh`.

### `RuntimeConfigTests` — dapi gRPC long-poll, ~5–10 min

| # | Proves |
|---|--------|
| 1 | Initial snapshot matches chain after sync (incl. phase-4 fields) |
| 2 | `max_wait=0` → immediate `unchanged` (legacy client) |
| 3 | Long-poll times out when chain idle |
| 4 | Ordinary blocks do not wake long-poll |
| 5 | Epoch change bumps height + `current_epoch_id` within ~30s |
| 6 | Governance `UpdateParams` wakes long-poll (`max_nonce`) within ~30s |
| 7 | Governance `refusal_timeout` propagates to runtime snapshot within ~30s |
| 8 | Governance `execution_timeout` propagates within ~30s |
| 9 | Governance `validation_rate` propagates within ~30s |
| 10 | Governance `vote_threshold_factor` propagates within ~30s |

### `DevsharddRuntimeConfigTests` — versiond + devshardd host path, ~6–12 min

| # | Proves |
|---|--------|
| 1 | Governance flip → host 503 then 200 via proxy — **`@Disabled`**: chain and versiond react correctly; proxy/curl path does not surface 503 fast enough (accepted) |
| 2 | After `waitForNextEpoch`, devshardd long-poll epoch matches chain within ~30s |
| 3 | Restart `genesis-api`; chat completion recovers within ~90s |

**Coverage split:** `RuntimeConfigTests` owns the **long-poll RPC contract** (idle, epoch, governance wake). `DevsharddRuntimeConfigTests` adds **host inference + dapi restart** only — avoid duplicating the long-poll matrix in the devshardd class.

**Infra:** versiond genesis boot, `build/devshardd`, compose overlay — [`testermint-infrastructure.md`](./testermint-infrastructure.md).
