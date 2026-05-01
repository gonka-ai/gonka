# devshard testenv — port plan

Status: proposal. Target branch: `devshard-testenv`.

This document formalizes how the legacy `subnet-testenv` (on branch
`subnet-testenv`) is ported to the current `devshard` implementation on `main`,
and how the port is validated. It supersedes any ad-hoc porting notes.

---

## 1. Goals and non-goals

### Goals

1. Reproduce the operator-visible value of `subnet-testenv`: a self-contained
   Docker Compose stack that exercises devshard protocol end-to-end, without a
   live Cosmos chain and without real ML nodes.
2. Extract the "authenticated chain headers" surface into a **reusable module**
   (`devshard/blockoracle`) that ships with both the testenv and with the real
   `decentralized-api`.
3. Keep the testenv deterministic, runnable on a laptop, and CI-gatable within
   a few minutes.
4. Preserve production topology: each host is its own container; the
   dapi-facing surface is an in-process library link; there is exactly one
   shared component for authenticated chain state.

### Non-goals

1. Running `versiond` inside testenv. The testenv launches `devshardd-testenv`
   directly from compose; supervised upgrades are a production concern.
2. Running real `decentralized-api` inside testenv. A thin `mock-dapi` library
   satisfies devshardd's dapi-facing interfaces in-process.
3. Exercising the Cosmos keyring end-to-end. Testenv uses hex-encoded private
   keys for all identities; the keyring code path is unit-tested separately.
4. Authz warm-key flows, BLS DKG, NATS, PoC workers. These remain testermint
   territory.

---

## 2. Architecture

```text
┌────────────────────────┐
│      mock-chain        │   container (×1) — state source
│  gRPC :9090            │   escrows, participants, grantees, settlement
└───────────┬────────────┘
            │ gRPC (polled / subscribed)
            ▼
┌────────────────────────┐
│     height-sync        │   container (×1) — reusable block-oracle binary
│  HTTP :9100 + SSE      │   emits authenticated headers; in PROD this is
│                        │   replaced by real decentralized-api
└───────────┬────────────┘
            │ HTTP + SSE (1 publisher, N subscribers)
            ▼
┌───────────────────────────────────────────────────────┐
│             devshardd-testenv container (×N)          │
│                                                       │
│  ┌─────────────────────────────────────────────┐      │
│  │  mock-dapi  (Go library, linked in-process) │      │
│  │    BlockOracle  — SSE subscriber + verifier │      │
│  │    NodeManager  — no-op stub                │      │
│  └─────────────────────┬───────────────────────┘      │
│                        │ Go interface calls           │
│                        ▼                              │
│              devshardd (prod code, imported)          │
│  SQLite /data · HTTP :950X · hex signer · gossip p2p  │
└───────────────────────────────────────────────────────┘
                   ▲
                   │ HTTP /sessions/:id/chat/completions
                   │
┌───────────────────────────────────────────────────────┐
│           devshardctl container (×1, on-demand)       │
│       OpenAI-compatible developer proxy, hex signer   │
└───────────────────────────────────────────────────────┘
```

### 2.1 Container inventory

| Component          | Packaging          | Count | Testenv-only? | Notes                                       |
|--------------------|--------------------|-------|---------------|---------------------------------------------|
| `mock-chain`       | container          | 1     | yes           | seeds escrows and participants from config  |
| `height-sync`      | container          | 1     | yes           | runs the reusable `blockoracle` binary      |
| `devshardd-testenv`| container          | N     | yes           | one per host; each embeds `mock-dapi`       |
| `devshardctl`      | container          | 1     | no (shared)   | `profiles: [tools]`; started on demand      |

The **only** component that is a library rather than a container is
`mock-dapi`. It is linked in-process inside every `devshardd-testenv`.

In addition to the protocol components above, the stack ships an
**observability plane** (VictoriaMetrics, Grafana Alloy, cAdvisor,
Node Exporter, Loki, Grafana) on reserved IP range `172.30.0.100–119`.
Observability containers are operator-facing diagnostics and do not
participate in the protocol; they are detailed in §13.

### 2.2 Why each host is its own container

- Isolation: each host owns its own SQLite file, ports, signer key, and
  process failure domain.
- Wire-level gossip: peer-to-peer signatures travel across the docker network
  as they do in production. Collapsing hosts into one process would defeat
  half of the cPoC protocol's authentication surface.
- Fault injection: protocol-scenario tests require `docker stop devshardd-k`,
  network partitions, and per-host clock skew.
- Topology parity: one-container-per-host matches real deployment.

### 2.3 Why `mock-dapi` is a library

- In production, each devshardd runs next to exactly one `decentralized-api`
  on the same host; the in-process link is the production topology.
- `mock-dapi` has no independent lifecycle, no storage, and no shared state.
  Its only job is to satisfy dapi-facing interfaces for its one devshardd.
- Shared chain state lives in `height-sync`. `mock-dapi` is a stateless
  adapter that subscribes to it. That is precisely the shape of a library.

### 2.4 Why `height-sync` is its own container

- All hosts must observe the same authenticated chain state for `H(V)` to be
  meaningful across verifiers. A single shared publisher is the simplest way
  to achieve that.
- In production this same role is filled in-process by real dapi. The testenv
  extracts it into a standalone binary so the testenv can run without dapi.
- The binary exposes the same HTTP + SSE protocol that real dapi will expose,
  so devshardd's wire interface is unchanged between the two environments.

---

## 3. Reusable module: `devshard/blockoracle`

### 3.1 Package layout

```text
devshard/blockoracle/
├── doc.go
├── types.go              # Header, Commit, CommitSig, Proof (wire types)
├── oracle.go             # BlockOracle interface
├── observer/
│   ├── observer.go       # ChainObserver: tendermint RPC/gRPC-backed producer
│   └── mock.go           # fabricated-header producer for testenv
├── server/
│   └── http.go           # HTTP + SSE handler, mountable on any echo.Echo
├── client/
│   └── http.go           # HTTP + SSE client, caches latest header
├── verifier/
│   └── verifier.go       # pure: (*Header, ValidatorSet) -> error
└── standalone/
    └── main.go           # entrypoint for the height-sync container
```

### 3.2 Wire types (summary)

```go
type Header struct {
    Height             int64
    Time               time.Time
    ChainID            string
    BlockHash          []byte
    AppHash            []byte
    ValidatorsHash     []byte
    NextValidatorsHash []byte
    Commit             Commit
}

type Commit struct {
    Height     int64
    Round      int32
    BlockID    []byte
    Signatures []CommitSig
}

type CommitSig struct {
    ValidatorAddress []byte
    Timestamp        time.Time
    Signature        []byte
}

type Proof struct {
    Path  string
    Value []byte
    Ops   [][]byte // IAVL ops
}
```

### 3.3 `BlockOracle` interface

```go
type BlockOracle interface {
    Latest(ctx context.Context) (*Header, error)
    At(ctx context.Context, height int64) (*Header, error)
    Prove(ctx context.Context, path string, height int64) (*Proof, error)
    Subscribe(ctx context.Context, fromHeight int64) (<-chan *Header, error)
}
```

### 3.4 HTTP surface

Identical between testenv (`height-sync`) and production (`decentralized-api`):

- `GET /block/latest` → `Header`
- `GET /block/{height}` → `Header`
- `GET /block/{height}/prove?path=...` → `Proof`
- `GET /block/stream?from={height}` → SSE stream of `Header`
- `GET /healthz` → `200 ok`

### 3.5 Runtime matrix

| Runtime                          | Observer                        | Server           | Client                       | Verifier       |
|----------------------------------|---------------------------------|------------------|------------------------------|----------------|
| `height-sync` (testenv)          | `observer.NewMock(...)`         | mounted on :9100 | —                            | —              |
| real `decentralized-api` (prod)  | `observer.NewTendermint(...)`   | mounted on dapi  | —                            | —              |
| `mock-dapi` (testenv, in-process)| —                               | —                | `client.NewHTTP(HEIGHT_SYNC_URL)` | skipped (trust) |
| `devshardctl` (testenv)          | —                               | —                | `client.NewHTTP(HEIGHT_SYNC_URL)` | runs (pinned)   |
| real dapi in-process callers     | —                               | —                | `client.NewInProcess(obs)`   | runs           |

Two roles share the same `blockoracle/client` implementation but use
different trust settings:

- **Hosts (`mock-dapi`)** pass `Verifier: nil`. The client accepts every
  header the oracle serves and caches the full `Commit.Signatures`
  vector so downstream settlement can forward multi-sig proofs. This
  mirrors the production contract where `decentralized-api` trusts its
  own in-process observer.
- **Auditors (`devshardctl`, cross-host checks)** pin the
  `ChainID` + 10-validator `ValidatorSet` and re-verify every ingested
  header. The strict `> 3/4` quorum the mock enforces is stricter than
  the verifier's `> 2/3` rule, so verification always passes on a
  well-behaved producer.

### 3.6 Dependency rules

These are enforced by CI (§8.4):

- `devshard/blockoracle` MUST NOT import anything under `devshard/testenv`.
- `devshard/blockoracle` MUST NOT import anything under `decentralized-api`.
- `devshard/testenv/mockdapi` MUST import `devshard/blockoracle/client`.
- `devshard/testenv/mockdapi` MUST NOT import anything under `decentralized-api`.

---

## 4. Testenv layout

### 4.1 Directory tree

```text
devshard/testenv/
├── README.md
├── DEVELOPMENT-MODE.md
├── OBSERVABILITY.md
├── Makefile
├── config.yaml                        # operator-authored stack description
├── docker-compose.yml                 # generated base + manual observability tail
├── docker-compose.dev.yml             # dev overlay (live-reload + dlv)
├── Dockerfile.mock-chain
├── Dockerfile.height-sync
├── Dockerfile.devshardd-testenv
├── Dockerfile.devshardctl
├── Dockerfile.dev                     # shared base with go toolchain + air + dlv
├── .air.mock-chain.toml
├── .air.mock-chain.debug.toml
├── .air.height-sync.toml
├── .air.height-sync.debug.toml
├── .air.devshardd.toml
├── .air.devshardd.debug.toml
├── .air.devshardctl.toml
├── vscode-launch.json                 # IDE launch configs for attaching to dlv ports
├── proto/
│   └── mockchain.proto
├── bridge/
│   └── grpc.go                        # MainnetBridge impl against mock-chain
├── engine/
│   ├── inference_mock.go
│   └── validation_mock.go
├── mockdapi/                          # the library (see §5)
│   ├── doc.go
│   ├── mockdapi.go
│   ├── blockoracle.go
│   ├── nodemanager.go
│   └── config.go
├── observability/
│   ├── alloy/
│   │   └── config.alloy               # scrape → remote_write + docker logs → loki
│   ├── loki/
│   │   └── config.yaml
│   └── grafana/
│       ├── provisioning/
│       │   ├── datasources/datasources.yaml
│       │   └── dashboards/dashboards.yaml
│       └── dashboards/
│           ├── devshard-overview.json
│           ├── cadvisor-containers.json
│           └── node-exporter-full.json
└── cmd/
    ├── mockchain/
    │   └── main.go
    ├── heightsyncd/
    │   └── main.go
    ├── devshardd-testenv/
    │   └── main.go
    └── gencompose/
        └── main.go
```

### 4.2 Environment variable contract

| Service            | Env var                 | Purpose                                          |
|--------------------|-------------------------|--------------------------------------------------|
| `mock-chain`       | `CONFIG_PATH`           | path to seed yaml                                |
|                    | `PORT`                  | gRPC port (default 9090)                         |
| `height-sync`      | `CONFIG_PATH`           | path to testenv config YAML                      |
|                    | `HEIGHT_SYNC_PORT`      | HTTP port override (default from YAML: 9100)     |
| `devshardd-testenv`| `TESTENV_PRIVATE_KEY`   | hex signer for this host                         |
|                    | `ESCROW_ID`             | escrow this host participates in                 |
|                    | `SLOT_INDEX`            | slot position within the escrow                  |
|                    | `MOCK_CHAIN_URL`        | gRPC URL of mock-chain                           |
|                    | `HEIGHT_SYNC_URL`       | HTTP URL of height-sync                          |
|                    | `CHAIN_ID`              | pinned chain ID                                  |
|                    | `HTTP_PORT`             | devshardd HTTP port (default 9500)               |
|                    | `DATA_DIR`              | SQLite directory (default `/data`)               |
| `devshardctl`      | `TESTENV_PRIVATE_KEY`   | developer hex signer                             |
|                    | `DEVSHARDD_URL`         | chosen host to proxy to                          |
|                    | `ESCROW_ID`             | escrow the developer drives                      |
|                    | `CONFIG_PATH`           | testenv config (needed to pin the validator set) |

The mock-mainnet validator set (10 keys by default, configurable under
`height_sync.validators` in `config.yaml`) is the single source of
truth. `height-sync` reads the private keys to sign commits;
`devshardctl` reads the derived addresses to verify them.
`devshardd-testenv` hosts *do not* verify — they trust the oracle and
cache the full commit (including every `Commit.Signatures` entry) so
they can forward proofs to settlement downstream.

No service consumes `KEY_NAME`, `KEYRING_BACKEND`, `KEYRING_DIR`, or
`KEYRING_PASSWORD`.

---

## 5. `mock-dapi` library

Package `devshard/testenv/mockdapi`.

```go
type Config struct {
    HeightSyncURL    string        // required: base URL of height-sync container
    ChainID          string        // informational; trust mode does not filter on it
    ResubscribeAfter time.Duration // 0 = client default (1s)
    StaleAfter       time.Duration // 0 = client default (10s)
    HTTPClient       *http.Client  // nil = default; tests inject an httptest client
}

type MockDapi struct {
    Oracle      blockoracle.BlockOracle
    NodeManager nmpb.NodeManagerClient // devshard/mlnode/gen.NodeManagerClient
}

func New(ctx context.Context, cfg Config) (*MockDapi, error) { /* ... */ }
func (m *MockDapi) Close()                                   { /* ... */ }
```

- `Oracle` is a `blockoracle/client` instance in *host trust mode*
  (`Verifier: nil`). Hosts trust the oracle by construction — there is
  no per-host config knob to flip this — and cache every incoming
  header, including the full `Commit.Signatures` vector, so downstream
  settlement can forward multi-sig proofs without a second round trip.
  Non-host consumers (devshardctl) pin the 10-validator set and verify
  the stream end-to-end.
- `NodeManager` is a `NoopNodeManager` that satisfies the generated
  `gen.NodeManagerClient` interface byte-for-byte so no prod wiring
  shape changes. `AcquireMLNode` returns a monotonically-increasing
  synthetic lock id (so concurrent tests can disambiguate calls),
  fixed placeholder endpoint (`mockdapi://noop`) and node id
  (`mockdapi-node-0`); `ReleaseMLNode` is a no-op and accepts every
  `ReleaseOutcome` without a gRPC error. Stub engines never
  dereference the endpoint. Heterogeneous per-node behavior (e.g. the
  REST control plane in [`testenv-stub-engines.md`](testenv-stub-engines.md))
  replaces this stub at the call site.
- `Close()` tears down the oracle's background SSE goroutine; it is
  idempotent and nil-safe, so scenarios that close both on defer and
  via context cancellation do not panic.
- Each `devshardd-testenv` process has its own `MockDapi` instance. All
  instances converge on the same `H(V)` by subscribing to the same
  `height-sync`.

Wiring inside `devshardd-testenv/main.go` (conceptual; a central
`HostManager` does not exist in tree today — per-escrow hosts are
constructed directly via `host.NewHost`, and a manager is tracked as a
follow-up refactor orthogonal to this port):

```go
md, err := mockdapi.New(ctx, mockdapi.Config{
    HeightSyncURL: os.Getenv("HEIGHT_SYNC_URL"),
    ChainID:       os.Getenv("CHAIN_ID"),
})

signer := signing.MustSignerFromHex(os.Getenv("TESTENV_PRIVATE_KEY"))
br := testenvbridge.NewGRPCBridge(ctx, os.Getenv("MOCK_CHAIN_URL"))

// Per-escrow host construction (today's seam). The BlockOracle is
// injected via host.WithBlockOracle; cPoC stamping reads H(V) through
// Host.LatestHeight.
h, err := host.NewHost(
    sm, signer, testenvengine.NewMockInference(),
    escrowID, group, checker,
    host.WithStorage(mustOpenSQLite(dataDir)),
    host.WithVerifier(signing.NewSecp256k1Verifier()),
    host.WithValidator(testenvengine.NewMockValidation()),
    host.WithBlockOracle(md.Oracle),
)
// NodeManager is held on the mockdapi instance; stub engines do not
// dereference it, so no option is required on Host today. When a prod
// NodeManager seam lands, it gets its own HostOption alongside
// WithBlockOracle.
_ = br
_ = md.NodeManager
```

Swapping `mockdapi.New(...)` for a real dapi-local oracle (via
`blockoracle/client.NewInProcess(obs)`) in production is the only
difference between the two wirings on the oracle axis.

---

## 6. Step-by-step port plan

Phases are intended to land as separate PRs where practical. Every phase
ends with green unit tests; phases 10 and later are gated on integration
tests.

### Phase 0 — branch and skeleton

- Create `devshard-testenv` branch off `main`.
- Create the directory tree in §4.1; all files stubbed with `doc.go` placeholders.
- Add `devshard/testenv/` to the repo's module graph; ensure `go build ./...` passes.

### Phase 1 — `devshard/blockoracle` package

- Implement `types.go`, `oracle.go`, `verifier/verifier.go`.
- Implement `observer/mock.go` (fabricated headers, signed with a configured
  validator key).
- Implement `server/http.go` with the four endpoints from §3.4.
- Implement `client/http.go` with subscribe + verify-on-ingest + cache.
- Stub `observer/tendermint.go` with an explicit `NotImplementedYet` panic
  and a TODO pointing at the follow-up PR.
- Unit tests per §8.1.

### Phase 2 — `mock-chain` container

- Port `subnet/testenv/cmd/mockserver` → `devshard/testenv/cmd/mockchain`.
- Rewrite `proto/mockchain.proto` to match current bridge interface:
  `GetDevshardEscrow`, `GetParticipant`, `GetGrantees`, `CreateEscrow`,
  `SettleEscrow`.
- `SettleEscrow` calls `devshard/state.VerifySettlement` for real verification.
- Seed escrows from `config.yaml` at start.
- Healthcheck on gRPC reflection endpoint.

### Phase 3 — `height-sync` container

- Implement `devshard/testenv/cmd/heightsyncd/main.go` as a thin wrapper over
  `blockoracle/standalone` (which itself composes `observer/mock` +
  `server/http`).
- Mock-mainnet is simulated by a set of N validators (default 10, fully
  configurable via `height_sync.validators` in `config.yaml`). Every
  fabricated block is multi-signed by most of them; the retained voting
  power is always strictly > 3/4 of the total so external auditors'
  > 2/3 checks pass with headroom.
- Optionally poll `mock-chain` to seed `AppHash` from a hash of seeded state
  (nice-to-have; can start with a deterministic in-memory KV).
- Expose HTTP on :9100 and `/healthz`.
- Acceptance tests live in §8.1.1 (unit) and §8.2 rows I9 / I10 (integration).

### Phase 4 — bridge adaptation

- Port `subnet/testenv/bridge/grpc.go` → `devshard/testenv/bridge/grpc.go`.
- Implement the full current `devshard/bridge.MainnetBridge`:
  - Query methods (`GetEscrow`, `GetHostInfo`, `VerifyWarmKey`) round-trip
    to mock-chain via gRPC. `codes.NotFound` is translated to the
    prod-bridge sentinel errors (`ErrEscrowNotFound`,
    `ErrParticipantNotFound`). `VerifyWarmKey` performs a membership check
    against `GetGrantees(validator, "/inference.inference.MsgStartInference")`
    and memoizes the result per (warm, validator) pair — matching the
    caching behavior of `devshard/bridge.RESTBridge`.
  - Notifications (`OnEscrowCreated`, `OnSettlementProposed`,
    `OnSettlementFinalized`) and actions (`SubmitDisputeState`) return
    `ErrNotImplemented` to match the prod bridge; mock-chain has no chain
    event stream and no dispute flow.
- `NewGRPCBridge(ctx, "mock-chain:9090")` is the prod call path.
  `NewGRPCBridgeWithClient(mockChainClient)` is reserved for tests that
  dial over bufconn.
- No separate inference-query adapter is needed — `MainnetBridge` is the
  only chain-facing seam devshardd exposes today. If that changes when a
  proto/inference gRPC client is added, the testenv bridge should grow a
  parallel adapter in the same package.
- Acceptance tests: §8.1 row `testenv/bridge.GRPCBridge`.

### Phase 5 — prod seam: inject `BlockOracle` into `host.Host`

This is the only prod-side change this port forces.

- Add `blockoracle.BlockOracle` to `host.Host` as an optional dependency
  via `host.WithBlockOracle(o)`. Exposes two accessors:
  - `Host.LatestHeight(ctx)` — returns the oracle's latest `Header.Height`,
    or `host.ErrNoBlockOracle` when unwired. This is the seam cPoC
    stamping (see `proposals/CPOC_PROTOCOL.md`) reads `H(V)` through.
  - `Host.BlockOracle()` — returns the injected instance (or `nil`) for
    callers that need `At(h)` / `Prove(...)` for dispute evidence.
- No existing code stamps `H(V)` / `height_at[n]` yet, so there are no
  call-sites to migrate in this phase. Wiring the seam now lets cPoC
  PRs land without touching transport, host lifecycle, or session
  wiring — they only add call-sites to `LatestHeight`.
- Passing `nil` to `WithBlockOracle` is a documented no-op: the host
  stays in the unwired state and `LatestHeight` returns
  `ErrNoBlockOracle`, so mis-wired deployments fail loud rather than
  silently stamping `height=0`.
- A central `HostManager` + `HostManagerConfig` does not exist in tree
  today; a future refactor may introduce it to replace the per-escrow
  `host.NewHost` construction path, at which point `BlockOracle` moves
  from `HostOption` to `HostManagerConfig`. That refactor is orthogonal
  to this port and tracked separately.
- Real dapi wiring (in `decentralized-api`) should pass
  `blockoracle/client.NewInProcess(obs)` built around the dapi-local
  observer once that observer is ready. Until then, the constructor
  accepts any `blockoracle.BlockOracle`, so a stub that returns a
  fixed height is an acceptable interim.
- Acceptance tests: `devshard/host/blockoracle_test.go` covers unwired,
  wired happy-path, live updates (no stale cache), upstream-error
  propagation, `WithBlockOracle(nil)` no-op, and context cancellation.

### Phase 6 — `mockdapi` library

- Implement §5:
  - `Config` with `HeightSyncURL` (required), `ChainID` (informational),
    `ResubscribeAfter`, `StaleAfter`, `HTTPClient`.
  - `MockDapi{Oracle, NodeManager}` + idempotent, nil-safe `Close()`.
  - `Oracle` is a `blockoracle/client` in host-trust mode
    (`Verifier: nil`) so hosts cache full `Commit.Signatures` for
    downstream settlement proofs.
  - `NodeManager` is `NoopNodeManager` implementing
    `devshard/mlnode/gen.NodeManagerClient` — compile-time +
    runtime contract assertions keep it honest across codegen
    regenerations.
- Constructor validates `HeightSyncURL` and `ctx`; rejects both with
  explicit errors so wiring bugs do not surface as "first Latest()
  panics" at runtime.
- Tests land in `devshard/testenv/mockdapi/mockdapi_test.go`, driven
  by the real `blockoracle/observer` + `blockoracle/server` stack
  wired through `httptest` — trust-mode ingest, live-producer advances
  over SSE, `Close` idempotence/nil-safety, `NoopNodeManager` lock-id
  uniqueness, "never exhausts", every `ReleaseOutcome` succeeds,
  context cancellation, contract satisfaction. See §8.1 for the
  coverage summary row.

### Phase 7 — stub engines (7a landed)

#### Implemented

- `devshard/testenv/engine/inference_mock.go` — `MockInferenceEngine`
  satisfying `devshard.InferenceEngine`:
  - Deterministic response body keyed off
    `(InferenceID, EscrowID, Model)` so the same triple always yields
    the same `ResponseBody` and `ResponseHash = sha256(ResponseBody)`
    — the property protocol tests rely on when asserting the second
    verifier observed the same response as the first.
  - Token accounting defaults to 80/40 (matching subnet-testenv so
    existing scenarios don't need re-pinning). Override via
    `MockInferenceConfig.InputTokens` / `OutputTokens`.
  - Latency via `MockInferenceConfig.Latency` — respects
    `ctx.Done()` so cancelling a verifier mid-flight unblocks
    promptly (asserted by `TestExecute_CancelDuringLatency`).
  - Streaming path writes two SSE frames
    (`data: <body>\n\n` + `data: [DONE]\n\n`) when
    `ExecuteRequest.ResponseWriter` is set; falls through with a loud
    Error log when the writer is not an `http.Flusher`.
  - Env-var factory `NewMockInferenceFromEnv()` reads
    `TESTENV_INFERENCE_LATENCY_MS`, `TESTENV_INFERENCE_INPUT_TOKENS`,
    `TESTENV_INFERENCE_OUTPUT_TOKENS`. Malformed values log at Error
    and fall back to defaults — the test environment never fails to
    start just because one env var is wrong.

- `devshard/testenv/engine/validation_mock.go` — `MockValidationEngine`
  satisfying `devshard.ValidationEngine`:
  - `MockValidationConfig.DefaultValid` (default `true`) picks the
    verdict returned when no per-inference override is set.
  - Per-inference verdict overrides via `SetVerdict(id, valid)` /
    `ClearVerdict(id)`.
  - Per-inference error overrides via `SetError(id, err)` /
    `ClearError(id)`. Error overrides precede verdict overrides and
    return `(nil, err)` so scenarios can also exercise transport-level
    validation failure paths. `ErrTestenvInjectedFault` is a provided
    sentinel for callers that just want a recognizable error.
  - `Reset()` wipes both override maps — useful when one host serves
    back-to-back scenarios.
  - Latency via `MockValidationConfig.Latency`, ctx-aware sleep.
  - Env-var factory `NewMockValidationFromEnv()` reads
    `TESTENV_VALIDATION_VERDICT` (`valid` / `invalid`) and
    `TESTENV_VALIDATION_LATENCY_MS`.
  - Override maps are guarded by an `RWMutex` so scenarios can flip
    verdicts concurrently with executing Validate calls without
    racing (asserted under `-race` by
    `TestValidate_ConcurrentReadersAndWriters`).

Both engines log at `Debug` on entry / result and at `Info` on
inference completion (no `Trace` level in `devshard/logging`, so the
subnet-testenv `trace` calls map onto `Debug`). Compile-time
assertions (`var _ devshard.InferenceEngine = ...`, `var _
devshard.ValidationEngine = ...`) keep them contract-locked.

Note: the originally-sketched `X-Testenv-Inject-Fault: invalid`
header-driven path is intentionally deferred. The current
`devshard.ValidateRequest` does not carry HTTP headers, so
header-driven injection requires a transport-level plumb (Phase 7c,
covered by `testenv-stub-engines.md`). Phase 7a covers the same
scenarios via `SetVerdict` / `SetError` called from the scenario
orchestrator.

#### Deferred (Phase 7b / 7c)

Per-node profiles keyed off the `node_id` returned by the mock
`NodeManager`, a testenv-only REST control plane driven by a test
orchestrator, strict prod-build isolation, and header-based fault
injection are all designed in
[`testenv-stub-engines.md`](testenv-stub-engines.md). They land
incrementally from that doc without blocking Phase 8.

### Phase 8 — `devshardd-testenv` binary (landed)

`devshard/testenv/cmd/devshardd-testenv/main.go` is the single-process,
single-escrow host used in the compose network. Every container in
§4.1 labeled `devshardd-testenv` runs one instance of this binary.

#### Wiring

Implements the §5 contract end-to-end:

1. `loadEnvConfig()` resolves the §4.2 env-var contract
   (`TESTENV_PRIVATE_KEY`, `ESCROW_ID`, `MOCK_CHAIN_URL`,
   `HEIGHT_SYNC_URL`, optional `CHAIN_ID`, `HTTP_PORT` (default 9500),
   `DATA_DIR` (default `/data`)). Missing required vars are reported
   as a composite error so operators see every misconfiguration at
   once.
2. `signing.SignerFromHex(cfg.PrivateKeyHex)` — no keyring, no
   cosmos-sdk dependency.
3. `testenvbridge.NewGRPCBridge(ctx, cfg.MockChainURL)` for the
   `MainnetBridge` implementation. `defer br.Close()` tears the gRPC
   connection down on shutdown.
4. `mockdapi.New(ctx, mockdapi.Config{HeightSyncURL, ChainID})` for
   the host-trust `BlockOracle` and `NoopNodeManager`. `NodeManager`
   is pinned to `_` to document the intentional gap: the stub
   engines never dereference it, but the wiring survives swapping in
   a real gRPC NodeManager without shape changes.
5. `storage.NewSQLite(filepath.Join(cfg.DataDir, "devshardd.db"))` —
   the compose volume mount makes the DB survive container restarts.
6. `bridge.BuildGroup` + `br.GetEscrow` → `types.SessionConfigWithPrice`.
   `store.CreateSession(...)` is best-effort — a stale sqlite from a
   previous run makes it a no-op, not a fatal error, so the process
   recovers cleanly from a crash-restart cycle.
7. `state.NewStateMachine(..., state.WithWarmKeyResolver(br.VerifyWarmKey))`.
8. Engines: `testenvengine.NewMockInferenceFromEnv()` and
   `NewMockValidationFromEnv()` — tuning via Phase 7a env vars.
9. `host.NewHost(sm, signer, inference, escrowID, group, nil,
   WithStorage, WithVerifier, WithValidator, WithBlockOracle(md.Oracle))`.
   `ResponseWriter`-based streaming from the inference stub flows
   through the transport layer untouched.
10. Gossip: `buildPeerClients` resolves every peer via
    `br.GetHostInfo`, dedupes multi-slot hosts, excludes our own
    address, and builds one authenticated `transport.HTTPClient` per
    unique peer (signed with OUR key so the remote's auth middleware
    recognizes us as a group member). `gossip.NewGossip(escrowID,
    primarySlotID, peers, h.HostMempool(), WithSigAccumulator(h),
    WithRecovery(peers[0], h))`.
11. `transport.NewServer(h, store, verifier, creator,
    transport.WithBridge(br))`, routes mounted at `/v1/devshard` via
    `srv.Register`, with a `/healthz` sidecar for container healthcheck.
12. Graceful shutdown on SIGINT / SIGTERM: context cancel → stop
    gossip → `e.Shutdown` with a 5 s budget → close bridge + storage
    + mockdapi (order handled by Go's `defer` stack).

No import reaches into `decentralized-api/...`. The dependency
boundary is enforced at the command level — this binary is the one
place the testenv would be tempted to import dapi.

#### Unit tests (landed)

`main_test.go` covers the pure-Go wiring without a running compose
network:

- `TestLoadEnvConfig_HappyPath` — every required var consumed; defaults applied for `HTTP_PORT` and `DATA_DIR`.
- `TestLoadEnvConfig_OverridesApplied` — non-default `HTTP_PORT` / `DATA_DIR` values survive.
- `TestLoadEnvConfig_MissingRequiredVars` — every missing required var is listed in the error.
- `TestLoadEnvConfig_InvalidHTTPPort` — malformed port is explicit, no silent fallback.
- `TestPrimarySlotID_PicksFirstOwned` — helper returns the first slot in iteration order owned by this address.
- `TestPrimarySlotID_ReturnsZeroWhenAbsent` — defensive fallback for hosts not in the group.
- `TestEnvOr_FallbackAndOverride` — guards the precedence of the env helper.
- `TestBuildPeerClients_DedupesAndExcludesSelf` — multi-slot hosts collapse to one client; self excluded.
- `TestBuildPeerClients_PropagatesBridgeError` — bridge errors surface rather than being silently dropped.
- `TestBuildPeerClients_SoloGroupReturnsNone` — degenerate 1-host escrow yields zero peers and no error.

End-to-end `run(cfg)` smoke coverage is gated on Phase 10 integration
tests (real compose network), since it requires a live mock-chain +
height-sync stack.

### Phase 9 — devshardctl integration (landed)

Goal: teach the existing `devshard/cmd/devshardctl` binary to drive
the testenv without forking a second CLI — same flags and env
contract as prod, plus testenv-specific knobs that default off.

#### Wiring

1. **Config resolution extracted.** `main()` now calls `parseFlags` →
   `resolveConfig(fs, os.Getenv)` → `buildBridge(cfg)` so every knob
   has one documented precedence path and unit tests can exercise it
   without spawning a process.
2. **`TESTENV_PRIVATE_KEY` env var.** Recognised as a fallback after
   `--private-key` and `DEVSHARD_PRIVATE_KEY`. The binary already
   routed every key through `signing.SignerFromHex` (no keyring
   dependency in `devshardctl`) — Phase 9 is the env-name plumbing on
   top of that existing path.
3. **`ESCROW_ID` env var.** Mirrors the `TESTENV_PRIVATE_KEY` story
   for the escrow ID: falls back after `--escrow-id` and
   `DEVSHARD_ESCROW_ID`, matching the §4.2 testenv contract.
4. **`--mock-chain` flag + `MOCK_CHAIN_URL` env.** When set,
   `buildBridge` constructs `testenvbridge.NewGRPCBridge` to talk to
   the compose `mock-chain:9090` service instead of `bridge.NewRESTBridge`.
   `--mock-chain` wins over `--chain-rest` so a developer cannot
   accidentally hit a real chain when `MOCK_CHAIN_URL` is set.
5. **`--host` flag + `DEVSHARDD_URL` env.** When set, the constructed
   bridge is wrapped with `pinnedHostBridge`, a tiny decorator that
   overrides only `GetHostInfo(address)` to return the pinned URL.
   The queried address is returned verbatim as the `HostInfo.Address`
   so signature auth on the devshardd side still looks callers up by
   address, not by URL. Every other `MainnetBridge` method forwards
   unchanged to the inner bridge.
6. **Startup log line** tags the active bridge (`bridge=mock-chain:…`
   vs `bridge=rest:…`) and the host pin (`host_pin=auto` vs the
   pinned URL) so operators can verify they got the intended runtime
   without having to inspect flags or env.

The `devshardctl` binary still targets a single process; Phase 10's
`gencompose` emits the service behind `profiles: ["tools"]` so the
operator CLI is opt-in (`docker compose --profile tools up` /
`docker compose run --rm devshardctl`).

#### Unit tests (landed)

Added `cmd/devshardctl/config_test.go` with 18 cases covering:

- Flag beats env for private key and escrow id.
- `DEVSHARD_*` env vars beat the new `TESTENV_PRIVATE_KEY` / `ESCROW_ID`
  aliases (prod-first precedence for mixed setups).
- Testenv env aliases kick in only when prod ones are unset.
- Missing key / missing escrow errors name every recognised source
  so ops have a human-readable fix path.
- `--mock-chain` flag and `MOCK_CHAIN_URL` env both override
  `--chain-rest`; REST default stays when neither is set.
- `--host` flag beats `DEVSHARDD_URL` env; env is the fallback;
  unset leaves auto host discovery enabled.
- Default model, port, and storage path layout are pinned (guards
  against accidental drift of the documented OpenAI-compatible port
  and on-disk location).
- `describeBridge` / `describeHostPin` helpers produce unambiguous
  startup-log tags.
- `pinnedHostBridge` pins every address to one URL, preserves the
  queried address verbatim, forwards every other bridge method to
  the inner implementation, and panics fast on an empty pinned URL.
- `buildBridge` picks the testenv gRPC bridge when
  `MockChainURL` is set and wraps with the pinned decorator when
  `PinnedHostURL` is set.

### Phase 10 — `gencompose` (landed)

#### Implemented

- `devshard/testenv/cmd/gencompose/main.go` — single-file binary with
  three flags (`-config`, `-out`, `-obs-fragment`) and one helper per
  responsibility (`generateHosts`, `generateUser`, `generateValidators`,
  `assignSlots`, `fillNetworkDefaults`, `writeCompose`).
- Input: `testenv/config.yaml` as shipped. Missing file → built-in
  `defaultConfig` (4 hosts, 10 mock-mainnet validators, deterministic
  engines) so a fresh clone can run `gencompose` once and get a
  working stack.
- Fills every missing `private_key_hex` (hosts, user, and every
  `height_sync.validators[]`), derives bech32 addresses, and rewrites
  `config.yaml` through `Config.Save` with a regeneration banner.
  `isPlaceholderKey` treats `""`, whitespace, `TODO…`, and `CHANGEME…`
  as unset so committed skeletons round-trip cleanly; operator-
  supplied hex keys (and hand-picked addresses) are preserved across
  runs — `TestFillConfig_Idempotent` pins the no-churn invariant.
- `assignSlots` is round-robin (slot `i` → host `i % len(hosts)`) and
  clears any previous assignment so re-running is idempotent; invalid
  inputs (no hosts) are left to `Config.Validate` rather than
  panicking here.
- `fillNetworkDefaults` stamps deterministic container IPs so the
  observability overlay (reserved `.100–119`) never collides:
  `mock-chain=.2`, `height-sync=.3`, `devshardctl=.9`,
  `devshardd-testenv-i = .10 + i`.
- Output: `docker-compose.yml` with one `mock-chain`, one
  `height-sync`, N `devshardd-testenv-<i>`, and a `devshardctl`
  service gated behind `profiles: ["tools"]` (opt-in via
  `docker compose --profile tools up`). Every service mounts the
  shared `config.yaml` read-only; each host gets its own
  `./db/<id>` bind-mount for the SQLite store.
- Env vars emitted match §4.2: `CONFIG_PATH`, `MOCK_CHAIN_PORT`,
  `HEIGHT_SYNC_PORT`, `TESTENV_PRIVATE_KEY`, `ESCROW_ID`,
  `SLOT_INDEX`, `MOCK_CHAIN_URL`, `HEIGHT_SYNC_URL`, `CHAIN_ID`,
  `HTTP_PORT`, `DATA_DIR`, `DEVSHARDD_URL`. `devshardctl` pins to
  `hosts[0].URL` so `docker compose run devshardctl` works with zero
  arguments; override with `DEVSHARDD_URL` on the command line when
  targeting a different host.
- Optional observability overlay: if
  `observability/compose-fragment.yaml` exists it is appended verbatim
  (minus the bare `services:` key via `stripServicesKey`) so
  regeneration only ever touches the generated service region above
  it. Missing overlay is logged and skipped — Phase 10 does not
  require Phase 13 to land.
- No keyring volumes — private keys travel through env vars only, the
  same path production uses.

#### Supporting package changes

- `devshard/signing.Secp256k1Signer.PrivateKeyHex()` — needed so
  gencompose can serialise freshly-generated keys back into
  `config.yaml` without reaching into the unexported ECDSA key.
- `devshard/testenv/config.Config.Save()` — YAML marshal plus a
  "generated by gencompose" banner so operators don't mistake the
  file for a hand-edited skeleton.
- `devshard/testenv/config.Config.ApplyDefaults()` — exported wrapper
  so `defaultConfig` in gencompose stamps the same defaults
  `Load` does without needing a round-trip through disk.

#### Unit tests (landed)

Added `testenv/cmd/gencompose/main_test.go` with 12 cases covering:

- `isPlaceholderKey` across `""`, whitespace, `TODO…`, `CHANGEME…`,
  and legitimate hex input (no false-positive overwrites).
- `generateHosts` fills key+address+id+port for empty slots, preserves
  a caller-supplied id, derives addresses from pre-set keys, and
  surfaces malformed keys as errors (no silent regeneration).
- `generateUser` generates when missing and preserves a supplied key
  + derives the matching address.
- `generateValidators` fills placeholder / empty entries, defaults
  `Power` to `1`, and never changes the declared count (operators
  control cardinality by editing the skeleton).
- `assignSlots` is round-robin, clears prior assignments, and handles
  the zero-host edge without panicking.
- `fillNetworkDefaults` stamps `172.30.0.(10+i)` / service-name URLs
  and leaves operator-supplied overrides alone.
- `defaultConfig` + `fillConfig` round-trips `Config.Validate` cleanly
  and inherits `escrow.creator_address` from `user.address` by
  default.
- `writeCompose` end-to-end: compose contains every expected service
  name, env var, and pinned IP; the output parses as YAML; saved
  `config.yaml` reloads without TODOs; the observability fragment is
  spliced in with a single top-level `services:` key after
  `stripServicesKey`.
- `TestFillConfig_Idempotent` — a second run produces identical
  host/user/validator keys so committing the generated config is
  safe.
- Helper coverage for `firstSlot`, `slotList`, and `stripServicesKey`.

### Phase 11 — Dockerfiles (landed)

The four service Dockerfiles share a single multi-stage shape that
gencompose's compose output expects:

- **Build context**: `..` (the `devshard/` module root). gencompose
  emits `context: ..` and `dockerfile: testenv/Dockerfile.<svc>` for
  every service, so all `go build` paths inside the Dockerfiles are
  rooted at `devshard/` (not the repo root). A regression on this
  contract used to surface only as a six-minute docker build
  failure, so it is now pinned by `testenv/dockerfiles_test.go`.
- **Builder stage**: `golang:1.24-alpine AS build`. Modules are
  downloaded in their own layer so editing anything under `testenv/`
  does not bust the dependency cache. Both build steps mount BuildKit
  caches (`/go/pkg/mod`, `/root/.cache/go-build`) so iterating on
  Dockerfiles stays fast on a developer laptop.
- **Static binaries**: `CGO_ENABLED=0 go build -trimpath
  -ldflags="-s -w"`. devshardd uses `modernc.org/sqlite` (pure Go),
  so we can stay CGO-free across every image and ship to
  `distroless/static`. The flags strip symbol tables for a smaller
  layer; debugging is handled by the dev overlay (Phase 12) instead.
- **Runtime stage**: `gcr.io/distroless/static-debian12:latest`. No
  shell, no package manager, just CA roots and the binary at
  `/usr/local/bin/<name>`. `ENTRYPOINT` always points at that path so
  compose `command:` overrides land where operators expect them.

Per-service deltas from the shared shape:

- `Dockerfile.mock-chain` and `Dockerfile.height-sync` set
  `ENV CONFIG_PATH=/app/config.yaml` so the binary picks up the
  bind-mounted shared config without an explicit `--config` flag in
  the compose file.
- `Dockerfile.devshardd-testenv` pre-creates `/data` in the build
  stage and copies it into the runtime image. distroless has no shell,
  so we cannot `mkdir -p /data` from an init script before SQLite
  opens the file. The image also defaults `ENV DATA_DIR=/data` to
  match the per-host bind mount in compose.
- `Dockerfile.devshardctl` builds from `./cmd/devshardctl` (the
  production CLI binary) — there is no testenv-specific fork. The
  compose service runs under the `tools` profile so `docker compose
  up` does not pull it in by default.

`Dockerfile.dev` is **not** rewritten in Phase 11 — it is the shared
`golang:alpine` base with `air` + `dlv` for live-reload and remote
debugging, owned by Phase 12.

#### Test pinning

`devshard/testenv/dockerfiles_test.go` parses each Dockerfile and
asserts:

- The `go build ./…` target resolves to a real package under
  `devshard/` (the build context).
- The runtime stage is `distroless/static-debian12`, never `alpine:`.
- `CGO_ENABLED=0` is preserved so distroless stays viable.
- `ENTRYPOINT` matches the well-known `/usr/local/bin/<binary>` path.
- `Dockerfile.devshardd-testenv` pre-creates `/data` and exports
  `DATA_DIR=/data`.
- `Dockerfile.dev` keeps its `air` + `dlv` toolchain and the `air`
  ENTRYPOINT so the Phase 12 live-reload contract is not broken by a
  drive-by edit to the Phase 11 images.

### Phase 12 — dev overlay (live-reload + remote debugger) (landed)

This phase ports the in-container hot-reload + `dlv` workflow that existed in
`subnet-testenv`. The dev image is **not a different binary** — it's the same
source compiled with `-gcflags='all=-N -l'` and supervised by `air`, so every
edit to Go source under `/workspace/devshard/**/*.go` triggers a rebuild and
restart inside the container.

The overlay is purely additive: the base `docker-compose.yml` emitted by
`gencompose` is unchanged, the production `devshardd` binary is unchanged,
and flipping between "prod-shape stack" and "dev-loop stack" is a
`make dev-up` / `make up` swap. `testenv/devoverlay_test.go` pins the
contract every piece below depends on.

#### 12.1 Dev image (`Dockerfile.dev`)

One shared toolchain image used by every testenv service in dev mode:

- Base `golang:1.24-alpine` (pinned to the devshard `go.mod`).
- Installs `air` and `dlv` at image-build time.
- `WORKDIR /workspace/devshard/testenv` lines up with the air `root` so
  `air -c <relative.toml>` from the overlay resolves cleanly.
- Pre-warms the module cache via `COPY devshard/go.mod devshard/go.sum
  /workspace/devshard/` so the first bind-mounted rebuild inside the
  container doesn't also pay the `go mod download` tax.
- `ENTRYPOINT ["air"]` — the overlay passes `-c <air-config>` as
  `command:` per service.

Build context is **repo root** (`../..` from `devshard/testenv/`) because
the Go module lives at `devshard/go.mod`. `docker-compose.dev.yml` pins
`context: ../..` explicitly on every service.

#### 12.2 `air` configs

Seven files under `devshard/testenv/.air.*.toml`, one plain + one `.debug`
per service that may be debugged. Shared invariants (pinned by
`TestAirConfigs_StaticContract`):

- `root = "/workspace/devshard"` — air watches the Go module, not the
  entire repo, so edits under `decentralized-api/` don't churn devshardd.
- `tmp_dir = "/tmp/air/<svc>"` — per-service tmp keeps concurrent
  rebuilds from stomping each other.
- `include_ext = ["go", "yaml", "toml", "proto"]` — losing YAML from
  this list silently hid config edits in subnet-testenv.
- `exclude_dir` contains at least `docs`, `testenv/docs`,
  `testenv/observability`, `testenv/db`, `.git` — rebuild storms when
  logs or tsdb files are written were the #1 churn source.
- `delay = 500`, `kill_delay = "1s"`, `stop_on_error = true` — match
  the subnet-testenv tuning; the last flag forbids running a stale
  binary when the rebuild fails, which used to hide compile errors
  behind "everything seems fine" logs.

Debug variants additionally:

- Build with `-gcflags 'all=-N -l'` (no inlining, no optimisations) so
  `dlv` hits line-accurate breakpoints.
- Replace `bin` with
  `dlv exec <binary> --listen=:<port> --headless --api-version=2 --accept-multiclient --continue --`.
  `--accept-multiclient` is non-negotiable: the IDE re-attaches after
  every air rebuild, and without it `dlv` terminates on disconnect and
  the next rebuild never exposes a listener again.
- `devshardd.debug.toml` parametrises the port via `${DLV_PORT:-2347}`
  so operators opting extra hosts into debug mode just set
  `DLV_PORT=2348, 2349, …` without editing the air config.

| File                             | Service                       | Debugger |
|----------------------------------|-------------------------------|----------|
| `.air.mock-chain.toml`           | `mock-chain`                  | no       |
| `.air.mock-chain.debug.toml`     | `mock-chain` in debug mode    | yes      |
| `.air.height-sync.toml`          | `height-sync`                 | no       |
| `.air.height-sync.debug.toml`    | `height-sync` in debug mode   | yes      |
| `.air.devshardd.toml`            | `devshardd-testenv-{1..N-1}`  | no       |
| `.air.devshardd.debug.toml`      | `devshardd-testenv-0`         | yes      |
| `.air.devshardctl.toml`          | `devshardctl`                 | no       |

#### 12.3 `docker-compose.dev.yml` overlay

Overlay, not replacement. Used via `make dev-up` which expands to
`docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d`.

Per service the overlay:

- Swaps `build.dockerfile` to `devshard/testenv/Dockerfile.dev` with
  `context: ../..`; tags the image as `devshard-dev:latest` so every
  service shares one `go mod download` layer.
- Sets `working_dir: /workspace/devshard/testenv` and
  `command: ["-c", "/workspace/devshard/testenv/.air.<svc>.toml"]`.
- Bind-mounts `../..:/workspace` and attaches named volumes
  `gomodcache:/go/pkg/mod` + `gobuildcache:/root/.cache/go-build`.
  `dev-down` preserves the caches; `dev-clean` drops them.
- For debug-enabled services:
  - `cap_add: [SYS_PTRACE]`, `security_opt: [seccomp:unconfined]` —
    Docker Desktop drops `SYS_PTRACE` by default, and `dlv` refuses to
    attach without it.
  - Publishes the dlv listener on the host at the matching port.
  - For `devshardd-testenv-0` also injects `DLV_PORT=2347` so the
    parametrised `.air.devshardd.debug.toml` stays generic.

The overlay statically enumerates the default 4 devshardd replicas
gencompose emits. Scaling hosts via `config.yaml` requires either
duplicating a devshardd block in the overlay or running the extra
hosts without live-reload; documented in `DEVELOPMENT-MODE.md`.

Debug port map (pinned by `TestDockerComposeDev_DlvPortsMatchAirConfigs`):

| Host port | Container service       | Purpose                     |
|-----------|-------------------------|-----------------------------|
| `:2345`   | `mock-chain`            | dlv remote                  |
| `:2346`   | `height-sync`           | dlv remote                  |
| `:2347`   | `devshardd-testenv-0`   | dlv remote (first host)     |

#### 12.4 IDE wiring

`devshard/testenv/vscode-launch.json` ships six "remote attach" entries
— `mock-chain`, `height-sync`, and `devshardd-testenv-{0..3}` — each
pinned to `127.0.0.1` at the port matrix above, with
`remotePath = /workspace` and a
`substitutePath: ${workspaceFolder} → /workspace` rule so breakpoints in
the host-side source tree map to the container paths without path
juggling. Operators paste the file into `.vscode/launch.json` verbatim
(or merge `configurations` into an existing launch file).

`TestVSCodeLaunchJSON_MatchesDlvPorts` pins the (name, port) pairs to
the compose overlay, so a future port reshuffle can't silently break
the attach entries.

GoLand / IntelliJ: *Run → Edit Configurations → + → Go Remote* with
host `127.0.0.1`, port `2345`/`2346`/`2347`, and path mapping
`<repo-root>` → `/workspace`.

On every air rebuild the `dlv` process is SIGINT'd and relaunched on the
same port. The IDE loses its connection briefly; VS Code's Go extension
offers a one-click reconnect, GoLand's *Rerun debugger* achieves the
same. Documented front-and-centre in `DEVELOPMENT-MODE.md`.

#### 12.5 Makefile targets for dev mode

All dev loop targets pass
`-f docker-compose.yml -f docker-compose.dev.yml` so the overlay is
always stacked on top of the gencompose output:

```makefile
make dev-build        # docker compose ... build           (shared dev image)
make dev-up           # docker compose ... up -d           (start overlay)
make dev-down         # docker compose ... down            (keep caches)
make dev-clean        # docker compose ... down -v         (drop caches)
make dev-logs         # docker compose ... logs -f         (follow every service)
make dev-logs-<k>     # docker compose ... logs -f devshardd-testenv-<k>
make dev-restart-<svc># docker compose ... restart <svc>   (kick one service)
```

`dev-logs-<k>` clamps `k` to `0..DEVSHARDD_HOSTS-1` so mis-typed
indices fail fast instead of silently tailing nothing.

#### 12.6 macOS / Docker Desktop caveats

- `SYS_PTRACE` and `seccomp:unconfined` work on Docker Desktop's Linux
  VM with no host-side config.
- On M-series Macs, `FROM golang:1.24-alpine` resolves to `linux/arm64`
  automatically so compile + run happen under the same architecture
  inside the VM. `Dockerfile.dev` intentionally does not pin
  `--platform` — Docker Desktop picks the right one and operators
  switching between arm64 and amd64 laptops don't have to edit the
  overlay.
- First-run rebuild populates both the module and build caches inside
  the named volumes; budget ~30 s for the first `devshardd-testenv`
  rebuild. Subsequent edits are sub-second.

### Phase 13 — observability stack (landed)

Ported from `subnet-testenv`'s three-phase design (metrics first, logs second,
traces later) and generalized to devshard service names.

#### 13.1 Layout

Manually maintained; **not** emitted by `gencompose`. Lives under
`devshard/testenv/observability/` and is appended to `docker-compose.yml` in
a clearly marked section so regeneration by `gencompose` touches only the
service region above it.

```text
observability/
├── alloy/config.alloy                    # pipeline definition (River syntax)
├── loki/config.yaml                       # single-node loki config
└── grafana/
    ├── provisioning/
    │   ├── datasources/datasources.yaml  # VM + Loki
    │   └── dashboards/dashboards.yaml    # auto-provisioning loader
    └── dashboards/
        ├── devshard-overview.json         # per-host height, gossip, inferences
        ├── cadvisor-containers.json       # resource usage per container
        └── node-exporter-full.json        # host/VM metrics
```

#### 13.2 Services and IP allocations

Reserve `172.30.0.100–119` on the testenv docker network for observability.

| Component           | Image                             | Container IP      | Host port | Role                                    |
|---------------------|-----------------------------------|-------------------|-----------|-----------------------------------------|
| `victoria-metrics`  | `victoriametrics/victoria-metrics`| `172.30.0.100`    | `8428`    | time-series storage, MetricsQL query UI |
| `alloy`             | `grafana/alloy`                   | `172.30.0.101`    | `12345`   | OTel collector, pipeline UI             |
| `cadvisor`          | `gcr.io/cadvisor/cadvisor`        | `172.30.0.102`    | `18080`   | per-container metrics exposition        |
| `node-exporter`     | `prom/node-exporter`              | `172.30.0.103`    | `9100`    | host/VM metrics exposition              |
| `loki`              | `grafana/loki`                    | `172.30.0.104`    | `3100`    | log aggregation backend                 |
| `grafana`           | `grafana/grafana`                 | `172.30.0.105`    | `3000`    | dashboards (metrics + logs)             |
| *(reserved)*        | Tempo                             | `172.30.0.106`    | —         | traces, future phase                    |

Phase rollout:

1. **Metrics (initial port)** — VictoriaMetrics + Alloy + cAdvisor + Node
   Exporter + Grafana. Grafana is provisioned with VM as a datasource and
   two dashboards: `cadvisor-containers` and `node-exporter-full`.
2. **Logs** — add Loki; add `loki.source.docker` to `config.alloy` so all
   container stdout/stderr flows into Loki with labels
   `{service, container, image}`. Grafana gets Loki as a second datasource;
   `devshard-overview.json` grows a panel correlating CPU usage with gossip
   log volume per host.
3. **Traces (future)** — reserve IP `172.30.0.106` for Tempo; no code in this
   port. OTel instrumentation will be added inside devshardd in a separate
   PR.

#### 13.3 Alloy pipeline (`observability/alloy/config.alloy`)

Phase 1 (metrics only):

```alloy
prometheus.scrape "cadvisor" {
  targets    = [{ "__address__" = "cadvisor:8080" }]
  scrape_interval = "15s"
  forward_to = [prometheus.remote_write.vm.receiver]
}

prometheus.scrape "node_exporter" {
  targets    = [{ "__address__" = "node-exporter:9100" }]
  scrape_interval = "15s"
  forward_to = [prometheus.remote_write.vm.receiver]
}

prometheus.scrape "alloy_self" {
  targets    = [{ "__address__" = "localhost:12345" }]
  scrape_interval = "30s"
  forward_to = [prometheus.remote_write.vm.receiver]
}

prometheus.remote_write "vm" {
  endpoint { url = "http://victoria-metrics:8428/api/v1/write" }
}
```

Phase 2 adds a `loki.source.docker` component, a `loki.write` sink, and a
relabel rule that attaches `{service, container}` to every line:

```alloy
discovery.docker "all" {
  host = "unix:///var/run/docker.sock"
}

loki.source.docker "containers" {
  host           = "unix:///var/run/docker.sock"
  targets        = discovery.docker.all.targets
  forward_to     = [loki.write.loki.receiver]
  relabel_rules  = loki.relabel.docker.rules
}

loki.relabel "docker" {
  rule {
    source_labels = ["__meta_docker_container_name"]
    target_label  = "container"
  }
  rule {
    source_labels = ["__meta_docker_container_label_com_docker_compose_service"]
    target_label  = "service"
  }
  forward_to = []
}

loki.write "loki" {
  endpoint { url = "http://loki:3100/loki/api/v1/push" }
}
```

#### 13.4 Grafana provisioning

- `provisioning/datasources/datasources.yaml` declares:
  - `VictoriaMetrics` of type `prometheus`, URL `http://victoria-metrics:8428`.
  - `Loki` of type `loki`, URL `http://loki:3100`. (Phase 2; present from the
    start and resolves when Loki starts.)
- `provisioning/dashboards/dashboards.yaml` points at
  `/var/lib/grafana/dashboards` so any JSON file dropped there is
  auto-loaded. Grafana watches the directory.
- `devshard-overview.json` is the protocol-aware dashboard:
  - Row *Chain*: height from each host (label by `container`), lag between
    `max(H_i)` and `min(H_i)`, block-hash mismatches counter.
  - Row *Gossip*: inbound/outbound messages per host (requires devshardd to
    export `/metrics`; add a Prometheus handler in devshardd-testenv — see
    §13.6).
  - Row *Inferences*: rate of `MsgStartInference` vs `MsgConfirmStart`;
    cPoC-skip counter.
  - Row *Resource*: CPU and memory per host (via cAdvisor labels).
- `cadvisor-containers.json` and `node-exporter-full.json` are imported
  verbatim from their canonical upstream JSONs, pinned by SHA in
  `observability/README.md` so they can be refreshed deliberately.

#### 13.5 macOS / Docker Desktop caveats (documented in `OBSERVABILITY.md`)

- `cadvisor` works correctly on all platforms — it reads cgroup data via the
  Docker daemon socket, which is always a Linux kernel.
- `node-exporter` reports metrics for the Docker Desktop Linux VM, not for
  the macOS host. This is clearly flagged in `OBSERVABILITY.md`. Operators
  on macOS can skip it by creating a local `docker-compose.override.yml`
  that assigns `profiles: ["linux-only"]` to `node-exporter`.
- VictoriaMetrics has no auth by default. Acceptable here because testenv
  binds ports on `localhost` only and the write endpoint is reachable only
  from within the `testenv` docker network. Production would add `vmauth`;
  call this out in the doc.

#### 13.6 devshardd metrics export

To make protocol-aware dashboards possible:

- Add a `/metrics` Prometheus handler to `devshardd-testenv` (guarded by an
  `EXPORT_METRICS=1` env var, off by default in prod).
- Export counters/gauges used by `devshard-overview.json`:
  `devshardd_gossip_messages_total{direction,kind}`,
  `devshardd_diff_nonce`, `devshardd_pending_verdicts`,
  `devshardd_height_at_latest_nonce`, `devshardd_cpoc_skips_total{path,verdict}`.
- Alloy gets one more `prometheus.scrape` block that targets all
  devshardd-testenv services; `discovery.docker` on the `com.docker.compose.service`
  label automates the target list.

This scope is intentionally small and does not require instrumenting the
production `devshardd` binary. The handler is provided by a shared package
so it can be ported into prod dapi later without churn.

#### 13.7 Makefile targets for observability

Observability services come up with the main stack (merged into
`docker-compose.yml` by `gencompose` when the fragment exists). The Phase 14
`Makefile` in `devshard/testenv` defines (see `make help`):

```makefile
make obs-up              # same as `make up` (full base stack, incl. obs when present)
make obs-logs            # victoria-metrics, alloy, grafana, loki
make obs-grafana-open    # open http://localhost:3000 (or print URL) — macOS: `open`
make obs-query-open      # open VMUI at http://127.0.0.1:8428/vmui
make obs-reset           # --force-recreate selected obs services; volume wipe: OBSERVABILITY.md
make obs-down            # stop obs containers only; app stack may keep running
```

### Phase 14 — Makefile and documentation (landed)

- Full target set in `devshard/testenv/Makefile`: `proto`, `gen-compose`,
  `build`, `test`, `up`, `down`, `logs`, `ctl`, `clean`, `dev-build`,
  `dev-up`, `dev-down`, `dev-clean`, `dev-logs`, `dev-logs-<k>`,
  `dev-restart-<svc>`, `obs-up`, `obs-up-linux`, `obs-down`, `obs-logs`,
  `obs-grafana-open`, `obs-query-open`, `obs-reset`, `integration-test`
  (full `devshard` module `go test -race` with a 15-minute cap; can run for
  several minutes without being stuck; see Phase 15 for docker-compose
  integration / `ci-integration`).
- `README.md`: quick start, env-var table per service, architecture diagram,
  Makefile target list, dev-mode and observability callouts.
- `DEVELOPMENT-MODE.md`: hot-reload + debugger workflow per §12.
- `OBSERVABILITY.md`: per §13, including the macOS caveats and subnet alignment.

### Phase 15 — CI wiring (landed)

- **Local / PR targets** (from `devshard/`, see `Makefile` §8.8):
  - `make ci-dep-check` — §8.4 rules 1–6 (incl. BlockOracle golden + dapi
    `blockoracle_compile_check` `go build`).
  - `make ci-unit` — `go test -race` over the `devshard` module (§8.1).
  - `make ci-integration` — `testenv/citest` with `-tags=testenvci`: compose
    `config` + full-stack **I1** + **I2** + **I9** + **§8.7** (see §8.2). Full
    `testenv/docker-compose.yml` up. `TESTENV_SKIP_DOCKER_STACK=1` skips the slow
    stack; then only the compose `config` check (and non-stack tests) run.
  - `make ci-scenarios` — `go test ./testenv/scenarios/...` (config sanity today;
    C1…C14 protocol cases are follow-up). Nightly / optional on PR.
- **CI**: GitHub Actions `.github/workflows/devshard.yml` (paths `devshard/**`,
  `decentralized-api/**`) runs the makes above. **§8.2** automated in `citest`:
  **I1**, **I2** (VM `devshardd_height_at_latest_nonce` spread), **I9** (20 fresh
  `/block/latest` headers vs `config.yaml` verifier), plus **§8.7** in the same
  stack. **I3–I8, I10** and full **§8.3** protocol cases still manual / future
  harness. Node-exporter on the host is **:19100** (see
  `observability/compose-fragment.yaml`) to avoid clashing with height-sync **:9100**.

---

## 7. Production reuse plan (follow-up, not blocking)

After this port lands, a separate PR wires the same `blockoracle` package
into real `decentralized-api`:

1. Instantiate `observer.NewTendermint(rpcClient, validatorSetProvider)`
   inside dapi startup.
2. Mount `server/http.Mount(e.Group("/"), obs)` on dapi's public HTTP
   router.
3. Pass `client.NewInProcess(obs)` into dapi's internal callers that
   currently do ad-hoc height queries.
4. Optionally publish the validator pubkey via a dapi config endpoint so
   devshard hosts can pin it automatically.

This PR is out of scope here but its surface is guaranteed by the
reusability tests in §8.4.

---

## 8. Test plan

### 8.1 Unit tests

| Target                                       | Coverage                                                                                          |
|----------------------------------------------|---------------------------------------------------------------------------------------------------|
| `blockoracle/observer/mock`                  | monotonic height, correct block cadence, same seed → same bytes, subscribers fan-out, multi-validator quorum always > 3/4, deterministic drop pattern for identical (seed, height), heavy-power validator never dropped when its absence would breach the floor, drop set rotates across all validators over long runs |
| `blockoracle/verifier`                       | accepts valid commits; rejects tampered `BlockHash`, `AppHash`, `ValidatorsHash`; rejects tampered single signature inside a 10-sig commit; rejects foreign signature appended to an otherwise-valid 10-sig commit; rejects duplicate signatures; rejects < 2/3 voting power; rejects stale height |
| `blockoracle/server/http`                    | round-trips `Header` byte-identical; SSE ordering; `/block/{h}/prove` returns stable proofs       |
| `blockoracle/client/http`                    | cache coherence; re-subscribe on disconnect; verify-on-ingest rejects tampered headers when a Verifier is pinned; host trust mode (nil Verifier) forwards full `Commit.Signatures` untouched; stale-detection flag after quiet period |
| `blockoracle/standalone`                     | constructor rejects empty/malformed validator lists; end-to-end stream verifies against a 10-validator pinned set; host trust mode receives full multi-sig commits and records zero rejections |
| `testenv/config`                             | `HeightSyncValidators()` parses every entry, rejects TODO placeholders, catches duplicates, applies `power=1` default |
| `testenv/cmd/heightsyncd` (validator wiring) | `buildStandaloneConfig` maps every `height_sync.validators[]` entry to a `standalone.Validator` with matching 20-byte address, power (including the `1` default), and signer; `ChainID`, `InitialHeight`, `Seed`, `BlockInterval`, and `Addr` all round-trip from YAML; empty / TODO / duplicate entries surface as composite errors (no partial boot); shipped skeleton parses once TODO placeholders are filled in; end-to-end run of the standalone server signs blocks whose `Commit.Signatures[].ValidatorAddress` are all configured, signer power is strictly > 3/4 of the configured total, and an external `verifier.Verifier` pinned to the same yaml accepts the live header |
| `host.Host` (BlockOracle seam)               | `LatestHeight` returns `ErrNoBlockOracle` when unwired; returns oracle height when wired; reflects live oracle updates (no caching); propagates oracle errors unchanged; `WithBlockOracle(nil)` is a no-op; honors context cancellation; `BlockOracle()` exposes the injected instance verbatim |
| `testenv/mockdapi`                           | `New` rejects empty `HeightSyncURL` and nil ctx; builds an Oracle that returns live headers against an `httptest`-backed `observer` + `server` stack and forwards full `Commit.Signatures` (trust mode); Oracle reflects live producer advances over SSE without additional round trips; `Close` is idempotent and nil-safe; `NoopNodeManager.AcquireMLNode` returns distinct monotonically-increasing lock ids, fixed endpoint + node id, and never returns `ResourceExhausted` across 1024 calls; `ReleaseMLNode` succeeds for every `ReleaseOutcome` value; both RPCs honor context cancellation; compile-time + runtime assertion that `NoopNodeManager` satisfies `gen.NodeManagerClient` |
| `testenv/bridge.GRPCBridge`                  | query methods map proto fields 1:1 and return independent byte-slice copies; `codes.NotFound` → `ErrEscrowNotFound` / `ErrParticipantNotFound`; non-NotFound errors are wrapped, not reinterpreted; `VerifyWarmKey` returns true for listed addresses, false for non-grantees and NotFound, and memoizes per (warm, validator) pair; all four notification / dispute stubs return `ErrNotImplemented`; compile-time + runtime assertion that the type satisfies `MainnetBridge`; constructor rejects empty target and nil context |
| `testenv/engine.MockInferenceEngine`         | `NewMockInference` fills zero fields from the pinned 80/40 defaults and keeps explicit overrides; `ResponseHash == sha256(ResponseBody)`; same `(InferenceID, EscrowID, Model)` triple → identical bytes + hash; changing any triple field produces a distinct hash; honors `Latency` (wall-clock floor) and short-circuits promptly on ctx cancel during the sleep; SSE streaming writes `data: <body>\n\n` + `data: [DONE]\n\n` when `ResponseWriter` is set, no-op when unset; `NewMockInferenceFromEnv` reads `TESTENV_INFERENCE_LATENCY_MS` / `_INPUT_TOKENS` / `_OUTPUT_TOKENS` and falls back to defaults on malformed values; 32-way concurrent `Execute` is race-free |
| `testenv/engine.MockValidationEngine`        | `DefaultValid=true` and `=false` are both respected verbatim; `SetVerdict` / `ClearVerdict` flip a single inference without touching others; `SetError` precedes verdict overrides and returns `(nil, err)`; `SetError(_, nil)` clears the override; `Reset` wipes both maps; honors `Latency` and ctx-cancels during the sleep; `NewMockValidationFromEnv` parses `TESTENV_VALIDATION_VERDICT` (`valid`/`invalid`) and `_LATENCY_MS`, falling back on malformed values; 64-way concurrent `Validate` + `SetVerdict` is race-free |
| `testenv/cmd/devshardd-testenv` (wiring)     | `loadEnvConfig` consumes every required env var, applies defaults for `HTTP_PORT` (9500) and `DATA_DIR` (`/data`), lists every missing required var in one composite error, and rejects malformed `HTTP_PORT`; `primarySlotID` returns the first slot owned by the address (any order) and falls back to 0 when absent; `buildPeerClients` dedupes multi-slot hosts, excludes our own address, surfaces `GetHostInfo` errors verbatim, and returns an empty slice (no error) for a 1-host escrow; `envOr` precedence pinned |
| `cmd/devshardctl` (testenv wiring)           | `resolveConfig` honours flag→`DEVSHARD_*`→`TESTENV_*`/`ESCROW_ID` precedence for private key and escrow id; missing-key / missing-escrow errors name every source; `--mock-chain` + `MOCK_CHAIN_URL` swap in the testenv gRPC bridge and override `--chain-rest`; `--host` + `DEVSHARDD_URL` pin every `GetHostInfo` to one URL via `pinnedHostBridge` while the queried address is returned verbatim (signature auth preserved); every other `MainnetBridge` method forwards unchanged; `buildBridge` applies the pin only when set; `describeBridge` / `describeHostPin` produce the pinned startup-log tags; default model, port, and on-disk storage-path layout are pinned |
| `testenv/cmd/gencompose`                     | `isPlaceholderKey` only overwrites empty / TODO / CHANGEME strings (no false positives on real hex); `generateHosts` fills missing key+address+id+port, preserves caller-supplied ids, derives addresses from pre-set keys, and surfaces malformed keys instead of regenerating; `generateUser` generates or preserves as above; `generateValidators` fills placeholder/empty entries, defaults `Power=1`, and never changes the declared count; `assignSlots` is round-robin, clears prior assignments, and handles the zero-host edge without panicking; `fillNetworkDefaults` stamps `172.30.0.(10+i)` + service-URL defaults and leaves overrides alone; `defaultConfig` + `fillConfig` Validate-cleanly and inherit `escrow.creator_address` from `user.address`; `writeCompose` output contains every expected service, env var, and pinned IP; parses as YAML; saved `config.yaml` reloads with no TODOs remaining; observability fragment is spliced in with a single top-level `services:` key via `stripServicesKey`; `TestFillConfig_Idempotent` pins no-churn across re-runs; `firstSlot` / `slotList` helper edge cases |
| `decentralized-api/internal/devshard.NewSignerFromKeyring` | given fixture armored key, derives expected bech32 (pins the prod keyring path that testenv skips) |
| `testenv/dockerfiles_test.go` (Phase 11)     | every `Dockerfile.<svc>`'s `go build` target resolves to a real package under `devshard/` (the gencompose build context); runtime stage is `gcr.io/distroless/static-debian12` and never `alpine:`; `CGO_ENABLED=0` is preserved so distroless stays viable; `ENTRYPOINT` matches `/usr/local/bin/<binary>`; `Dockerfile.devshardd-testenv` pre-creates `/data` and exports `DATA_DIR=/data` so SQLite boots without a shell-driven `mkdir`; `Dockerfile.dev` keeps `golang:alpine` + `air` + `dlv` + `ENTRYPOINT ["air"]` so Phase 12 live-reload survives Phase 11 edits |
| `testenv/devoverlay_test.go` (Phase 12)      | every `.air.<svc>.toml` points at a real Go package under `devshard/` (no "package not found" rebuild storms); `root = "/workspace/devshard"`, `delay = 500`, `kill_delay = "1s"`, `stop_on_error = true`, and the `{go, yaml, toml, proto}` include list are all pinned so YAML/proto edits don't get silently ignored; debug variants build with `-gcflags 'all=-N -l'` and wrap the binary in `dlv exec --accept-multiclient --listen=:<port>` with the matching port; `docker-compose.dev.yml` gives every service `context: ../..`, `dockerfile: devshard/testenv/Dockerfile.dev`, `image: devshard-dev:latest`, `working_dir: /workspace/devshard/testenv`, an explicit `/workspace/devshard/testenv/.air.*.toml` command, and the repo bind-mount + named `gomodcache`/`gobuildcache` volumes; debug services (`mock-chain`, `height-sync`, `devshardd-testenv-0`) publish their dlv port, carry `cap_add [SYS_PTRACE]` + `security_opt [seccomp:unconfined]`, and — for devshardd-testenv-0 — inject `DLV_PORT=2347` consumed by the parametrised `.air.devshardd.debug.toml`; `vscode-launch.json` pins the (name, port) pairs to the same matrix so a silent port rename breaks CI, not the IDE |

Runtime budget: ≤ 60 s aggregate.

#### 8.1.1 Phase 3 acceptance tests (multi-validator block oracle)

Named cases that pin the Phase 3 behavior end-to-end. All are unit tests
inside `devshard/`, run under `make ci-unit`, and included in the
aggregate budget above. Integration coverage for the same concerns lives
in §8.2 rows I9 and I10.

| Test                                                             | Package                  | Asserts                                                                                                                                                                             |
|------------------------------------------------------------------|--------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `TestMockObserver_MultiValidator_QuorumFloor`                    | `blockoracle/observer`   | Over ≥ 200 blocks from a 10-equal-power mock, every commit retains voting power strictly > 3/4 of the total; both full-sign and partial-sign blocks are observed.                   |
| `TestMockObserver_PowerWeighted_HeavyAlwaysSigns`                | `blockoracle/observer`   | With a non-uniform set `[5,1,1,1,1,1]`, the heavy validator is present in every block; pinned `Verifier` accepts the whole stream.                                                  |
| `TestMockObserver_SignerRotation`                                | `blockoracle/observer`   | Over 500 blocks with 10 equal-power validators, every validator is dropped from at least one block — the drop algorithm is not stuck on a fixed subset.                             |
| `TestMockObserver_DeterministicForSameSeed`                      | `blockoracle/observer`   | Same `(seed, validators)` ⇒ byte-identical headers across restarts, including `Commit.Signatures` order.                                                                            |
| `TestMockObserver_SingleValidator_FullSign`                      | `blockoracle/observer`   | Degenerate cases (N ≤ 3) sign with the full set — drop budget collapses to zero.                                                                                                    |
| `TestMockObserver_RejectsBadConfig`                              | `blockoracle/observer`   | Empty validator list, malformed addresses, duplicates, zero power are all rejected by the constructor.                                                                              |
| `TestVerifier_RejectsTamperedSignatureInMultiSig`                | `blockoracle/verifier`   | In a valid 10-sig commit, flipping one byte of one signature causes the verifier to reject — the per-signature ecrecover path is exercised inside the multi-signer setting.         |
| `TestVerifier_RejectsForeignSignatureInMultiSig`                 | `blockoracle/verifier`   | Appending a valid signature from a signer outside the pinned set to an otherwise-correct 10-sig commit causes rejection with "not in pinned set", regardless of aggregate power.    |
| `TestVerifier_RejectsInsufficientVotingPower`                    | `blockoracle/verifier`   | Only 1 of 3 equal validators signs; verifier rejects with "insufficient voting power" (covers manually crafted < 2/3 headers the mock never emits).                                 |
| `TestVerifier_RejectsDuplicateSignatures`                        | `blockoracle/verifier`   | Two sigs from the same validator in one commit are rejected — prevents power-inflation via sig duplication.                                                                         |
| `TestService_LatestAfterAdvance` / `TestService_StreamDelivers*` | `blockoracle/standalone` | End-to-end stream against a 10-validator pinned `Verifier` accepts every header; each commit carries ≥ 8 signatures (the > 3/4 floor).                                              |
| `TestService_HostTrustMode`                                      | `blockoracle/standalone` | Client with `Verifier: nil` forwards the full `Commit.Signatures` set untouched and records zero rejections — host trust mode preserves proof material for later auditing.          |
| `TestClient_RejectsTamperedHeader`                               | `blockoracle/client`     | With a pinned verifier, a header with tampered `AppHash` is rejected on ingest and dropped from the cache.                                                                          |
| `TestClient_TrustModeAcceptsEverything`                          | `blockoracle/client`     | With `Verifier: nil`, even a tampered header is forwarded to subscribers with all signatures intact — hosts trust the authenticated oracle and defer cryptographic checks.          |
| `TestConfig_HeightSyncValidators*`                               | `testenv/config`         | YAML `height_sync.validators` list parses, applies `power: 1` default, rejects empty lists, TODO placeholders, and duplicate-address entries.                                       |

### 8.2 Integration tests (docker compose)

Each is a Go test that invokes `docker compose`, waits for health, runs the
assertion, and tears down.

| #   | Scenario                                                                | Assertion                                                                                                                                                              |
|-----|-------------------------------------------------------------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| I1  | Bootstrap                                                               | All containers healthy ≤ 30 s; `GET height-sync:9100/block/latest` returns `height > 0`.                                                                               |
| I2  | Height convergence                                                      | Query each devshardd's diagnostic endpoint; `max(H_i) − min(H_i) ≤ 1` in steady state.                                                                                 |
| I3  | Hostile header rejection                                                | A test double replaces height-sync and emits a header with tampered `AppHash` but valid old signature; every `mock-dapi` rejects it; devshardd continues on cache.     |
| I4  | Height-sync outage                                                      | Stop `height-sync` for 2×`BLOCK_INTERVAL`; `mock-dapi.Latest()` returns cached header with `stale=true`; verdicts requiring fresh `H(V)` enter `pending_verdicts{stale}`. Restart; full reconvergence ≤ 2×`BLOCK_INTERVAL`. |
| I5  | Inference happy path                                                    | `devshardctl chat ...` via `devshardd-0`; response returned; `Diff` on all hosts contains `MsgStartInference @ R_req` and `MsgConfirmStart @ R_req+1`.                 |
| I6  | Gossip consistency                                                      | After 50 inferences, diffs on all N devshards hash-match at every nonce.                                                                                               |
| I7  | Settlement                                                              | `devshardctl settle`; `mock-chain`'s `SettleEscrow` accepts the payload; log shows `VerifySettlement = true`.                                                          |
| I8  | Host crash and recovery                                                 | `docker stop devshardd-2` mid-session; protocol continues; restart; host replays `Diff` from storage and reconciles height; no corruption.                             |
| I9  | Multi-validator stream vs. auditor                                      | `devshardctl` pins the 10-validator set and subscribes to `height-sync` for 20 consecutive headers; every header verifies; at least one commit carries < 10 but ≥ 8 signatures (exercises the partial-quorum drop path end to end). |
| I10 | Foreign-signature injection                                             | A test double in front of `height-sync` appends an 11th signature from a non-pinned key; `devshardctl` rejects with `not in pinned set` on the first poisoned header; hosts in trust mode keep ingesting (cache records full `Commit.Signatures`) and their downstream proofs still fail external verification. |

Runtime budget: ≤ 5 min. Runs on PR. **Status:** I1, I2, and I9 are implemented
in `devshard/testenv/citest` (`-tags=testenvci`, same docker-compose run as
§8.7). I3–I8 and I10 are not yet automated.

### 8.3 cPoC protocol scenario tests

`make ci-scenarios` runs `go test` under `devshard/testenv/scenarios/`
(today: config load sanity). Full cases C1…C14 are scripted from this tree as
the harness lands. Each test poses as a buggy developer or host and asserts the
expected verdict.

| Case   | Setup                                                                                                                   | Expected                                                                                     |
|--------|-------------------------------------------------------------------------------------------------------------------------|----------------------------------------------------------------------------------------------|
| C1     | Host schedules cPoC at `H`; developer sends `MsgStartInference`; host returns `CPoCSkipResponse`; developer carries it. | `Diff` accepts; verdict `Valid`; no vote emitted.                                            |
| C1'    | Developer probes; host signals `ready`; developer sends inference.                                                      | `Diff` shows probe + inference; verdict `Valid`.                                             |
| C2     | Injected host returns `CPoCSkipResponse` where `Schedule(H_i, H) = false`.                                              | Every verifier emits `CPoCVote{Invalid, target: H_i}`; developer collects quorum.            |
| C2'    | Host signs both `MsgConfirmStart` and contributes to `CarrySkip` for same `R_req`.                                      | Step-2 mutual-exclusion check fires; `Invalid` vote against host.                            |
| C3     | Developer delays `CarrySkip` but `H` remains within `[h_X, h_carry]`.                                                   | `Valid`.                                                                                     |
| C3'    | `CarrySkip` references probe with contradicting stamps.                                                                 | `Invalid`; D-target vote recorded and optimistic-gap marker logged.                          |
| C13    | Host signals `ready`; developer refuses to route inference.                                                             | Next executor emits `RouteFairnessRefusal`; `withholding_alert` set.                         |
| C14    | Quiet session; developer honors heartbeat cadence.                                                                      | Bounds stay tight; no violation.                                                             |
| C14-fail | Quiet session; developer skips heartbeat.                                                                             | Future executor detects bound drift; `Invalid` against developer.                            |

Runtime budget: ≤ 15 min. Runs nightly, not on PR.

### 8.4 Reusability sanity checks (CI, every PR)

Enforce the dependency rules in §3.6:

1. `go list -deps devshard/blockoracle/... | rg 'devshard/testenv'` MUST be empty.
2. `go list -deps devshard/blockoracle/... | rg 'decentralized-api'` MUST be empty.
3. `go list -deps devshard/testenv/mockdapi | rg 'blockoracle/client'` MUST match.
4. `go list -deps devshard/testenv/mockdapi | rg 'decentralized-api'` MUST be empty.
5. Interface snapshot test: record `BlockOracle` method set in
   `devshard/blockoracle/testdata/blockoracle_interface_golden.txt`; any
   change to the method set requires an explicit golden update
   (`TestBlockOracleInterfaceGolden`), signaling an intentional
   prod-affecting API break.
6. Compile-only: leaf package
   `decentralized-api/internal/devshard/blockoraclecompile/` (build tag
   `blockoracle_compile_check`) references `observer.NewTendermint` only, so
   the check compiles the `devshard` oracle edge without pulling the rest of
   dapi. `make ci-dep-check` runs
   `go build -tags=blockoracle_compile_check -o /dev/null ./internal/devshard/blockoraclecompile/`
   from the `decentralized-api` module. Normal dapi builds omit the package.

Runtime budget: ≤ 30 s.

### 8.5 Dev-mode smoke (manual checklist)

Not CI-gated; run before merging any change to the dev overlay
(§12).

1. `make dev-build` succeeds cold (empty module cache) within a reasonable
   time window on the target machine.
2. `make dev-up` succeeds; all services report healthy.
3. Edit a file under `devshard/host/`; `air` rebuilds
   `devshardd-testenv-0` ≤ 5 s; every other `devshardd-testenv-k`
   rebuilds in parallel.
4. IDE attaches to `:2347` using `vscode-launch.json` and hits a breakpoint
   in `HandleInference`.
5. `air` reconnect after rebuild: save the same file again; IDE reconnects
   (auto or manually) and the next request hits the breakpoint again.
6. Edit a file under `devshard/blockoracle/observer/`; `height-sync`
   restarts; `devshardd-testenv-*` cache flush triggered by SSE reconnect;
   protocol keeps running.
7. `dev-down` shuts down cleanly with no dangling volumes.

### 8.6 Observability smoke (manual checklist)

Not CI-gated; run before merging any change under
`devshard/testenv/observability/` (§13).

1. `make up` brings up all observability containers healthy; `obs-logs`
   shows Alloy scraping without errors.
2. <http://localhost:8428/vmui> loads; query
   `container_memory_working_set_bytes{name=~"testenv-devshardd.*"}` returns
   non-empty series for every host.
3. <http://localhost:12345> (Alloy) shows a green pipeline graph with
   non-zero scrape counters for cAdvisor, Node Exporter, and devshardd
   targets.
4. <http://localhost:3000> (Grafana) auto-provisioned; `devshard-overview`
   dashboard renders; Chain row shows height from every host and a lag
   panel pinned near zero.
5. Logs: after Phase 2 lands, Grafana Explore on the Loki datasource
   returns log lines filtered by `{service="devshardd-testenv-0"}`.
6. macOS: with `docker-compose.override.yml` disabling `node-exporter` via
   profile, stack still comes up; `cadvisor` dashboards remain populated;
   only the "host-level" Node Exporter panels are empty.

### 8.7 Observability CI smoke (automated, minimal)

A short, non-brittle integration test that only verifies wiring (not
dashboard content). Runs as part of `make ci-integration`.

1. After stack bootstrap, assert
   `GET http://localhost:8428/api/v1/query?query=up{job!=""}` returns at
   least three non-zero series (alloy-self, cadvisor, node-exporter).
2. Assert
   `GET http://localhost:3000/api/health` returns `{database: "ok"}`.
3. After Phase 2: assert
   `GET http://localhost:3100/ready` returns `ready`.
4. Dashboards render: fetch
   `/api/dashboards/uid/devshard-overview` from Grafana and assert the
   returned JSON has at least the Chain, Gossip, and Resource rows.

Runtime budget for 8.7: ≤ 15 s inside a stack that is already up.

### 8.8 CI targets

- `make ci-unit` — runs §8.1. ≤ 60 s. Every PR.
- `make ci-integration` — runs §8.2 and §8.7. ≤ 5 min. Every PR.
- `make ci-dep-check` — runs §8.4. ≤ 30 s. Every PR.
- `make ci-scenarios` — runs §8.3. ≤ 15 min. Nightly.

---

## 9. Risks and mitigations

| Risk                                                                             | Mitigation                                                                                                                   |
|----------------------------------------------------------------------------------|------------------------------------------------------------------------------------------------------------------------------|
| `blockoracle` API churn breaks real dapi integration                             | Interface snapshot test (§8.4 item 5) forces explicit review on any change.                                                  |
| Mock-vs-tendermint observer drift                                                | Shared `Header` type + shared verifier; integration test I3 exercises the verifier from both producer shapes.                |
| `mock-dapi` accidentally imports real dapi and ties testenv to dapi build graph  | CI dep-check (§8.4 item 4).                                                                                                  |
| Hex-key signer diverges from keyring-derived signer                              | Unit test in §8.1 last row pins the keyring path; `signing.Secp256k1Signer` is the sole consumer in both paths.             |
| Scenario tests become slow and flaky                                             | Nightly-only; deterministic mock observer; fixed seeds for key generation.                                                   |
| Operators copy testenv configs into prod                                         | Explicit "testenv-only" callout in `README.md`; different binary names (`heightsyncd`, `devshardd-testenv`).                |
| `air` rebuilds race the dlv attach and leave breakpoints unset                   | `send_interrupt=true`, `kill_delay=1s` in `.air.*.toml`; IDE configs enable auto-reconnect; documented in `DEVELOPMENT-MODE.md`. |
| Observability dashboards silently drift from metric names exported by devshardd  | CI smoke test §8.7 item 4 fails if the `devshard-overview` dashboard loses its Chain/Gossip/Resource rows.                   |

---

## 10. Out of scope

- Wiring `blockoracle.NewTendermintObserver` into real `decentralized-api`
  startup. Tracked in a follow-up PR (§7).
- `versiond` integration inside testenv. Versioned supervision is
  production-only.
- Real Cosmos keyring integration in testenv. Covered by a unit test only.
- Authz warm-key flow end-to-end. Remains testermint territory.
- BLS DKG, PoC workers, NATS, model manager.

---

## 11. Glossary

- **`H(V)`**: mainnet block height as known to verifier `V`. Supplied by the
  block oracle.
- **Block oracle**: the reusable `devshard/blockoracle` package; the
  authenticated source of `H(V)` and associated chain state.
- **`mock-dapi`**: Go library linked into each `devshardd-testenv`; provides
  dapi-facing interfaces (`BlockOracle`, `NodeManager`) using the testenv's
  `height-sync` container as a backend.
- **`height-sync`**: testenv-only container running the reusable block-oracle
  binary. In production this role is filled by real `decentralized-api`
  in-process.
- **`mock-chain`**: testenv-only container serving mock cosmos/tendermint
  state queries (escrows, participants, grantees, settlement).
- **`devshardd-testenv`**: testenv-specific `devshardd` binary that uses a
  hex signer and stub engines, and links `mock-dapi` in-process. Production
  `devshardd` is not modified.
- **Dev mode**: overlay of `docker-compose.dev.yml` that replaces the prod
  images with `Dockerfile.dev` (Go toolchain + `air` + `dlv`), bind-mounts
  the repo at `/workspace`, and exposes dlv remote-debug ports for a
  selected subset of services. Governed by §12.
- **Observability plane**: VictoriaMetrics + Grafana Alloy + cAdvisor +
  Node Exporter + Loki + Grafana, reserved on IP range `172.30.0.100–119`.
  Provides per-container metrics, host/VM metrics, docker-log scraping
  (Phase 2), and provisioned Grafana dashboards. Governed by §13.
