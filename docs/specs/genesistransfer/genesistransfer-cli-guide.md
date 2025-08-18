# Genesis Transfer CLI Reference

## Query Commands

```bash
# Check transfer status
inferenced query genesistransfer transfer-status [genesis-address]

# Check transfer eligibility  
inferenced query genesistransfer transfer-eligibility [genesis-address]

# List transfer history
inferenced query genesistransfer transfer-history

# View module parameters
inferenced query genesistransfer params

# View allowed accounts (if whitelist enabled)
inferenced query genesistransfer allowed-accounts
```

## Transaction Commands

```bash
# Transfer ownership (must be signed by genesis account owner)
inferenced tx genesistransfer transfer-ownership [genesis-address] [recipient-address] \
  --from [genesis-account-key] \
  --gas 2000000 \
  --yes
```

## Command Examples

```bash
# Complete transfer workflow
inferenced query genesistransfer transfer-eligibility gonka1genesis...
inferenced tx genesistransfer transfer-ownership gonka1genesis... gonka1recipient... \
  --from genesis-key --gas 2000000 --yes
inferenced query genesistransfer transfer-status gonka1genesis...

# Check account balances
inferenced query bank balances gonka1genesis...
inferenced query bank balances gonka1recipient...

# Batch transfer script
for account in gonka1genesis1... gonka1genesis2...; do
  inferenced tx genesistransfer transfer-ownership $account gonka1recipient... \
    --from genesis-key --gas 2000000 --yes
done
```

See the deployment guide for configuration and integration details.