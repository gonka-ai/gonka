# Transfer Restrictions CLI Guide

## Overview

This guide covers all CLI commands and queries available for the Transfer Restrictions module (x/restrictions). The module provides temporary restrictions on user-to-user transfers while preserving essential network operations.

**Parameter Configuration**: The module defaults to `restriction_end_block: 0` (no restrictions) for testing environments. Production networks must set the restriction end block via genesis configuration.

## Query Commands

### Check Restriction Status

Get current restriction status and remaining time:

```bash
inferenced query restrictions transfer-restriction-status
```

**Example Output:**
```json
{
  "is_active": true,
  "restriction_end_block": "1555000",
  "current_block_height": "125000", 
  "remaining_blocks": "1430000"
}
```

**Fields:**
- `is_active`: Whether restrictions are currently active
- `restriction_end_block`: Block height when restrictions end
- `current_block_height`: Current blockchain height
- `remaining_blocks`: Blocks remaining until automatic lifting

### List Emergency Exemptions

View all available emergency transfer exemptions:

```bash
inferenced query restrictions transfer-exemptions
```

**Example Output:**
```json
{
  "exemptions": [
    {
      "exemption_id": "validator-operations-001",
      "from_address": "*",
      "to_address": "cosmos1validator_address",
      "max_amount": "1000000",
      "usage_limit": "100",
      "expiry_block": "1500000",
      "justification": "Validator operational expenses"
    }
  ],
  "pagination": {
    "next_key": null,
    "total": "1"
  }
}
```

**Pagination Options:**
```bash
# Get exemptions with pagination
inferenced query restrictions transfer-exemptions --page=2 --limit=10

# Get exemptions with key-based pagination
inferenced query restrictions transfer-exemptions --page-key="next_key_value"
```

### Check Exemption Usage

View usage statistics for a specific exemption and account:

```bash
inferenced query restrictions exemption-usage [exemption-id] [account-address]
```

**Example:**
```bash
inferenced query restrictions exemption-usage validator-operations-001 cosmos1user_address
```

**Example Output:**
```json
{
  "usage_entries": [
    {
      "exemption_id": "validator-operations-001",
      "account_address": "cosmos1user_address",
      "usage_count": "3"
    }
  ]
}
```

### Module Parameters

View current module parameters:

```bash
inferenced query restrictions params
```

**Example Output:**
```json
{
  "params": {
    "restriction_end_block": "1555000",
    "emergency_transfer_exemptions": [...],
    "exemption_usage_tracking": [...]
  }
}
```

## Transaction Commands

### Execute Emergency Transfer

Execute a transfer using an approved emergency exemption:

```bash
inferenced tx restrictions execute-emergency-transfer \
  --exemption-id="validator-operations-001" \
  --from-address="cosmos1source_address" \
  --to-address="cosmos1destination_address" \
  --amount="100000" \
  --denom="uatom" \
  --from="cosmos1source_address" \
  --gas=auto \
  --gas-adjustment=1.3
```

**Required Flags:**
- `--exemption-id`: ID of the emergency exemption to use
- `--from-address`: Source address for the transfer
- `--to-address`: Destination address for the transfer  
- `--amount`: Amount to transfer (must be within exemption limit)
- `--denom`: Token denomination
- `--from`: Account signing the transaction

**Example with Multiple Denominations:**
```bash
inferenced tx restrictions execute-emergency-transfer \
  --exemption-id="multi-token-exemption" \
  --from-address="cosmos1source" \
  --to-address="cosmos1destination" \
  --amount="100000" \
  --denom="uatom" \
  --from="cosmos1source" \
  --gas=200000
```

## Governance Commands

### Submit Parameter Change Proposal

Submit a governance proposal to update restriction parameters:

```bash
inferenced tx gov submit-proposal update-restrictions-params \
  --restriction-end-block=1600000 \
  --emergency-exemptions='[{
    "exemption_id": "new-emergency-001",
    "from_address": "cosmos1critical_address",
    "to_address": "*",
    "max_amount": "5000000",
    "usage_limit": "10",
    "expiry_block": "1580000",
    "justification": "Critical infrastructure emergency"
  }]' \
  --exemption-usage-tracking='[]' \
  --title="Add Emergency Exemption" \
  --summary="Add critical emergency exemption for infrastructure" \
  --deposit="10000uatom" \
  --from="cosmos1proposer_address"
```

**Parameter Flags:**
- `--restriction-end-block`: New restriction end block height
- `--emergency-exemptions`: JSON array of exemption objects
- `--exemption-usage-tracking`: JSON array of usage tracking entries
- `--title`: Proposal title
- `--summary`: Proposal description
- `--deposit`: Proposal deposit (minimum required)

### Vote on Proposals

Vote on governance proposals:

```bash
# Vote yes on a proposal
inferenced tx gov vote [proposal-id] yes --from="cosmos1voter_address"

# Vote no on a proposal
inferenced tx gov vote [proposal-id] no --from="cosmos1voter_address"

# Abstain from voting
inferenced tx gov vote [proposal-id] abstain --from="cosmos1voter_address"

# Vote no with veto (burns deposit if proposal passes)
inferenced tx gov vote [proposal-id] no_with_veto --from="cosmos1voter_address"
```

### Query Governance Proposals

View restriction-related governance proposals:

```bash
# List all proposals
inferenced query gov proposals

# Get specific proposal
inferenced query gov proposal [proposal-id]

# Filter by status
inferenced query gov proposals --status=voting_period
```

## Common Use Cases

### For Users

#### Check if transfers are restricted:
```bash
# Quick status check
inferenced query restrictions transfer-restriction-status | jq .is_active

# Time remaining (in blocks)
inferenced query restrictions transfer-restriction-status | jq .remaining_blocks
```

#### Execute emergency transfer:
```bash
# 1. First, check available exemptions
inferenced query restrictions transfer-exemptions

# 2. Check your usage for specific exemption
inferenced query restrictions exemption-usage exemption-001 $(inferenced keys show mykey --address)

# 3. Execute transfer if within limits
inferenced tx restrictions execute-emergency-transfer \
  --exemption-id="exemption-001" \
  --from-address=$(inferenced keys show mykey --address) \
  --to-address="cosmos1destination" \
  --amount="50000" \
  --denom="uatom" \
  --from=mykey
```

### For Validators

#### Monitor restriction status:
```bash
# Create monitoring script
cat << 'EOF' > check_restrictions.sh
#!/bin/bash
STATUS=$(inferenced query restrictions transfer-restriction-status)
IS_ACTIVE=$(echo $STATUS | jq -r .is_active)
REMAINING=$(echo $STATUS | jq -r .remaining_blocks)

echo "Restrictions Active: $IS_ACTIVE"
echo "Blocks Remaining: $REMAINING"

if [ "$REMAINING" -lt "100000" ]; then
    echo "WARNING: Less than 100,000 blocks remaining!"
fi
EOF

chmod +x check_restrictions.sh
```

#### Vote on restriction proposals:
```bash
# List active proposals
PROPOSALS=$(inferenced query gov proposals --status=voting_period | jq -r '.proposals[] | select(.messages[0]["@type"] | contains("restrictions")) | .id')

# Vote on each restriction-related proposal
for PROPOSAL_ID in $PROPOSALS; do
    echo "Voting on proposal $PROPOSAL_ID"
    inferenced tx gov vote $PROPOSAL_ID yes --from=validator_key
done
```

### For Network Operators

#### Emergency exemption creation:
```bash
# Create emergency exemption for critical infrastructure
cat << 'EOF' > emergency_exemption.json
{
  "exemption_id": "emergency-$(date +%s)",
  "from_address": "cosmos1critical_service",
  "to_address": "*", 
  "max_amount": "1000000",
  "usage_limit": "50",
  "expiry_block": "1500000",
  "justification": "Critical service recovery after system failure"
}
EOF

# Submit proposal
inferenced tx gov submit-proposal update-restrictions-params \
  --emergency-exemptions="[$(cat emergency_exemption.json)]" \
  --title="Emergency Infrastructure Exemption" \
  --summary="Critical exemption for service recovery" \
  --deposit="10000uatom" \
  --from=operator_key
```

#### Bulk exemption management:
```bash
# Export current exemptions
inferenced query restrictions transfer-exemptions | jq .exemptions > current_exemptions.json

# Add new exemption to existing list
NEW_EXEMPTION='{
  "exemption_id": "bulk-operations-001",
  "from_address": "*",
  "to_address": "cosmos1operations_pool",
  "max_amount": "2000000", 
  "usage_limit": "200",
  "expiry_block": "1400000",
  "justification": "Bulk operations for ecosystem development"
}'

jq ". += [$NEW_EXEMPTION]" current_exemptions.json > updated_exemptions.json

# Submit updated exemption list
inferenced tx gov submit-proposal update-restrictions-params \
  --emergency-exemptions="$(cat updated_exemptions.json)" \
  --title="Update Exemption List" \
  --summary="Add bulk operations exemption" \
  --deposit="10000uatom" \
  --from=operator_key
```

## Script Examples

### Monitoring Script

```bash
#!/bin/bash
# restrictions_monitor.sh - Monitor transfer restrictions

LOG_FILE="/var/log/restrictions_monitor.log"

# Function to log with timestamp
log() {
    echo "$(date '+%Y-%m-%d %H:%M:%S') $1" | tee -a $LOG_FILE
}

# Check restriction status
STATUS=$(inferenced query restrictions transfer-restriction-status 2>/dev/null)
if [ $? -ne 0 ]; then
    log "ERROR: Failed to query restriction status"
    exit 1
fi

IS_ACTIVE=$(echo $STATUS | jq -r .is_active)
REMAINING=$(echo $STATUS | jq -r .remaining_blocks)
CURRENT_HEIGHT=$(echo $STATUS | jq -r .current_block_height)

log "Restriction Status: Active=$IS_ACTIVE, Remaining=$REMAINING blocks, Height=$CURRENT_HEIGHT"

# Alert if nearing end (less than 7 days assuming 6s block time)
if [ "$REMAINING" -lt "100800" ] && [ "$IS_ACTIVE" = "true" ]; then
    log "ALERT: Less than 7 days remaining until restriction lifting!"
fi

# Check exemption usage
EXEMPTIONS=$(inferenced query restrictions transfer-exemptions 2>/dev/null | jq -r '.exemptions[].exemption_id')
for EXEMPTION_ID in $EXEMPTIONS; do
    # Note: This would need account addresses to check usage
    log "Exemption available: $EXEMPTION_ID"
done
```

### Backup Script

```bash
#!/bin/bash
# backup_restrictions_config.sh - Backup restriction configuration

BACKUP_DIR="/backup/restrictions/$(date +%Y%m%d_%H%M%S)"
mkdir -p $BACKUP_DIR

# Backup current parameters
inferenced query restrictions params > $BACKUP_DIR/params.json

# Backup exemptions
inferenced query restrictions transfer-exemptions > $BACKUP_DIR/exemptions.json

# Backup current status
inferenced query restrictions transfer-restriction-status > $BACKUP_DIR/status.json

# Create restore script
cat << 'EOF' > $BACKUP_DIR/restore.sh
#!/bin/bash
# Restore restrictions configuration from backup

echo "This would require a governance proposal to restore configuration:"
echo "inferenced tx gov submit-proposal update-restrictions-params \\"
echo "  --emergency-exemptions='$(cat exemptions.json | jq -c .exemptions)' \\"
echo "  --title='Restore Restrictions Configuration' \\"
echo "  --summary='Restore from backup taken $(date)' \\"
echo "  --deposit='10000uatom' \\"
echo "  --from=governance_key"
EOF

chmod +x $BACKUP_DIR/restore.sh

echo "Backup created in $BACKUP_DIR"
```

### Testing Script

```bash
#!/bin/bash
# test_restrictions.sh - Test restriction functionality

# Test 1: Verify restrictions are active
echo "Test 1: Checking restriction status..."
STATUS=$(inferenced query restrictions transfer-restriction-status)
IS_ACTIVE=$(echo $STATUS | jq -r .is_active)

if [ "$IS_ACTIVE" = "true" ]; then
    echo "✓ Restrictions are active"
else
    echo "✗ Restrictions are not active"
fi

# Test 2: Try user-to-user transfer (should fail)
echo "Test 2: Testing user-to-user transfer restriction..."
USER1=$(inferenced keys show test-user1 --address 2>/dev/null)
USER2=$(inferenced keys show test-user2 --address 2>/dev/null)

if [ -n "$USER1" ] && [ -n "$USER2" ]; then
    RESULT=$(inferenced tx bank send $USER1 $USER2 1000uatom --dry-run 2>&1)
    if echo "$RESULT" | grep -q "transfer restricted"; then
        echo "✓ User-to-user transfers properly blocked"
    else
        echo "✗ User-to-user transfers not blocked"
    fi
else
    echo "⚠ Test users not found, skipping transfer test"
fi

# Test 3: Check emergency exemptions
echo "Test 3: Checking emergency exemptions..."
EXEMPTIONS=$(inferenced query restrictions transfer-exemptions)
EXEMPTION_COUNT=$(echo $EXEMPTIONS | jq '.exemptions | length')
echo "Available exemptions: $EXEMPTION_COUNT"

# Test 4: Verify module account detection
echo "Test 4: Checking module account access..."
FEE_COLLECTOR=$(inferenced query auth module-account fee_collector 2>/dev/null | jq -r .account.base_account.address)
if [ -n "$FEE_COLLECTOR" ]; then
    echo "✓ Fee collector module account accessible: $FEE_COLLECTOR"
else
    echo "✗ Fee collector module account not found"
fi

echo "Restriction testing complete"
```

## Troubleshooting

### Common Errors

#### "transfer restricted during bootstrap period"
```bash
# Check restriction status
inferenced query restrictions transfer-restriction-status

# If active, check for available exemptions
inferenced query restrictions transfer-exemptions

# Use emergency exemption if available
inferenced tx restrictions execute-emergency-transfer [flags]
```

#### "exemption not found"
```bash
# List all available exemptions
inferenced query restrictions transfer-exemptions

# Verify exemption ID spelling
# Check if exemption has expired
```

#### "usage limit exceeded" 
```bash
# Check current usage
inferenced query restrictions exemption-usage [exemption-id] [your-address]

# Wait for exemption limit reset or request new exemption via governance
```

#### "invalid authority"
```bash
# Only governance module can update parameters
# Submit proposal through governance process
inferenced tx gov submit-proposal update-restrictions-params [flags]
```

### Debug Commands

```bash
# Check transaction details
inferenced query tx [transaction-hash]

# Verify account balances
inferenced query bank balances [address]

# Check gas estimation
inferenced tx restrictions execute-emergency-transfer [flags] --dry-run

# View module accounts
inferenced query auth module-accounts
```

## Best Practices

### Security
- Always use `--dry-run` to test commands before execution
- Verify exemption details before using emergency transfers
- Keep exemption usage within reasonable limits
- Monitor exemption usage regularly

### Performance
- Use specific addresses instead of wildcards when possible
- Batch operations when multiple exemptions needed
- Cache query results for frequently accessed data

### Governance
- Provide clear justification for all proposals
- Allow adequate voting time for parameter changes
- Coordinate with validators before submitting proposals
- Document all changes for future reference

This CLI guide provides comprehensive coverage of all transfer restrictions commands and common usage patterns. Regular practice with these commands ensures smooth operation during restriction periods.
