# devshard testenv

Docker Compose-based integration test environment for the devshard
stack. The canonical design lives in
[`devshard/docs/testenv.md`](../docs/testenv.md); this README is the
operator-facing summary plus a runbook.

- Live-reload / remote-debug workflow: [`DEVELOPMENT-MODE.md`](DEVELOPMENT-MODE.md) (Phase 12).
- Metrics / logs / dashboards: [`OBSERVABILITY.md`](OBSERVABILITY.md) (Phase 13).
- Makefile and operator docs: this file + Phase 14 in
  [`../docs/testenv.md`](../docs/testenv.md).

## 1. What this stack simulates

A devshard group talking to a mocked mainnet so protocol code can be
driven end-to-end without a live Cosmos chain. Three pieces matter:

- **`mock-chain`** — gRPC mock of the Gonka chain queries the bridge
  uses (escrows, participants, grantees, warm-key verification). Owned
  by `cmd/mockchain`, seeded from the shared `config.yaml`.
- **`height-sync`** — real `devshard/blockoracle` producer signing each
  fabricated block with 10 (configurable) validators so external
  auditors that enforce `> 2/3` voting power accept the commits.
  Retransmits the full header (including `Commit.Signatures`) over
  HTTP + SSE. Hosts trust the oracle on ingest but still cache signed
  headers so they can forward proofs to verifiers downstream. Owned by
  `cmd/heightsyncd`.
- **`devshardd-testenv` × N** — production `devshardd` wiring with
  three seams swapped for testenv stubs: the `MainnetBridge` points at
  `mock-chain`, the `BlockOracle` + `NodeManager` come from the
  in-process `mockdapi` library, and inference / validation engines
  are deterministic mocks (`engine.MockInferenceEngine`,
  `engine.MockValidationEngine`). Owned by `cmd/devshardd-testenv`.
- **`devshardctl`** — the same operator CLI production users run,
  started behind `profiles: ["tools"]` so it is opt-in. Routes its
  calls either through auto-discovery or a pinned
  `DEVSHARDD_URL`.

## 2. Architecture at a glance

```text
                    ┌────────────────────┐
                    │  config.yaml (ro)  │  single source of truth
                    └──────┬─────────────┘
                           │ bind-mount
        ┌──────────────────┼─────────────────────┐
        │                  │                     │
        ▼                  ▼                     ▼
  ┌───────────┐      ┌───────────┐         ┌───────────┐
  │ mock-chain│      │ height-   │         │devshardctl│  (profile: tools)
  │  (gRPC)   │      │  sync     │         │  (CLI)    │
  └────┬──────┘      │ (HTTP+SSE)│         └────┬──────┘
       │             └────┬──────┘              │
       │ gRPC             │ SSE                 │ HTTP (auth)
       │                  │                     │
       ▼                  ▼                     ▼
  ┌─────────────────────────────────────────────────────┐
  │  devshardd-testenv-0 … devshardd-testenv-(N-1)      │
  │  - testenv/bridge.GRPCBridge → mock-chain           │
  │  - mockdapi.BlockOracle    ← height-sync (trust)    │
  │  - mockdapi.NoopNodeManager                         │
  │  - engine.MockInferenceEngine + MockValidationEngine│
  │  - real host.Host / gossip / state / SQLite store   │
  └─────────────────────────────────────────────────────┘
         (gossip, cross-host RPC between peers)
```

Deterministic IPs leave `172.30.0.100–119` reserved for the
observability overlay:

| Container               | IP              | Host port         |
| ----------------------- | --------------- | ----------------- |
| `mock-chain`            | `172.30.0.2`    | `9090`            |
| `height-sync`           | `172.30.0.3`    | `9100`            |
| `devshardctl`           | `172.30.0.9`    | `8081`            |
| `devshardd-testenv-<i>` | `172.30.0.10+i` | `8080 + i` (opt.) |

## 3. Configuration model

### 3.1 One YAML, many consumers

`testenv/config.yaml` is the **only** hand-maintained input. Schema in
`testenv/config/config.go` (`Config` and friends). Every setting —
chain id, block time, mock-chain port, 10 validator keys, escrow seed,
host keys + slots, user key, network CIDR — lives there.

The YAML is bind-mounted read-only into every service that reads
structured data (`mock-chain`, `height-sync`, `devshardctl`) under
`/app/config.yaml`. Per-host scalars travel as env vars instead of a
generated per-service file, so we never emit a second configuration
artefact that can drift from the source.

### 3.2 Env-var contract

Stamped by `gencompose` from the YAML; read by each binary at startup.

| Service              | Env var               | Purpose                                                |
| -------------------- | --------------------- | ------------------------------------------------------ |
| `mock-chain`         | `CONFIG_PATH`         | path to `config.yaml`                                  |
|                      | `MOCK_CHAIN_PORT`     | gRPC port (default `9090`)                             |
| `height-sync`        | `CONFIG_PATH`         | path to `config.yaml` (pins validator set)             |
|                      | `HEIGHT_SYNC_PORT`    | HTTP port (default `9100`)                             |
| `devshardd-testenv`  | `TESTENV_PRIVATE_KEY` | hex signer for this host                               |
|                      | `ESCROW_ID`           | escrow this host participates in                       |
|                      | `SLOT_INDEX`          | first slot this host owns                              |
|                      | `MOCK_CHAIN_URL`      | gRPC target for the bridge (`mock-chain:9090`)         |
|                      | `HEIGHT_SYNC_URL`     | HTTP target for the oracle (`http://height-sync:9100`) |
|                      | `CHAIN_ID`            | pinned chain id                                        |
|                      | `HTTP_PORT`           | devshardd HTTP port (default `9500`)                   |
|                      | `DATA_DIR`            | SQLite directory (default `/data`)                    |
|                      | `EXPORT_METRICS`     | set `1` to enable Prometheus on `METRICS_PORT` (default in generated compose) |
|                      | `METRICS_PORT`        | listen address for `/metrics` (default `9600` in compose) |
| `devshardctl`        | `TESTENV_PRIVATE_KEY` | developer hex signer                                   |
|                      | `ESCROW_ID`           | escrow the developer drives                            |
|                      | `MOCK_CHAIN_URL`      | swaps in the testenv gRPC bridge                       |
|                      | `DEVSHARDD_URL`       | pin every host lookup to this URL (opt.)              |
|                      | `CONFIG_PATH`         | pins the mock-mainnet validator set                    |

`devshardd-testenv` is deliberately the only service that does **not**
mount `config.yaml`. Production devshardd has no shared testenv YAML
either, so the testenv host process stays shape-compatible with
production.

### 3.3 Key management

`gencompose` fills every missing `private_key_hex` (hosts, user, each
`height_sync.validators[]`), derives bech32 addresses, and writes the
updated YAML back with a `"generated by gencompose"` banner.
Placeholders it rewrites: empty, whitespace, `TODO…`, `CHANGEME…`.
Real hex keys supplied by an operator are preserved across reruns —
`TestFillConfig_Idempotent` pins the no-churn invariant so committing
the generated config is safe.

### 3.4 Mock-mainnet validator set

The testenv now runs a real `devshard/blockoracle` producer so every
block carries a multi-validator Cosmos-style commit. The entire
producer-side validator set is declared once in
`config.yaml → height_sync.validators` and consumed verbatim by
`heightsyncd` and by every non-host auditor:

```yaml
height_sync:
  port: 9100
  initial_height: 1
  seed: 0
  # 10 entries by default. Add / remove freely; gencompose fills any
  # missing or TODO private_key_hex field, power defaults to 1.
  validators:
    - { private_key_hex: "…", power: 3 }
    - { private_key_hex: "…", power: 1 }
    - { private_key_hex: "…" }
    …
```

- **Producer.** `cmd/heightsyncd` loads the yaml, resolves each
  entry into a signer + 20-byte address via
  `Config.HeightSyncValidators()`, and hands the result straight to
  `blockoracle/standalone.Config.Validators`. Each fabricated block is
  signed by a deterministic subset of these validators whose power sum
  is always strictly `> 3/4` of the total — tighter than the `> 2/3`
  rule external auditors enforce, so authentic blocks are never
  rejected.
- **Hosts.** `devshardd-testenv` runs `blockoracle/client` with a nil
  `Verifier` (host-trust mode): it skips signature verification on
  ingest but caches every `Commit.Signature` so hosts can forward
  full proofs to downstream consumers.
- **Auditors (`devshardctl`, cross-host checks).** They mount the same
  `config.yaml` (that's why `devshardctl` gets `CONFIG_PATH`) and
  build a pinned `verifier.ValidatorSet` from the same list. Hence
  the set is never transmitted out-of-band — the yaml is the only
  contract between producer and auditor.
- **Count and power are free parameters.** Change `validators[]` (add
  entries, tweak `power`), rerun `gencompose`, restart the stack.
  Nothing else needs to know.
- **Placeholders.** Empty / `TODO…` / `CHANGEME…` entries are rewritten
  by `gencompose` so the shipped skeleton (10 × TODO) becomes a
  working set after one run. Real hex keys supplied by an operator
  are preserved across reruns.
- **Tests pinning this contract.**
  - `testenv/config`: parsing, TODO rejection, duplicate-address
    rejection, power defaulting.
  - `testenv/cmd/gencompose`: end-to-end fill (10 validators + 4
    hosts + user) plus idempotence across re-runs.
  - `testenv/cmd/heightsyncd`: yaml → `standalone.Config` plumbing
    preserves count / addresses / power, and an end-to-end run
    (`TestHeightSync_SignsWithConfiguredValidators`) boots the
    producer from a synthetic yaml, subscribes a verifying client,
    and asserts every `Commit.Signature.ValidatorAddress` resolves
    to a configured validator with signer-power strictly above the
    `> 3/4` floor.

## 4. Runbook

This section is the **Phase 14** operator runbook: prefer
[`Makefile`](Makefile) targets over hand-typed `docker compose` / `go run`
when a target exists (`make help` lists them all). Working directory is
`devshard/testenv` unless noted. Docker 24+ and a recent Go toolchain are
assumed.

### 4.0 Makefile targets (`Makefile`)

`make help` lists every target with a one-line description. The Phase 14
surface is:

| Target | What it does |
| ------ | --- |
| `proto` | Regenerate `testenv/proto/mockchainpb` from `proto/mockchain.proto` (needs `protoc` + `protoc-gen-go` + `protoc-gen-go-grpc` on `PATH`, e.g. under `~/go/bin`). |
| `gen-compose` | Run `go run ./cmd/gencompose` → `docker-compose.yml` + fill `config.yaml` keys. |
| `build` / `test` | `go build` / `go test` of packages under this directory (`devshard/testenv/...` only for `test`). |
| `integration-test` | From `devshard/`: `go test -race -timeout=15m ./...` (entire module; expect **several minutes**). Not docker I1–I10 (Phase 15 / `ci-integration`). |
| `up` / `down` / `clean` / `logs` | `docker compose` for the **base** `docker-compose.yml` only. |
| `ctl` | `devshardctl` via `--profile tools run --rm`. |
| `dev-build` / `dev-up` / `dev-down` / `dev-clean` / `dev-logs` | Stack with `docker-compose.dev.yml` (live-reload + `dlv` — see `DEVELOPMENT-MODE.md`). |
| `dev-logs-<k>` | Tail `devshardd-testenv-<k>` only; `<k>` must be in `0 .. DEVSHARDD_HOSTS-1`. |
| `dev-restart-<svc>` | `docker compose … restart <svc>` on the dev overlay. |
| `obs-up` | Same as `up` (observability services are merged into the base file). |
| `obs-up-linux` | Same as `obs-up` (kept for scripts; node-exporter is on by default). |
| `obs-down` / `obs-logs` / `obs-grafana-open` / `obs-query-open` / `obs-reset` | See [`OBSERVABILITY.md`](OBSERVABILITY.md). |

**Faster test loops:** `make test` runs only packages under `devshard/testenv/...`.
`make integration-test` runs **`go test -race` for the whole `devshard` module**
(many minutes; 15m timeout) — use for pre-push / CI-style checks, not for
tight edit-test cycles.

### 4.1 One-time: fill keys and render compose

```bash
cd devshard/testenv

# Preferred (Phase 14):
make gen-compose

# Equivalent:
go run ./cmd/gencompose -config config.yaml -out docker-compose.yml
```

The command renders `docker-compose.yml` from `config.yaml`, fills missing
private keys and addresses, assigns slots round-robin, and rewrites
`config.yaml` with a generated banner.

After the first run, `config.yaml` holds real hex keys and
`docker-compose.yml` references one `mock-chain`, one `height-sync`,
N `devshardd-testenv-<i>`, and a `devshardctl` service (opt-in via
`--profile tools`). Both files are safe to commit. Regeneration is
idempotent.

If `observability/compose-fragment.yaml` exists it is appended
verbatim (Phase 13). Otherwise `gencompose` logs a note and skips it.

### 4.2 Start the base stack

```bash
make up                         # same as: docker compose -f docker-compose.yml up -d
                                # (mock-chain, height-sync, N hosts, observability if present)
docker compose ps               # verify all services healthy
make logs                       # follow all services; Ctrl+C to stop

# One host only (example):
docker compose -f docker-compose.yml logs -f devshardd-testenv-0
# Or, with the dev overlay up: make dev-logs-0
```

The stack is ready when `height-sync` has logged its first block
(height 1) and every `devshardd-testenv-<i>` reports
`devshardd-testenv starting … mock_chain=mock-chain:9090
height_sync=http://height-sync:9100`.

### 4.3 Drive it with `devshardctl`

The Makefile exposes `make ctl` → `docker compose --profile tools run --rm
devshardctl` (no args). For **subcommands** (e.g. `chat-completion`), call
`docker compose` explicitly or pass args after the service name as usual.

```bash
# One-shot call through the containerized CLI (profile tools):
docker compose -f docker-compose.yml --profile tools run --rm devshardctl \
  chat-completion --prompt "hello"

# Or run the CLI from the devshard module root (repo layout: cmd/ is not under testenv/):
( cd .. && go run ./cmd/devshardctl \
  --mock-chain mock-chain:9090 \
  --escrow-id 1 \
  --private-key "$(docker compose -f testenv/docker-compose.yml --profile tools exec -T devshardctl printenv TESTENV_PRIVATE_KEY)" \
  chat-completion --prompt "hello" )
```

`DEVSHARDD_URL` in the compose file pins the CLI to
`devshardd-testenv-0` by default; override on the command line to
target a different host.

### 4.4 Rebuild after code changes (base stack)

```bash
docker compose -f docker-compose.yml build
docker compose -f docker-compose.yml up -d    # or: make up
```

For hot-reload, use the dev overlay — `make dev-up` (after `make dev-build`).
See [`DEVELOPMENT-MODE.md`](DEVELOPMENT-MODE.md); target list in §4.0.

### 4.5 Tear down

```bash
make down                       # stop base stack, keep volumes
make clean                      # same as: docker compose down -v (wipe anonymous + named vols in project)
rm docker-compose.yml           # optional: drop generated compose
```

`config.yaml` is intentionally **not** deleted by any target — the
filled-in keys are the only seed state you need to reproduce a run.

### 4.6 Regenerate after editing `config.yaml`

1. Edit `config.yaml` (e.g. add a host, tweak `escrow.slots`, bump
   `height_sync.validators[].power`).
2. `make gen-compose` — rewrites `docker-compose.yml` and fills any new
   TODO placeholders.
3. `make up` — Compose recreates only the changed services where needed.

Adding a host: append a `hosts:` entry with `id: devshardd-testenv-4`,
leave `private_key_hex` empty or `TODO`, bump `escrow.slots` if you
want the new host to own slots, rerun `gencompose`.

Changing the mock-mainnet validator set: edit
`height_sync.validators` in `config.yaml` — add or remove entries,
tweak `power`, or clear a `private_key_hex` back to `TODO` to have
`gencompose` mint a fresh key. Rerun `gencompose` and restart only
`height-sync` (hosts re-subscribe automatically; external auditors
re-read `config.yaml` on their next call):

```bash
docker compose -f docker-compose.yml restart height-sync
```

### 4.7 Useful one-liners

```bash
# Reset the on-disk state of all hosts without rebuilding:
make down
sudo rm -rf db/ && make up

# Fast: testenv packages only (seconds):
make test

# Full devshard module, race detector (several minutes; 15m cap):
make integration-test

# Tail only the block oracle stream:
docker compose -f docker-compose.yml logs -f height-sync

# Exec into a host to poke its SQLite:
docker compose -f docker-compose.yml exec devshardd-testenv-0 sh
# then: sqlite3 /data/devshardd.db ".tables"
```

### 4.8 Development mode and observability

Phase 12 and 13 are **not** separate compose files you start by hand for
day-to-day work — they are documented alongside Phase 14:

- **Live-reload + `dlv`:** `make dev-build` once, then `make dev-up`,
  `make dev-logs`, `make dev-logs-0`, … — full runbook in
  [`DEVELOPMENT-MODE.md`](DEVELOPMENT-MODE.md).
- **Metrics / logs / Grafana / VMUI:** services are merged into the same
  `docker-compose.yml` (when `observability/compose-fragment.yaml` exists).
  `make obs-up` is an alias of `make up`. URLs and cleanup:
  [`OBSERVABILITY.md`](OBSERVABILITY.md) (`make obs-logs`, `make obs-grafana-open`, …).

## 5. Directory map

| Path                                        | Purpose                                                                           |
| ------------------------------------------- | --------------------------------------------------------------------------------- |
| `config.yaml`                               | single source of truth (hand-edited, `gencompose`-filled)                         |
| `config/`                                   | Go schema + defaults for `config.yaml`                                            |
| `proto/`                                    | mock-chain gRPC service definition                                                |
| `bridge/`                                   | `MainnetBridge` implementation backed by `mock-chain`                             |
| `engine/`                                   | Deterministic stub inference + validation engines                                 |
| `mockdapi/`                                 | In-process `BlockOracle` client + no-op `NodeManager` for `devshardd-testenv`     |
| `observability/`                            | Alloy / Loki / Grafana / VictoriaMetrics provisioning (Phase 13)                  |
| `cmd/mockchain/`                            | mock-chain binary (gRPC)                                                          |
| `cmd/heightsyncd/`                          | height-sync binary (HTTP + SSE; thin wrapper over `blockoracle/standalone`)       |
| `cmd/devshardd-testenv/`                    | devshardd host binary with hex signer + stubbed seams                             |
| `cmd/gencompose/`                           | compose generator; wrap with `make gen-compose` (§4.1)                            |
| `Dockerfile.mock-chain`                     | build recipe for `mock-chain`                                                     |
| `Dockerfile.height-sync`                    | build recipe for `height-sync`                                                    |
| `Dockerfile.devshardd-testenv`              | build recipe for one host                                                         |
| `Dockerfile.devshardctl`                    | build recipe for the operator CLI                                                 |
| `Dockerfile.dev`                            | shared base with `air` + `dlv` for the dev overlay                                |
| `docker-compose.yml` *(generated)*          | emitted by `gencompose`; do not hand-edit                                         |
| `docker-compose.dev.yml`                    | live-reload overlay (Phase 12)                                                    |
| `Makefile`                                  | `make help` — `proto` through `integration-test` (see §4.0)                |
| `vscode-launch.json`                        | IDE launch configs for attaching `dlv` to running containers                      |

## 6. Troubleshooting

- **`height_sync.validators[i].private_key_hex is a TODO placeholder`**
  — run `go run ./cmd/gencompose` once; placeholders are rewritten and
  real hex keys are committed into `config.yaml`.
- **Hosts fail to start with `required env vars missing`** — the
  generated compose was edited by hand or an env was unset. Rerun
  `gencompose`.
- **`mock-chain` unreachable** — verify the service came up healthy
  (`docker compose ps`). The bridge retries on transient errors but
  surfaces startup failures verbatim.
- **CLI requests time out** — `devshardctl` pins a single host via
  `DEVSHARDD_URL`. If that host is down, either pass
  `--host http://<other-host>:8080` or drop the pin to fall back to
  auto-discovery.
- **Stale block oracle warning in logs** — expected when blocks do not
  arrive for `StaleAfter` (default 10 s). Verdicts are deferred, not
  failed, until the oracle catches up; see `docs/testenv.md` §5 for
  the contract.

## 7. Where to go next

- Implementation plan, phase status, and **Phase 14** checklist:
  [`../docs/testenv.md`](../docs/testenv.md) (port plan §6).
- Makefile index: `make help` from `devshard/testenv` (see §4.0).
- Future stub engines (per-node REST control plane, fault injection):
  [`../docs/testenv-stub-engines.md`](../docs/testenv-stub-engines.md).
- Live-reload + remote debug workflow: [`DEVELOPMENT-MODE.md`](DEVELOPMENT-MODE.md).
- Metrics / logs / dashboards: [`OBSERVABILITY.md`](OBSERVABILITY.md).
