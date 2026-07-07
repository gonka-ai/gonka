# Devshard Gateway Update

Update a devshard gateway to a new image version behind nginx without dropping in-flight requests. Assumes multi-devshard **gateway-proxy** mode (routing prefix `/devshard-gateway/`) behind the nginx `proxy` container. Single-instance host, two ways — **Manual** (steps by hand) and **Automated** (one script). For a 2+ instance pool, see the Pool section below.

An **escrow** and a **devshard** are the same object. The examples assume:

```bash
KEY="$DEVSHARD_ADMIN_API_KEY"
MAIN="http://127.0.0.1:18080"
TEMP="http://127.0.0.1:18081"
REPO="ghcr.io/gonka-ai/devshard-gateway"
# <OLD> / <NEW> = current / target image tag; <MODEL> = a model id you serve
```

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
  -d '{"amount":5000000000,"model_id":"<MODEL>","private_key_env":"DEVSHARD_PRIVATE_KEY","protocol_version":"1"}'
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

**7. Bump the image tag in compose:**

```bash
sed -i "s#$REPO:<OLD>#$REPO:<NEW>#g" docker-compose.devshard-gateway.yml
```

**8. Recreate main on the new image** (compose is the image source — not an env var):

```bash
docker compose -f docker-compose.devshard-gateway.yml up -d --no-deps --force-recreate devshard-gateway
```

**9. Verify main** — status responds and a chat smoke test passes (same as step 4, against `$MAIN`).

**10. Switch nginx back to main** — same as step 5 with `devshard-gateway-temp` → `devshard-gateway`.

**11. Drain temp, then stop AND remove it** (`--restart unless-stopped` means a leftover container would resurrect and corrupt state):

```bash
curl -fsS "$TEMP/v1/status" -H "Authorization: Bearer $KEY" | jq '[.devshards[].active_requests]|max'   # until 0
docker stop devshard-gateway-temp && docker rm devshard-gateway-temp
```

**12. Import + activate the temp escrows into main** (so their funds aren't stranded) — for each temp escrow `<ID>`:

```bash
BODY='{"id":"<ID>","model":"<MODEL>","storage_path":"/root/.devshardctl/temp/escrow-<ID>/state.db","protocol_version":"1","private_key_env":"DEVSHARD_PRIVATE_KEY"}'
curl -fsS -X POST "$MAIN/v1/admin/devshards/import" -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' -d "$(jq '.active=false' <<<"$BODY")"
curl -fsS -X POST "$MAIN/v1/admin/devshards"        -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' -d "$BODY"
```

**Rollback:** if a step fails, do not switch back to a bad main — keep the alias on temp, restore `/tmp/nginx.conf.bak` and reload, and re-pull the old image tag. If you stopped after minting temp escrows, finish steps 11–12 (or settle each via `POST /v1/admin/devshards/{id}/settle`) so their funds are recovered.

## Automated update (single instance)

The script runs steps 1–12 from a JSON config (models and everything else are config-driven — nothing hardcoded).

```bash
cp scripts/update.config.sample.json update.config.json     # edit: image tags, models[], nginx, compose
./scripts/update.sh --config update.config.json run          # dry run — prints the full plan
./scripts/update.sh --config update.config.json run --run    # execute
```

- `--run` executes (default is dry-run); `--yes` skips confirmations (unattended).
- Run a single step: `./scripts/update.sh --config update.config.json <step>`.
- Recover stranded temp escrows after an aborted run: `./scripts/update.sh --config update.config.json recover` (or `recover --settle`).

The config (see [scripts/update.config.sample.json](scripts/update.config.sample.json)) holds the image (`repository`, `from_tag`, `to_tag`), the `models[]` array (`model`, `escrow_count`, `escrow_amount`), `allow_unavailable_models`, and the nginx / compose / timeout blocks. Env vars override individual fields.

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
