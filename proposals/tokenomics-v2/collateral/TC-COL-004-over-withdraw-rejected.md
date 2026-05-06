# TC-COL-004: Withdrawal above active collateral fails safely

**Priority:** P0  
**Status:** Draft  

## Description

Verify that attempting to **withdraw more than active collateral** fails with a **clear error** and does **not** leave collateral or bank state inconsistent (no partial module updates).

## Preconditions

- [ ] **Active collateral** amount `A` is known from `show-collateral` (if `A` is zero, run a small deposit first).
- [ ] Participant can sign a `withdraw-collateral` message.

## Steps

1. Record **active collateral** `A` and **bank balances**.  
   **Expected:** Documented baseline.

2. Submit `withdraw-collateral` with an amount **strictly greater than** `A` (e.g. `A + 1ngonka` or a large round amount).  
   **Expected:** The chain **rejects** the transaction or it **fails** check-tx with an explicit error (e.g. insufficient collateral); capture raw log / code.

3. Re-query `show-collateral` and **bank balances**.  
   **Expected:** Active collateral unchanged; bank unchanged except possible **fee** loss if the client submitted a failing tx in a way that still charges fees—document which case occurred.

4. Confirm no new **unbonding** entry appeared for the failed withdrawal amount.  
   **Expected:** Unbonding state matches pre-attempt (for that withdrawal).

### Example commands

Set once (match your environment):

```bash
export NODE="http://89.169.111.79:8000/chain-rpc/"
export CHAIN_ID="gonka-testnet"
export ADDR="gonka1wcem5tsrudnpmjcr2puvuuf5545lugdqknukzy"
export KEY="gonka-account-key"
export CLI_HOME="/srv/dai/.inference"
```

**Step 1 — Baseline: active collateral `A`, bank, unbonding**

```bash
./inferenced query collateral show-collateral "$ADDR" \
  --node "$NODE" --chain-id "$CHAIN_ID" -o json | tee collateral-before.json

./inferenced query bank balances "$ADDR" \
  --node "$NODE" --chain-id "$CHAIN_ID" -o json | tee bank-before.json

./inferenced query collateral show-unbonding-collateral "$ADDR" \
  --node "$NODE" --chain-id "$CHAIN_ID" -o json | tee unbonding-before.json
```

Read `A` from `collateral-before.json` (field is usually `.amount.amount` in `ngonka`). **Expected:** Files saved; note `A` in your run log.

**Step 2 — Withdraw more than `A`**

Pick one:

- **Simplest:** use an amount you know is larger than any realistic `A`, e.g. `999999999999999999999999999ngonka`.
- **Tight:** `A + 1ngonka` — get the integer with `jq`, then add 1 with a big-int-safe tool, e.g.  
  `python3 -c "import json,sys; a=json.load(open('collateral-before.json')); n=int(a['amount']['amount']); print(f'{n+1}ngonka')"`

Then broadcast (expect **non-zero code** or client-side simulation error; capture full stderr/stdout):

```bash
WITHDRAW_TRY="<paste amount from above>"

./inferenced tx collateral withdraw-collateral "$WITHDRAW_TRY" \
  --from "$KEY" \
  --home "$CLI_HOME" \
  --keyring-backend file \
  --node "$NODE" --chain-id "$CHAIN_ID" \
  -y --gas auto --gas-adjustment 1.3 -o json | tee withdraw-over-attempt.json
```

**Expected:** `code` ≠ `0` and/or `raw_log` mentions insufficient collateral (or equivalent); **or** the CLI fails before broadcast — record which. If a `txhash` appears, still run `q tx` and confirm failure.

**Step 3 — Re-query collateral and bank**

```bash
./inferenced query collateral show-collateral "$ADDR" \
  --node "$NODE" --chain-id "$CHAIN_ID" -o json | tee collateral-after.json

./inferenced query bank balances "$ADDR" \
  --node "$NODE" --chain-id "$CHAIN_ID" -o json | tee bank-after.json

diff collateral-before.json collateral-after.json || true
diff bank-before.json bank-after.json || true
```

**Expected:** `collateral-before.json` and `collateral-after.json` match on active collateral. Bank may differ **only** by fees if a tx was broadcast but failed in deliver — note that explicitly.

**Step 4 — Unbonding unchanged**

```bash
./inferenced query collateral show-unbonding-collateral "$ADDR" \
  --node "$NODE" --chain-id "$CHAIN_ID" -o json | tee unbonding-after.json

diff unbonding-before.json unbonding-after.json || true
```

**Expected:** No new unbonding row / amount for the failed withdrawal; `unbonding-after.json` equals `unbonding-before.json` (or only unrelated changes if something else happened on-chain — document).

## Notes to record

- Exact error string or ABCI code.
- Whether failure was at simulation, check-tx, or deliver.
- Before/after queries to prove no silent partial update.
