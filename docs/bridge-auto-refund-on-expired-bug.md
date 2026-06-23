# Bug: auto-refund does not run when BLS threshold signing expires (live testnet)

**Status:** Open  
**GitHub:** [#1352](https://github.com/gonka-ai/gonka/issues/1352)  
**Severity:** High (user funds locked in bridge escrow until manual cancel)  
**Chain:** `gonka-testnet-4` (3-node cluster on `xj7-5.s.filfox.io`)  
**Component:** `x/inference` bridge escrow + `x/bls` failure hooks  
**Manual test case:** #18 (auto-refund on `EXPIRED`)  
**Workaround:** Case #19 — `cancel-bridge-operation` with **plaintext** `request_id` from `bridge_mint_requested`

---

## Summary

When outbound bridge mint BLS signing reaches terminal `EXPIRED` (APIs stopped, threshold never reached), the chain emits `EventThresholdSigningFailed` but **does not** emit `bridge_operation_auto_refunded`, **does not** refund escrow, and leaves BLS status as `EXPIRED` (not `CANCELLED`). Manual cancel with the correct plaintext `request_id` refunds successfully.

Unit tests pass because they call `ProcessAutoRefundForFailedBridgeOperation` directly and/or insert pending refunds **after** expiry **without** wiring `BlsHooks` on the BLS keeper.

---

## Expected behavior

1. `RequestBridgeMint` locks GNK in `bridge_escrow` and stores pending refund in `BridgeMintRefundsMap` (key = `hex(keccak256(plaintext_request_id))`).
2. BLS collects partial signatures; after `signing_deadline_blocks` × `max_signing_attempts` (10 × 3 ≈ 30 blocks) with no quorum → terminal `EXPIRED`.
3. `finalizeFailedThresholdSigningRequest` → `maybeCloseRetryAfterFailedPostProcess` → `BlsHooks.AfterThresholdSigningFailed` → `ProcessAutoRefundForFailedBridgeOperation`.
4. Escrow returns GNK to minter; pending map entry removed; event `bridge_operation_auto_refunded`; BLS status → `CANCELLED` (`closeRetry=true`).
5. User does **not** need to call `cancel-bridge-operation`.

---

## Actual behavior (live testnet, reproduced twice)

| Step | Expected | Actual |
|------|----------|--------|
| Terminal BLS status | `EXPIRED` or `CANCELLED` | `EXPIRED` |
| `bridge_operation_auto_refunded` | Present in `finalize_block_events` | **Absent** |
| Bridge escrow | −1 GNK | **+1 GNK locked** |
| Minter balance | +1 GNK | Unchanged |
| Manual cancel (correct `request_id`) | N/A or “not found” if auto-refund ran | **Refunds successfully** |

---

## Evidence — run A (first attempt, seed `api` still running on 18222)

| Field | Value |
|-------|--------|
| Setup | `api` stopped on join nodes `18223`, `18226` only; seed `18222` `api` still up |
| Mint tx | `8E610948CB1623381F1EB131A7242E6DED87A21CA233F8CD1178A37B74BD3C71` |
| Mint height | `35726` |
| BLS deadline (attempt 1) | `35736` |
| Terminal BLS status | `EXPIRED` (~block `35756`, attempt 3) |
| Auto-refund event | **No** |
| Escrow while `EXPIRED` | +1 GNK vs pre-mint |
| Manual cancel | Succeeded (~block `35758`); BLS → `CANCELLED`; funds returned |

---

## Evidence — run B (primary reproduction, all `api` stopped)

### Environment

- Chain: `gonka-testnet-4`
- Host: `702111` (`ssh decentai@xj7-5.s.filfox.io -p 18222`)
- Sepolia bridge: `0x53eA3fF2057B7B7fb3d96A4ef63AE10558c08A9b`
- Minter: `gonka-account-key` → `gonka1fvp2q5ly3su27q40nzh8f2cgymwudqa3ar2zmj`
- Escrow: `gonka1cjwjmyguyjaey70cgxxclxjh4wph3c8w0vvv63`
- BLS params: `signing_deadline_blocks=10`, `max_signing_attempts=3`
- Test procedure: stop `api` on **18222, 18223, 18226**; mint 1 GNK; wait for terminal `EXPIRED`; **do not** cancel; observe balances/events

### Mint @ block 36114

| Field | Value |
|-------|--------|
| Mint height | `36114` |
| BLS `request_id` (base64) | `KDTcSiJ3Jrv45kvMOivlDDznH4MT3bKLB1QYQKRpEuA=` |
| BLS key (hex) | `2834dc4a227726bbf8e64bcc3a2be50c3ce71f8313ddb28b07541840a46912e0` |
| Plaintext `request_id` length | 738 chars (`req_36114_0acd010aca...`) |
| `keccak256(plaintext)` | **Matches** BLS `request_id` above ✓ |
| First attempt deadline | `36124` |
| Final attempt deadline | `36144` |
| Epoch | `100` |
| Post-mint escrow | `29000000000` ngonka (+1 GNK vs `28000000000`) |

### Terminal failure @ block 36144

```text
finalize_block_events:
  inference.bls.EventThresholdSigningFailed   ← present
  bridge_operation_auto_refunded              ← absent
```

BLS signing history (after expiry):

```json
{
  "request_id": "KDTcSiJ3Jrv45kvMOivlDDznH4MT3bKLB1QYQKRpEuA=",
  "status": "THRESHOLD_SIGNING_STATUS_EXPIRED",
  "created_block_height": "36114",
  "deadline_block_height": "36144",
  "attempt": 3,
  "current_epoch_id": "100"
}
```

Balances while `EXPIRED`: escrow `29000000000`, minter unchanged.

### Manual cancel (Case #19) — success

| Field | Value |
|-------|--------|
| Cancel tx | `9AA0F01607708D5AA2CAF0D0BD0D3CA308510328753B7412CA439816D1E1D5B9` |
| `--request-id` | Full plaintext from `/tmp/bridge_req_id.txt` (738 chars) |
| Result | `code: 0` |
| Post-cancel escrow | `28000000000` (−1 GNK) |

**Note:** Cancel with BLS base64 id (`KDTcSiJ3...=`) fails with `pending bridge operation not found` (double-hash). Only plaintext `req_<height>_...` works.

### Node logs

```bash
docker logs node --since 30m 2>&1 | grep -iE \
  'threshold signing failure|auto-refund|Failed to run threshold|failed to auto-refund'
```

No matches around failure time — consistent with hook returning `(false, nil)` silently (e.g. empty `MultiBlsHooks`) rather than logging an error.

---

## Code path (where refund should fire)

```
bls/module EndBlock
  → ProcessThresholdSigningDeadlines
    → finalizeFailedThresholdSigningRequest (status=EXPIRED)
      → store EXPIRED
      → maybeCloseRetryAfterFailedPostProcess
          → Hooks().AfterThresholdSigningFailed(request.RequestId, ...)
              → inference/bls_hooks.go: ProcessAutoRefundForFailedBridgeOperation
      → emitThresholdSigningFailed
```

Key files:

- `inference-chain/x/bls/keeper/threshold_signing.go` — `finalizeFailedThresholdSigningRequest`, `maybeCloseRetryAfterFailedPostProcess`
- `inference-chain/x/inference/module/bls_hooks.go` — `AfterThresholdSigningFailed`
- `inference-chain/x/inference/keeper/bridge_pending_refund.go` — `ProcessAutoRefundForFailedBridgeOperation`, `processAutoRefundMint`
- `inference-chain/x/inference/keeper/msg_server_request_bridge_mint.go` — `setBridgeMintPendingRefund` at mint time
- `inference-chain/x/bls/module/module.go` — `InvokeSetBlsHooks` (depinject wiring)

### Silent failure modes

| Condition | On-chain symptom |
|-----------|------------------|
| `MultiBlsHooks` empty / hooks not registered | `(false, nil)` — no log, no refund, status stays `EXPIRED` |
| Pending not in map | `(false, nil)` — same |
| Refund error | Log: `Failed to run threshold signing failure hooks` |
| Success | `CANCELLED`, `bridge_operation_auto_refunded`, escrow refunded |

There is **no retry queue** for failed failure-hooks (unlike `COMPLETED` post-process retries).

---

## Test coverage gap

| Test | What it proves | What it misses |
|------|----------------|----------------|
| `TestProcessAutoRefundForFailedBridgeOperation_Mint` | Refund logic in isolation | Pending set **after** `ProcessThresholdSigningDeadlines`; calls `ProcessAutoRefund` **directly**; **no `BlsHooks`** on BLS keeper |
| `TestMsgServer_CancelBridgeOperation_MintSuccess` | Manual cancel after `EXPIRED` | Same — pending inserted after expiry; no hook wiring |
| `TestProcessThresholdSigningDeadlines_*` (bls) | Retry/expire mechanics | No inference module / no bridge escrow |

**Missing:** integration test: `RequestBridgeMint` → pending at mint → `ProcessThresholdSigningDeadlines` with real `BlsHooks` → assert `bridge_operation_auto_refunded` + escrow balance + `CANCELLED`.

---

## Reproduction (testnet)

```bash
# On each node (18222, 18223, 18226)
cd /srv/dai/gonka/deploy/join && source config.env
files=(-f docker-compose.yml -f docker-compose.mlnode.yml)
[ -f docker-compose.env-override.yml ] && files+=(-f docker-compose.env-override.yml)
[ -f docker-compose.genesis-override.yml ] && files+=(-f docker-compose.genesis-override.yml)
[ -f docker-compose.runtime-override.yml ] && files+=(-f docker-compose.runtime-override.yml)
docker compose "${files[@]}" stop api

# On seed 18222 — mint (save plaintext request_id from block events!)
# Wait ~30+ blocks; check finalize_block_events at terminal block
# Do NOT cancel for case #18

# Verify failure
curl -s "$NODE/block_results?height=$FAIL_BLOCK" | jq '.result.finalize_block_events[].type'
# Expect: EventThresholdSigningFailed, NOT bridge_operation_auto_refunded

# Restore apis when done
docker compose "${files[@]}" up -d --no-deps api
```

---

## Suggested fixes

1. **Add integration test** wiring `NewBlsHooks(k)` via `blsKeeper.SetHooks` in keeper test setup; full mint → expire → assert auto-refund.
2. **Investigate live node:** confirm `InvokeSetBlsHooks` runs at app init on testnet binary (`api:0.2.13` / `inferenced` build).
3. **Observability:** log when `AfterThresholdSigningFailed` returns `(false, nil)` with reason (`no pending refund` vs `no hooks registered`).
4. **Resilience:** consider failure-hook retry queue (mirror `enqueueCompletedPostProcessRetry`).
5. **Docs/CLI:** document that `cancel-bridge-operation --request-id` requires plaintext `req_<height>_...`, not BLS base64.

---

## Related manual test matrix

| Case | Result |
|------|--------|
| #18 Auto-refund on `EXPIRED` | **FAIL** (runs A & B) |
| #19 Manual cancel after `EXPIRED` | **PASS** (with correct plaintext `request_id`) |
| Unit `TestProcessAutoRefundForFailedBridgeOperation_Mint` | PASS (isolated) |
