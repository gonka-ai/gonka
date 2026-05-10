# Development mode

Live-reload (`air`) and remote debugging (`dlv`) for every service in
`devshard/testenv`. The dev overlay is additive — nothing in the base
`docker-compose.yml` or the production `devshardd` build changes.

> The canonical spec lives in [`../docs/testenv.md`](../docs/testenv.md)
> §Phase 12. This file is the operator-facing runbook.
> **Make targets** (`dev-up`, `dev-logs-0`, …) live in
> [README.md §4.0](README.md#40-makefile-targets-makefile) (Phase 14).

## What the overlay gives you

- Every testenv container runs the same `Dockerfile.dev` image (Go +
  `air` + `dlv`) with the repo root bind-mounted at `/workspace`. No
  binaries are baked in.
- `air` watches `/workspace/devshard/**/*.go` and rebuilds + restarts
  the owning service on change (debounced by 500 ms).
- Three services come up with a `dlv` listener already attached:
  `mock-chain:2345`, `height-sync:2346`, `devshardd-testenv-0:2347`.
  Hosts 1..N-1 run live-reload only, so a multi-host scenario stays
  responsive on a laptop.
- Module + build caches are kept in named volumes so rebuilds inside
  the container are warm across restarts.

## Quick start

```bash
# 1. Generate config + base compose (one-time; idempotent).
cd devshard/testenv
make gen-compose

# 2. Build the shared dev image (first run only, or after toolchain bumps).
make dev-build

# 3. Bring everything up with live-reload + dlv attached.
make dev-up

# 4. Watch every service rebuild/log in one pane:
make dev-logs

# 5. Attach the IDE (see vscode-launch.json) to :2345/:2346/:2347.

# 6. Tear down when done:
make dev-down       # keep caches
make dev-clean      # also drop the module/build caches
```

## Per-service air config matrix

| File                           | Service                       | Debugger | Debug port |
|--------------------------------|-------------------------------|----------|------------|
| `.air.mock-chain.toml`         | `mock-chain`                  | no       | —          |
| `.air.mock-chain.debug.toml`   | `mock-chain` (debug)          | yes      | `2345`     |
| `.air.height-sync.toml`        | `height-sync`                 | no       | —          |
| `.air.height-sync.debug.toml`  | `height-sync` (debug)         | yes      | `2346`     |
| `.air.devshardd.toml`          | `devshardd-testenv-{1..N-1}`  | no       | —          |
| `.air.devshardd.debug.toml`    | `devshardd-testenv-0`         | yes      | `2347`     |
| `.air.devshardctl.toml`        | `devshardctl`                 | no       | —          |

All configs pin `root = "/workspace/devshard"` (the Go module root),
`include_ext = ["go", "yaml", "toml", "proto"]`, `delay = 500`,
`kill_delay = "1s"`, `stop_on_error = true`. Debug variants build
with `-gcflags='all=-N -l'` and wrap the binary in
`dlv exec --headless --accept-multiclient --continue --listen=:<port>`
so the IDE reconnects cleanly after every air rebuild.

The `devshardd.debug.toml` listener port is parameterised via
`$DLV_PORT`. Only host-0 exports it in `docker-compose.dev.yml`; to
also debug host-1, copy the host-0 block, set `DLV_PORT: "2348"`,
and publish port `2348:2348`.

## IDE wiring

### VS Code

Copy `vscode-launch.json` to `.vscode/launch.json` (or merge the
`configurations` array). All six profiles — `mock-chain`, `height-sync`,
and `devshardd-testenv-{0..3}` — attach to `127.0.0.1` at the ports
above and remap `${workspaceFolder}` ↔ `/workspace`. Breakpoints set
in Go source in your editor translate 1-for-1 to the running container.

### GoLand / IntelliJ

Use *Run → Edit Configurations → + → Go Remote*:

- Host `127.0.0.1`, port `2345` / `2346` / `2347`.
- *Path mappings*: `<repo-root>` → `/workspace` (Go source → remote).

## Rebuild cycle & reconnect

`air` rebuilds happen inside the container, so the host-side compiled
binary never changes. Each rebuild:

1. Kills the current `dlv exec` + binary pair (SIGINT, then SIGKILL
   after `kill_delay = "1s"`).
2. Re-runs `go build` with the pinned `-gcflags`.
3. Re-launches `dlv exec` on the same port.

**The IDE's dlv connection drops at step 1** — enable "auto-reconnect"
in your Go debugger setup, or re-hit *Start debugging* after each save.
VS Code's Go extension detects dropped remote sessions and offers a
one-click *Reconnect* prompt.

## Troubleshooting

- **Breakpoints don't bind.** The binary was built without
  `-gcflags='all=-N -l'`. Check that you launched via the `.debug`
  air config; if you did, delete `/tmp/air/<service>` inside the
  container and save a Go file to force a fresh rebuild:
  `docker compose -f docker-compose.yml -f docker-compose.dev.yml exec <service> rm -rf /tmp/air/<service>`.
- **`dlv: accepting multiple client connections`.** Expected with
  `--accept-multiclient`. If the IDE still refuses to connect, confirm
  no other process owns the host port (`lsof -i:2347`).
- **air rebuild loops on write-heavy IDE actions.** Raise the debounce
  by editing `delay = 500` in the relevant `.air.*.toml` and
  `make dev-restart-<service>` to pick it up.
- **"go: go.mod file not found in current directory or any parent
  directory".** `root` in the air config points at the wrong path.
  Every checked-in config pins `/workspace/devshard`; don't override
  it unless you also move the module.
- **Builds are slow on first `dev-up`.** The shared module cache is
  empty. `make dev-clean` drops it on purpose; `make dev-down`
  preserves it. Stick to `dev-down` between iterations.
- **dlv fails with "could not attach to pid … operation not
  permitted".** `SYS_PTRACE` or `seccomp:unconfined` was stripped.
  Check the service's overrides in `docker-compose.dev.yml` and
  `docker inspect <container>` for the effective `CapAdd` /
  `SecurityOpt`.

## macOS / Docker Desktop notes

- `SYS_PTRACE` and `seccomp:unconfined` work on Docker Desktop's
  Linux VM with no host-side config.
- On M-series Macs `FROM golang:1.25-alpine` resolves to `linux/arm64`
  so compiling and running happen under the same arch inside the VM.
  `Dockerfile.dev` intentionally omits `--platform`; Docker Desktop
  picks the correct variant.
- First-run rebuild is noticeably slower than subsequent runs because
  `air` populates both the module cache and the build cache inside
  the named volumes. Budget ~30 s for the first rebuild of
  `devshardd-testenv`.
