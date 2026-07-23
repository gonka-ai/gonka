# Bridge reliability test report — outages, epoch change, upgrade simulation

**Date:** 2026-06-23  
**Audience:** Developers, QA, bridge operators  
**Networks:** `gonka-testnet-4` (3-validator cluster on `xj7-5.s.filfox.io`), Sepolia bridge `0xD8240aFA1f41…`, mainnet bridge `0x972a7a92…` (observational)

This report consolidates manual and operational testing of **inbound** (Ethereum → Gonka), **outbound** (Gonka → Ethereum), **epoch transitions**, **BLS threshold signing**, and **chain halt / upgrade simulation** (`docker pause node`). Detailed evidence lives in linked sub-reports.

---

## Executive summary


| Area                                                        | Verdict                         | Notes                                                                                |
| ----------------------------------------------------------- | ------------------------------- | ------------------------------------------------------------------------------------ |
| **BLS quorum for outbound (WGNK, USDT, USDC)**              | **PASS**                        | Requires >50% **slot-holding** validators; completes in few blocks when healthy      |
| **Outbound mint + finalize (happy path)**                   | **PASS**                        | epoch key must exist must exit on Sepolia/Ethereum                                   |
| **Epoch / metadata edge cases (#1–17, #24–25)**             | **PASS** (except #16)           | Contract rejects bad sigs, epoch lag, replays — see matrix                           |
| **Inbound after full stack restart**                        | **PASS**                        | Majority hosts down; pending deposit completed after bridge/API/node restart         |
| **Inbound during upgrade simulation** (`docker pause node)` | **FAIL**                        | Votes lost on unpause — [#1358](https://github.com/gonka-ai/gonka/issues/1358)       |
| **Outbound auto-refund on BLS EXPIRED (#18)**               | **FAIL**                        | Unit tests pass; live fails — [#1352](https://github.com/gonka-ai/gonka/issues/1352) |
| **Finalize before epoch key pushed (#16)**                  | **FAIL** (expected, ops timing) | Depends on how fast `submitGroupKey` runs after epoch change                         |
| **Epoch sync automation + mainnet funding**                 | **PASS**                        | Daemon + ~$200/yr operational budget works                                           |


**Bottom line:** Cryptography, epoch semantics, and BLS quorum work under normal conditions. **Recovery gaps** remain: inbound votes lost during chain halt (not full restart), auto-refund on signing timeout, and finalize blocked until epoch key is pushed to Sepolia/Ethereum bridge.

---

## Test environments


| Host / cluster                                                        | Role                                  | Bridge                                                           |
| --------------------------------------------------------------------- | ------------------------------------- | ---------------------------------------------------------------- |
| `gonka-testnet-4` — seed `702111` (`18222`), joins `702127`, `702105` | 3-node validator set                  | Sepolia `0xD8240aFA1f414873817d808693d53133F92EdC20`             |
| `computeinstance-e00frqe06ef08bxt7h` (`89.169.111.79`)                | Single-node testnet / epoch-sync host | Same Sepolia bridge `0x8395733b8ecc2d1d3a7eb1b8b921d71ee4620b02` |
| Mainnet (`node2.gonka.ai`)                                            | Production reference                  | Ethereum `0x972a7a92d92796a98801a8818bcf91f1648f2f68`            |


**Upgrade / outage simulations:**


| Simulation                                 | What stopped                                   | Intent                                          |
| ------------------------------------------ | ---------------------------------------------- | ----------------------------------------------- |
| **Full stack outage**                      | `bridge` + `api` + `node` on majority of hosts | Cold restart recovery                           |
| **Upgrade simulation `docker pause node`** | Chain only; bridge Geth + `api` kept running   | Upgrade window (validators briefly unavailable) |


---

## BLS threshold signing — outbound quorum (WGNK, USDT, USDC)

**Result: PASS** (all three asset types)

### How it works

Any time `request-bridge-mint` or a withdraw-with-signature path succeeds, the chain emits `EventThresholdSigningRequested`. Each validator's **API** (`decentralized-api`) listens and automatically submits partial BLS signatures. Finalize tooling polls until status is `COMPLETED`.

### Quorum rules (not just “half the nodes”)

- The chain needs **>50% of signing slot power** — aggregated **slot-holding validators**, not merely “any 50% of nodes.”
- Collection must complete within **~30 blocks** total (`signing_deadline_blocks=10` × `max_signing_attempts=3` on testnet; same BLS params on mainnet).
- In normal conditions, enough validators respond within **a few blocks**, well inside the first 10-block window.

### Mainnet risk assessment


| Scenario                                     | Risk                                              |
| -------------------------------------------- | ------------------------------------------------- |
| Healthy mainnet, validators up               | **Very low** — signing usually finishes quickly   |
| Occasional single validator down             | **Low** — large set, redundant slot coverage      |
| Mass outage (>~half slot holders offline)    | **Real** — mints/withdraws hang, then **EXPIRED** |
| Bad upgrade / BLS cache issues on many nodes | **Possible** incident, not day-to-day             |


**Developer note:** When quorum is not reached in time, escrow should auto-refund ([#18](#bls-signing-lifecycle--cancelrefund) / [#1352](https://github.com/gonka-ai/gonka/issues/1352)) — currently **broken** on live testnet.

---

## Mainnet operational funding

**Result: PASS** — ~**$200/year** requested and approved for mainnet bridge operational costs (epoch `submitGroupKey` submitter wallet).

### Gas budget (epoch sync only)

Measured on mainnet (~Jun 2026):


| Metric                    | Value                                          |
| ------------------------- | ---------------------------------------------- |
| Gas per `submitGroupKey`  | ~**0.00006 ETH**                               |
| Epochs per year           | ~**342** (`epoch_length` ~15,391 blocks × ~6s) |
| Steady-state annual cost  | **~0.02–0.05 ETH** (~$35–$85 @ $2,500/ETH)     |
| Recommended wallet buffer | **~0.1 ETH** (~$170) for gas spikes            |


---

## Inbound (Ethereum → Gonka)

### Full stack outage on majority hosts — PASS

**Scenario:** Bridge, API, and node containers down on **majority** of hosts while a Sepolia USDT inbound tx was already on-chain with **one** validator vote (`BRIDGE_PENDING`).

**Outcome:** After bridge, API, and node restart on all hosts, validators came back, voting completed, and the deposit finalized.


| Check                           | Result                              |
| ------------------------------- | ----------------------------------- |
| Deposit detected in bridge Geth | Yes                                 |
| Initial on-chain state          | `BRIDGE_PENDING`, one cold-key vote |
| After restart                   | Voting completed; mint succeeded    |
| Pending tx lost?                | **No**                              |


**Distinction:** This is **not** the same failure mode as the pause test below. Full restart allowed remaining validators to vote successfully once the chain was live again.

---

### Upgrade simulation — `docker pause node` — FAIL

**Scenario:** `docker pause node` on all hosts (~12 min); bridge Geth + `api` kept running; Sepolia USDT inbound during/after halt.

**Result: FAIL** — [GitHub #1358](https://github.com/gonka-ai/gonka/issues/1358), [full report](bridge-inbound-pause-test-report.md)


| Field           | Value                                          |
| --------------- | ---------------------------------------------- |
| Sepolia block   | `11116928`                                     |
| Receipt index   | `199`                                          |
| USDT contract   | `0x7169d38820dfd117c3fa1f22a697dba58d90ba06`   |
| Gonka recipient | `gonka1qdwgkcxww42sxmcm9gk0trvugev2g0248upwyq` |


**On-chain result:** `BRIDGE_PENDING`, **one** validator (`gonka1zwcutshh…`), `totalValidationPower: 21459`. Wrapped USDT balance unchanged (**2**).

**Timeline:**

1. **During pause:** Height stalled; API stale block time; Geth timed out on `GET /v1/bridge/addresses`.
2. **On unpause (~16:39):** Geth burst-posted block `11116928` to all APIs. Join validators logged `Processing receipt` with **no** error — but votes **not** on-chain.
3. **Seed (~16:46):** Only seed vote recorded (manual submission) — insufficient majority on 3-node set.

**Root cause:** API bridge queue processes once with no retry; `MsgBridgeExchange` uses `SendTransactionAsyncNoRetry`; Geth cursor advances on HTTP 200 without vote confirmation. Stale block time at unpause likely caused CheckTx rejection with silent failure.

---

## Manual test matrix — full results

### Epoch sync (Ethereum contract)


| #   | Test case                                                                            | Expected                              | Result   | Notes                         |
| --- | ------------------------------------------------------------------------------------ | ------------------------------------- | -------- | ----------------------------- |
| 1   | Push epoch key **after** mint in same Gonka epoch, then finalize                     | PASS                                  | **PASS** | Happy path                    |
| 2   | Contract **1 epoch behind**; mint in current epoch; finalize before `submitGroupKey` | `InvalidEpoch`                        | **PASS** |                               |
| 3   | Contract 1 epoch behind; push key; finalize mint from **previous** Gonka epoch       | PASS                                  | **PASS** |                               |
| 4   | Try to `submitGroupKey` **skip** an epoch (push 8 when contract at 6)                | Revert                                | **PASS** | `no DKG data found for epoch` |
| 5   | Bulk catch-up: contract many epochs behind; mint in epoch N while catching up        | Finalize only after `isValidEpoch(N)` | **PASS** |                               |


### Wrong metadata / replay


| #   | Test case                                                                   | Expected                           | Result   | Notes |
| --- | --------------------------------------------------------------------------- | ---------------------------------- | -------- | ----- |
| 6   | Mint with `chain_id=sepolia`; finalize on bridge with `ETHEREUM_CHAIN_ID=1` | `InvalidSignature` (permanent)     | **PASS** |       |
| 7   | Mint with **wrong** `destination_bridge_address`                            | `InvalidSignature` on new contract | **PASS** |       |
| 8   | Finalize **same request twice** on Ethereum                                 | 2nd: `RequestAlreadyProcessed`     | **PASS** |       |
| 9   | Finalize with **tampered** recipient/amount vs BLS payload                  | `InvalidSignature`                 | **PASS** |       |
| 10  | Mint on testnet chain ID; finalize on bridge bound to `gonka-mainnet`       | `InvalidSignature`                 | **PASS** |       |


### Epoch transition timing (Gonka-side)


| #   | Test case                                            | Expected                              | Result   | Notes                                                                |
| --- | ---------------------------------------------------- | ------------------------------------- | -------- | -------------------------------------------------------------------- |
| 11  | Mint in **last blocks of epoch N** (before flip)     | Signature epoch = N                   | **PASS** |                                                                      |
| 12  | Mint in **first blocks after flip** (epoch N+1)      | Signature epoch = N+1                 | **PASS** |                                                                      |
| 13  | Mint exactly at `**set_new_validators_delay`** block | Epoch depends on exact block          | **PASS** | Just before → old epoch; on/after → new epoch; bridge key must match |
| 14  | Mint during **DKG / PoC validation** window          | Signing works if validators available | **PASS** |                                                                      |


### Finalize vs BLS state


| #   | Test case                                                                  | Expected                    | Result   | Notes                                                                                                                                                    |
| --- | -------------------------------------------------------------------------- | --------------------------- | -------- | -------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 15  | Finalize while BLS **COLLECTING**                                          | Script waits                | **PASS** | `bridge-mint-eth.js` polls until `COMPLETED`                                                                                                             |
| 16  | Finalize immediately after BLS **COMPLETED**, before epoch key on contract | `InvalidEpoch`              | **FAIL** | **Ops timing gap** — outcome depends on how quickly `submitGroupKey` runs after epoch change; not a contract bug but affects UX during epoch transitions |
| 17  | Two mints same epoch; one finalize before `submitGroupKey`, one after      | Independent per `requestId` | **PASS** |                                                                                                                                                          |


### BLS signing lifecycle & cancel/refund


| #   | Test case                                                               | Expected                         | Result                     | Notes                                                                                                                                                       |
| --- | ----------------------------------------------------------------------- | -------------------------------- | -------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 18  | Mint, do not push epoch, wait for BLS **EXPIRED/FAILED** (~10×3 blocks) | Auto-refund from escrow via hook | **FAIL**                   | Unit test **PASS** in isolation; live **FAIL** — [#1352](https://github.com/gonka-ai/gonka/issues/1352), [bug report](bridge-auto-refund-on-expired-bug.md) |
| 19  | Same as #18, then manual `CancelBridgeOperation`                        | Refund or no pending             | **PASS**                   | No pending left to cancel when #18 auto-refund failed; manual cancel works with plaintext `req_<height>_…`                                                  |
| 20  | Mint stuck (no epoch key), BLS **COMPLETED**, try cancel                | FAIL — funds stay in escrow      | **PASS** (expected reject) |                                                                                                                                                             |
| 21  | Mint, BLS **COLLECTING**, user calls cancel before deadline             | Reject                           | **PASS** (rejected)        | `can only cancel failed or expired requests`; status `COLLECTING_SIGNATURES`                                                                                |
| 22  | Mint, BLS **COMPLETED**, user calls cancel                              | Reject                           | **PASS** (rejected)        | `threshold signing already completed`                                                                                                                       |
| 23  | Cancel by **non-creator** address                                       | Reject                           | **PASS** (rejected)        | `creator mismatch for pending bridge mint request`                                                                                                          |


### Ops / funding


| #   | Test case                                               | Expected                                     | Result   | Notes                                                    |
| --- | ------------------------------------------------------- | -------------------------------------------- | -------- | -------------------------------------------------------- |
| 24  | `submitGroupKey` with **insufficient ETH** on submitter | Tx fails; contract stays behind              | **PASS** | Observed running out of gas during manual epoch catch-up |
| 25  | Mint succeeds; epoch key not pushed for many epochs     | Old mints finalizable once `isValidEpoch(N)` | **PASS** | Sequential `submitGroupKey` catch-up works               |


---

## Findings — needs developer attention

### P0 — Inbound votes lost after chain halt (upgrade simulation)

[#1358](https://github.com/gonka-ai/gonka/issues/1358) — see [inbound pause section](#upgrade-simulation--docker-pause-node--fail) and [bridge-inbound-pause-test-report.md](bridge-inbound-pause-test-report.md).

**Code fixes (priority):**

1. Surface `resp.Code != 0` from bridge broadcast as failure.
2. Use `SendTransactionAsyncWithRetry` for `MsgBridgeExchange`.
3. Do not drop block from queue until vote confirmed.
4. Optional: reconciliation for `BRIDGE_PENDING`; Geth cursor ack on success not queue-accept.

---

### P0 — Auto-refund does not run when BLS signing expires (#18)

[#1352](https://github.com/gonka-ai/gonka/issues/1352) — [bridge-auto-refund-on-expired-bug.md](bridge-auto-refund-on-expired-bug.md)

**Impact:** User sends WGNK / USDT / USDC outbound during an incident when enough validators are offline → BLS expires → GNK **not** returned automatically despite refund code existing.


| Step                             | Expected                | Actual                               |
| -------------------------------- | ----------------------- | ------------------------------------ |
| BLS terminal status              | `EXPIRED` → auto-refund | `EXPIRED`, escrow unchanged          |
| `bridge_operation_auto_refunded` | Present                 | **Absent**                           |
| Manual cancel (#19)              | N/A if auto works       | **PASS** with plaintext `request_id` |


**Suggested fixes:** Integration test with wired `BlsHooks`; verify `InvokeSetBlsHooks` on deployed binaries; log silent hook no-ops; failure-hook retry queue.

---

### P1 — Finalize blocked until epoch key pushed to Sepolia/Ethereum (#16)

**Symptom:** BLS `COMPLETED` but `mintWithSignature` on Sepolia/Ethereum reverts `InvalidEpoch` if `submitGroupKey` for that epoch has not run yet.

**Not a security bug** — expected contract behavior — but creates a **user-visible gap** between Gonka mint success and Ethereum finalize, especially right after epoch flip. Mitigated by `bridge-epoch-sync` daemon; risk remains if submitter is behind or out of gas.

---

### [FIXED] P1 — Wrong `chain_id` on outbound mint (`sepolia` vs `ethereum`)

Mint with `sepolia` → BLS domain `11155111`; test bridge has `ETHEREUM_CHAIN_ID=1` → permanent `InvalidSignature`. Withdrawals fixed in `9a88727`; **mints still require `ethereum` on CLI**.

---

### P2 — Observability & operator traps


| Issue                                             | Impact                                  |
| ------------------------------------------------- | --------------------------------------- |
| `Processing receipt` when broadcast failed        | Misleading during halt recovery (#1358) |
| `cancel-bridge-operation` needs plaintext `req_…` | Operator confusion                      |


---

### P2 — Test coverage gaps


| Scenario                              | Unit test       | Live test                   |
| ------------------------------------- | --------------- | --------------------------- |
| BLS quorum / slot threshold           | Partial         | **PASS** (WGNK, USDT, USDC) |
| Cancel while `COLLECTING` (#21)       | Yes             | **PASS** (reject)           |
| Cancel after `EXPIRED` (#19)          | Yes             | **PASS**                    |
| **Auto-refund after `EXPIRED` (#18)** | PASS (isolated) | **FAIL**                    |
| Inbound vote retry after `pause node` | **No**          | **FAIL** (#1358)            |
| Inbound recovery after full restart   | **No**          | **PASS**                    |
| Mint → expire with wired `BlsHooks`   | **No**          | **FAIL**                    |


---

## Recommendations

### For developers (priority order)

1. **Fix inbound bridge vote retry** — [#1358](https://github.com/gonka-ai/gonka/issues/1358).
2. **Fix auto-refund on BLS `EXPIRED`** — [#1352](https://github.com/gonka-ai/gonka/issues/1352).
3. **[FIXED] Unify `chain_id` handling for mints** (same as withdrawal fix).
4. **Reduce #16 gap** — alert or UI when BLS complete but epoch key missing on Sepolia/Ethereum.
5. **Improve observability** — failed broadcast in bridge logs; pending mint/escrow queries.
6. **Add reconciliation** — retry `BRIDGE_PENDING` inbound; optional Geth cursor semantics.

### For operators (until code fixes ship)

- **Inbound after halt:** check `bridge-transaction` for `BRIDGE_PENDING`; re-post block or `bridge-exchange` from missing validators ([#1358](https://github.com/gonka-ai/gonka/issues/1358)).
- **Inbound after full restart:** pending deposits with partial votes **can** complete — observed PASS.
- **Outbound:** use `chain_id=ethereum` on Sepolia test bridge; keep `bridge-epoch-sync` running.
- **BLS timeout:** manual `cancel-bridge-operation` with plaintext `req_<height>_…` ([#1352](https://github.com/gonka-ai/gonka/issues/1352)).

---

## Conclusion

**25 manual outbound/epoch cases executed; 22 PASS, 3 FAIL** (#16 ops timing, #18 auto-refund). BLS quorum signing works for **WGNK, USDT, and USDC** when validators are healthy. Inbound behavior **depends on outage type**: full restart recovers pending deposits; **chain halt with live bridge/API does not** ([#1358](https://github.com/gonka-ai/gonka/issues/1358)). Outbound escrow does not auto-release on signing timeout ([#1352](https://github.com/gonka-ai/gonka/issues/1352)). 