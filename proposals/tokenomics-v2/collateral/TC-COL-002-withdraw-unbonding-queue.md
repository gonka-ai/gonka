# TC-COL-002: Partial withdraw creates unbonding queue with expected completion epoch

**Priority:** P0  
**Status:** Draft  

## Description

Verify that a **partial** withdrawal reduces **active collateral** and adds an **unbonding** entry whose **completion epoch** matches chain params (`current_epoch + UnbondingPeriodEpochs` per spec), without releasing funds to the bank yet.

## Preconditions

- [ ] TC-COL-001 (or an equivalent prior deposit) left **active collateral** greater than zero.
- [ ] `UnbondingPeriodEpochs` is known from `inferenced query collateral params`.
- [ ] Current epoch index is known (for completion-epoch check).

## Steps

1. Record current **epoch**, **active collateral**, and any existing **unbonding** state (`show-unbonding-collateral`).  
   **Expected:** Baseline documented.

2. Submit **partial** `withdraw-collateral` (amount strictly less than full active collateral).  
   **Expected:** Transaction succeeds; capture tx hash and any `completion_epoch` in the response.

3. Derive or confirm **completion epoch** (e.g. from tx result or `current_epoch + UnbondingPeriodEpochs`).  
   **Expected:** Matches [`../collateral.md`](../collateral.md) and params for this network.

4. Query `show-collateral` and `show-unbonding-collateral` for the participant.  
   **Expected:** Active collateral decreased by the withdrawal amount; unbonding shows the withdrawn amount and the expected completion epoch; bank balance has **not** yet received the unbonding principal.

## Notes to record

- Withdrawal amount, pre/post active collateral, unbonding entry payload.
- How current epoch was obtained (query name, explorer field, etc.).
- Any divergence between tx-reported completion epoch and query state.
