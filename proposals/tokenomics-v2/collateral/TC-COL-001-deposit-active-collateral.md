# TC-COL-001: Deposit GNK and consistent active collateral queries

**Priority:** P0  
**Status:** Draft  

## Description

Verify that a participant can **deposit GNK** (`ngonka` on-chain) into `x/collateral` and that **collateral** and **bank** queries stay consistent with the transfer and fees.

## Preconditions

- [ ] [`pre-test-checks.md`](./pre-test-checks.md) completed for this run.
- [ ] Participant key is available; spendable **GNK** covers the chosen deposit plus fees.
- [ ] Deposit amount is meaningful for the network (above dust, within wallet / policy limits).

## Steps

1. Record **bank balances** for the participant before the deposit.  
   **Expected:** Baseline documented for comparison.

2. Submit `deposit-collateral` for the chosen `ngonka` amount (see [`commands.md`](./commands.md)).  
   **Expected:** Transaction is included successfully; capture tx hash.

3. Query `collateral show-collateral <participant>`.  
   **Expected:** **Active collateral** reflects the deposit on top of any prior balance (within documented rounding), with no ambiguous or empty state if a deposit occurred.

4. Query `bank balances` for the participant.  
   **Expected:** Spendable balance decreased by the deposit amount plus fees (approximately); no unexplained residual mismatch.

## Notes to record

- Tx hash and block height / epoch at deposit.
- Exact query output for `show-collateral` and bank before/after.
- Any rounding, fee deduction, or explorer discrepancies worth support runbooks.
