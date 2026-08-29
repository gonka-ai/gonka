# Devshard gateway migration to v4 (broker guide)

---

## Image

```bash
docker pull ghcr.io/gonka-ai/devshard-gateway:mainnet-v0.2.15-v4-latest
```

`-latest` is a moving tag (bugfixes land there). For production, pin the
digest you tested:

```bash
docker inspect --format '{{index .RepoDigests 0}}' \
  ghcr.io/gonka-ai/devshard-gateway:mainnet-v0.2.15-v4-latest
```

## Strategy: run v4 alongside v3

Do **not** upgrade in place. Add a second gateway service on the new image
with a **fresh storage volume** (a new `gateway.db`; v4 must not open the v3
one), mint v4 escrows on it, switch client traffic, then drain v3.

Timing: pick a window inside the **Inference** phase, never during PoC or
cPoC, with enough blocks left before the next PoC for escrow creation and
smoke tests. Check:

```bash
curl -s https://node3.gonka.ai/v1/epochs/latest | jq '{phase, epoch: .epoch_stages.epoch_index}'
```

## Step 1 — v4 environment

Compose service sketch (adapt names/ports to your deploy):

```yaml
  devshardctl-v4:
    image: ghcr.io/gonka-ai/devshard-gateway@sha256:<pinned-digest>
    restart: unless-stopped
    environment:
      DEVSHARD_PORT: "8080"
      DEVSHARD_API_KEYS: ${GATEWAY_API_KEY}
      DEVSHARD_ADMIN_API_KEY: ${DEVSHARD_ADMIN_API_KEY}
      DEVSHARD_PRIVATE_KEY: ${DEVSHARD_PRIVATE_KEY}
      DEVSHARDS_JSON: "[]"                      # multi-devshard mode, no bootstrap escrow
      DEVSHARD_CHAIN_ID: gonka-mainnet
      # v4 chain access — see notes below
      DEVSHARD_CHAIN_RPC: https://node3.gonka.ai/chain-rpc/
      DEVSHARD_CHAIN_GRPC: none
      DEVSHARD_PUBLIC_API: https://node3.gonka.ai
      DEVSHARD_STORAGE_DIR: /root/.devshardctl
      # keep escrow lifecycle manual during the migration
      DEVSHARD_ESCROW_ROTATION_ENABLED: "false"
      DEVSHARD_ESCROW_ROTATION_SETTLEMENT_ENABLED: "false"
      # accounting ledger for the tracker (see Stats section)
      DEVSHARD_STATS_ENABLED: "true"
    volumes:
      - devshard_v4_data:/root/.devshardctl     # FRESH volume, not the v3 one
    ports:
      - "127.0.0.1:18081:8080"                  # admin API: loopback only
```

Chain access notes (the part that silently breaks):

- `DEVSHARD_CHAIN_REST` is **ignored** in v4 — remove it. The admin API even
  logs `chain_rest is deprecated and ignored` if you POST it to settings.
- **Public chain node** (no gRPC exposed — this is most brokers):
  `DEVSHARD_CHAIN_RPC=https://node3.gonka.ai/chain-rpc/` — the **trailing
  slash is required**. Without it nginx replies `301`, the JSON-RPC POST
  turns into a GET and you get HTML instead of RPC.
  Set `DEVSHARD_CHAIN_GRPC=none` — otherwise the binary persists its default
  `localhost:9090` into `gateway.db` and keeps probing a dead endpoint.
- **Own chain node in the same docker network** (join stack): keep
  `DEVSHARD_CHAIN_GRPC=<node>:9090` from the join template instead; RPC is
  auto-derived. Do not copy the `none` + public-RPC config blindly.
- Do **not** carry over `DEVSHARD_ROUTE_PREFIX=/devshard/v3` from your v3 env.
  Either drop the variable or set it to `/devshard/v4` — it is the fallback
  route for escrows created without an explicit `route_prefix`.

Start it:

```bash
docker compose up -d devshardctl-v4
curl -s http://127.0.0.1:18081/v1/status | head -c 200   # should answer (empty gateway)
```

## Step 2 — freeze v3 escrow lifecycle

Nothing may settle or rotate mid-migration:

```bash
KEY="$DEVSHARD_ADMIN_API_KEY"
V3="http://127.0.0.1:18080"
curl -fsS "$V3/v1/admin/settings" -H "Authorization: Bearer $KEY" \
  | jq '.escrow_rotation.enabled = false' \
  | curl -fsS -X POST "$V3/v1/admin/settings" -H "Authorization: Bearer $KEY" \
      -H 'Content-Type: application/json' -d @-
```

## Step 3 — mint v4 escrows (per model)

```bash
V4="http://127.0.0.1:18081"
curl -fsS -X POST "$V4/v1/admin/escrows" -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "amount": 2500000000,
    "model_id": "<MODEL>",
    "private_key_env": "DEVSHARD_PRIVATE_KEY",
    "route_prefix": "/devshard/v4",
    "register": true
  }'
```

- **`route_prefix` is the v4 way.** `protocol_version` is not a field of the
  v4 request struct — it is dropped without an error, and the escrow falls
  back to `DEVSHARD_ROUTE_PREFIX` / the binary default.
- **Always pass `model_id`.** It is not validated; omitting it submits a real
  chain tx with the binary's default model and burns gas on a useless escrow.
- Repeat per model you serve (a model with no v4 escrow is unavailable after
  the switch). One escrow per model is enough to start; add redundancy later.

Wait for hosts to fill the slots, then verify:

```bash
curl -fsS "$V4/v1/admin/state" -H "Authorization: Bearer $KEY" \
  | jq '{hosts: .capacity.host_count,
         escrows: [.devshards[] | {id, model, route: .route_prefix,
                  session: .runtime.session_version, active}]}'
```

Every escrow must show `route: "/devshard/v4"` and `session: "v4"`.

## Step 4 — smoke test v4 directly

Per model, before touching client traffic:

```bash
curl -fsS -X POST "$V4/v1/chat/completions" \
  -H "Authorization: Bearer $GATEWAY_API_KEY" -H 'Content-Type: application/json' \
  -d '{"model":"<MODEL>","max_tokens":16,"messages":[{"role":"user","content":"ok"}]}'
```

If you serve tool-calling clients, also run one request with `tools` +
`tool_choice` and check `finish_reason: "tool_calls"` — tool flows are exactly
what broke on earlier v4 builds.

## Step 5 — switch client traffic

Repoint whatever fronts the gateway (your proxy env, nginx/Caddy upstream)
from the v3 service to the v4 one, and reload/recreate that component only.
In a compose setup this is a one-line change
(`GATEWAY_URL: http://devshardctl-v4:8080/v1` or the upstream host in the
router config).

## Step 6 — drain v3

**Do not drain via `GET /v1/status` on v4** (the v3-era harness does exactly that) 
— and be careful even on v3 late in the drain: with a single loaded
escrow `GET /v1/status` returns a legacy escrow card with no `.devshards[]`, so
`jq '[.devshards[].active_requests]|max'` prints `0` while streams are still
running. The reliable source is the admin state:

```bash
# v3 (being drained):
curl -fsS "$V3/v1/admin/state" -H "Authorization: Bearer $KEY" \
  | jq '{in_flight: .limiter.in_flight_requests,
         per_escrow: [.devshards[] | {id, active_req: .runtime.active_requests}]}'
```

Repeat until `in_flight` is `0`. Long SSE streams legitimately take minutes.

## Step 7 — keep v3, verify v4

- **Keep the v3 container and volume** until in-flight requests finish. Nothing about v4 requires deleting
  v3, and the untouched volume is your rollback.
- Verify traffic is flowing via `/devshard/v4`: gateway logs show your
  escrows with `session_version=v4`, and `GET /v1/admin/state` on v4 shows
  `active_requests > 0` under real load.

## Stats / tracker

`DEVSHARD_STATS_ENABLED=true` starts the accounting API on `0.0.0.0:9091`
(inside the container). It has **no authentication** — never publish the port
to the internet. Expose a read-only slice behind your own proxy instead
(GET-only allowlist of `/api/v1/epochs...`), and share that public URL with
@TechSupportEngineerOps to get listed on tracker.gonka.pro/gateway.

## Rollback

1. Point client traffic back at the v3 service (undo step 5).
2. Re-enable v3 escrow rotation if you disabled it (undo step 2).
3. Leave the v4 container running until in-flight requests finish.

## Pitfalls checklist (each one bit someone)

- [ ] Fresh volume for v4 — never point it at the v3 `gateway.db`.
- [ ] `DEVSHARD_CHAIN_RPC` has the trailing slash.
- [ ] `DEVSHARD_CHAIN_GRPC=none` on public-node setups (or your real
      `node:9090` on join stacks) — no silent `localhost:9090`.
- [ ] No `DEVSHARD_ROUTE_PREFIX=/devshard/v3` left in the v4 env.
- [ ] Escrows minted with `route_prefix`, not `protocol_version`.
- [ ] `model_id` present in every escrow mint.
- [ ] Drain checked via `/v1/admin/state`, not `/v1/status`.
- [ ] Rotation/auto-settle off during the whole migration.
- [ ] Whole window inside Inference phase, away from PoC/cPoC.
- [ ] `:9091` not exposed publicly.
