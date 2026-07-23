# Bridge inbound pause test — developer report

**Date:** 2026-06-22  
**Network:** `gonka-testnet-4` (3 validators: seed + 2 joins)  
**Scenario:** `docker pause node` on all hosts; bridge + API kept running; Sepolia USDT inbound during/after halt  

## Summary

Pausing only the Gonka `node` container successfully halts the chain while the bridge (Geth) and `api` stay up. Inbound bridge deposits are detected and queued at unpause, but **votes from join validators can be lost silently**. The deposit under test (`ethereum` block `11116928`, receipt `199`) remained **`BRIDGE_PENDING`** with **one validator** and **no USDT mint** (`2` → still `2`).

This is a **code-path gap**, not an operator error: there is no automatic retry or replay after a failed vote during chain halt/recovery.

## Test artifact

| Field | Value |
|-------|--------|
| Sepolia block | `11116928` |
| Receipt index | `199` |
| Receipts root | `0x89909ccf6494c61516e0e0bfe45867dc96a566b500566b39ad3462d5ab38068f` |
| USDT contract | `0x7169d38820dfd117c3fa1f22a697dba58d90ba06` |
| Gonka recipient | `gonka1qdwgkcxww42sxmcm9gk0trvugev2g0248upwyq` |

## Observed timeline

1. **During pause:** Gonka height stalled; API logged stale block time (~12 min). Bridge Geth could not reach `GET /v1/bridge/addresses` on `api` (timeouts) and lagged on older Sepolia blocks.
2. **On unpause (~16:39:20–24):** Bridge burst-posted backlog including block `11116928` to all three APIs. Join hosts (`702127`, `702105`) logged `Processing receipt` for `11116928/199` with **no** `Error processing bridge exchange` line.
3. **On-chain after joins:** No join validator in `validators` list for this receipt.
4. **Seed (~16:46:27):** Seed API processed the same receipt; on-chain query showed **one** validator (`gonka1zwcutshh…`, cold key), `status: null` (`BRIDGE_PENDING`), `totalValidationPower: 21459`. Wrapped USDT balance unchanged at **2**.

## Root cause (three compounding gaps)

### 1. API bridge queue: process once, no retry

`getNextBlock()` **deletes** the block from `pendingBlocks` before processing receipts. A failed or ineffective vote is not re-queued. The 5-minute ticker only processes blocks still in the queue.

**File:** `decentralized-api/internal/server/public/bridge_handlers.go` (`getNextBlock`, `processReceipt`)

### 2. `MsgBridgeExchange` uses `SendTransactionAsyncNoRetry`

Bridge votes do **not** use the retry/halt-aware path (`SendTransactionAsyncWithRetry`). `SendTransactionAsyncNoRetry`:

- Calls `updateChainHalt()` but **ignores** the halt flag and still broadcasts.
- Returns `nil` error from `broadcastMessage` even when `resp.Code != 0` (CheckTx failure is logged as `Broadcast failed immediately` in tx_manager, not surfaced to the bridge handler).

**Files:**

- `decentralized-api/cosmosclient/cosmosclient.go` — `BridgeExchange`
- `decentralized-api/cosmosclient/tx_manager/tx_manager.go` — `SendTransactionAsyncNoRetry`, `broadcastMessage`

**Contrast:** PoC/validation messages use `SendTransactionAsyncWithRetry`, which pauses on stale block time and re-queues on failure.

### 3. Geth bridge does not replay delivered blocks

Geth posts each finalized Ethereum block once to `POST /admin/v1/bridge/block`. After HTTP 200 (`Block queued for processing`), it advances its cursor. It does not know whether the downstream vote landed on-chain. There is no chain-side job to re-request votes for `BRIDGE_PENDING` transactions.

**File:** `bridge/script.sh` (`--bridge.postblock`)

## Why join votes failed at unpause (likely)

At **16:39:24**, the chain had been frozen for ~12 minutes. The tx manager signs unordered txs with `timeout = latestBlockTime + 60s`. With **stale** `latestBlockTime`, the timeout can be **in the past** when broadcast runs, causing CheckTx rejection. Because `broadcastMessage` does not return that as an error to `BridgeExchange`, the bridge handler logs `Processing receipt` and moves on.

Seed’s successful vote ~7 minutes later ran against fresh block time and a live chain.

## Impact

| Area | Effect |
|------|--------|
| **Availability** | Pause test goal met: chain halts, bridge/API survive |
| **Correctness** | Inbound bridge deposits can stall at `BRIDGE_PENDING` indefinitely |
| **Observability** | `Processing receipt` is misleading; failures may only appear as `Broadcast failed immediately` in tx_manager logs |
| **Recovery** | No automatic self-heal; requires manual re-post of the block or manual `bridge-exchange` CLI from validators that have not yet voted |

Majority rule is unchanged: mint requires `totalValidationPower >= floor(totalEpochPower/2) + 1` (`inference-chain/x/inference/keeper/msg_server_bridge_exchange.go`). One validator is insufficient on a 3-node set.

## Recommendations

**Short term (ops):** Document that after chain halt, operators may need to re-post affected bridge blocks or submit `bridge-exchange` from validators missing from the on-chain `validators` list.

**Code fixes (suggested priority):**

1. **Surface broadcast failures** — `BridgeExchange` should treat `resp.Code != 0` as failure (or use `classifyBroadcastResponse` like the retry path).
2. **Retry bridge votes** — Use `SendTransactionAsyncWithRetry` for `MsgBridgeExchange`, or re-queue the receipt/block when broadcast fails or chain is halted/stale.
3. **Do not drop the block until vote is confirmed** — Remove from queue only after successful broadcast (or after tx inclusion), or keep a “failed receipts” retry set.
4. **Optional: reconciliation loop** — Periodically query `BRIDGE_PENDING` for the local validator’s epoch and submit missing votes for receipts the bridge has already seen.
5. **Optional: bridge cursor** — Only advance Geth post cursor after API acknowledges processing success, not merely queue accept (harder; needs API contract change).

## Verification commands

```bash
# On-chain state for test receipt
inferenced q inference bridge-transaction \
  --origin-chain ethereum --block-number 11116928 --receipt-index 199 \
  --node http://localhost:8000/chain-rpc/ -o json

# Wrapped balance
inferenced q inference wrapped-token-balances \
  --address gonka1qdwgkcxww42sxmcm9gk0trvugev2g0248upwyq \
  --node http://localhost:8000/chain-rpc/ -o json

# Hidden failure signals on join API (around unpause time)
docker logs api 2>&1 | grep -iE 'Broadcast failed|node block time is stale|Error processing bridge'
```

## Conclusion

The pause test demonstrates that **inbound bridge processing is not halt-safe**: votes fired during chain recovery can be lost with no retry, while logs suggest success. Developers should treat this as a reliability bug in the API tx path and bridge queue, not as expected bridge behavior.
