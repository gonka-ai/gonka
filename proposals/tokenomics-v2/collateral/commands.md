# Collateral — example commands

Replace placeholders: `CHAIN_ID`, `NODE`, `KEY`, `PARTICIPANT`, amounts. Collateral uses **`ngonka`** (GNK smallest unit on-chain).

## Queries

```bash
inferenced status 2>/dev/null | head

inferenced query collateral params --node "$NODE" --chain-id "$CHAIN_ID"

inferenced query collateral show-collateral "$PARTICIPANT" --node "$NODE" --chain-id "$CHAIN_ID"

inferenced query collateral show-unbonding-collateral "$PARTICIPANT" --node "$NODE" --chain-id "$CHAIN_ID"

inferenced query bank balances "$PARTICIPANT" --node "$NODE" --chain-id "$CHAIN_ID"
```

Inference params (grace epoch, collateral-related fields):

```bash
inferenced query inference params --node "$NODE" --chain-id "$CHAIN_ID"
```

Current **epoch index** (effective epoch; use for grace checks and collateral `completion_epoch` math):

```bash
inferenced query inference get-current-epoch --node "$NODE" --chain-id "$CHAIN_ID"
```

Optional richer snapshot (latest epoch struct, block height, confirmation PoC flag):

```bash
inferenced query inference epoch-info --node "$NODE" --chain-id "$CHAIN_ID"
```

## Weight and epoch-group queries

Pick **one** primary source for “weight” for the whole run and write it in the TC (e.g. TC-COL-005 / TC-COL-006). Collateral state for the same participant:

```bash
inferenced query collateral show-collateral "$PARTICIPANT" --node "$NODE" --chain-id "$CHAIN_ID"
```

**Option A — current epoch group (full group):** effective weights live in `validation_weights` after collateral adjustment (post-grace). Good when you need the whole committee.

```bash
inferenced query inference current-epoch-group-data --node "$NODE" --chain-id "$CHAIN_ID"
```

**Option B — participant slice:** same underlying data as the current epoch group, filtered to one address (`weight` + `reputation`).

```bash
inferenced query inference get-participant-current-stats "$PARTICIPANT" --node "$NODE" --chain-id "$CHAIN_ID"
```

**Option C — fixed epoch index:** historical / compare across epochs; `epoch_index` must match the epoch you are reasoning about (not always equal to `get-current-epoch` depending on chain timing—note height/epoch in your log).

```bash
inferenced query inference show-epoch-group-data <EPOCH_INDEX> --node "$NODE" --chain-id "$CHAIN_ID"
```

In the test log, record: **chosen option (A/B/C)**, **exact command**, **JSON path** for the weight field you compare (e.g. `validation_weights[].weight` for your address), and **timestamp / block / epoch** of the query.

## Transactions

Deposit and withdraw (signer must be the participant):

```bash
inferenced tx collateral deposit-collateral 1000000000ngonka \
  --from "$KEY" --node "$NODE" --chain-id "$CHAIN_ID" -y

inferenced tx collateral withdraw-collateral 500000000ngonka \
  --from "$KEY" --node "$NODE" --chain-id "$CHAIN_ID" -y
```

Inspect result (tx hash from previous command):

```bash
inferenced q tx <TXHASH> --node "$NODE" --chain-id "$CHAIN_ID"
```

## Notes

- `deposit-collateral` / `withdraw-collateral` autocli may bind the transaction signer to the **participant** field; confirm against your `inferenced` version if flags differ.
- Use [`pre-test-checks.md`](./pre-test-checks.md) to record which `--node`, `--chain-id`, and key you used for the run.
