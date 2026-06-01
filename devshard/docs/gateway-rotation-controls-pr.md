# Devshard Gateway Rotation Controls

## Summary

The devshard gateway has two automatic escrow rotation paths:

- Epoch/PoC rotation creates temporary bridge escrows before PoC, then returns to regular escrows after PoC.
- Depletion rotation creates a replacement escrow when an active escrow is low on balance or close to the nonce limit.

This change makes `escrow_rotation.enabled` the master switch for both paths. When it is `false`, the gateway no longer performs automatic PoC rotation or low-balance/high-nonce replacement.

It also adds `escrow_rotation.settlement_enabled`. When rotation is enabled but settlement is not enabled, the gateway still creates replacement escrows and locally deactivates old escrows, but it skips automatic finalization and on-chain settlement. Manual settlement through the admin API remains available.

For first-boot env config, `DEVSHARD_ESCROW_ROTATION_SETTLEMENT_ENABLED=false` is the default and maps to `escrow_rotation.settlement_enabled=false`.

## Behavior

- `escrow_rotation.enabled=false`: no automatic escrow rotation.
- `escrow_rotation.enabled=true` and `settlement_enabled=true`: create replacements, deactivate old escrows, and settle them on-chain.
- `escrow_rotation.enabled=true` and `settlement_enabled=false`: create replacements and deactivate old escrows locally, but do not auto-settle.

`GET /v1/admin/settings` shows the persisted gateway settings from `gateway.db`, which are the effective settings after first boot.

## Test Plan

- `(cd devshard && go test ./cmd/devshardctl)`
