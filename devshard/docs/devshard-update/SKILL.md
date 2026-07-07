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

## Safety rules

- Treat any live gateway host as production. Show the exact command and get approval before running it.
- Dry-run first (`run` without `--run`) and read the plan.
- MAIN's image comes from the compose file; the `bump-main-image` step rewrites the tag from `image.from_tag` to `image.to_tag`.
- Temp escrows must cover every model MAIN serves — `preflight` fails closed if a served model has no temp coverage and isn't in `allow_unavailable_models`.
- Public verification runs only if `nginx.public_base_url` is set — never trust loopback.
- Do not assume the nginx config path — inspect with `docker exec <proxy> sh -lc 'nginx -T'`.

## Config

`scripts/update.config.sample.json` — blocks: `image {repository, from_tag, to_tag}`, `models[] {model, escrow_count, escrow_amount}`, `allow_unavailable_models[]`, `escrow`, `main`, `temp`, `nginx`, `compose`, `timeouts`, `rotation`. Any field is overridable by the matching env var.

## Steps & actions

Full-flow order (aborts on the first failed gate and names the step):
`init` → `preflight` → `disable-main-rotation` → `create-temp-gateway` → `create-temp-escrows` → `check-temp` → `switch-to-temp` → `check-alias-temp` → `drain-main` → `bump-main-image` → `update-main` → `check-main-direct` → `switch-to-main` → `check-alias-main` → `drain-temp` → `stop-temp` → `import-temp` → `activate-temp` → `restore-main-rotation` → `status`.

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
