# devshard testenv v2 — operator runbook

Local Docker lab for **devshard-only** integration testing: mock chain (Cosmos gRPC +
CometBFT RPC + LCD REST), mock-dapi (chainoracle + NodeManager long-poll), mock-openai,
versiond × N supervising devshardd, versiond-router, and devshardctl gateway.

**Stack scenarios & tests:** [`testenv/docs/scenarios.md`](../testenv/docs/scenarios.md)  
**gRPC transport consolidation (Phase 12b):** [`testenv/docs/chain-transport-consolidation.md`](../testenv/docs/chain-transport-consolidation.md)  
**Package README:** [`testenv/README.md`](../testenv/README.md)

## Quick start

```bash
cd devshard/testenv
make gen-compose
make build-devshardd          # linux binary for containers
make up
make logs
```

Dev overlay (air + dlv, no image rebuild for Go edits):

```bash
make gen-compose && make dev-build && make dev-up
```

See [`testenv/DEVELOPMENT-MODE.md`](../testenv/DEVELOPMENT-MODE.md).

## Version naming

| Concept | Example | Where |
|---------|---------|-------|
| Protocol version | `v2` | `DEVSHARD_VERSION`, mock-dapi `/versions` |
| Binary log version | `0.2.13-v2-r2` | `DEVSHARD_BINARY_VERSION`, devshardd logs |

Build from repo root:

```bash
make devshardd-build DEVSHARD_VERSION=v2 DEVSHARD_BINARY_VERSION=0.2.13-v2-r2
```

## Config

Edit [`testenv/config/config.yaml`](../testenv/config/config.yaml), then `make gen-compose`.

| Section | Role |
|---------|------|
| `versiond.mode` | `single` (1 host, no Postgres) or `multi` (N hosts + Postgres) |
| `hosts[]` | One versiond + devshardd slot per entry |
| `mock_chain` / `mock_dapi` / `mock_openai` | Fake chain, dapi, ML upstream |
| `postgres` | Required when `versiond.mode: multi` |

## Validation (local)

From repo root or `devshard/`:

```bash
make -C devshard ci-testenv-unit
```

Covers: `common/runtimeconfig` (+ client), `common/chain`, `devshard/chainoracle`,
`devshard/runtimeparams`, `devshard/testenv` unit tests, `decentralized-api/nodemanager`.

Docker stack behavior and adversarial citests:

```bash
make -C devshard ci-testenv-integration
# or stepwise from testenv/:
make build-devshardd && make citest-stack && make citest-adversarial
```

gRPC-only gateway transport (G1–G2, G4; see [`chain-transport-consolidation.md`](../testenv/docs/chain-transport-consolidation.md)):

```bash
make -C devshard/testenv citest-grpc-transport
```

Generate an isolated citest workspace:

```bash
OUT=$(devshard/testenv/scripts/gen-integration-testenv-config.sh)
cd "$OUT" && docker compose up -d
```

## CI

Workflow: [`.github/workflows/devshard-testenv.yml`](../../.github/workflows/devshard-testenv.yml)

| Job | Trigger | Command |
|-----|---------|---------|
| **unit** | PRs touching testenv-related paths | `make -C devshard ci-testenv-unit` |
| **integration** | `workflow_dispatch` with `integration: true` | `make -C devshard ci-testenv-integration` |

## Observability (optional)

```bash
cd devshard/testenv
make obs-up
make citest-observability   # O1 smoke
```

Jaeger UI: http://127.0.0.1:11686/jaeger/ — Grafana: http://127.0.0.1:13000/

Roadmap: [`testenv/docs/observability-plan.md`](../testenv/docs/observability-plan.md)

## Relation to other environments

| Environment | Role |
|-------------|------|
| `devshard/testenv` | Fast devshard lab; **mock** chain; no `inferenced`, no production dapi |
| `local-test-net` | Full node + dapi + edge-api + optional versiond |
| `deploy/join` | Production compose; testenv validates versiond-router wiring |

## Troubleshooting

| Symptom | Check |
|---------|-------|
| `protocol mismatch` in versiond logs | Rebuild devshardd with `DEVSHARD_VERSION=v2` |
| Router 502 on sticky session | `docker compose ps`; `TestVersiondStickySessionFailover` expects first-502 failover to the survivor |
| Long-poll stuck | mock-dapi `/healthz`; mock-chain gRPC `:9090` |
| Citest port conflict | Citest uses `172.31.0.0/24` and ports `18080+` — stop dev `make up` or use harness isolation |
