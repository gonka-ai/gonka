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

| Runtime                          | Observer                        | Server           | Client                       | Verifier |
|----------------------------------|---------------------------------|------------------|------------------------------|----------|
| `height-sync` (testenv)          | `observer.NewMock(...)`         | mounted on :9100 | —                            | runs     |
| real `decentralized-api` (prod)  | `observer.NewTendermint(...)`   | mounted on dapi  | —                            | runs     |
| `mock-dapi` (testenv, in-process)| —                               | —                | `client.NewHTTP(HEIGHT_SYNC_URL)` | runs |
| real dapi in-process callers     | —                               | —                | `client.NewInProcess(obs)`   | runs     |

The client variant re-verifies every received header against a pinned
`ChainID` and `ValidatorSet`. The same verifier is called in every runtime.

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
| `height-sync`      | `CHAIN_ID`              | fabricated chain ID                              |
|                    | `BLOCK_INTERVAL`        | time between fabricated blocks (e.g. `2s`)       |
|                    | `VALIDATOR_KEY_HEX`     | secp256k1 hex key used to sign commits           |
|                    | `MOCK_CHAIN_URL`        | gRPC URL of mock-chain                           |
|                    | `PORT`                  | HTTP port (default 9100)                         |
| `devshardd-testenv`| `TESTENV_PRIVATE_KEY`   | hex signer for this host                         |
|                    | `ESCROW_ID`             | escrow this host participates in                 |
|                    | `SLOT_INDEX`            | slot position within the escrow                  |
|                    | `MOCK_CHAIN_URL`        | gRPC URL of mock-chain                           |
|                    | `HEIGHT_SYNC_URL`       | HTTP URL of height-sync                          |
|                    | `VALIDATOR_PUB_HEX`     | pubkey used to verify headers                    |
|                    | `CHAIN_ID`              | pinned chain ID                                  |
|                    | `HTTP_PORT`             | devshardd HTTP port (default 9500)               |
|                    | `DATA_DIR`              | SQLite directory (default `/data`)               |
| `devshardctl`      | `TESTENV_PRIVATE_KEY`   | developer hex signer                             |
|                    | `DEVSHARDD_URL`         | chosen host to proxy to                          |
|                    | `ESCROW_ID`             | escrow the developer drives                      |

No service consumes `KEY_NAME`, `KEYRING_BACKEND`, `KEYRING_DIR`, or
`KEYRING_PASSWORD`.

---

## 5. `mock-dapi` library

Package `devshard/testenv/mockdapi`.

```go
type Config struct {
    HeightSyncURL    string
    ValidatorPubHex  string
    ChainID          string
    ResubscribeAfter time.Duration
}

type MockDapi struct {
    Oracle      blockoracle.BlockOracle
    NodeManager devshard.NodeManager
}

func New(ctx context.Context, cfg Config) (*MockDapi, error) { /* ... */ }
```

- `Oracle` is a `blockoracle/client` instance with `Subscribe=true` and
  `Verify=true`. It opens one SSE stream per process lifetime; every incoming
  header is verified before being cached.
- `NodeManager` is a no-op: `Acquire` returns a synthetic lock id; `Release`
  is a no-op. Stub engines never dereference it.
- Each `devshardd-testenv` process has its own `MockDapi` instance. All
  instances converge on the same `H(V)` by subscribing to the same
  `height-sync`.

Wiring inside `devshardd-testenv/main.go`:

```go
md, err := mockdapi.New(ctx, mockdapi.Config{
    HeightSyncURL:   os.Getenv("HEIGHT_SYNC_URL"),
    ValidatorPubHex: os.Getenv("VALIDATOR_PUB_HEX"),
    ChainID:         os.Getenv("CHAIN_ID"),
})
manager := devshard.NewHostManager(devshard.HostManagerConfig{
    Signer:           signing.MustSignerFromHex(os.Getenv("TESTENV_PRIVATE_KEY")),
    Bridge:           testenvbridge.NewGRPCBridge(ctx, os.Getenv("MOCK_CHAIN_URL")),
    BlockOracle:      md.Oracle,
    NodeManager:      md.NodeManager,
    InferenceEngine:  testenvengine.NewMockInference(),
    ValidationEngine: testenvengine.NewMockValidation(),
    Storage:          mustOpenSQLite(dataDir),
})
```

Swapping `mockdapi.New(...)` for real `dapi.New(...)` in production is the
only difference between the two wirings.

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
  `blockoracle/observer/mock` + `blockoracle/server/http`.
- Optionally poll `mock-chain` to seed `AppHash` from a hash of seeded state
  (nice-to-have; can start with a deterministic in-memory KV).
- Expose HTTP on :9100 and `/healthz`.

### Phase 4 — bridge adaptation

- Port `subnet/testenv/bridge/grpc.go` → `devshard/testenv/bridge/grpc.go`.
- Implement the full current `devshard/bridge.MainnetBridge`, including
  `VerifyWarmKey` (implemented as membership check against
  `GetGrantees(coldAddr)`).
- Provide an `InferenceQueryClientProvider` adapter so devshardd's chain
  client seam is satisfied without constructing a real cosmos client.

### Phase 5 — prod seam: inject `BlockOracle` into `HostManager`

This is the only prod-side change this port forces.

- Add `BlockOracle blockoracle.BlockOracle` to `devshard.HostManagerConfig`.
- Replace whichever code currently produces `H(V)` / `height_at[n]` stamps
  with calls to `oracle.Latest()`.
- Update real dapi wiring to pass a `blockoracle/client.NewInProcess(obs)`
  built around the dapi-local observer. (If real observer wiring is not
  ready, pass a stub that returns a fixed height; the testenv does not
  require real dapi.)

### Phase 6 — `mockdapi` library

- Implement §5.
- Unit tests in §8.1.

### Phase 7 — stub engines

- Port `subnet/testenv/cmd/subnethost/engine.go` → `devshard/testenv/engine/`.
- `MockInferenceEngine`: deterministic completion, streams tokens via channel,
  latency configurable via env var for scenario tests.
- `MockValidationEngine`: returns `Valid` unless the request carries
  `X-Testenv-Inject-Fault: invalid` (used by protocol scenarios).

### Phase 8 — `devshardd-testenv` binary

- New `devshard/testenv/cmd/devshardd-testenv/main.go` per §5.
- Uses hex signer (no keyring).
- Uses mockdapi for dapi-facing interfaces.
- Uses stub engines.
- HTTP server: same routes as prod devshardd; port configurable via env.

### Phase 9 — devshardctl integration

- Extend `devshard/cmd/devshardctl` with a `TESTENV_PRIVATE_KEY` code path
  that bypasses the keyring and uses `signing.SignerFromHex`.
- Add `--host` flag so the operator picks a specific devshardd container.
- Package in its own container; compose uses `profiles: [tools]` so it is
  opt-in.

### Phase 10 — `gencompose`

- Port `subnet/testenv/cmd/gencompose`.
- Input: `config.yaml` with chain id, block interval, escrow id, slot count,
  participants.
- Output: `docker-compose.yml` with one `mock-chain`, one `height-sync`,
  N `devshardd-testenv`, one `devshardctl` (profile: tools).
- Fill missing `private_key_hex` entries, derive bech32 addresses, assign
  `SLOT_INDEX` round-robin.
- Inject `VALIDATOR_PUB_HEX` matching `height-sync`'s `VALIDATOR_KEY_HEX`.
- No keyring volumes.

### Phase 11 — Dockerfiles

- `Dockerfile.mock-chain`, `Dockerfile.height-sync`,
  `Dockerfile.devshardd-testenv`, `Dockerfile.devshardctl`: multi-stage,
  `distroless/static` runtime.
- `Dockerfile.dev`: shared base with `air` + `dlv` for the dev overlay.

### Phase 12 — dev overlay (live-reload + remote debugger)

This phase ports the in-container hot-reload + `dlv` workflow that existed in
`subnet-testenv`. The dev image is **not a different binary** — it's the same
source compiled with `-gcflags='all=-N -l'` and supervised by `air`, so every
edit to Go source under `/workspace/devshard/**/*.go` triggers a rebuild and
restart inside the container.

#### 12.1 Dev image (`Dockerfile.dev`)

One shared image used by every service in dev mode:

```dockerfile
FROM golang:1.25-alpine
RUN apk add --no-cache git ca-certificates tzdata
RUN go install github.com/air-verse/air@latest \
 && go install github.com/go-delve/delve/cmd/dlv@latest
WORKDIR /workspace/devshard/testenv
# pre-warm module cache; source is bind-mounted at runtime
COPY devshard/go.mod devshard/go.sum /workspace/devshard/
COPY devshard/testenv/go.mod devshard/testenv/go.sum /workspace/devshard/testenv/
RUN go mod download
ENTRYPOINT ["air"]
```

No binary is baked in; source is bind-mounted at `/workspace` from the repo
root and compiled inside the container on each restart.

#### 12.2 `air` configs

One pair of `.air.*.toml` per service: plain and `.debug`. Both pin
`-gcflags='all=-N -l'` so `dlv` can set breakpoints. The `.debug` variant
wraps the compiled binary in `dlv exec --headless --continue --listen=:2345`.

| File                             | Service                       | Debugger |
|----------------------------------|-------------------------------|----------|
| `.air.mock-chain.toml`           | `mock-chain`                  | no       |
| `.air.mock-chain.debug.toml`     | `mock-chain` in debug mode    | yes      |
| `.air.height-sync.toml`          | `height-sync`                 | no       |
| `.air.height-sync.debug.toml`    | `height-sync` in debug mode   | yes      |
| `.air.devshardd.toml`            | `devshardd-testenv-{1..N-1}`  | no       |
| `.air.devshardd.debug.toml`      | `devshardd-testenv-0`         | yes      |
| `.air.devshardctl.toml`          | `devshardctl`                 | no       |

`include_dir = ["devshard"]` so a change under either `devshard/` or
`devshard/testenv/` triggers rebuilds everywhere; `delay=500`, `kill_delay=1s`,
`stop_on_error=true` match the subnet-testenv values.

#### 12.3 `docker-compose.dev.yml` overlay

Overlay, not replacement. Used as `-f docker-compose.yml -f docker-compose.dev.yml`.
What it changes per service:

- `build.dockerfile: Dockerfile.dev`; image tagged `:dev`.
- `command: ["-c", "/workspace/devshard/testenv/.air.<service>.toml"]`.
- `volumes: [../..:/workspace]` to bind-mount the repo root.
- For debug-enabled services:
  - `cap_add: [SYS_PTRACE]`
  - `security_opt: [seccomp:unconfined]`
  - `ports: [<host-port>:2345]` to expose the dlv listener.
- YAML anchor `x-devshardd-dev` DRYs up the N devshardd replicas.

Debug port map:

| Host port | Container service       | Purpose                     |
|-----------|-------------------------|-----------------------------|
| `:2345`   | `mock-chain`            | dlv remote                  |
| `:2346`   | `height-sync`           | dlv remote                  |
| `:2347`   | `devshardd-testenv-0`   | dlv remote (first host)     |

Only `devshardd-testenv-0` gets a debugger by default; the rest run with live
reload but no `dlv` wrapper to avoid overhead on multi-host scenarios.

#### 12.4 IDE wiring

- Ship `vscode-launch.json` at `devshard/testenv/vscode-launch.json` with four
  entries: `Attach mock-chain`, `Attach height-sync`, `Attach devshardd-0`,
  and a compound launch that attaches all three.
- Default `remotePath = /workspace`, `localRoot = ${workspaceFolder}`, so
  breakpoints map cleanly.
- Document in `DEVELOPMENT-MODE.md` that on `air` rebuild the IDE will lose
  the dlv connection and needs to reconnect; enable the IDE's auto-reconnect
  if available.

#### 12.5 Makefile targets for dev mode

```makefile
make dev-build       # docker compose -f … -f docker-compose.dev.yml build
make dev-up          # both files, up -d
make dev-down        # both files, down
make dev-logs        # follow all services
make dev-logs-k      # follow devshardd-testenv-k only (k=0..N-1)
```

#### 12.6 macOS / Docker Desktop caveats

- `SYS_PTRACE` and `seccomp:unconfined` work on Docker Desktop's Linux VM
  without extra host config.
- On M-series Macs, `FROM golang:1.25-alpine` must resolve to `linux/arm64`
  so both compiling and running happen in the same architecture inside the
  VM. `Dockerfile.dev` does not pin `--platform`; Docker Desktop picks it
  automatically.

### Phase 13 — observability stack

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

Observability services come up alongside the main stack — they are in the
base compose file. Additional convenience targets:

```makefile
make obs-up          # docker compose up -d (already does it; alias)
make obs-logs        # follow alloy + grafana
make obs-dashboards-open   # macOS: open http://localhost:3000
make obs-query-open        # macOS: open http://localhost:8428/vmui
make obs-reset       # docker compose down -v victoria-metrics loki grafana alloy
```

### Phase 14 — Makefile and documentation

- Full target set: `proto`, `gen-compose`, `build`, `up`, `down`, `logs`,
  `ctl`, `clean`, `dev-build`, `dev-up`, `dev-down`, `dev-logs`, `dev-logs-k`,
  `obs-up`, `obs-logs`, `obs-reset`.
- `README.md`: quick start, env-var table per service, architecture diagram,
  list of Makefile targets, dev-mode callout, observability callout.
- `DEVELOPMENT-MODE.md`: hot-reload + debugger workflow per §12.
- `OBSERVABILITY.md`: per §13, including the macOS caveats and phase
  roadmap.
- Update `HEIGHT_SYNC_HEADERS_PROPOSAL.md` with a reference to
  `devshard/blockoracle` as the realization of `H(V)`.

### Phase 15 — CI wiring

- `make ci-unit`, `make ci-integration`, `make ci-scenarios`,
  `make ci-dep-check` per §8.

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
| `blockoracle/observer/mock`                  | monotonic height, correct block cadence, same seed → same bytes, subscribers fan-out              |
| `blockoracle/verifier`                       | accepts valid commits; rejects tampered `BlockHash`, `AppHash`, `ValidatorsHash`; rejects < 2/3 voting power; rejects stale height |
| `blockoracle/server/http`                    | round-trips `Header` byte-identical; SSE ordering; `/block/{h}/prove` returns stable proofs       |
| `blockoracle/client/http`                    | cache coherence; re-subscribe on disconnect; verify-on-ingest rejects tampered headers from a hostile server; stale-detection flag after quiet period |
| `testenv/mockdapi`                           | `New` yields working oracle; wrong `VALIDATOR_PUB_HEX` fails fast; `NodeManager.Acquire/Release` are no-ops but return valid shapes |
| `testenv/bridge.GRPCBridge`                  | every `MainnetBridge` method returns seeded mock data; `VerifyWarmKey` returns false for non-grantees |
| `testenv/engine.MockInferenceEngine`         | deterministic output; respects `X-Testenv-Latency` header                                         |
| `testenv/engine.MockValidationEngine`        | returns `Valid` by default; returns `Invalid` on injected fault header                            |
| `decentralized-api/internal/devshard.NewSignerFromKeyring` | given fixture armored key, derives expected bech32 (pins the prod keyring path that testenv skips) |

Runtime budget: ≤ 60 s aggregate.

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

Runtime budget: ≤ 5 min. Runs on PR.

### 8.3 cPoC protocol scenario tests

Scripted from `devshard/testenv/scenarios/`. Each test poses as a buggy
developer or host and asserts the expected verdict.

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
5. Interface snapshot test: record `BlockOracle` method set in a txtar
   golden file; any change to the method set requires an explicit golden
   update, signaling an intentional prod-affecting API break.
6. Compile-only test: a file under `decentralized-api/` imports
   `blockoracle.NewTendermintObserver` and instantiates it against a dummy
   RPC URL. Fails the build if the oracle package grows a testenv-only
   dependency. (The file is behind a `//go:build blockoracle_compile_check`
   tag so normal builds ignore it.)

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
