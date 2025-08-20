# Transfer Restrictions CLI Reference

## Query Commands

```bash
# Check current restriction status
inferenced query restrictions transfer-restriction-status

# List emergency exemptions
inferenced query restrictions transfer-exemptions

# Check exemption usage for account
inferenced query restrictions exemption-usage [exemption-id] [account-address]
```

## Transaction Commands

```bash
# Execute emergency transfer (if exemption exists)
inferenced tx restrictions execute-emergency-transfer \
  [exemption-id] [from-address] [to-address] [amount] [denom] \
  --from [key-name] --gas 300000
```

## Command Examples

```bash
# Check if restrictions are active
inferenced query restrictions transfer-restriction-status

# Example output:
# {
#   "is_active": true,
#   "restriction_end_block": "1555000",
#   "current_block_height": "125000",
#   "remaining_blocks": "1430000"
# }

# Execute emergency transfer
inferenced tx restrictions execute-emergency-transfer \
  emergency-001 gonka1from... gonka1to... 1000 ugonka \
  --from from-key --gas 300000 --yes
```

See the deployment guide for configuration and integration details.