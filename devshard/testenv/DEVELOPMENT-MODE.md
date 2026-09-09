# Development mode (Phase 4)

Live-reload and remote debugging for mock-chain without rebaking Docker images.

## Quick start

From `devshard/testenv/`:

```bash
make gen-compose    # fill TODO keys, sync mock-chain seed, write docker-compose.yml
make dev-build      # build shared dev toolchain image (Go + air + dlv)
make dev-up         # start mock-chain with live reload + dlv on :2345
make dev-logs       # follow logs
```

Edit Go under `devshard/testenv/mockchain/` — air rebuilds inside the container (~500 ms debounce).

Usage:

```bash
make gen-compose   # fills keys, writes docker-compose.yml
make dev-build && make dev-up   # live reload + dlv on :2345
```

## Config

- Skeleton: `config/config.yaml` with `TODO` keys for hosts, user, and warm grantee.
- `make gen-compose` writes back pinned keys and syncs `participants`, `escrows`, `grantees`.
- mock-chain reads config via `CONFIG_PATH` or `MOCK_CHAIN_CONFIG` (both supported).

## Remote debug

- mock-chain debug port: **2345** (host → container)
- Attach with Delve or VS Code Go remote attach to `localhost:2345`
- Copy or merge `vscode-launch.json` into `.vscode/launch.json` — profile **Attach: mock-chain**
- After air rebuilds, reconnect the debugger (`--accept-multiclient` is enabled)

## macOS / Docker Desktop

Debug services need `SYS_PTRACE` and `seccomp:unconfined` (already set in `docker-compose.dev.yml`).

## Chain transport (gateway)

devshardctl uses **gRPC only** for chain queries and escrow tx (`DEVSHARD_CHAIN_GRPC` / `NODE_GRPC_URL`). mock-chain serves gRPC `:9090`, CometBFT RPC `:26657`, and testenv admin — no LCD REST face. See [`docs/chain-transport-consolidation.md`](docs/chain-transport-consolidation.md).

## Stop

```bash
make dev-down
```
