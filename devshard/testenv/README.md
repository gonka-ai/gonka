# devshard testenv

Docker Compose-based integration test environment for the devshard stack.

The canonical design and porting plan lives in
[`devshard/docs/testenv.md`](../docs/testenv.md). This directory is the
implementation of that plan; anything marked `TODO(phase-N)` in source
files refers to a phase in that document.

## Status

Phase 0 complete: directory tree and package skeletons in place; the
devshard module still compiles. All functionality is staged behind the
numbered phases in the plan.

## Quick map

| Directory                    | Purpose                                                   | Phase |
| ---------------------------- | --------------------------------------------------------- | ----- |
| `proto/`                     | mock-chain gRPC service definition                        | 2     |
| `bridge/`                    | `MainnetBridge` backed by mock-chain                      | 4     |
| `engine/`                    | Mock inference/validation engines                         | 7     |
| `mockdapi/`                  | In-process library wrapping `blockoracle` client + no-op `NodeManager` | 5 |
| `cmd/mockchain/`             | mock-chain binary                                         | 2     |
| `cmd/heightsyncd/`           | height-sync binary (thin wrapper over `blockoracle/standalone`) | 3 |
| `cmd/devshardd-testenv/`     | devshardd host binary with hex signer + stubs             | 8     |
| `cmd/gencompose/`            | compose generator from `config.yaml`                      | 10    |
| `observability/`             | Alloy / Loki / Grafana provisioning                       | 13    |
| `.air.*.toml`, `Dockerfile.dev`, `docker-compose.dev.yml` | Live-reload + dlv overlay  | 12    |

## Related docs

- Full plan: [`../docs/testenv.md`](../docs/testenv.md)
- Dev mode (live reload, remote debugging): [`DEVELOPMENT-MODE.md`](DEVELOPMENT-MODE.md)
- Observability: [`OBSERVABILITY.md`](OBSERVABILITY.md)
