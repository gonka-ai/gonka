---
name: devshard-update
description: Update a Gonka devshard gateway to a new image version without dropping in-flight /v1/chat/completions requests, driven by a JSON config. Single-instance blue/green (temp gateway + nginx switch + drain + config-driven temp escrows + import); a recover command reclaims stranded temp escrows; multi-instance pools use a rolling update.
---

# Devshard Gateway Update

Companion to [README.md](README.md) (manual steps + the command) and [scripts/update.sh](scripts/update.sh).

## One command (single instance)

```bash
cp scripts/update.config.sample.json update.config.json     # edit: image tags, models[], nginx, compose
./scripts/update.sh --config update.config.json run          # dry run — prints the plan
./scripts/update.sh --config update.config.json run --run    # execute
```

Add `--yes` for unattended. Everything is config-driven (models included) — nothing is hardcoded.

## Canary (operator-paced, no built-in wait)

Same config; temp = target image, MAIN stays on source until `finish`. You wait between steps:

```bash
./scripts/update-canary.sh --config update.config.json prepare --run
./scripts/update-canary.sh --config update.config.json set 10 --run
./scripts/update-canary.sh --config update.config.json check --run   # wait yourself, re-check
./scripts/update-canary.sh --config update.config.json set 50 --run
./scripts/update-canary.sh --config update.config.json check --run
./scripts/update-canary.sh --config update.config.json finish --run  # 100% temp → recreate MAIN → fold
```

Modes: `prepare` | `set <0..100>` | `check` | `finish` | `recover`. Mid-canary uses nginx upstream **weights**; `0`/`100` pin a single host. Hard-coded `proxy_pass http://host:port` paths do not follow weights — `check` warns.

## Safety rules

- Treat any live gateway host as production. Show the exact command and get approval before running it.
- Dry-run first (`run` without `--run`) and read the plan.
- MAIN's image comes from the compose file; the `bump-main-image` step rewrites the full compose image ref from `image.from_tag` to `image.to_tag` (same-repo tag bump via `image.repository`, or full `repo:tag` strings in both fields for cross-repo moves).
- For a protocol change, the script stages `DEVSHARD_ROUTE_PREFIX` in the gateway env file during `create-temp-gateway` / canary `prepare` (backup first; default `/devshard/v<protocol>` or set `escrow.route_prefix`). Running MAIN keeps its old env until recreate; temp and the recreated MAIN read the target route.
- For a protocol change, configure `escrow.source_protocol_version`, target `escrow.protocol_version`, and `models[].main_seed_count >= 1`. The script deactivates source escrows without settlement before recreating main, then creates target seed escrows before switching traffic back.
- Schedule protocol changes shortly after `set_new_validators` in `Inference`, with fresh escrows and enough blocks before the next PoC. Do not resume after the chain's escrow-pruning threshold.
- Temp escrows must cover every model MAIN serves — `preflight` fails closed if a served model has no temp coverage and isn't in `allow_unavailable_models`.
- Public verification runs only if `nginx.public_base_url` is set — never trust loopback.
- Do not assume the nginx config path — inspect with `docker exec <proxy> sh -lc 'nginx -T'`.

## Config

`scripts/update.config.sample.json` — blocks: `image {repository?, from_tag, to_tag, skip_pull}` (`from_tag`/`to_tag` may be bare tags joined with `repository`, or full `repo:tag` refs for cross-registry upgrades), `models[] {model, escrow_count, main_seed_count, escrow_amount}`, `allow_unavailable_models[]`, `escrow {source_protocol_version, protocol_version, private_key_env}`, `main`, `temp`, `nginx`, `compose`, `timeouts`, `rotation`. Use `image.skip_pull=true` only when the exact target image is already loaded locally; both registry pulls are then skipped. Any field is overridable by the matching env var.

## Steps & actions

Full-flow order (aborts on the first failed gate and names the step):
`init` → `preflight` → `disable-main-rotation` → `create-temp-gateway` → `create-temp-escrows` → `check-temp` → `switch-to-temp` → `check-alias-temp` → `drain-main` → `deactivate-main-source` → `bump-main-image` → `update-main` → `create-main-seed-escrows` → `check-main-direct` → `switch-to-main` → `check-alias-main` → `drain-temp` → `stop-temp` → `import-temp` → `activate-temp` → `restore-main-rotation` → `status`.

- Single step: `./scripts/update.sh --config <file> <step> --run`.
- Resume: `... run --run --from-step drain-main`.
- Recover stranded temp escrows after an aborted run: `./scripts/update.sh --config <file> recover --run` (add `--settle` to settle them instead of folding into main).
- `plan` / `validate` / `list-steps` for inspection (no side effects).

## Remote execution

```bash
ssh -p <port> <user>@<host> "$(cat <<'REMOTE'
set -euo pipefail
cd <deploy-dir>
./scripts/update.sh --config update.config.json run --run --yes
REMOTE
)"
```
