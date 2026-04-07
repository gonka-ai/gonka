# Community Sale Migration

This document provides instructions for migrating the `community-sale` contract (e.g., the instance from proposal 14) to the latest version. This upgrade adds support for IBC trading assets while preserving existing configurations like `native_denom`.

## Prerequisites

1.  **New Code ID**: You must first store the new `community-sale.wasm` on-chain and obtain its `CODE_ID`.
2.  **Required Parameters**:
    *   `accepted_ibc_denom`: The IBC token to be accepted for payment (obtained from the Bank module).
    *   `native_denom` (Optional): The native token to be sold. If not provided, the existing one from the contract state will be used.

## Migration Message

The `MigrateMsg` must include the new parameters.

```json
{
  "accepted_ibc_denom": "ibc/...",
  "native_denom": "ngonka"
}
```

## Governance Proposal Command

Use the following command to submit a governance proposal for the migration:

```bash
./inferenced tx gov submit-proposal wasm-migrate <CONTRACT_ADDRESS> <NEW_CODE_ID> \
    '{"accepted_ibc_denom":"ibc/...", "native_denom":"ngonka"}' \
    --title "Community Sale Migration to V2" \
    --summary "Upgrading the community-sale contract to support IBC trading as part of the IBC pool trade support initiative." \
    --from <YOUR_KEY> \
    --gas auto --gas-adjustment 1.3 \
    --broadcast-mode sync --output json --yes
```

## Verification

After the proposal passes and the migration is executed, you can verify the new configuration using a smart query:

```bash
./inferenced q wasm contract-state smart <CONTRACT_ADDRESS> '{"config":{}}'
```

The response should now include the `native_denom` and `accepted_ibc_denom` fields.
