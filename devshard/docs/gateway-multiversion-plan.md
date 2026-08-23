# Gateway Multi-Version Support (v1 + v2) — Plan

This plan describes how the gateway (`devshardctl`) supports **multiple
devshard protocol versions at the same time** — concretely, running `v1` and
`v2` escrows side by side in one process while keeping full backward
compatibility.

It is grounded in the current code. Where something already exists it is
labelled **HAVE**; where work is required it is labelled **TODO**.

Companion docs:
- Target architecture: `devshard/docs/upgrade.md`
- Temporary release shape: `devshard/docs/upgrade-impl-notes.md`

## TL;DR

A single `devshardctl` gateway runs **N state machines, one per escrow**, and
each state machine is **version-bound for its whole life**. "Supporting
multiple devshard versions at the gateway" therefore means **supporting
multiple version-bound state machines, one per escrow** — not one state
machine that switches versions.

Most of the structural support already exists. The remaining work is: (1)
version-aware admission/validation, (2) version-aware pooled routing, (3)
config + oper: declaring v1 and v2 runtimes together, and (4) tests +
observability proving v1 and v2 coexist correctly.

## Why one state machine cannot serve two versions

The bind tag lives **inside** the escrow state on each machine:

- `EscrowState.StateRootAndProtocolVersion` is hashed into **every** state root
  (`devshard/state/hash.go` `ComputeVersionHash`, `ComputeStateRoot`).
- Settlement carries that same tag and mainnet recomputes the root with
  `version_hash = sha256(tag)` (`devshard/state/settlement.go`).
- Storage records `sessions.version` and rejects mismatches with
  `storage.ErrSessionVersionConflict` (`devshard/storage/sqlite.go`,
  `postgres.go`, `memory.go`).
- Hosts running a different binary refuse to sign, so a version-mixing session
  cannot reach quorum (`devshard/cmd/devshardd/session/manager.go`
  "session version mismatch").

A single `state.StateMachine` holds exactly one `EscrowState` and thus exactly
one bind tag. Mixing v1 and v2 diffs in one machine would break roots and host
quorum. Multi-version support is necessarily multi-machine.

## Target structure (confirmed against code)

```
Gateway (single devshardctl process)
│
├── devshardRuntime  escrow="42"
│   ├── user.Session
│   ├── state.StateMachine   ← StateRootAndProtocolVersion = "v1"
│   ├── SQLite  (storage_path .../escrow-42)
│   └── HTTP clients → RoutePrefix /devshard/v1/...
│
├── devshardRuntime  escrow="87"
│   ├── user.Session
│   ├── state.StateMachine   ← StateRootAndProtocolVersion = "v2"
│   ├── SQLite  (storage_path .../escrow-87)
│   └── HTTP clients → RoutePrefix /devshard/v2/...
│
└── ...
```

Code anchors:

- Runtime container: `devshardRuntime` and the `Gateway.runtimes` /
  `runtimeOrder` index — `devshard/cmd/devshardctl/gateway.go` (lines ~41,
  ~68).
- Each runtime is built in `buildRuntime` via `user.NewHTTPSession`, which
  returns both a `*user.Session` and a `*state.StateMachine` held together on
  the `Proxy` — `devshard/cmd/devshardctl/proxy.go` (lines ~157-161),
  `devshard/user/httpsession.go`.
- Version bind chain: `RuntimeConfig.RoutePrefix` →
  `resolveRuntimeRoutePrefix` → `user.NewHTTPSession` →
  `devshard.VersionForRoutePrefix(routePrefix)` → `state.WithVersion(...)` →
  `EscrowState.StateRootAndProtocolVersion` — `devshard/paths.go`,
  `devshard/user/httpsession.go`, `devshard/state/machine.go`.

| Layer | Multi-version support |
|-------|----------------------|
| Gateway process | One process, N runtimes |
| State machines | N machines, one per active escrow |
| Version bind | Per runtime, fixed at first session bind |
| Host calls | Each runtime uses its own `RoutePrefix` (`/devshard/<version>/...`) |
| Storage | One SQLite per escrow (`storage_path`) |
| Settlement | Each SM settles with its own bound tag |

## Routing semantics

Two public entry points on the gateway (`Gateway.Handler()` in
`gateway.go`, line ~990):

1. **Escrow-keyed**: `/devshard/{escrowID}/v1/chat/completions` →
   `handleDevshard` → `parseDevshardPath` → `g.runtimes[escrowID]`. The path
   segment is the **escrow ID**, not the protocol version. The version follows
   from whichever runtime owns that escrow. **HAVE** — already version-correct.

2. **Pooled**: `POST /v1/chat/completions` → `handlePooledChat` →
   `reserveRuntimeForModel`. Picks a runtime by **model + load only** today; it
   does not consider protocol version. A pooled request can land on either a v1
   or a v2 escrow. That is acceptable for backward compatibility (clients never
   choose a version), but we want explicit, observable behavior. **TODO**:
   version-aware pooled selection (see Phase 3).

Key point: the pooled chat route picks **which escrow/runtime**, and the
protocol version is whatever that escrow's SM is bound to. Clients do not pick
a protocol version directly.

## Backward compatibility model

We must keep both working simultaneously:

- **Legacy `v1`**: existing escrows bound to `v1`. Route prefix
  `/devshard/v1/...` to hosts; bind tag `v1`; settles with `v1`. The wider
  legacy client path `/v1/devshard/*` (dapi in-process) described in
  `upgrade.md` is unchanged and out of scope for the gateway.
- **New `v2`**: new escrows bound to `v2`. Route prefix `/devshard/v2/...`;
  bind tag `v2`; settles with `v2`.

Compatibility rules:

- **No in-place migration of an existing escrow.** A v1 escrow stays v1 until
  settled. To move volume to v2 you settle the v1 escrow (user-driven) and
  create a new escrow on the v2 prefix. This is forced by the bind being baked
  into every state root.
- Hosts must run **both** versiond slots so `/devshard/v1/...` and
  `/devshard/v2/...` each reach the correct `devshardd` child (see
  `upgrade.md` "Multiple versions per host").
- Recovery preserves the bound version: `RecoverSession` validates the stored
  `sessions.version` against the requested bind and fails on mismatch
  (`devshard/user/recover.go` "session version mismatch"). A v1 escrow on disk
  can only be recovered as v1.

## Gap analysis

| Capability | Status | Notes |
|-----------|--------|-------|
| Per-runtime version bind | **HAVE** | `RoutePrefix` → `VersionForRoutePrefix` → `WithVersion` |
| N runtimes per process, escrow-keyed | **HAVE** | `Gateway.runtimes` map, `handleDevshard` |
| Per-escrow storage isolation | **HAVE** | `RuntimeConfig.StoragePath`, per-escrow SQLite |
| Per-version host route prefix | **HAVE** | `transport.HTTPClient.RoutePrefix` |
| Settlement uses bound tag | **HAVE** | `StateRootAndProtocolVersion` in payload + root |
| Host refuses mixed version | **HAVE** | `ErrSessionVersionConflict`, manager bind check |
| `session_version` in status | **HAVE** | `snapshot()` sets `SessionVersion = st.StateRootAndProtocolVersion` |
| Config declares v1 + v2 together | **TODO** | Need example + validation; admin add supports `route_prefix` |
| Route-prefix → approved-version validation | **TODO** | Reject prefixes not in `approved_versions` |
| Version-aware pooled selection | **TODO** | Optional preference / per-model pinning |
| Per-version capacity / limiter accounting | **TODO** | Confirm limiter buckets don't conflate versions |
| Cross-version chat cache safety | **TODO** | Cache key must not serve a v1 body as v2 (or vice versa) |
| Tests: v1 + v2 coexist + settle | **TODO** | Testermint scenario + Go unit tests |

## Implementation plan

### Phase 0 — Confirm invariants (no code, audit only)

- Verify a runtime's bound version is observable end to end: `buildRuntime` →
  `sm.SnapshotState().StateRootAndProtocolVersion`. Add a debug assertion/log
  at startup that logs `escrow`, `route_prefix`, and resolved `version`.
- `runtimeStatus.SessionVersion` is already populated in `snapshot()`
  (`gateway.go` ~378, from `st.StateRootAndProtocolVersion`). Use it as the
  source of truth for a runtime's bound version in logs and admin output.

### Phase 1 — Config and validation for declaring v1 + v2

`RuntimeConfig` already carries `RoutePrefix` (`gateway.go` line ~37) and the
admin add/import requests carry `route_prefix` (lines ~1855, ~2535, ~2847,
~2956). Work:

- **TODO** Add validation in `buildRuntime` / `resolveRuntimeRoutePrefix`:
  - reject a `route_prefix` whose `VersionForRoutePrefix` does not parse;
  - when an approved-versions source is available, reject a version that is not
    approved (warn-only in the temporary release, hard-fail later).
- **TODO** Provide a multi-runtime config example (see "Config examples").
- **HAVE** One escrow → one runtime → one version: the `runtimes` map is keyed
  by escrow ID, so a single escrow cannot accidentally host two versions.

### Phase 2 — Settlement and storage per version (mostly verification)

- **HAVE** Settlement: `Proxy.settlementJSON` / `writeSettlement` use
  `state.BuildSettlement(escrowID, sm.SnapshotState(), ...)`, carrying that
  SM's `StateRootAndProtocolVersion`. No change needed; add a test asserting a
  v2 runtime settles with tag `v2` and a v1 runtime with `v1`.
- **HAVE** Storage isolation: each runtime opens its own SQLite at
  `StoragePath`. Ensure v1 and v2 runtimes never share a `storage_path`
  (validate uniqueness at load).
- **TODO** Add a guard that two runtimes do not point at the same
  `storage_path` (would corrupt `sessions.version`).

### Phase 3 — Version-aware pooled routing

Today `reserveRuntimeForModel` (`gateway.go` ~1333) ranks candidates by model
match and load, ignoring version. Options (pick one; default = A):

- **A. Version-agnostic (recommended for first cut).** Keep current behavior:
  pooled chat may land on v1 or v2 transparently. Document that operators
  control the mix by which escrows they fund. Lowest risk, preserves backward
  compatibility exactly.
- **B. Preferred-version with fallback.** Add an optional gateway setting
  `pooled_preferred_version` (e.g. `v2`). `reserveRuntimeForModel` prefers
  runtimes whose bound version equals the preference, falling back to others
  when none are routable. Lets operators drain v1 by preferring v2 while
  v1 escrows still serve.
- **C. Per-model version pin.** Extend model limits/settings so a given model
  is pinned to a version. Most control, most config surface.

Regardless of option:

- **TODO** Tag accounting and logs with the selected runtime's version
  (`gateway_runtime_selected` should include `version`).
- **TODO** Chat cache: include the runtime's bound version in the cache entry /
  key so a cached v1 response is never replayed as a v2 result and vice versa
  (`chatCacheKey`, `chatResponseCache`). At minimum store `version` on the
  entry and skip cross-version reuse.

### Phase 4 — Capacity, limiter, and metrics

- **TODO** Audit `CapacityState` / `GatewayLimiter` buckets: confirm weight and
  rate accounting are per-escrow (already) and that mixing versions does not
  double-count a host that runs both slots.
- **TODO** Metrics: add a `version` label to per-runtime gauges/counters so
  dashboards can split v1 vs v2 volume, errors, and settlement outcomes.

### Phase 5 — Observability and admin

- **HAVE (partial)** `handlePooledStatus` returns the per-runtime
  `snapshot()`, which already includes `session_version`. **TODO**: also
  surface each runtime's `route_prefix` in `handleAdminState` so an operator
  can see "escrow 42 = v1, escrow 87 = v2" alongside the host path.
- **TODO** `/v1/debug/state` and `handleStatus` already expose phase/nonce;
  add the bound version field.

### Phase 6 — Tests

- **TODO** Go unit: a `Gateway` with two runtimes (v1, v2) — assert
  `handleDevshard` routes each escrow to its own SM, and each settles with its
  own tag.
- **TODO** Go unit: route-prefix validation (good `/devshard/v2`, bad prefix,
  unapproved version).
- **TODO** Go unit: cross-version cache isolation.
- **TODO** Testermint: extend the standalone scenario
  (`testermint/src/test/kotlin/DevshardVersiondSessionTests.kt` /
  `DevshardVersiondAdvancedTests.kt`, `DevshardTestSupport.kt`) to bring up hosts with both versiond slots, create a
  v1 and a v2 escrow, run inferences on both, and settle both, asserting the
  on-chain settlement tag matches the bind for each.

## Config examples

Multi-runtime gateway config (JSON list consumed at load, `gateway.go`
~1763), declaring one v1 and one v2 escrow:

```json
[
  {
    "id": "escrow-42",
    "private_key_env": "DEVSHARD_KEY_42",
    "model": "Qwen/Qwen2.5-7B-Instruct",
    "storage_path": "./devshards/escrow-42",
    "route_prefix": "/devshard/v1"
  },
  {
    "id": "escrow-87",
    "private_key_env": "DEVSHARD_KEY_87",
    "model": "Qwen/Qwen2.5-7B-Instruct",
    "storage_path": "./devshards/escrow-87",
    "route_prefix": "/devshard/v2"
  }
]
```

Admin add of a v2 runtime at runtime (handled by `handleAdminDevshards`,
`adminDevshardRequest`):

```json
{
  "id": "escrow-87",
  "private_key_env": "DEVSHARD_KEY_87",
  "model": "Qwen/Qwen2.5-7B-Instruct",
  "storage_path": "./devshards/escrow-87",
  "route_prefix": "/devshard/v2"
}
```

Notes:
- `route_prefix` is the only thing that selects the protocol version for a
  runtime; omitting it falls back to `DEVSHARD_ROUTE_PREFIX` then
  `devshard.DefaultRoutePrefix()` (`resolveRuntimeRoutePrefix`).
- `storage_path` must be unique per runtime.

## Operational runbook: introducing v2 alongside v1

1. Governance approves `v2` in `approved_versions` (name, URL, sha256).
2. Hosts' versiond downloads and runs the v2 child next to v1, exposing
   `/devshard/v2/...` (see `upgrade.md`).
3. Create a new escrow and add a gateway runtime with
   `route_prefix=/devshard/v2`. The new SM binds to `v2` on first request.
4. (Optional) Set `pooled_preferred_version=v2` (Phase 3 option B) to steer new
   pooled traffic to v2 while v1 escrows keep serving.
5. Drain v1: stop funding v1 escrows; let the user settle them during the
   voting window before v1 is deprecated. No escrow is migrated in place.

## Non-goals / risks

- **No in-place version migration.** Forced by state-root binding; settle and
  recreate instead.
- **No single-SM version switching.** Explicitly impossible (see "Why one
  state machine cannot serve two versions").
- **Host-side coverage is a prerequisite.** If a host lacks the v2 versiond
  slot, v2 sessions cannot reach quorum; the gateway will surface this as
  signing/quorum failures, not as a routing bug.
- **Cache and limiter conflation** are the main correctness risks when two
  versions run together — Phases 3 and 4 address them explicitly.
