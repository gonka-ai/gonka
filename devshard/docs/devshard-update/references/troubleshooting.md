# Reference — Troubleshooting & rollback

Symptoms, causes, and fixes during a zero-downtime gateway update. Endpoints in [admin-api.md](admin-api.md); nginx behavior in [nginx-alias-switching.md](nginx-alias-switching.md).

## Drain never reaches zero

`active_requests` stays above 0 past the poll window.

- **A long stream is genuinely running.** Large `max_tokens` / slow model can hold a request for minutes. Raise `timeouts.drain_timeout_seconds` in the config (default `7200`) rather than force-killing.
- **A hung/abandoned stream.** A client that vanished mid-SSE can leave a request parked. Confirm via `GET /v1/status` per devshard; if one runtime is stuck with a client that's gone, decide whether to wait out the server-side timeout or accept dropping that single request before recreate.
- **New requests still arriving.** The nginx switch didn't take. Re-check routing through the **public** URL — you may have verified loopback only.

## Escrows missing / not routable after update

`GET /v1/admin/devshards` returns empty or fewer than expected.

- **Volume not mounted.** The new container didn't get `/root/.devshardctl`. The gateway then ran the first-boot env path instead of loading `gateway.db`. Stop it, fix the `-v .../.devshardctl:/root/.devshardctl` mount, restart — nothing is lost, sqlite is still on disk.
- **Imported escrow inactive.** After `import-temp` the escrow is `active:false` by design. Run `activate-temp` (`POST /v1/admin/devshards`) to add it to the routing pool.
- **Wrong `storage_path` on import.** Import re-attaches an escrow's `state.db` by path. If the path is wrong the escrow imports but can't serve. Point it at the temp escrow's real container path (e.g. `/root/.devshardctl/temp-<run-id>/escrow-<id>/state.db`).

## nginx reload fails

- **`nginx -t` errors.** The switch is blocked (old workers keep serving — no outage). Fix the config or restore the backup `<backup-dir>/nginx.conf.blue-green-backup`, then reload.
- **`sed` matched nothing.** The upstream pattern didn't match (a `proxy_pass` via upstream *name*, a variable, a different port, or already switched). Inspect with `nginx -T`; set `nginx.old_upstream` / `nginx.new_upstream` / `nginx.upstream_port` in the config.

## Temp gateway won't start

- **Admin port clash.** `TEMP_ADMIN_PORT` (default `18081`) is already bound. Pick a free port.
- **Same escrow as main.** Never let temp bootstrap the main escrows — it starts with `DEVSHARDS_JSON=[]` for a reason (one writer per escrow). If you see nonce/state errors, temp is fighting main over an escrow; give temp its own fresh escrows only.

## MAIN came back on the old version

`update-main` recreates MAIN from the compose file using the image tag pinned there. The `bump-main-image` step rewrites that tag from `image.from_tag` to `image.to_tag` before `update-main` runs. If MAIN came back on the old version, the tag wasn't bumped — confirm `bump-main-image` ran (running steps by hand, edit the compose image tag before `update-main`).

## New image is bad

- Do **not** `switch-to-main` (Path B) or return the instance to the pool (Path A).
- Re-pull the previous image tag and `--force-recreate` back to it; verify `check-main-direct`; then retry.
- Routing rollback is instant: restore the nginx backup and reload, or keep the alias pointed at the still-good temp/other instance.

## Settlement fired unexpectedly

- `escrow_rotation.enabled = false` is the master switch — it stops the rotator and all auto-settlement. The `disable-main-rotation` step sets it on MAIN; if a settle fired mid-migration, rotation was still on there (a running gateway keeps whatever is in `gateway.db`, not the first-boot default). Settle deliberately afterward via `POST /v1/admin/devshards/{id}/settle`.
- A settle that was *queued* (`settlement_queued_waiting_for_drain`) is normal and harmless — it waits for `active_requests == 0` and does not drop requests.

## Rollback checklist

1. Stop routing new traffic to the bad target (restore nginx backup + reload, or keep alias on the good side).
2. Confirm the good side is serving via the **public** URL smoke test.
3. Recreate the bad container on the last-known-good image; verify direct.
4. Only then resume the normal step order.
