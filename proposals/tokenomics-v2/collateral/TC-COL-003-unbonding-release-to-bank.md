# TC-COL-003: Unbonding completes and GNK returns to spendable bank balance

**Priority:** P0  
**Status:** Draft  

## Description

After the **completion epoch** for an unbonding entry is processed, verify that the withheld **GNK** returns to the participant’s **spendable bank** balance and the unbonding entry is cleared or reduced per spec.

## Preconditions

- [ ] TC-COL-002 (or equivalent) produced an unbonding entry with a known **completion epoch**.
- [ ] There is a practical way to reach that completion boundary (wait, fast-epoch devnet, or documented operator procedure).

## Steps

1. Record **bank balances** and `show-unbonding-collateral` **before** the completion epoch is processed.  
   **Expected:** Unbonding entry still present for the amount under test.

2. Advance the chain until the **completion epoch** boundary is processed (per [`../collateral.md`](../collateral.md)).  
   **Expected:** No manual state surgery; only normal chain progression.

3. Re-query `show-unbonding-collateral` and **bank balances**.  
   **Expected:** Spendable bank increased by the unbonded amount (within fee rounding); the corresponding unbonding entry is **removed** or **reduced** as specified.

4. *(Optional)* Withdraw any remaining active collateral and repeat steps 1–3 for a second release if you need extra confidence.  
   **Expected:** Same consistency for the second tranche.

## Notes to record

- Epoch / height when release was observed.
- Before/after bank and unbonding query output.
- Any delay or batching behavior at epoch boundaries.
