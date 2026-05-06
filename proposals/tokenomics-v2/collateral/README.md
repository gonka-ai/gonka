# Collateral — manual regression

Manual regression for **`x/collateral`**: deposit, withdraw, unbonding, bank release, grace vs post-grace behavior, and basic observability. Collateral is **GNK** (CLI coin type **`ngonka`**).

## Documents

| File | Purpose |
|------|--------|
| [`plan.md`](./plan.md) | Goal, scope, must work / fail safely, exit criteria, optional extended cases |
| [`commands.md`](./commands.md) | Example `inferenced` commands (adjust flags and endpoints for your network) |
| [`pre-test-checks.md`](./pre-test-checks.md) | Record environment before the run |
| `TC-COL-001` … `TC-COL-007` | Ordered case files in this folder |
| [`post-test-validation.md`](./post-test-validation.md) | Final queries and summary |

## Execution order

1. `pre-test-checks.md`
2. `TC-COL-001-deposit-active-collateral.md`
3. `TC-COL-002-withdraw-unbonding-queue.md`
4. `TC-COL-003-unbonding-release-to-bank.md`
5. `TC-COL-004-over-withdraw-rejected.md`
6. `TC-COL-005-grace-period-behavior.md`
7. `TC-COL-006-post-grace-weight-vs-collateral.md` (if applicable)
8. `TC-COL-007-events-and-observability.md`
9. `post-test-validation.md`

Optional cases are listed in [`plan.md`](./plan.md) only (no separate files).

## References

- Spec: [`../collateral.md`](../collateral.md)
- Tracker: [`../collateral-todo.md`](../collateral-todo.md)
- Automated tests: `testermint/src/test/kotlin/CollateralTests.kt`
