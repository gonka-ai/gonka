# Collateral — regression test plan

## Goal

Confirm that released **`x/collateral`** behavior matches the product and docs for real participants using **GNK**:

- deposit and query **active collateral** consistently
- withdraw into **unbonding** with the expected **completion epoch** (`UnbondingPeriodEpochs`)
- after the unbonding period, **funds return** to the participant’s spendable bank balance
- **grace period** vs **post-grace** weight behavior matches [`collateral.md`](./collateral.md) (including `AdjustWeightsByCollateral` where observable)
- **observability**: deposit, withdraw initiation, release, and slash paths are visible enough (events / explorer / logs) to debug a user report

Collateral amounts in this pass are **GNK**; wrong-denom deposit checks stay in automated tests unless you explicitly add them here.

## Why this needs manual QA

Integration tests cover core keeper logic, but they do not fully replace:

- end-to-end timing against **real epoch boundaries** and **unbonding completion**
- **operator ergonomics** (`inferenced` CLI, explorer, REST) on a live or shared network
- **weight vs collateral** correlation when the only signal is epoch group / indexer / non-CLI surfaces
- **slashing** and **staking-hook** paths that are risky or impractical on a shared testnet without controlled keys

When the network is still in **grace** (`epoch ≤ GracePeriodEndEpoch`), post-grace weight math may be **N/A** until the epoch passes that threshold—record that state rather than forcing a proof.

## In scope

- Participant (or test) account with known key and **GNK** for collateral + fees.
- Deposit → query active collateral → partial/full withdraw → unbonding queue → **release to bank** after completion epoch.
- At least one **negative** check: withdraw more than active collateral.
- Grace-period documentation: weight **not** reduced for missing collateral while grace applies (doc §2.1.1).
- Post-grace: collateral vs **effective / validation weight** **when** the network epoch is past `GracePeriodEndEpoch` and queries allow it.
- Events / explorer / tx logs for the happy path (and slash events if you run an extended case).

## Out of scope

- Wrong-denom deposits (covered by integration tests unless extended).
- Formal economic security proof of `BaseWeightRatio` / `CollateralPerWeightUnit`.
- Deep adversarial analysis (collusion around PoC weight and collateral).
- Full product UX (wallets, dashboards) beyond **CLI + explorer**.
- Long-horizon stress (very large participant counts, mempool contention) unless scheduled separately.
- Fee and gas internals already covered by automated tests.

## Must work

- A funded participant address can **deposit GNK** into `x/collateral` and see consistent **active collateral** in queries.
- **Withdrawal** reduces active collateral and creates an **unbonding** entry with an expected **completion epoch**.
- After the queue is processed at the documented boundary, **unbonded GNK** is **spendable** in the bank balance (within fee rounding).
- **Events** (or equivalent indexer/explorer fields) exist for deposit, withdrawal initiation, release, and slash—enough to support support/debug without raw state surgery.

## Must fail safely

- **Withdraw more than active collateral** returns a **clear** error (no silent partial success or inconsistent queries).
- After a withdraw, **effective weight** (post–grace) must **not** still assume withdrawn **active** collateral; unbonding remains **slashable** per spec.

## Recommended execution order

1. `TC-COL-000-pre-test-checks.md`
2. `TC-COL-001-deposit-active-collateral.md`
3. `TC-COL-002-withdraw-unbonding-queue.md`
4. `TC-COL-003-unbonding-release-to-bank.md`
5. `TC-COL-004-over-withdraw-rejected.md`
6. `TC-COL-005-grace-period-behavior.md`
7. `TC-COL-006-post-grace-weight-vs-collateral.md` *(if epoch > `GracePeriodEndEpoch` and queries allow)*
8. `TC-COL-007-events-and-observability.md`
9. `TC-COL-008-post-test-validation.md`

## Additional cases (optional)

Run on a **dedicated** or **sacrificial-key** environment where noted.

- `TC-COL-009-multiple-unbondings-same-completion-epoch.md` — aggregated unbonding (doc §2.2.2).
- `TC-COL-010-slash-invalid-participant.md` — INVALID + `SlashFractionInvalid` (doc §2.3.1); sacrificial participant only.
- `TC-COL-011-slash-downtime-epoch.md` — downtime + `SlashFractionDowntime` (doc §2.3.2).
- `TC-COL-012-unbonding-slashed-before-release.md` — slash vs unbonding balance (doc §2.2.2, §2.3).
- `TC-COL-013-staking-hook-before-validator-slashed.md` — consensus slash vs collateral (doc §3.1); test validator only, extreme care.
- `TC-COL-014-governance-param-update.md` — gov updates to `UnbondingPeriodEpochs` or inference `CollateralParams`.
- `TC-COL-015-upgrade-store-and-params.md` — post-upgrade store and params (doc §5); when exercising an upgrade cutover.

## Exit criteria

This pass is complete when:

- at least one **fresh participant** has completed **deposit → withdraw → unbond → bank release** with **query + bank** evidence recorded
- **grace period** state for the run is **recorded**; post-grace weight claims are either **shown with evidence** or marked **N/A** with reason (e.g. epoch still in grace)
- **over-withdraw** behavior is **exercised or noted** as covered elsewhere, with any gap documented
- **Must work** / **Must fail safely** bullets above are **checked or explicitly documented** with real observations where applicable
- any deviations are tracked as **defects**, **follow-up doc or tooling work**, or **explicit spec/code alignment notes**

## Environment assumptions

- Network build has **`x/collateral`** enabled and wired to **`x/inference`**.
- **RPC/REST** reachable; `inferenced` matches (or is compatible with) the network.
- Way to advance epochs (wait or fast-epoch devnet).
- Optional: governance to tune `GracePeriodEndEpoch` or collateral params for shorter experiments.
- Optional: disposable validator for staking-hook or consensus-slash additional cases.

## References

- Spec: [`collateral.md`](./collateral.md)
- Implementation tracker: [`collateral-todo.md`](./collateral-todo.md)
- Automated tests: `testermint/src/test/kotlin/CollateralTests.kt`
- Weight adjustment: `inference-chain/x/inference/keeper/collateral.go`
- Docs: `docs/tokenomics.md` (Collateral System section)
