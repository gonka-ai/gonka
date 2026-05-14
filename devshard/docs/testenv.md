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
| `devshardctl`      | container          | 1     | no (shared)   | default compose stack (operator HTTP proxy) |

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

These are enforced by CI (§7.4):

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

## 6. Production reuse plan (follow-up, not blocking)

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
reusability tests in §7.4.

---

## 7. Test plan

### 7.1 Unit tests

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
| `cmd/devshardctl` (testenv wiring)           | `resolveConfig` honours flag→`DEVSHARD_*`→`TESTENV_*`/`ESCROW_ID` precedence for private key and escrow id; missing-key / missing-escrow errors name every source; `--mock-chain` + `MOCK_CHAIN_URL` swap in the testenv gRPC bridge and override `--chain-rest`; `buildBridge` returns that bridge or REST `MainnetBridge` with no URL override — `GetHostInfo` URLs come from mock-chain / chain REST per validator; `describeBridge` tags the startup log; default model, port, and on-disk storage-path layout are pinned |
| `testenv/cmd/gencompose`                     | `isPlaceholderKey` only overwrites empty / TODO / CHANGEME strings (no false positives on real hex); `generateHosts` fills missing key+address+id+port, preserves caller-supplied ids, derives addresses from pre-set keys, and surfaces malformed keys instead of regenerating; `generateUser` generates or preserves as above; `generateValidators` fills placeholder/empty entries, defaults `Power=1`, and never changes the declared count; `assignSlots` is round-robin, clears prior assignments, and handles the zero-host edge without panicking; `fillNetworkDefaults` stamps `172.30.0.(10+i)` + service-URL defaults and leaves overrides alone; `defaultConfig` + `fillConfig` Validate-cleanly and inherit `escrow.creator_address` from `user.address`; `writeCompose` output contains every expected service, env var, and pinned IP; parses as YAML; saved `config.yaml` reloads with no TODOs remaining; observability fragment is spliced in with a single top-level `services:` key via `stripServicesKey`; `TestFillConfig_Idempotent` pins no-churn across re-runs; `firstSlot` / `slotList` helper edge cases |
| `decentralized-api/internal/devshard.NewSignerFromKeyring` | given fixture armored key, derives expected bech32 (pins the prod keyring path that testenv skips) |
| `testenv/dockerfiles_test.go` (Phase 11)     | every `Dockerfile.<svc>`'s `go build` target resolves to a real package under `devshard/` (the gencompose build context); runtime stage is `gcr.io/distroless/static-debian12` and never `alpine:`; `CGO_ENABLED=0` is preserved so distroless stays viable; `ENTRYPOINT` matches `/usr/local/bin/<binary>`; `Dockerfile.devshardd-testenv` pre-creates `/data` and exports `DATA_DIR=/data` so SQLite boots without a shell-driven `mkdir`; `Dockerfile.dev` keeps `golang:alpine` + `air` + `dlv` + `ENTRYPOINT ["air"]` so Phase 12 live-reload survives Phase 11 edits |
| `testenv/devoverlay_test.go` (Phase 12)      | every `.air.<svc>.toml` points at a real Go package under `devshard/` (no "package not found" rebuild storms); `root = "/workspace/devshard"`, `delay = 500`, `kill_delay = "1s"`, `stop_on_error = true`, and the `{go, yaml, toml, proto}` include list are all pinned so YAML/proto edits don't get silently ignored; debug variants build with `-gcflags 'all=-N -l'` and wrap the binary in `dlv exec --accept-multiclient --listen=:<port>` with the matching port; `docker-compose.dev.yml` gives every service `context: ../..`, `dockerfile: devshard/testenv/Dockerfile.dev`, `image: devshard-dev:latest`, `working_dir: /workspace/devshard/testenv`, an explicit `/workspace/devshard/testenv/.air.*.toml` command, and the repo bind-mount + named `gomodcache`/`gobuildcache` volumes; debug services (`mock-chain`, `height-sync`, `devshardd-testenv-0`) publish their dlv port, carry `cap_add [SYS_PTRACE]` + `security_opt [seccomp:unconfined]`, and — for devshardd-testenv-0 — inject `DLV_PORT=2347` consumed by the parametrised `.air.devshardd.debug.toml`; `vscode-launch.json` pins the (name, port) pairs to the same matrix so a silent port rename breaks CI, not the IDE |

Runtime budget: ≤ 60 s aggregate.

#### 7.1.1 Phase 3 acceptance tests (multi-validator block oracle)

Named cases that pin the Phase 3 behavior end-to-end. All are unit tests
inside `devshard/`, run under `make ci-unit`, and included in the
aggregate budget above. Integration coverage for the same concerns lives
in §7.2 rows I9 and I10.

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

### 7.2 Integration tests (docker compose)

Each is a Go test that invokes `docker compose`, waits for health, runs the
assertion, and tears down.

| #   | Scenario                                                                | Assertion                                                                                                                                                              |
|-----|-------------------------------------------------------------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| I1  | Bootstrap                                                               | All containers healthy ≤ 30 s; `GET height-sync:9100/block/latest` returns `height > 0`.                                                                               |
| I2a | Height convergence (protocol)                                           | One tight loop: GET each host's published `127.0.0.1:<public_metrics_port>/metrics`, parse `devshardd_height_at_latest_nonce`; log per host; `max(H_i)−min(H_i) ≤ 1`. |
| I2b | Height convergence (observability)                                      | VictoriaMetrics instant query on the same gauge (Alloy scrape path); `max(H_i)−min(H_i) ≤ 3` (scrape skew vs I2a).                                                      |
| I3  | Hostile header rejection                                                | A test double replaces height-sync and emits a header with tampered `AppHash` but valid old signature; every `mock-dapi` rejects it; devshardd continues on cache.     |
| I4  | Height-sync outage                                                      | Stop `height-sync` for 2×`BLOCK_INTERVAL`; `mock-dapi.Latest()` returns cached header with `stale=true`; verdicts requiring fresh `H(V)` enter `pending_verdicts{stale}`. Restart; full reconvergence ≤ 2×`BLOCK_INTERVAL`. |
| I5  | Inference happy path                                                    | `devshardctl chat ...` via `devshardd-0`; response returned; `Diff` on all hosts contains `MsgStartInference @ R_req` and `MsgConfirmStart @ R_req+1`.                 |
| I6  | Gossip consistency                                                      | After 50 inferences, diffs on all N devshards hash-match at every nonce.                                                                                               |
| I7  | Settlement                                                              | `devshardctl settle`; `mock-chain`'s `SettleEscrow` accepts the payload; log shows `VerifySettlement = true`.                                                          |
| I8  | Host crash and recovery                                                 | `docker stop devshardd-2` mid-session; protocol continues; restart; host replays `Diff` from storage and reconciles height; no corruption.                             |
| I9  | Multi-validator stream vs. auditor                                      | `devshardctl` pins the 10-validator set and subscribes to `height-sync` for 20 consecutive headers; every header verifies; at least one commit carries < 10 but ≥ 8 signatures (exercises the partial-quorum drop path end to end). |
| I10 | Foreign-signature injection                                             | A test double in front of `height-sync` appends an 11th signature from a non-pinned key; `devshardctl` rejects with `not in pinned set` on the first poisoned header; hosts in trust mode keep ingesting (cache records full `Commit.Signatures`) and their downstream proofs still fail external verification. |

Runtime budget: ≤ 5 min. Runs on PR. **Status:** I1, I2a, I2b, and I9 are implemented
in `devshard/testenv/citest` (`-tags=testenvci`, same docker-compose run as
§7.7). I3–I8 and I10 are not yet automated.

### 7.3 cPoC protocol scenario tests

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

### 7.4 Reusability sanity checks (CI, every PR)

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

### 7.5 Dev-mode smoke (manual checklist)

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

### 7.6 Observability smoke (manual checklist)

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

### 7.7 Observability CI smoke (automated, minimal)

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

Runtime budget for 7.7: ≤ 15 s inside a stack that is already up.

### 7.8 CI targets

- `make ci-unit` — runs §7.1. ≤ 60 s. Every PR.
- `make ci-integration` — runs §7.2 and §7.7. ≤ 5 min. Every PR.
- `make ci-dep-check` — runs §7.4. ≤ 30 s. Every PR.
- `make ci-scenarios` — runs §7.3. ≤ 15 min. Nightly.

---

## 8. Risks and mitigations

| Risk                                                                             | Mitigation                                                                                                                   |
|----------------------------------------------------------------------------------|------------------------------------------------------------------------------------------------------------------------------|
| `blockoracle` API churn breaks real dapi integration                             | Interface snapshot test (§7.4 item 5) forces explicit review on any change.                                                  |
| Mock-vs-tendermint observer drift                                                | Shared `Header` type + shared verifier; integration test I3 exercises the verifier from both producer shapes.                |
| `mock-dapi` accidentally imports real dapi and ties testenv to dapi build graph  | CI dep-check (§7.4 item 4).                                                                                                  |
| Hex-key signer diverges from keyring-derived signer                              | Unit test in §7.1 last row pins the keyring path; `signing.Secp256k1Signer` is the sole consumer in both paths.             |
| Scenario tests become slow and flaky                                             | Nightly-only; deterministic mock observer; fixed seeds for key generation.                                                   |
| Operators copy testenv configs into prod                                         | Explicit "testenv-only" callout in `README.md`; different binary names (`heightsyncd`, `devshardd-testenv`).                |
| `air` rebuilds race the dlv attach and leave breakpoints unset                   | `send_interrupt=true`, `kill_delay=1s` in `.air.*.toml`; IDE configs enable auto-reconnect; documented in `DEVELOPMENT-MODE.md`. |
| Observability dashboards silently drift from metric names exported by devshardd  | CI smoke test §7.7 item 4 fails if the `devshard-overview` dashboard loses its Chain/Gossip/Resource rows.                   |

---

## 9. Out of scope

- Wiring `blockoracle.NewTendermintObserver` into real `decentralized-api`
  startup. Tracked in a follow-up PR (§6).
- `versiond` integration inside testenv. Versioned supervision is
  production-only.
- Real Cosmos keyring integration in testenv. Covered by a unit test only.
- Authz warm-key flow end-to-end. Remains testermint territory.
- BLS DKG, PoC workers, NATS, model manager.

---

## 10. Glossary

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
