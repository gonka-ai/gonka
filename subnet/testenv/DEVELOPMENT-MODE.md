# Development Mode

This document explains how to run `subnet/testenv` in live-reload + debugger mode.

## What dev mode changes

Dev mode uses both compose files:

- `docker-compose.yml` (generated base stack)
- `docker-compose.dev.yml` (dev overrides)

The override switches `mock-server` and `participant-*` to the dev image (`Dockerfile.dev`) and runs them through `air` for hot reload.  
`participant-0` and `mock-server` additionally run under `dlv` (Delve) for debugging.

`subnetctl` still runs (from base compose), but it is not hot-reloaded/debugged.

## Commands

Run from `subnet/testenv/`:

```bash
make gen-compose   # optional but recommended after config/template changes
make dev-build     # build subnet-mockserver:dev and subnet-host:dev
make dev-up        # start base + dev override stack
make dev-logs      # follow logs
```

Stop:

```bash
make dev-down
```

## Debug ports

- `2345` -> `mock-server` Delve
- `2346` -> `participant-0` Delve

These map to container Delve servers. Attach from Cursor/VSCode using your launch configuration.

## File watching / reload behavior

Inside dev containers, repo root is mounted at `/workspace` and `air` watches Go source:

- `mock-server`: `.air.mockserver.debug.toml`
- `participant-0`: `.air.subnethost.debug.toml`
- `participant-1..9`: `.air.subnethost.toml`

Changing Go files under `subnet/` triggers rebuild/restart automatically for the relevant services.

## Notes

- Base compose must be up-to-date (`make gen-compose`) because dev mode is an overlay.
- `subnetctl` reads key/config from `/app/config.yaml` (mounted from `./config.yaml`).
- Subnetctl session DB persists at `./db/subnetctl` (mounted to `/root/.cache/gonka` in container).
