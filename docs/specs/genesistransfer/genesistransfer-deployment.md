# Genesis Account Ownership Transfer - Deployment Guide

## Overview

This guide provides comprehensive instructions for deploying and managing the genesis account ownership transfer system in production environments. The system enables secure, atomic, and irreversible transfer of genesis accounts including all liquid balances and vesting schedules.

## Prerequisites

### System Requirements

- **Cosmos SDK**: v0.50+ with modern dependency injection support
- **Go Version**: 1.21+ for compilation
- **Network Status**: Active blockchain network with governance functionality
- **Authority Access**: Governance module authority for executing transfers

### Module Dependencies

- **Bank Module**: For balance transfers and module account management
- **Auth Module**: For account management and vesting account support
- **Governance Module**: For parameter management and transfer authorization
- **Bookkeeper Module**: For transaction logging and audit trails (if available)

## Genesis Account Setup and Configuration

### 1. Genesis Account Identification

Before deployment, identify all genesis accounts that may require ownership transfer:

```bash
# List all genesis accounts with balances
inferenced query bank balances [genesis-address] --output json

# Check if account has vesting schedule
inferenced query auth account [genesis-address] --output json

# Verify account type and vesting details
inferenced query streamvesting vesting-schedule [genesis-address] --output json
```

### 2. Genesis Account Documentation

Create a comprehensive registry of genesis accounts:

```json
{
  "genesis_accounts": [
    {
      "address": "gonka1abc...",
      "purpose": "Team allocation",
      "initial_balance": "1000000ugonka",
      "vesting_type": "PeriodicVesting",
      "vesting_periods": 24,
      "intended_recipient": "gonka1def...",
      "transfer_priority": "high"
    }
  ]
}
```

### 3. Module Parameter Configuration

Configure module parameters before deployment:

```bash
# Set whitelist enforcement (optional)
inferenced tx gov submit-proposal param-change proposal.json \
  --from governance \
  --keyring-backend file

# Example proposal.json for whitelist configuration
{
  "title": "Enable Genesis Transfer Whitelist",
  "description": "Enable whitelist enforcement for genesis account transfers",
  "changes": [
    {
      "subspace": "genesistransfer",
      "key": "RestrictToList",
      "value": "true"
    },
    {
      "subspace": "genesistransfer", 
      "key": "AllowedAccounts",
      "value": ["gonka1abc...", "gonka1def..."]
    }
  ]
}
```

## Transfer Execution Procedures and Best Practices

### 1. Pre-Transfer Validation Checklist

Before executing any ownership transfer, complete this validation checklist:

#### Account Validation
```bash
# 1. Verify genesis account exists and has balance
inferenced query bank balances [genesis-address]

# 2. Check transfer eligibility
inferenced query genesistransfer transfer-eligibility [genesis-address]

# 3. Verify no previous transfer
inferenced query genesistransfer transfer-status [genesis-address]

# 4. Validate recipient address format
inferenced keys parse [recipient-address]
```

#### Network Status Validation
```bash
# 5. Check current block height and network status
inferenced status

# 6. Verify governance module is functional
inferenced query gov params

# 7. Check if transfer restrictions are active
inferenced query restrictions restriction-status
```

### 2. Transfer Execution Procedure

#### Step 1: Prepare Transfer Transaction
```bash
# Create transfer transaction (dry-run first)
inferenced tx genesistransfer transfer-ownership \
  [genesis-address] [recipient-address] \
  --from governance \
  --keyring-backend file \
  --dry-run \
  --gas 2000000

# Example output validation:
# - Transaction should estimate gas successfully
# - No simulation errors should occur
# - Estimated gas should be reasonable (<500k typically)
```

#### Step 2: Execute Transfer
```bash
# Execute actual transfer
inferenced tx genesistransfer transfer-ownership \
  [genesis-address] [recipient-address] \
  --from governance \
  --keyring-backend file \
  --gas 2000000 \
  --gas-adjustment 1.5 \
  --yes

# Wait for transaction inclusion
inferenced query tx [transaction-hash]
```

#### Step 3: Verify Transfer Completion
```bash
# 1. Check transfer record creation
inferenced query genesistransfer transfer-status [genesis-address]

# 2. Verify balance transfer
inferenced query bank balances [genesis-address]   # Should be 0 or minimal
inferenced query bank balances [recipient-address] # Should show transferred amount

# 3. Verify vesting schedule transfer (if applicable)
inferenced query auth account [recipient-address]  # Should show vesting account type
```

### 3. Batch Transfer Operations

For multiple genesis accounts, use this systematic approach:

#### Batch Transfer Script Template
```bash
#!/bin/bash
# batch_genesis_transfer.sh

GENESIS_ACCOUNTS=(
  "gonka1abc...:gonka1recipient1..."
  "gonka1def...:gonka1recipient2..."
)

for transfer in "${GENESIS_ACCOUNTS[@]}"; do
  IFS=':' read -r genesis recipient <<< "$transfer"
  
  echo "Processing transfer: $genesis → $recipient"
  
  # Pre-validation
  if ! inferenced query genesistransfer transfer-eligibility "$genesis" | grep -q "eligible.*true"; then
    echo "ERROR: $genesis not eligible for transfer"
    continue
  fi
  
  # Execute transfer
  if inferenced tx genesistransfer transfer-ownership "$genesis" "$recipient" \
    --from governance --keyring-backend file --gas 2000000 --yes; then
    echo "SUCCESS: Transfer initiated for $genesis"
    sleep 10  # Wait for block inclusion
  else
    echo "ERROR: Transfer failed for $genesis"
  fi
  
  # Verify completion
  if inferenced query genesistransfer transfer-status "$genesis" | grep -q "completed.*true"; then
    echo "VERIFIED: Transfer completed for $genesis"
  else
    echo "WARNING: Transfer status unclear for $genesis"
  fi
  
  echo "---"
done
```

## Security Considerations and Private Key Management

### 1. Authority Key Security

The genesis transfer module requires governance authority for execution:

#### Key Storage Best Practices
```bash
# Use hardware security module (HSM) for governance keys
export KEYRING_BACKEND=file
export KEYRING_DIR=/secure/keyring/path

# Create secure keyring directory
mkdir -p /secure/keyring/path
chmod 700 /secure/keyring/path

# Import governance key securely
inferenced keys import governance governance-key.json \
  --keyring-backend file \
  --keyring-dir /secure/keyring/path
```

#### Multi-Signature Governance (Recommended)
```bash
# For production networks, use multi-signature governance
# This requires multiple validators to sign transfer transactions

# Create multi-sig governance account
inferenced keys add governance-multisig \
  --multisig validator1,validator2,validator3 \
  --multisig-threshold 2

# Execute transfers with multi-sig
inferenced tx genesistransfer transfer-ownership [genesis] [recipient] \
  --from governance-multisig \
  --generate-only > transfer-tx.json

# Each validator signs
inferenced tx sign transfer-tx.json \
  --from validator1 \
  --multisig governance-multisig > sig1.json

# Combine signatures and broadcast
inferenced tx multisign transfer-tx.json governance-multisig sig1.json sig2.json > signed-tx.json
inferenced tx broadcast signed-tx.json
```

### 2. Transfer Security Validation

#### Pre-Transfer Security Checklist
- [ ] **Authority Verification**: Confirm governance key control
- [ ] **Recipient Validation**: Verify recipient address ownership
- [ ] **Network Security**: Ensure network is not under attack
- [ ] **Backup Procedures**: Document recovery procedures
- [ ] **Audit Trail**: Prepare monitoring and logging

#### Post-Transfer Security Verification
```bash
# 1. Verify transfer record integrity
inferenced query genesistransfer transfer-status [genesis-address] --output json

# 2. Confirm balance transfer accuracy
EXPECTED_AMOUNT=[calculated-amount]
ACTUAL_AMOUNT=$(inferenced query bank balances [recipient-address] --output json | jq -r '.balances[0].amount')

if [ "$EXPECTED_AMOUNT" = "$ACTUAL_AMOUNT" ]; then
  echo "✅ Balance transfer verified"
else
  echo "❌ Balance mismatch: expected $EXPECTED_AMOUNT, got $ACTUAL_AMOUNT"
fi

# 3. Verify vesting schedule preservation (if applicable)
inferenced query auth account [recipient-address] --output json | jq '.account.vesting_periods'
```

## Monitoring and Audit Trail Setup

### 1. Transfer Event Monitoring

Set up monitoring for transfer-related events:

#### Event Subscription
```bash
# Subscribe to ownership transfer events
inferenced query tx-events \
  --query "ownership_transfer_completed.genesis_address='gonka1abc...'" \
  --page 1 --limit 100

# Monitor transfer record creation
inferenced query tx-events \
  --query "message.module='genesistransfer'" \
  --page 1 --limit 100
```

#### Log Monitoring Configuration
```bash
# Configure log aggregation for transfer events
# Add to your log monitoring system:

# Successful transfers
grep "balance transfer completed via module account" /var/log/inferenced.log

# Transfer validation failures  
grep "transfer validation failed" /var/log/inferenced.log

# Transfer record storage
grep "transfer record stored" /var/log/inferenced.log
```

### 2. Audit Trail Verification

#### Daily Audit Procedures
```bash
#!/bin/bash
# daily_audit.sh

echo "=== Daily Genesis Transfer Audit ==="
echo "Date: $(date)"

# 1. Check total transfers completed
TOTAL_TRANSFERS=$(inferenced query genesistransfer transfer-history --output json | jq '.transfer_records | length')
echo "Total completed transfers: $TOTAL_TRANSFERS"

# 2. Verify transfer record integrity
inferenced query genesistransfer transfer-history --output json | \
  jq -r '.transfer_records[] | "Transfer: \(.genesis_address) → \(.recipient_address) at height \(.transfer_height)"'

# 3. Check for any failed transactions
inferenced query tx-events \
  --query "message.module='genesistransfer' AND tx.result.code!=0" \
  --page 1 --limit 10

# 4. Verify no duplicate transfers
DUPLICATE_CHECK=$(inferenced query genesistransfer transfer-history --output json | \
  jq -r '.transfer_records[].genesis_address' | sort | uniq -d)

if [ -z "$DUPLICATE_CHECK" ]; then
  echo "✅ No duplicate transfers detected"
else
  echo "❌ ALERT: Duplicate transfers detected: $DUPLICATE_CHECK"
fi

echo "=== Audit Complete ==="
```

### 3. Monitoring Dashboard Setup

#### Key Metrics to Monitor
- **Transfer Completion Rate**: Successful vs failed transfers
- **Transfer Volume**: Total amounts and denominations transferred
- **Account Types**: Vesting vs non-vesting account transfers
- **Timeline**: Transfer frequency and patterns
- **Security**: Failed validation attempts and reasons

#### Grafana Dashboard Configuration
```json
{
  "dashboard": {
    "title": "Genesis Transfer Monitoring",
    "panels": [
      {
        "title": "Total Transfers Completed",
        "type": "stat",
        "targets": [
          {
            "expr": "count(cosmos_genesistransfer_transfers_total)",
            "legendFormat": "Completed Transfers"
          }
        ]
      },
      {
        "title": "Transfer Amounts by Denomination", 
        "type": "graph",
        "targets": [
          {
            "expr": "sum by (denom) (cosmos_genesistransfer_amount_total)",
            "legendFormat": "{{denom}}"
          }
        ]
      }
    ]
  }
}
```

## Troubleshooting Common Issues and Recovery Procedures

### 1. Common Transfer Issues

#### Issue: Transfer Blocked by Restrictions
```bash
# Symptoms: Transfer fails with "user-to-user transfers are restricted"
# Cause: Transfer restrictions active, module account bypass not working
# Solution: Verify module account permissions and restrictions module integration

# Check module account permissions
inferenced query auth account $(inferenced keys show genesistransfer --address) --output json

# Verify restrictions module status
inferenced query restrictions restriction-status

# Check if genesistransfer is in known modules list
grep -n "genesistransfer" x/restrictions/keeper/send_restriction.go
```

#### Issue: One-Time Enforcement Not Working
```bash
# Symptoms: Same genesis account can be transferred multiple times
# Cause: Transfer record not being stored or retrieved correctly
# Solution: Verify transfer record storage and retrieval

# Check transfer record storage
inferenced query genesistransfer transfer-status [genesis-address]

# Verify store key configuration
grep -n "TransferRecordKeyPrefix" x/genesistransfer/types/keys.go

# Test transfer record functionality
inferenced query genesistransfer transfer-history
```

#### Issue: Vesting Schedule Not Transferred
```bash
# Symptoms: Balance transferred but vesting schedule missing
# Cause: Vesting account creation or registration failure
# Solution: Verify account type detection and vesting account creation

# Check original account type
inferenced query auth account [genesis-address] --output json | jq '.account."@type"'

# Verify recipient account after transfer
inferenced query auth account [recipient-address] --output json | jq '.account'

# Check vesting creation logs
grep "vesting account created" /var/log/inferenced.log
```

### 2. Recovery Procedures

#### Emergency Transfer Rollback (Not Recommended)
```bash
# WARNING: This is only for extreme emergencies and may require chain halt
# Genesis transfers are designed to be irreversible for security

# If absolutely necessary, use governance emergency procedures:
# 1. Halt the chain
# 2. Export state
# 3. Manually edit genesis state
# 4. Restart with corrected state
# 5. Use governance to prevent future issues
```

#### Transfer Record Correction
```bash
# If transfer records are corrupted, use administrative functions
# (These should only be used in development/testing environments)

# Delete incorrect transfer record (testing only)
inferenced tx genesistransfer delete-transfer-record [genesis-address] \
  --from governance

# Verify record deletion
inferenced query genesistransfer transfer-status [genesis-address]
```

#### Whitelist Emergency Updates
```bash
# Add emergency account to whitelist
inferenced tx gov submit-proposal param-change whitelist-emergency.json \
  --from governance

# Example whitelist-emergency.json
{
  "title": "Emergency Whitelist Update",
  "description": "Add critical account to transfer whitelist",
  "changes": [
    {
      "subspace": "genesistransfer",
      "key": "AllowedAccounts", 
      "value": ["gonka1existing...", "gonka1emergency..."]
    }
  ]
}
```

## Network Deployment Procedures

### 1. Pre-Deployment Testing

#### Testnet Validation
```bash
# 1. Deploy to testnet environment
make testnet-deploy

# 2. Execute comprehensive test suite
make node-test

# 3. Run end-to-end tests
cd testermint && ./gradlew test --tests GenesisTransferTests

# 4. Validate transfer restrictions integration
cd testermint && ./gradlew test --tests RestrictionsTests
```

#### Load Testing
```bash
# Test multiple concurrent transfers (testnet only)
for i in {1..10}; do
  inferenced tx genesistransfer transfer-ownership \
    "gonka1test$i..." "gonka1recipient$i..." \
    --from governance --yes &
done
wait

# Verify all transfers completed correctly
inferenced query genesistransfer transfer-history
```

### 2. Production Deployment

#### Phase 1: Module Activation
```bash
# 1. Deploy module via governance upgrade
inferenced tx gov submit-proposal software-upgrade genesis-transfer-v1 \
  --upgrade-height [target-height] \
  --upgrade-info '{"binaries":{"linux/amd64":"[binary-url]"}}' \
  --from governance

# 2. Wait for upgrade execution
inferenced query gov proposal [proposal-id]

# 3. Verify module activation
inferenced query genesistransfer params
```

#### Phase 2: Parameter Configuration
```bash
# Configure production parameters
inferenced tx gov submit-proposal param-change genesis-transfer-params.json \
  --from governance

# Monitor parameter change proposal
inferenced query gov proposal [proposal-id] --output json
```

#### Phase 3: Initial Transfer Execution
```bash
# Execute highest priority transfers first
# Use the batch transfer script with careful monitoring

# Start with test transfer (small amount)
inferenced tx genesistransfer transfer-ownership \
  [test-genesis-address] [test-recipient-address] \
  --from governance --gas 2000000 --yes

# Monitor and verify before proceeding with larger transfers
```

### 3. Post-Deployment Monitoring

#### Real-Time Monitoring Setup
```bash
# Set up continuous monitoring
tail -f /var/log/inferenced.log | grep "genesistransfer"

# Monitor transfer events
inferenced query tx-events \
  --query "message.module='genesistransfer'" \
  --subscribe

# Set up alerts for failed transfers
grep "ownership transfer failed" /var/log/inferenced.log | \
  mail -s "Genesis Transfer Alert" admin@example.com
```

## Operational Best Practices

### 1. Transfer Scheduling

- **Peak Hours Avoidance**: Execute transfers during low network activity
- **Batch Processing**: Group related transfers to minimize governance overhead
- **Priority Ordering**: Transfer critical accounts first
- **Verification Windows**: Allow time for verification between transfers

### 2. Communication Procedures

#### Stakeholder Notification
```bash
# Before transfer execution
echo "Genesis account transfer scheduled for [genesis-address] at block [height]" | \
  notify-stakeholders.sh

# After transfer completion
echo "Genesis account transfer completed: [genesis-address] → [recipient-address]" | \
  notify-stakeholders.sh
```

#### Documentation Updates
- Update internal account registries
- Notify relevant teams of address changes
- Update any hardcoded addresses in applications
- Document transfer completion in operational logs

### 3. Backup and Recovery

#### State Backup Before Major Transfers
```bash
# Export current state before major transfers
inferenced export > state-backup-$(date +%Y%m%d).json

# Verify backup integrity
jq '.app_state.genesistransfer' state-backup-$(date +%Y%m%d).json
```

#### Recovery Procedures
```bash
# If transfer issues occur, use these recovery steps:

# 1. Halt further transfers
echo "TRANSFER_HALT=true" > /etc/inferenced/transfer.conf

# 2. Analyze the issue
inferenced query genesistransfer transfer-history --output json > transfer-analysis.json

# 3. Prepare governance resolution
# 4. Execute corrective measures via governance
# 5. Resume operations with updated procedures
```

## Maintenance and Updates

### 1. Regular Maintenance Tasks

#### Weekly Tasks
- Review transfer completion logs
- Verify audit trail integrity
- Check parameter configuration
- Monitor network performance impact

#### Monthly Tasks
- Analyze transfer patterns and statistics
- Review security procedures
- Update documentation as needed
- Plan upcoming transfers

### 2. Module Updates

#### Governance Parameter Updates
```bash
# Update restriction end block (if needed)
inferenced tx gov submit-proposal param-change restriction-update.json

# Update whitelist (add/remove accounts)
inferenced tx gov submit-proposal param-change whitelist-update.json

# Monitor parameter change proposals
inferenced query gov proposals --status voting_period
```

#### Emergency Procedures
```bash
# Disable whitelist enforcement (emergency)
{
  "title": "Emergency Whitelist Disable",
  "description": "Disable whitelist enforcement for emergency transfers",
  "changes": [
    {
      "subspace": "genesistransfer",
      "key": "RestrictToList",
      "value": "false"
    }
  ]
}
```

## Compliance and Reporting

### 1. Regulatory Compliance

#### Transfer Documentation Requirements
- **Transfer Justification**: Document purpose of each transfer
- **Recipient Verification**: Confirm recipient identity and authorization
- **Amount Validation**: Verify transfer amounts against allocations
- **Timeline Compliance**: Ensure transfers meet regulatory timelines

#### Audit Report Generation
```bash
#!/bin/bash
# generate_audit_report.sh

echo "=== Genesis Transfer Audit Report ==="
echo "Report Date: $(date)"
echo "Network: $(inferenced status | jq -r '.NodeInfo.network')"

# Transfer summary
TOTAL_TRANSFERS=$(inferenced query genesistransfer transfer-history --output json | jq '.transfer_records | length')
echo "Total Transfers Completed: $TOTAL_TRANSFERS"

# Transfer details
inferenced query genesistransfer transfer-history --output json | \
  jq -r '.transfer_records[] | "Transfer: \(.genesis_address) → \(.recipient_address) | Amount: \(.transfer_amount) | Height: \(.transfer_height)"'

# Security validation
echo "=== Security Validation ==="
echo "One-time enforcement: $([ $TOTAL_TRANSFERS -eq $(inferenced query genesistransfer transfer-history --output json | jq -r '.transfer_records[].genesis_address' | sort | uniq | wc -l) ] && echo "✅ PASSED" || echo "❌ FAILED")"

echo "=== Report Complete ==="
```

### 2. Performance Monitoring

#### Network Impact Assessment
```bash
# Monitor gas usage for transfers
inferenced query tx-events \
  --query "message.module='genesistransfer'" \
  --output json | jq '.txs[].gas_used'

# Check block processing time impact
# (Transfers should not significantly impact block times)

# Monitor module account balance
inferenced query bank balances $(inferenced keys show genesistransfer --address)
```

This deployment guide provides comprehensive procedures for safely deploying and managing genesis account ownership transfers in production environments. Follow these procedures carefully to ensure secure and successful transfer operations.
