# Reference — Gateway Admin API (migration subset)

The endpoints a broker touches during a zero-downtime update. All admin routes require `Authorization: Bearer $DEVSHARD_ADMIN_API_KEY`; `/v1/status` is public. Handlers live in `devshard/cmd/devshardctl/gateway.go`.

## Status & draining

| Method / path | Purpose |
|---|---|
| `GET /v1/status` | Public health + per-devshard runtime snapshot. **The drain gate.** Each devshard reports `active_requests`. |
| `GET /v1/admin/devshards` | Full admin list of persisted devshards (active + inactive) with model, protocol_version, storage, balance. |
| `GET /v1/admin/state` | Broader admin snapshot (devshards + effective settings). |

**Drain check** — poll until zero:

```bash
curl -fsS http://127.0.0.1:18080/v1/status \
  -H "Authorization: Bearer $DEVSHARD_ADMIN_API_KEY" \
  | jq '[.devshards[]?.active_requests // 0] | max // 0'
```

`active_requests` is an atomic counter per runtime, incremented on request reserve and decremented on release, then serialized under `devshards[].active_requests`. It is also exported as the Prometheus gauge `devshard_runtime_active_requests`.

## Settings

| Method / path | Purpose |
|---|---|
| `GET /v1/admin/settings` | Effective persisted gateway settings. |
| `POST /v1/admin/settings` | Update settings live (e.g. `escrow_rotation`, `max_concurrent_requests`). |

During a migration, copy main's settings to the temp gateway but force rotation off so temp never auto-settles:

```bash
curl -fsS http://127.0.0.1:18080/v1/admin/settings -H "Authorization: Bearer $KEY" \
  | jq '.escrow_rotation.enabled = false | .escrow_rotation.models = []' \
  | curl -fsS -X POST http://127.0.0.1:18081/v1/admin/settings -H "Authorization: Bearer $KEY" \
      -H 'Content-Type: application/json' -d @-
```

`escrow_rotation.enabled = false` is the master off switch — it stops the rotator and thus all auto-settlement. Disable it on **MAIN** directly (a running gateway keeps whatever is in `gateway.db`, not the first-boot default); `settlement_enabled` and `models` are finer controls you don't need to touch for a swap.

## Escrow lifecycle

| Method / path | Purpose |
|---|---|
| `POST /v1/admin/escrows` | Create a **new** escrow on-chain (`amount`, `model_id`, `private_key_env`, `protocol_version`) and optionally register it. Used to seed **temp** escrows. |
| `POST /v1/admin/devshards/import` | Import an **existing** escrow into this gateway by `id` + `storage_path` + `private_key_env` (+ optional `perf_path`), with `active:false`. Carries an escrow's sqlite state into a fresh gateway process — the storage-portability tool. |
| `POST /v1/admin/devshards` | Register / **activate** a devshard (`id`, `model`, `storage_path`, `protocol_version`, `private_key_env`). Adds it to the routing pool. |
| `POST /v1/admin/devshards/{id}/deactivate` | Set `active=false` — removes from the routing pool, keeps the runtime loaded, **does not settle**. In-flight requests finish. |
| `POST /v1/admin/devshards/{id}/settle` | Deliberately settle an escrow on-chain (drain-aware). |
| `DELETE /v1/admin/devshards/{id}` | Remove a devshard cleanly. |

## protocol_version

The gateway accepts `"1"` / `"v1"`, `"2"` / `"v2"`, and `"3"` / `"v3"`;
stored values are normalized to `"1"`, `"2"`, and `"3"`.

This per-escrow value is separate from the gateway image's build-time protocol
and `DEVSHARD_ROUTE_PREFIX`. During a protocol change, deactivate source
escrows before recreating main on the target image. Otherwise the target binary
can fail startup while recovering incompatible source session state (for
example, `stored v2, requested v3`).

## Notes for migration

- **Import needs `storage_path`.** It is how an existing escrow's `state.db` is reattached to a new process. Point it at the temp escrow's container path (e.g. `/root/.devshardctl/temp-<run-id>/escrow-<id>/state.db`).
- **Create vs import:** `create` mints a *new* on-chain escrow; `import` re-attaches an *existing* one. Temp escrows are `create`d, then later `import`ed into main.
- **Deactivate ≠ settle.** Deactivating during a swap is safe and reversible; settlement is a separate, config-gated, drain-aware action.
- **`escrow` and `devshard` are the same object.** `create` mints it (`/v1/admin/escrows`); everything else lists/imports/activates/deactivates it (`/v1/admin/devshards`, and it appears under `/v1/status .devshards[]`).
- **Field name differs by call:** `create` takes `model_id`; `import` and `activate` take `model`.
