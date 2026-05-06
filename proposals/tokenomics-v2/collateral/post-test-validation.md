# Post-test validation

Final snapshot after the ordered cases. Attach outputs or ticket links.

## Re-query

- `inferenced query collateral params`
- `inferenced query inference params` (grace / collateral fields)
- `inferenced query collateral show-collateral <participant>`
- `inferenced query collateral show-unbonding-collateral <participant>`
- `inferenced query bank balances <participant>`

## Summary

| Check | Status |
|-------|--------|
| Deposit → withdraw → unbond → bank release demonstrated | |
| Over-withdraw fails safely (TC-COL-004) | |
| Grace behavior recorded (TC-COL-005) | |
| Post-grace weight vs collateral (TC-COL-006) or N/A | |
| Events / observability acceptable (TC-COL-007) | |

## Deviations

_List defects, doc/tooling follow-ups, or intentional spec vs code notes._

## Sign-off

| Field | Value |
|-------|--------|
| Date | |
| Operator | |
