# devshard testenv

Local Docker stack for integration testing: **mock-chain**, **mock-dapi**, **mock-openai**,
**versiond × N** (supervising **devshardd**), **versiond-router**, and **devshardctl**.

Config-driven via `config/config.yaml` and `cmd/gencompose`.

## Documentation

- **Stack behavior scenarios:** [`docs/scenarios.md`](docs/scenarios.md)
- **gRPC transport plan (G1–G4):** [`docs/chain-transport-consolidation.md`](docs/chain-transport-consolidation.md)
- **Phase 12 index:** [`docs/phase12-followup.md`](docs/phase12-followup.md)
- **Operator runbook:** [`../docs/testenv-v2.md`](../docs/testenv-v2.md)
- **Design plan:** [`docs/testenv-v2-plan.md`](docs/testenv-v2-plan.md)

## Development

Go packages under `devshard/testenv/` — what each one is for:

| Package | Purpose |
|---------|---------|
| **`config/`** | Schema for `config.yaml`: hosts, escrows, mock service ports, versiond mode, network CIDR. `Load` / `Validate` / `ApplyDefaults` / `Save`; chain-seed fields feed mock-chain. |
| **`cmd/gencompose`** | Reads config, generates keys, syncs chain seed, materializes keyrings, writes `docker-compose.yml` + `.env`. Single entry point for `make gen-compose`. |
| **`cmd/mockchain`** | Container entrypoint: loads config into an in-memory chain store and serves **gRPC**, **CometBFT RPC**, and **admin** HTTP from `mockchain/`. |
| **`cmd/mockdapi`** | Container entrypoint: gRPC NodeManager (`GetRuntimeConfig`, `AcquireMLNode`) + HTTP chainoracle (`/versions`, blocks SSE) + testenv fault proxy. |
| **`cmd/mockopenai`** | Container entrypoint: OpenAI-compatible `POST /v1/chat/completions` (JSON + SSE) with deterministic replies and fault knobs. |
| **`mockchain/`** | Fake Cosmos chain library: escrow/participant store, gRPC query/tx server, RPC events, seed loader, `/testenv` admin mutations. |
| **`mockdapi/`** | Fake dapi library: polls mock-chain for params, serves long-poll runtime config, hands out mock-openai URL as ML node, mounts `gatewayphase` stubs. |
| **`mockopenai/`** | Fake ML node library: minimal OpenAI HTTP API used by production `devshardd` after `AcquireMLNode`. |
| **`gatewayphase/`** | Tiny HTTP stubs for devshardctl’s **chain epoch phase** poller (`ChainPhaseGate`): `/v1/epochs/latest` and `/v1/epochs/current/participants`. Mounted on mock-dapi; not a mock gateway. |
| **`keymaterial/`** | Builds deterministic Cosmos **file keyrings** from config host keys so devshardd can sign txs in containers (`KEYRING_DIR`, `KEY_NAME`). |
| **`citest/`** | Go integration tests: compose validation, gateway wiring, full-stack behavior tests (`make citest-stack`), Phase 9 adversarial A1–A5 (`make citest-adversarial`, `-tags=testenvci`), and optional gateway chat smoke (`TESTENV_GATEWAY_SMOKE=1`). |

Production binaries (`devshardd`, `devshardctl`, `versiond`) are **not** reimplemented here — testenv only fakes their external dependencies (chain, dapi, ML) and wires them in Compose.

## Version naming

| Concept | Value | Where used |
|---------|-------|------------|
| **Protocol version** (approved slot name) | `v2` | `DEVSHARD_VERSION`, `VERSIOND_FORCE`, mock-dapi `/versions`, `versiond.version_name` |
| **Binary log version** (log prefix / build id) | `0.2.13-v2-r2` | `DEVSHARD_BINARY_VERSION`, devshardd log lines |

Build devshardd with **both** flags so versiond’s protocol check and log prefix match.

## Prerequisites

- Docker and Docker Compose v2
- Go 1.24+ (for tests and `gencompose`)
- Repo root: build **devshardd** with protocol **`v2`** (what mock-dapi `/versions` advertises)

## Config

Skeleton: [`config/config.yaml`](config/config.yaml).

| Section | Purpose |
|---------|---------|
| `mock_chain`, `mock_dapi`, `mock_openai` | Mock services (ports, hosts) |
| `versiond` | Protocol `version_name` (`v2`), `binary_version`, `mode` (`single` \| `multi`), devshardd override path, keyring |
| `versiond_router` | Sticky HAProxy router (`:8080`) |
| `devshardctl` | Gateway listen port |
| `postgres` | Shared Postgres — **required** for `versiond.mode: multi`; **off** for `single` (file payload fallback) |
| `hosts` | One **versiond** + **devshardd** slot per entry (`id`, keys, IP) |
| `user` | Escrow owner / gateway private key (filled by gencompose) |
| `escrow`, `network` | Slot layout and docker bridge CIDR |

### Single vs multi versiond mode

`versiond.mode` controls host count and whether shared Postgres is required:

| Mode | Hosts | Postgres | Payload storage |
|------|-------|----------|-----------------|
| `single` | 1 (`versiond-0` only) | `enabled: false` | File fallback under `{data-dir}/payloads` |
| `multi` (default) | 3 (`versiond-0..2`) | `enabled: true` | Shared `devshard-postgres` |

Set in `config/config.yaml`:

```yaml
versiond:
  mode: single   # or multi
```

`gencompose` trims or pads the `hosts` list to match. **Multi versiond without
Postgres is rejected** at validate time. Compose also sets
`DEVSHARD_STORAGE_MODE=postgres` on versiond services so devshardd boots in
Postgres-only mode: boot fails if Postgres is down, and any local SQLite
sessions are batch-migrated into Postgres once at startup.

### Three versiond instances (multi mode)

The default skeleton defines **three** hosts (`versiond-0`, `versiond-1`, `versiond-2`).
`gencompose` emits one compose service per host and puts `versiond-0` and
`versiond-1` behind the `versiond-pool` network alias:

```text
VERSIOND_POOL_HOST="versiond-pool"     # on versiond-router
```

The router resolves that name to whichever pool members are running and
health-checks each address. `versiond-2` is a **solo** participant reached via
direct `inference_url` (`http://versiond-2:8080`), not the HA pool. Escrow slots round-robin
across the HA identity (`hosts[0]`) and solo hosts (`hosts[2+]`). Solo uses **sqlite**
storage so it does not multi-write the HA pair’s shared Postgres diffs; the HA pair keeps
`DEVSHARD_STORAGE_MODE=postgres` (leases + sticky single-writer).

### Shared keyring

All versiond containers mount the **same** `./keyring/` directory at `/keyring`. `gencompose`
imports one Cosmos key per host (key name = host id). In **multi/HA** mode the HA pair
(`hosts[0]`, `hosts[1]`) sets `KEY_NAME` to the HA participant (`versiond-0`) — join-style
same-identity HA. Solo hosts (`hosts[2+]`) keep their own `KEY_NAME`. Escrow slots alternate
HA + solo so validations (and lease races) can run; a host never validates its own executions.
Passphrase is `versiond.keyring_password` in config (default `testenv1`).

To **recreate** the keyring after a failed or stale materialize:

```bash
make clean-keyring    # rm -rf ./keyring
make gen-compose      # rewrites keyring/, docker-compose.yml, .env
```

Do not run `gen-compose` in a half-written keyring state — delete `keyring/` first if
materialize failed or you changed `keyring_password`.

## Build

From **`devshard/testenv/`**:

```bash
# 1. Render config.yaml, .env, docker-compose.yml, and shared keyring/
make gen-compose

# 2. Build devshardd for Linux containers (requires Docker daemon)
make build-devshardd
```

`build-devshardd` runs at the repo root via Docker and produces `../../build/devshardd` (linux,
mounted into versiond). **Start Docker Desktop** before this step.

To only check version stamps on your Mac (no Docker):

```bash
make build-devshardd-local
../../build/devshardd --print-protocol-version   # v2
../../build/devshardd --print-binary-version     # 0.2.13-v2-r2
```

`build-devshardd-local` builds a **host** binary — use `build-devshardd` for the compose stack.

`build-devshardd` equivalent at repo root:

```bash
make devshardd-build DEVSHARD_VERSION=v2 DEVSHARD_BINARY_VERSION=0.2.13-v2-r2
```

## Start the stack

From **`devshard/testenv/`** (after `gen-compose` + `build-devshardd`):

```bash
docker compose config          # validate rendering
docker compose build           # first time or after image changes
make up
make logs                      # follow all services
```

Quick checks:

```bash
curl -s http://localhost:9100/versions | jq .
curl -s http://localhost:8088/healthz
docker compose logs versiond-0 --tail=40
```

Stop: `make down` (or `make clean` to remove volumes).

### Dev overlay (live reload)

For mock-chain / mock-dapi / mock-openai hacking without image rebuilds:

```bash
make gen-compose && make dev-build && make dev-up
```

See [DEVELOPMENT-MODE.md](DEVELOPMENT-MODE.md) and [`vscode-launch.json`](vscode-launch.json).

## Manual Phase 6 validation

1. **Generate + build** (above).
2. **Start:** `make up` → `docker compose ps` — all services `running`.
3. **Mocks:** `curl -sf http://localhost:8088/healthz`; `curl -sf http://localhost:9100/versions | jq .` — name **`v2`**.
4. **versiond child** (each host):

   ```bash
   docker compose logs versiond-0 2>&1 | grep -E 'override binary|starting child|devshardd starting|child exited|protocol mismatch|keyring'
   ```

   **Pass:** `using override binary` … `v2`; `starting child` … `version=v2`; `devshardd starting` … `protocol_version=v2` … `binary_log_version=0.2.13-v2-r2`; `chain node` … `mock-chain`.

   **Fail:** `protocol mismatch`, `keyring:` errors, `child exited` (rebuild with correct versions / arch).

5. **Router:** `docker compose logs versiond-router --tail=20` — upstreams `versiond-0:8080` … `versiond-2:8080`.

## Tests

```bash
cd devshard
go test ./testenv/cmd/gencompose/... ./testenv/citest/... ./testenv/keymaterial/... ./testenv/gatewayphase/... -count=1
go test ./testenv/... -count=1
```

### Stack citest (Docker)

The stack suite covers smoke, routing, runtime updates, gateway behavior,
versiond lifecycle, storage migration, validation leases, and rolling updates.
Tests use behavior-oriented names and isolated stacks on dedicated subnets with
Docker-assigned localhost ports, so they can run while a dev `make up` stack is
active.

```bash
cd devshard/testenv
make build-devshardd
make citest-stack                  # all core stack behavior tests
make citest-validation-lease-race # validation lease race only
make citest-versiond-rolling-update
# or: ./scripts/run-stack-citest.sh
```

`TestRouterStickiness` checks that repeated requests to
`/<version>/sessions/<id>/…` land on the same versiond upstream.

`TestParamsLongPoll` updates mock-dapi params while `GetRuntimeConfig` is blocked
and requires the poll to wake with the new values.

`TestEpochSwitch` advances the mock-chain epoch and requires runtime config to
report the higher epoch.

`TestGatewayChat` exercises non-streaming and SSE chat through the full stack.

`TestVersiondStickySessionFailover` stops one versiond and requires affected
sessions to fail over on the first upstream connection failure.

`TestValidationLeaseRace*` exercises lease exclusivity across a same-key HA pair
and a solo executor. `TestVersiondRollingUpdate*` separately checks same-version
sha replacement with Postgres overlap and the hybrid stop-then-start fallback.

**Phase 9 adversarial** (`make citest-adversarial`): A1 lost first SSE chunk, A2 ML 503, A3 stale escrow on chain gRPC, A4 bad warm-key grantees, A5 streamed HTTP 200 SSE error envelope accounted as `TIMEOUT_REASON_ERROR` (companion to A2; 3-host stack). Fault hooks: `mock-openai` `/testenv/fault`, mock-chain `/testenv/escrow` + `/testenv/grantees` (via mock-dapi).

### Phase 10 observability overlay (optional)

Jaeger, Loki, Promtail, Prometheus, and Grafana — adapted from `deploy/join/observability/`.
Host ports are offset from join defaults: Grafana **13000**, Loki **13101**, Prometheus **19099**, Jaeger UI **11686**.

**Backend roadmap (Tempo, Alloy, profile selection):** [docs/observability-plan.md](docs/observability-plan.md)  
When Alloy is enabled, OTLP goes to **`http://alloy:4317`** (Alloy forwards to Jaeger or Tempo). Baseline River config is ported from branch **`devshard-testenv`**.

**Manual (after `gen-compose` + `build-devshardd`):**

```bash
cd devshard/testenv
make obs-up          # enables TESTENV_OTEL_* and starts overlay
make up              # if stack was not already up — obs-up starts both
```

Send traffic, then explore:

| UI | URL | What to look for |
|----|-----|------------------|
| Jaeger | http://127.0.0.1:11686/jaeger/ | Service `devshardd` → spans `devshardd.request`, `devshardd.inference` (child of request on chat) |
| Grafana | http://127.0.0.1:13000/ (admin/admin1) | Dashboard **Devshard details**; Explore → Loki `{compose_service=~"versiond.*"} \| logfmt \| stage="terminal"` |
| Prometheus | http://127.0.0.1:19099/targets | Jobs `devshardd` (`/v2/metrics` via versiond) and `devshardctl` |
| Loki API | http://127.0.0.1:13101/ | Log lines with `stage=terminal`, `where=…`, `request_id=…` from devshardd inside versiond logs |

Quick probe after gateway chat:

```bash
curl -s 'http://127.0.0.1:11686/jaeger/api/traces?service=devshardd&operation=devshardd.inference&limit=5' | jq '.data | length'
curl -sG 'http://127.0.0.1:13101/loki/api/v1/query_range' \
  --data-urlencode 'query={compose_service=~"versiond.*"} |~ "devshard request terminal"' \
  --data-urlencode 'limit=5' | jq '.data.result | length'
curl -sf http://127.0.0.1:18080/v2/metrics | grep devshardd_request_duration_seconds | head
```

**Automated O1 citest:**

```bash
make citest-observability
# or: TESTENV_CITEST=1 go test -tags=testenvci ./citest/ -run TestO1_ObservabilitySmoke -v
```

Stop overlay: `make obs-down`.

Requires rebuilding **devshardd** after pulling observability init changes (`make build-devshardd`).

### Phase 7 gateway smoke (Docker)

Full path: `devshardctl` → `versiond-router` → `devshardd` → `mock-openai`.

```bash
cd devshard/testenv
make gen-compose && make build-devshardd
TESTENV_GATEWAY_SMOKE=1 go test ./citest/ -run TestGatewayPhase7_Smoke -count=1 -v
```

Quick gateway checks after `make up`:

```bash
curl -s http://localhost:8081/v1/status | jq .
curl -s http://localhost:8081/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"test-model","messages":[{"role":"user","content":"hello"}],"max_tokens":32}' | jq .
```

OpenAI path: `POST /v1/chat/completions` → gateway → versiond-router (sticky) → devshardd → mock-openai.

## Generated artifacts

`make gen-compose` writes (gitignored under `testenv/`):

| Path | Contents |
|------|----------|
| `config.yaml` | Filled keys + synced mock-chain seed |
| `.env` | `TESTENV_USER_PRIVATE_KEY`, `TESTENV_KEYRING_PASSWORD`, `TESTENV_CHAIN_ID` |
| `docker-compose.yml` | Full stack |
| `keyring/` | Shared Cosmos file keyring (all hosts) |

## Wire summary

| Service | Ports | Notes |
|---------|-------|-------|
| mock-chain | 9090 gRPC, 26657 RPC, 9191 admin | Chain seed from `config.yaml` |
| mock-dapi | 9400 gRPC, 9100 HTTP | `GetRuntimeConfig`, blocks SSE, `/versions` |
| mock-openai | 8088 | OpenAI-compatible ML stub |
| versiond-0..2 | 8080 (internal) | `VERSIOND_ORACLE_URL` → mock-dapi |
| versiond-router | 8080 | Sticky route to versiond by escrow/session |
| devshardctl | 8081 | Gateway — Phase 7: LCD tx + public API stubs + chat via router |

## Troubleshooting

| Symptom | Check |
|---------|--------|
| `devshardd-build` / docker.sock error | Start Docker Desktop, then `make build-devshardd` |
| `exec format error` on `../../build/devshardd` | Stale wrong-arch binary; rebuild with Docker (`make build-devshardd`) |
| versiond skips forced version | Linux `build/devshardd` exists; `--print-protocol-version` = `v2` inside container |
| Protocol mismatch in versiond logs | Rebuild: `DEVSHARD_VERSION=v2 DEVSHARD_BINARY_VERSION=0.2.13-v2-r2` |
| devshardd keyring error | Re-run `make gen-compose`; shared `./keyring/keyhash` exists |
| compose config fails | `make test-compose` |
