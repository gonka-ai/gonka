# Pre-test checks

Record the environment **before** collateral steps. Use output snippets or pointers (log file, ticket).

## Network and binary

| Field | Value |
|-------|--------|
| Chain ID | |
| RPC / node URL | |
| `inferenced` version (build) | |
| Block height (approx.) | |

## Params snapshot

Paste or attach:

- `inferenced query collateral params`
- `inferenced query inference params` (note **`GracePeriodEndEpoch`** and any collateral-related fields)

## Epoch context

| Field | Value |
|-------|--------|
| Current epoch index (source: query / explorer / doc) | |
| `GracePeriodEndEpoch` | |
| In grace? (`current ≤ GracePeriodEndEpoch`) | |

## Participant under test

| Field | Value |
|-------|--------|
| Bech32 address | |
| Key name in keyring | |
| Starting bank balance (GNK / `ngonka`) | |

## Result

- [ ] Ready to run `TC-COL-001`
- [ ] Blocked (reason):

**Notes:**
