# Devshard Gateway Update

Update a devshard gateway to a new image version without dropping in-flight requests. Full blue/green updates support nginx or Caddy routing; weighted canary updates are nginx-only. Single-instance host, two ways — **Manual** (nginx steps by hand) and **Automated** (one script). For a 2+ instance pool, see the Pool section below.

An **escrow** and a **devshard** are the same object. The examples assume:

```bash
KEY="$DEVSHARD_ADMIN_API_KEY"
MAIN="http://127.0.0.1:18080"
TEMP="http://127.0.0.1:18081"
REPO="ghcr.io/gonka-ai/devshard-gateway"
# <OLD> / <NEW> = current / target image tag; <MODEL> = a model id you serve
```

## Current mainnet v3 target

Use `ghcr.io/gonka-ai/devshard-gateway:mainnet-v0.2.13-v3-post1`.
Verify registry access before starting:

```bash
docker pull ghcr.io/gonka-ai/devshard-gateway:mainnet-v0.2.13-v3-post1
```

For the v1/v2 → v3 migration, prefer the automated workflow below. Configure
the exact image currently present in compose as `image.from_tag`, the image
above as `image.to_tag`, the running protocol as
`escrow.source_protocol_version`, and target protocol / route as `"3"` /
`/devshard/v3`. Keep `models[].main_seed_count >= 1`. Older gateways may omit
`protocol_version`; the script treats missing values as source-protocol state
and deactivates them locally without settlement before starting v3.

## Manual update (single instance)

Run from the gateway host's deploy dir (e.g. `deploy/join`). A temp gateway holds traffic while main updates.

**1. Disable escrow rotation on main** (so nothing settles on-chain mid-update):

```bash
curl -fsS "$MAIN/v1/admin/settings" -H "Authorization: Bearer $KEY" \
  | jq '.escrow_rotation.enabled = false' \
  | curl -fsS -X POST "$MAIN/v1/admin/settings" -H "Authorization: Bearer $KEY" \
      -H 'Content-Type: application/json' -d @-
```

**2. Start a temp gateway** on the new image, with its own storage and no escrows; reuse main's env file (keeps secrets out of `/tmp`):

```bash
docker run -d --name devshard-gateway-temp --restart unless-stopped \
  --network join_default --network-alias devshard-gateway-temp \
  --env-file ./config.devshard.env \
  -e DEVSHARDS_JSON='[]' -e DEVSHARD_STORAGE_DIR=/root/.devshardctl/temp -e DEVSHARD_PORT=8080 \
  -p 127.0.0.1:18081:8080 -v "$PWD/.devshardctl:/root/.devshardctl" \
  "$REPO:<NEW>"
curl -fsS "$MAIN/v1/admin/settings" -H "Authorization: Bearer $KEY" \
  | jq '.escrow_rotation.enabled = false | .escrow_rotation.models = []' \
  | curl -fsS -X POST "$TEMP/v1/admin/settings" -H "Authorization: Bearer $KEY" \
      -H 'Content-Type: application/json' -d @-
```

**3. Mint temp escrows — for every model main serves** (a model with no temp escrows is unavailable during the update; each mint spends on-chain funds):

```bash
curl -fsS -X POST "$TEMP/v1/admin/escrows" -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' \
  -d '{"amount":5000000000,"model_id":"<MODEL>","private_key_env":"DEVSHARD_PRIVATE_KEY","protocol_version":"<TARGET_PROTOCOL>"}'
# repeat per escrow and per model; then record the created ids:
curl -fsS "$TEMP/v1/admin/devshards" -H "Authorization: Bearer $KEY" | jq '.devshards[].id'
```

**4. Verify temp** — status responds and a chat smoke test passes (test each model):

```bash
curl -fsS "$TEMP/v1/status" -H "Authorization: Bearer $KEY" | jq '[.devshards[].active_requests]|length'
curl -fsS -X POST "$TEMP/v1/chat/completions" -H 'Content-Type: application/json' \
  -d '{"model":"<MODEL>","max_tokens":1,"messages":[{"role":"user","content":"ok"}]}' >/dev/null && echo ok
```

**5. Switch nginx to temp** (graceful reload; in-flight streams finish). This matches both a direct `proxy_pass http://host:8080` and a named-upstream `server host:8080;`:

```bash
docker exec proxy sh -lc "cp /etc/nginx/nginx.conf /tmp/nginx.conf.bak && \
  sed -E -i 's#http://devshard-gateway:8080#http://devshard-gateway-temp:8080#g; \
             s#(server[[:space:]]+)devshard-gateway:8080#\1devshard-gateway-temp:8080#g' /etc/nginx/nginx.conf && \
  nginx -t && nginx -s reload"
```

**6. Drain main** — repeat until this prints `0`:

```bash
curl -fsS "$MAIN/v1/status" -H "Authorization: Bearer $KEY" | jq '[.devshards[].active_requests]|max'
```

**Protocol change only:** deactivate each active source-protocol escrow after
main drains and before recreating it. Use
`POST /v1/admin/devshards/<ID>/deactivate`. Do not settle or delete it.

**7. Bump the image tag in compose:**

```bash
sed -i "s#$REPO:<OLD>#$REPO:<NEW>#g" docker-compose.devshard-gateway.yml
```

**8. Recreate main on the new image** (compose is the image source — not an env var):

```bash
docker compose -f docker-compose.devshard-gateway.yml up -d --no-deps --force-recreate devshard-gateway
```

**Protocol change only:** create at least one target-protocol seed escrow per
served model on main before returning traffic.

**9. Verify main** — status responds, target-protocol seed escrows are active,
and a chat smoke test passes (same as step 4, against `$MAIN`).

**10. Switch nginx back to main** — same as step 5 with `devshard-gateway-temp` → `devshard-gateway`.

**11. Drain temp, then stop AND remove it** (`--restart unless-stopped` means a leftover container would resurrect and corrupt state):

```bash
curl -fsS "$TEMP/v1/status" -H "Authorization: Bearer $KEY" | jq '[.devshards[].active_requests]|max'   # until 0
docker stop devshard-gateway-temp && docker rm devshard-gateway-temp
```

**12. Import + activate the temp escrows into main** (so their funds aren't stranded) — for each temp escrow `<ID>`:

```bash
BODY='{"id":"<ID>","model":"<MODEL>","storage_path":"/root/.devshardctl/temp/escrow-<ID>/state.db","protocol_version":"<TARGET_PROTOCOL>","private_key_env":"DEVSHARD_PRIVATE_KEY"}'
curl -fsS -X POST "$MAIN/v1/admin/devshards/import" -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' -d "$(jq '.active=false' <<<"$BODY")"
curl -fsS -X POST "$MAIN/v1/admin/devshards"        -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' -d "$BODY"
```

**Rollback:** if a step fails, do not switch back to a bad main — keep the alias on temp, restore `/tmp/nginx.conf.bak` and reload, and re-pull the old image tag. If you stopped after minting temp escrows, finish steps 11–12 (or settle each via `POST /v1/admin/devshards/{id}/settle`) so their funds are recovered.

## Automated update (single instance)

The script runs steps 1–12 from a JSON config (models and everything else are config-driven — nothing hardcoded).

Set `routing.type` to `nginx` (the backward-compatible default) or `caddy`.
Backend-specific switch functions stay independent; the update workflow only
selects whether traffic moves to temp or main. `update-canary.sh` supports nginx
only.

Gateway-2 requires an additional external gate: remove `10.0.1.5:18080` from
node4's active split and fallback path before the update. Node4 connects to the
host port directly and bypasses gateway-2's Caddy configuration.

### Before a protocol-changing update

The script stages the target route in the gateway env file (after making a
backup) before it creates temp. Before starting a protocol change:

1. Set `escrow.route_prefix` to the target route, for example
   `/devshard/v3`.
2. Verify the running main container still has the source route. Docker reads
   its env only when the container is created, so the existing main remains on
   v2 while temp and the later recreated main read v3.
3. Set `escrow.source_protocol_version` to the running protocol and
   `escrow.protocol_version` to the target (`"2"` and `"3"` for v2 to v3).
4. Set `models[].main_seed_count` to at least `1` for every served model.

The public client path remains `/devshard-gateway/v1/...`.

Schedule a protocol-changing run shortly after `set_new_validators`, once the
chain is back in `Inference`. Ensure enough blocks remain before the next PoC
for escrow creation, smoke checks, both nginx switches, and draining. On chains
with a two-epoch pruning threshold, use fresh source and temp escrows; do not
resume an interrupted run after its escrows cross that threshold.

After nginx moves traffic to temp and main drains, the script deactivates the
source-protocol escrows on main. Deactivation is local only: it does **not**
settle, delete, or transfer funds. This prevents the target binary from trying
to open incompatible source-protocol session state.

The default `deactivation.mode=api` calls the drained source gateway's admin
endpoint. Some legacy images do not provide that route. For those images only,
set `deactivation.mode=sqlite` and `deactivation.gateway_db`: the script stops
MAIN after the drain gate, backs up `gateway.db*`, marks only active
source-protocol records inactive, and requires `PRAGMA integrity_check` to
return `ok` before continuing.

After main restarts on the target image, the script creates the configured
target-protocol seed escrows and proves direct inference before switching the
selected router back. Temp is then drained and stopped before its escrows are
imported and activated on main, preserving one writer per escrow.

Temp escrow rotation is deliberately disabled throughout this handoff. Draining
means `active_requests == 0`; it does not exhaust escrows. If
`rotation.restore_after_update` is true, the script snapshots MAIN's complete
settings before disabling rotation and restores the exact original
`escrow_rotation` object afterward. This preserves enabled state, settlement
state, and per-model rotation targets.

```bash
cp scripts/update.config.sample.json update.config.json     # edit: image tags, models[], nginx, compose
./scripts/update.sh --config update.config.json run          # dry run — prints the full plan
./scripts/update.sh --config update.config.json run --run    # execute
```

- `--run` executes (default is dry-run); `--yes` skips confirmations (unattended).
- Run a single step: `./scripts/update.sh --config update.config.json <step>`.
- Recover stranded temp escrows after an aborted run: `./scripts/update.sh --config update.config.json recover` (or `recover --settle`).

The config (see [scripts/update.config.sample.json](scripts/update.config.sample.json)) holds the image (`repository`, `from_tag`, `to_tag`, `skip_pull`), the `models[]` array (`model`, temp `escrow_count`, `main_seed_count`, `escrow_amount`), source/target escrow protocol versions, explicit deactivation mode, `allow_unavailable_models`, generic `routing`, the selected `nginx` or `caddy` block, and compose / timeout settings. A gateway-2 starting point is provided at [update.config.gateway2.json](update.config.gateway2.json). Set `image.skip_pull=true` only when the exact target image is already loaded locally; the script verifies it with `docker image inspect` and skips both registry pulls. For same-protocol image updates, source and target protocol are equal and `main_seed_count` may remain `0`. Env vars override individual fields.

## Restore the nginx backup

The switch backs up the nginx config **before every change**, inside the `proxy` container next to the config: `<config_path>.blue-green-backup` (e.g. `/etc/nginx/nginx.conf.blue-green-backup`). The manual step 5 above instead writes `/tmp/nginx.conf.bak`. To revert routing, restore whichever you used and reload:

```bash
docker exec proxy sh -lc "cp /etc/nginx/nginx.conf.blue-green-backup /etc/nginx/nginx.conf && nginx -t && nginx -s reload"
# manual path: cp /tmp/nginx.conf.bak /etc/nginx/nginx.conf && nginx -t && nginx -s reload
```

Confirm the upstream it now points at:

```bash
docker exec proxy sh -lc "grep -nE 'devshard.*:8080' /etc/nginx/nginx.conf"
```

**Caveat — the backup is overwritten on each switch.** It holds the routing as of just *before the most recent switch*: after `switch-to-main` it points back at **temp**, not the pristine original. For a guaranteed clean revert, copy the original aside before you start:

```bash
docker exec proxy sh -lc "cp /etc/nginx/nginx.conf /etc/nginx/nginx.conf.orig"
```

## Pool (multi-instance)

If 2+ gateway instances sit behind nginx, update one at a time — no temp gateway:

1. Remove one instance from the nginx `upstream` (comment its `server` line or mark it `down`); `nginx -t && nginx -s reload`.
2. Drain it: `GET /v1/status` until `max(.devshards[].active_requests) == 0`.
3. Bump its image tag in compose; `docker compose -f <file> up -d --no-deps --force-recreate <service>`.
4. Verify: `/v1/status` responds and a `POST /v1/chat/completions` returns.
5. Return it to the pool; reload. Repeat for the next instance.

## References

- [references/admin-api.md](references/admin-api.md) — gateway admin endpoints used here.
- [references/nginx-alias-switching.md](references/nginx-alias-switching.md) — how the upstream switch keeps in-flight streams alive.
- [references/troubleshooting.md](references/troubleshooting.md) — drain stalls, unroutable escrows, rollback.
- [SKILL.md](SKILL.md) — operator/agent skill wrapping this process.
