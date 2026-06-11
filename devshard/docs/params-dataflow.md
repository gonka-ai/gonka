# Devshard params — data flow

**Motivation.** Decentralized API (dapi) already subscribes to chain events and keeps governance params in memory (`ConfigManager`). Devshard processes should not run their own periodic `QueryParams` / epoch polls. Instead, **dapi** is the single source of truth: its event listener refreshes params when blocks arrive, and **devshardd** pulls a snapshot over gRPC with **long-polling** so updates arrive as soon as dapi sees a param or epoch change—not on a fixed 30s (or 60s) timer.

See also: [session-config-flow-plan.md](./session-config-flow-plan.md) (implementation phases), [protocol-version.md](./protocol-version.md) (state-root vs runtime version), [params-provider-adaptive-plan.md](./params-provider-adaptive-plan.md) (adaptive gRPC ↔ chain supervisor — implemented).

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
| **`inference_seal_grace_nonces`**, **`inference_seal_grace_seconds`** | Snapshotted at escrow create from governance defaults (default grace seconds: **3600** / 1 hour); hashed into state root / auto-seal |
| `settled`, `model_id`, `amount` | Operational / display |

The bridge (`ChainBridge`, `RESTBridge`) is a **pure escrow query** — it does **not** call `QueryParams` or attach grace defaults to `EscrowInfo`.

#### Lane B — from dapi runtime cache / `GetRuntimeConfig` snapshot (**frozen at bind**)

Read once via `RuntimeParamsProvider` (`ConfigManagerRuntimeParams` embedded, `RuntimeConfigRuntimeParams` on standalone devshardd) and copied into `SessionConfig` in `HostManager.create` (`ApplyLiveSessionParams`):

| Field | Notes |
|-------|--------|
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
| `approved_versions` | versiond / routing | Child process policy; **only updated while long-poll is active** (see adaptive section) |
| `current_epoch_id` | Prune, availability | Epoch transitions wake long-poll or chain refresh |

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

`bindGraceDefaults`, `DevshardDefaults`, and nested `QueryParams` on `GetEscrow` are **removed** — governance session fields (except grace) come from lane B at bind; seal grace comes from lane A on the escrow row.

---

## 2. Long-poll data flow

**Server: dapi.** NodeManager gRPC `GetRuntimeConfig` is implemented on the decentralized API process (`NODE_MANAGER_ADDR`, default `:9400`). Same port as ML `AcquireMLNode` / `ReleaseMLNode` — not HTTP `:9100/versions`.

**How dapi gets params (no extra chain poll for clients).** On each relevant block, dapi’s **chain event listener** updates `ConfigManager` and the phase tracker from subscribed events. When governance params or epoch change, dapi bumps `params_block_height` and calls `RuntimeConfigNotifier.Notify()`. The RPC handler reads the **in-memory cache** built by that listener — it does not query the chain per `GetRuntimeConfig` call.

**Client: devshardd (adaptive).** By default devshardd runs `runtimeconfig.NewAdaptive`: one active feed at a time — either a single in-flight **long-poll** RPC to dapi, or a **chain** refresh loop (`QueryParams` + `QueryEpochInfo`). The supervisor switches between them without a process restart.

```
chain block
    → dapi event listener → ConfigManager (+ phase tracker)
    → param or epoch change → bump params_block_height, Notify()

devshardd (active_grpc):
    GetRuntimeConfig(client_height=H, max_wait≈60s)  →  dapi (server)
        → server_height > H: return RuntimeConfig from cache
        → else: dapi blocks until Notify() | max_wait | client cancel
    → apply snapshot locally (shared runtimeconfig base)
    → re-issue RPC with new H

devshardd (active_chain — fallback):
    QueryParams + QueryEpochInfo  →  chain (default every 60s)
    → apply snapshot locally (same base, same OnEpochChange / prune hooks)
    → periodic GetRuntimeConfig probe (max_wait=0) to detect dapi recovery
```

**`RuntimeConfig` snapshot fields (wire / cache):**

| Field | Bind lane |
|-------|-----------|
| `params_block_height`, `served_at_unix` | Metadata |
| `current_epoch_id` | C (live) |
| `logprobs_mode` | C |
| `devshard_requests_enabled` | C |
| `max_nonce` | C |
| `approved_versions` | C |
| `refusal_timeout`, `execution_timeout` | C (live at proxy; also copied at bind for `/status`) |
| `validation_rate`, `vote_threshold_factor` | B (frozen) |

**Consumers:** standalone **devshardd** (`runtimeconfig.NewAdaptive` + `RuntimeParamsProvider`); **embedded devshard inside dapi** uses the same `ConfigManager` in-process (no adaptive loop). **Epoch change** triggers `ManagedStorage.PruneOnce` (devshardd via `runtimeconfig.OnEpochChange` on the shared base; embedded dapi via `ConfigManager.SetEpochChangeHandler`). No 30s storage prune ticker.

**Idle cost (healthy):** when the chain is quiet and dapi is up, devshardd holds `active_grpc` and gets at most one long-poll RPC per `max_wait` (~1/min with a 60s cap). Chain polls run **only** while `active_chain`.

### Adaptive params (default — prefer gRPC, chain fallback)

Policy (no devshardd restart required for failover or failback):

| Event | Supervisor action |
|-------|-------------------|
| Boot / reprobe: `GetRuntimeConfig` OK or `unchanged` | Stay on or return to **long-poll** (`active_grpc`) |
| Boot / long-poll: `Unimplemented` | **Chain** (`active_chain`) |
| Long-poll errors, no successful apply for **90s** (with ≥1 error in that window) | **Chain** (`stale_window`) |
| On chain: **2** consecutive healthy probes (`max_wait=0`) | **Long-poll** (`failback`) |

Per-escrow / per-validation queries were always direct chain and are unchanged. NodeManager gRPC for ML nodes (`AcquireMLNode` / `ReleaseMLNode`) stays enabled regardless of params source.

#### `DEVSHARDD_PARAMS_SOURCE`

| Value | Behavior |
|-------|----------|
| `auto` or unset (default) | Adaptive supervisor (`source=adaptive` in logs) |
| `grpc` | **Deprecated** — identical to `auto`; emits a startup warning |
| `chain` | Chain poll only (debug / forced chain); never calls `GetRuntimeConfig` |

#### Environment variables

Adaptive tuning (optional; defaults work without setting any of these):

| Variable | Default | When it applies |
|----------|---------|-----------------|
| `DEVSHARDD_PARAMS_GRPC_STALE_SECONDS` | `90` | Fail over to chain after this long without a successful long-poll **apply**, if at least one poll error occurred in that window |
| `DEVSHARDD_PARAMS_GRPC_REPROBE_SECONDS` | `300` | While on chain, how often to probe `GetRuntimeConfig` (`max_wait=0`) for failback |
| `DEVSHARDD_PARAMS_GRPC_FAILBACK_PROBES` | `2` | Consecutive successful probes required before leaving chain |
| `DEVSHARDD_PARAMS_GRPC_PROBE_TIMEOUT_SECONDS` | `3` | Timeout for boot and reprobe `GetRuntimeConfig` calls |

Long-poll and chain runners (same as before):

| Variable | Default | When it applies |
|----------|---------|-----------------|
| `DEVSHARDD_RUNTIME_CONFIG_MAX_WAIT_SECONDS` | `60` | Long-poll `max_wait` (while `active_grpc`) |
| `DEVSHARDD_RUNTIME_CONFIG_CLIENT_DEADLINE_SLACK_SECONDS` | `5` | gRPC call deadline = max_wait + slack |
| `DEVSHARDD_PARAMS_CHAIN_REFRESH_SECONDS` | `60` | Chain refresh interval (while `active_chain`) |
| `DEVSHARDD_PARAMS_CHAIN_INITIAL_TIMEOUT_SECONDS` | `5` | First chain fetch timeout at startup / switch |

#### `approved_versions` and chain fallback

`approved_versions` comes from dapi’s in-memory cache over long-poll only. While **`active_chain`**:

- The snapshot keeps the **last** `approved_versions` from a prior gRPC apply, or **nil** if devshardd never had a successful long-poll apply.
- Chain fallback still updates governance fields present on chain (`max_nonce`, `devshard_requests_enabled`, `logprobs_mode`, epoch, etc.).
- After **failback** to `active_grpc`, the next full `RuntimeConfig` from dapi repopulates `approved_versions`.

#### Observability (logs)

Boot:

```text
runtime params provider source=adaptive policy=prefer_grpc_chain_fallback
runtime params provider settings (adaptive) max_wait_seconds=… grpc_stale_seconds=… …
```

Source switch (one line per transition):

| Direction | Level | `reason` values |
|-----------|-------|-----------------|
| → chain | `WARN` | `boot_probe_unimplemented`, `unimplemented`, `stale_window` |
| → gRPC | `INFO` | `boot_probe`, `failback` |

Example:

```text
runtime params: source switch from=grpc to=chain reason=stale_window
runtime params: source switch from=chain to=grpc reason=failback
```

There is **no** separate metrics endpoint for active source in v1; use logs or `paramsSetup.ActiveSource()` in code (`grpc` / `chain`).

#### Operator scenarios

| Situation | What to expect |
|-----------|----------------|
| Old dapi at boot (`GetRuntimeConfig` missing) | Starts on chain; upgrades to long-poll automatically after reprobe succeeds — **no devshardd restart** |
| dapi restarted / temporarily down | Failover to chain within ~stale window; returns to long-poll when dapi answers probes again |
| Force chain-only debugging | `DEVSHARDD_PARAMS_SOURCE=chain` |
| Mis-set `DEVSHARDD_PARAMS_SOURCE=grpc` | Same as default; deprecation warning only |

Implementation details: [params-provider-adaptive-plan.md](./params-provider-adaptive-plan.md).

---

## 3. What else can move to this flow

| Param | Status | Consumer |
|-------|--------|----------|
| `approved_versions` | On long-poll only | dapi cache while `active_grpc`; stale or nil on chain fallback; **versiond** may still poll `GET /versions` on `:9100` |
| `devshard_requests_enabled` | **Moved** | devshardd + `AvailabilityTracker` |
| `logprobs_mode` | **Moved** | Validation |
| Seal grace (`inference_seal_grace_*`) | On-chain escrow | Lane A — snapshotted at create; not on `RuntimeConfig` |
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
