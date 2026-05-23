# Devshard params — data flow

**Motivation.** Decentralized API (dapi) already subscribes to chain events and keeps governance params in memory (`ConfigManager`). Devshard processes should not run their own periodic `QueryParams` / epoch polls. Instead, **dapi** is the single source of truth: its event listener refreshes params when blocks arrive, and **devshardd** pulls a snapshot over gRPC with **long-polling** so updates arrive as soon as dapi sees a param or epoch change—not on a fixed 30s (or 60s) timer.

---

## 1. Escrow create vs what devshard loads at bind

### `MsgCreateDevshardEscrow` (tx — creator only)

| In message | On chain after create |
|------------|------------------------|
| `creator`, `amount`, `model_id` | `id`, `slots`, `epoch_index`, `app_hash`, `token_price`, `settled`, … |

Tx response: **`escrow_id` only** — not the full record.

### Session bind (host / user — protocol uses escrow id)

HTTP/storage key is the id. First bind calls **`GetEscrow(escrowID)`** (once per bind; group build reuses the result):

| Source | Fields used |
|--------|-------------|
| `QueryGetDevshardEscrow` | `slots`, `epoch_index`, `app_hash`, `token_price`, `settled`, `model_id`, `amount`, … |
| Grace defaults | `default_seal_grace_nonces`, `default_inference_clear_grace_seconds` from **dapi cache / `GetRuntimeConfig` snapshot** — frozen into `SessionConfig` (no separate `QueryParams` on bind) |
| Devshard defaults | `refusal_timeout`, `execution_timeout`, fees, `validation_rate`, … (`types.DefaultSessionConfig`) |

**Not** from chain at bind: governance flags that affect **runtime** (`logprobs_mode`, `devshard_requests_enabled`, `max_nonce`, `approved_versions`) — those come via long-poll (below). **Open sessions do not** hot-reload grace or timeouts after governance changes.

**Still per-escrow / per-address chain queries** (not on long-poll today): participant URL, epoch-group validation threshold, warm-key grants, account pubkeys.

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
    → apply snapshot locally
    → re-issue RPC with new H
```

**`RuntimeConfig` snapshot fields:** `params_block_height`, `current_epoch_id`, `logprobs_mode`, `devshard_requests_enabled`, seal/clear grace defaults, `max_nonce`, `approved_versions`, `served_at_unix`.

**Consumers:** standalone **devshardd** (`runtimeconfig` provider); **embedded devshard inside dapi** uses the same `ConfigManager` in-process (no gRPC loop). **Epoch change** triggers `ManagedStorage.PruneOnce` (devshardd via `runtimeconfig.OnEpochChange`; embedded dapi via `ConfigManager.SetEpochChangeHandler` on the same publish path). No 30s storage prune ticker.

**Idle cost:** when the chain is quiet, devshardd gets at most one RPC per `max_wait` (~1/min with a 60s cap) — not a repeating `QueryParams` or epoch-db poll on the devshard side.

---

## 3. What else can move to this flow

| On `GetRuntimeConfig` today | Consumer | Possible next step |
|-----------------------------|----------|-------------------|
| `approved_versions` | dapi cache; **versiond** still polls `GET /versions` on `:9100` | versiond uses long-poll gRPC; reconcile child processes on wake |
| `devshard_requests_enabled` | devshardd + `AvailabilityTracker` | in long-poll path |
| `logprobs_mode`, epoch, grace defaults, `max_nonce` | devshardd / dapi | in long-poll path |

| Still direct chain | Why |
|--------------------|-----|
| `QueryGetDevshardEscrow` | Per-escrow authoritative state |
| `QueryGetParticipant`, `QueryGetEpochGroupData` | Per executor / epoch+model |
| `QueryGranteesByMessageType`, `QueryAccountByAddress` | Per validator / address |
| REST `GetEscrow` via HTTP `/params` | devshardctl without dapi defaults provider |

Bind-time grace already comes from the runtime snapshot (embedded and standalone devshardd), not from a nested governance query at `GetEscrow`.

---

## 4. E2E test scenarios (Testermint)

Run: `cd testermint && ./gradlew :test --tests "<Class>" -DexcludeTags=unstable,exclude` after `local-test-net/./stop-rebuild.sh`.

### `RuntimeConfigTests` — dapi gRPC long-poll, ~5–10 min

| # | Proves |
|---|--------|
| 1 | Initial snapshot matches chain after sync |
| 2 | `max_wait=0` → immediate `unchanged` (legacy client) |
| 3 | Long-poll times out when chain idle |
| 4 | Ordinary blocks do not wake long-poll |
| 5 | Epoch change bumps height + `current_epoch_id` within ~30s |
| 6 | Governance `UpdateParams` wakes long-poll (e.g. `max_nonce`) within ~30s |

### `DevsharddRuntimeConfigTests` — versiond + devshardd host path, ~6–12 min

| # | Proves |
|---|--------|
| 1 | Governance flip → host 503 then 200 via proxy — **`@Disabled`**: chain and versiond react correctly; proxy/curl path does not surface 503 fast enough (accepted) |
| 2 | After `waitForNextEpoch`, devshardd long-poll epoch matches chain within ~30s |
| 3 | Restart `genesis-api`; chat completion recovers within ~90s |

**Coverage split:** `RuntimeConfigTests` owns the **long-poll RPC contract** (idle, epoch, governance wake). `DevsharddRuntimeConfigTests` adds **host inference + dapi restart** only — avoid duplicating the long-poll matrix in the devshardd class.

**Infra:** versiond genesis boot, `build/devshardd`, compose overlay — [`testermint-infrastructure.md`](./testermint-infrastructure.md).
