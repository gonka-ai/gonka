# Setup report: epoch fee readiness

`GET /admin/v1/setup/report` returns HTTP 200 with the report, including when
`feegrant_allowance` is `FAIL` or `UNAVAILABLE`. A failed check makes the report's
`overall_status` `FAIL`. Reports remain cached for one minute.

The fee check validates the cold-to-warm grant when the signer differs from the
cold account. A missing or expired grant fails even when epoch fees are disabled.
A cold signer does not need a grant to itself.

When the epoch fee group's price is positive, the check also compares the usable
cold balance with the one-epoch fee budget. It uses `FeePayerSpendable`: spendable
`ngonka`, capped by the remaining allowance for a warm signer. An unlimited grant
does not cap the balance. Vesting funds and the warm account's balance do not
count. Failed spendable queries are not replaced by total bank balances.

- Zero usable balance fails while epoch fees are enabled.
- A known budget greater than the usable balance fails; equality passes.
- A positive balance with an unknown budget does not fail for insufficient funds.
  The check's message explains that one-epoch coverage could not be determined.
- Query or decoding errors produce `UNAVAILABLE`, rather than an invented zero.

The StoreCommit count is resolved from the latest epoch's top participant,
summing that participant's counts across models, just as for
`GET /admin/v1/epoch-fee-budget` without a `count` parameter. No observation is an
unknown count, not an explicit zero. A budget can still be known without a count
when the per-count rate is zero.

Using the top participant is intentional: the setup check uses the same
conservative network-wide estimate as the budget endpoint, rather than switching
to this node's own count. Smaller nodes can therefore fail this readiness check
even if their balance would cover their own observed workload.

When available, `details` includes `denom`, `spendable_balance`, `budget_balance`,
`budget_known`, `count`, `count_source`, and `spendable_covers_budget`.
`spendable_covers_budget: false` alone is not a failure when `budget_known` is
false. These fields describe a diagnostic estimate, not a guarantee of future
transaction costs or funding.
