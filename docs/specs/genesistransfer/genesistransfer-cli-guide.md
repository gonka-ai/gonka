# Genesis Transfer CLI Guide

This comprehensive guide covers all CLI commands and procedures for the Genesis Transfer module, which enables secure ownership transfer of genesis accounts including liquid balances and vesting schedules.

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Query Commands](#query-commands)
- [Transaction Commands](#transaction-commands)
- [Common Workflows](#common-workflows)
- [Security Best Practices](#security-best-practices)
- [Troubleshooting](#troubleshooting)
- [Examples](#examples)

## Overview

The Genesis Transfer module provides CLI commands for:
- **Query Commands**: Check transfer status, history, and eligibility without making changes
- **Transaction Commands**: Execute ownership transfers (requires proper authority)
- **Parameter Management**: View module configuration and whitelist settings

All commands follow the standard Cosmos SDK CLI patterns and support both mainnet and testnet configurations.

## Prerequisites

### Required Setup

1. **Node Access**: Access to a running inference node with the genesis transfer module enabled
2. **CLI Binary**: The `inferenced` binary installed and configured
3. **Account Access**: For transactions, access to accounts with proper authority
4. **Network Configuration**: Proper chain-id and node endpoint configuration

### Authority Requirements

- **Query Commands**: No special permissions required - anyone can query public information
- **Transfer Transactions**: Requires authority over the genesis account being transferred
- **Parameter Updates**: Requires governance authority (typically x/gov module account)

## Query Commands

All query commands are read-only and do not require gas fees or account access.

### 1. Module Parameters

Query the current module configuration including whitelist settings.

```bash
# Basic parameters query
inferenced query genesistransfer params

# With specific node endpoint
inferenced query genesistransfer params --node https://rpc.inference-network.com:443

# JSON output format
inferenced query genesistransfer params --output json
```

**Response Fields:**
- `allowed_accounts`: List of whitelisted genesis account addresses
- `restrict_to_list`: Boolean indicating if whitelist enforcement is enabled

**Example Response:**
```json
{
  "params": {
    "allowed_accounts": [
      "cosmos1abc123...",
      "cosmos1def456..."
    ],
    "restrict_to_list": true
  }
}
```

### 2. Transfer Status

Check if a specific genesis account has been transferred and view transfer details.

```bash
# Check transfer status for a genesis account
inferenced query genesistransfer transfer-status <genesis-address>

# Example with specific address
inferenced query genesistransfer transfer-status cosmos1genesis123example456

# JSON output with node specification
inferenced query genesistransfer transfer-status cosmos1genesis123example456 \
  --node https://rpc.inference-network.com:443 \
  --output json
```

**Response Fields:**
- `is_transferred`: Boolean indicating if transfer is complete
- `transfer_record`: Detailed transfer information (if completed)
  - `genesis_address`: Original genesis account address
  - `recipient_address`: Destination account address
  - `transfer_height`: Block height when transfer occurred
  - `completed`: Transfer completion status
  - `transferred_denoms`: List of token denominations transferred
  - `transfer_amount`: Total amount transferred

**Example Response:**
```json
{
  "is_transferred": true,
  "transfer_record": {
    "genesis_address": "cosmos1genesis123example456",
    "recipient_address": "cosmos1recipient789example",
    "transfer_height": "1234567",
    "completed": true,
    "transferred_denoms": ["nicoin"],
    "transfer_amount": "1000000000"
  }
}
```

### 3. Transfer History

Retrieve all completed transfers with optional pagination.

```bash
# Get all transfer records
inferenced query genesistransfer transfer-history

# With pagination (limit 10 results)
inferenced query genesistransfer transfer-history --limit 10

# With pagination offset
inferenced query genesistransfer transfer-history --offset 20 --limit 10

# JSON output
inferenced query genesistransfer transfer-history --output json
```

**Response Fields:**
- `transfer_records`: Array of all transfer records
- `pagination`: Pagination information for large result sets

### 4. Transfer Eligibility

Validate whether a genesis account can be transferred.

```bash
# Check eligibility for transfer
inferenced query genesistransfer transfer-eligibility <genesis-address>

# Example with specific address
inferenced query genesistransfer transfer-eligibility cosmos1genesis123example456
```

**Response Fields:**
- `is_eligible`: Boolean indicating transfer eligibility
- `reason`: Explanation if not eligible
- `already_transferred`: Boolean indicating if already transferred

**Example Responses:**
```json
// Eligible account
{
  "is_eligible": true,
  "reason": "",
  "already_transferred": false
}

// Ineligible account
{
  "is_eligible": false,
  "reason": "account not in allowed list",
  "already_transferred": false
}
```

### 5. Allowed Accounts

View the whitelist of accounts eligible for transfer.

```bash
# Get allowed accounts list
inferenced query genesistransfer allowed-accounts

# JSON output
inferenced query genesistransfer allowed-accounts --output json
```

**Response Fields:**
- `allowed_accounts`: Array of whitelisted account addresses
- `restrict_to_list`: Whether whitelist enforcement is active

## Transaction Commands

Transaction commands modify blockchain state and require gas fees and proper authority.

### Transfer Ownership

Execute complete ownership transfer of a genesis account.

```bash
# Basic transfer command
inferenced tx genesistransfer transfer-ownership <genesis-address> <recipient-address> \
  --from <authority-key> \
  --chain-id <chain-id> \
  --gas auto \
  --gas-adjustment 1.5

# Complete example with all parameters
inferenced tx genesistransfer transfer-ownership \
  cosmos1genesis123example456 \
  cosmos1recipient789example \
  --from genesis-authority \
  --chain-id inference-1 \
  --node https://rpc.inference-network.com:443 \
  --gas auto \
  --gas-adjustment 1.5 \
  --gas-prices 0.025nicoin \
  --broadcast-mode block

# Dry run to estimate gas
inferenced tx genesistransfer transfer-ownership \
  cosmos1genesis123example456 \
  cosmos1recipient789example \
  --from genesis-authority \
  --dry-run
```

**Required Parameters:**
- `genesis-address`: The source genesis account to transfer
- `recipient-address`: The destination account for ownership transfer
- `--from`: Key name or address with authority over the genesis account

**Important Options:**
- `--gas auto`: Automatically estimate gas requirements
- `--gas-adjustment 1.5`: Safety margin for gas estimation
- `--broadcast-mode block`: Wait for transaction inclusion in block
- `--dry-run`: Simulate transaction without broadcasting

## Common Workflows

### 1. Pre-Transfer Validation Workflow

Before executing a transfer, validate all conditions:

```bash
# Step 1: Check module parameters and whitelist status
inferenced query genesistransfer params

# Step 2: Verify account eligibility
inferenced query genesistransfer transfer-eligibility cosmos1genesis123example456

# Step 3: Check current transfer status (should be not transferred)
inferenced query genesistransfer transfer-status cosmos1genesis123example456

# Step 4: Verify account balances
inferenced query bank balances cosmos1genesis123example456

# Step 5: Check for vesting schedules
inferenced query auth account cosmos1genesis123example456
```

### 2. Transfer Execution Workflow

Execute the transfer with proper verification:

```bash
# Step 1: Dry run to estimate gas and validate
inferenced tx genesistransfer transfer-ownership \
  cosmos1genesis123example456 \
  cosmos1recipient789example \
  --from genesis-authority \
  --dry-run

# Step 2: Execute the transfer
inferenced tx genesistransfer transfer-ownership \
  cosmos1genesis123example456 \
  cosmos1recipient789example \
  --from genesis-authority \
  --chain-id inference-1 \
  --gas auto \
  --gas-adjustment 1.5 \
  --broadcast-mode block

# Step 3: Verify transfer completion
inferenced query genesistransfer transfer-status cosmos1genesis123example456

# Step 4: Verify balance transfer
inferenced query bank balances cosmos1recipient789example
```

### 3. Batch Transfer Validation

For multiple transfers, create a validation script:

```bash
#!/bin/bash
# validate-transfers.sh

GENESIS_ACCOUNTS=(
  "cosmos1genesis1..."
  "cosmos1genesis2..."
  "cosmos1genesis3..."
)

echo "=== Genesis Transfer Validation Report ==="
for account in "${GENESIS_ACCOUNTS[@]}"; do
  echo "Checking account: $account"
  
  # Check eligibility
  eligibility=$(inferenced query genesistransfer transfer-eligibility $account --output json)
  is_eligible=$(echo $eligibility | jq -r '.is_eligible')
  
  # Check current status
  status=$(inferenced query genesistransfer transfer-status $account --output json)
  is_transferred=$(echo $status | jq -r '.is_transferred')
  
  echo "  Eligible: $is_eligible"
  echo "  Already transferred: $is_transferred"
  echo "---"
done
```

### 4. Post-Transfer Audit

Verify transfer results and maintain audit trail:

```bash
# Generate transfer report
echo "=== Transfer Audit Report ===" > transfer-audit.txt
echo "Generated: $(date)" >> transfer-audit.txt
echo "" >> transfer-audit.txt

# Get all transfer history
inferenced query genesistransfer transfer-history --output json | \
  jq -r '.transfer_records[] | "Genesis: \(.genesis_address) → Recipient: \(.recipient_address) | Height: \(.transfer_height) | Amount: \(.transfer_amount)"' >> transfer-audit.txt

# Verify specific transfer
GENESIS_ADDR="cosmos1genesis123example456"
echo "Verifying transfer for: $GENESIS_ADDR" >> transfer-audit.txt
inferenced query genesistransfer transfer-status $GENESIS_ADDR >> transfer-audit.txt
```

## Security Best Practices

### 1. Authority Key Management

- **Use Hardware Wallets**: Store authority keys on hardware security modules (HSMs)
- **Multi-Signature Setup**: Configure multi-sig governance for parameter updates
- **Key Rotation**: Regularly rotate authority keys following security policies

### 2. Pre-Transfer Validation

- **Always Dry Run**: Test transactions with `--dry-run` before execution
- **Double-Check Addresses**: Verify genesis and recipient addresses are correct
- **Validate Balances**: Confirm expected balances before and after transfers
- **Check Whitelist**: Ensure accounts are properly whitelisted if enforcement is enabled

### 3. Transaction Security

```bash
# Use specific gas settings to avoid failures
--gas 300000 --gas-prices 0.025nicoin

# Wait for block confirmation
--broadcast-mode block

# Use trusted RPC endpoints
--node https://trusted-rpc-endpoint.com:443

# Verify chain ID
--chain-id inference-1
```

### 4. Monitoring and Alerts

Set up monitoring for:
- Failed transfer attempts
- Unauthorized access attempts
- Parameter changes
- Large balance transfers

## Troubleshooting

### Common Issues and Solutions

#### 1. Transfer Rejected - "Not in allowed list"

**Problem**: Account not whitelisted for transfer

**Solution**:
```bash
# Check whitelist status
inferenced query genesistransfer params

# If whitelist is enabled, account must be added via governance
# Check if account should be in whitelist
inferenced query genesistransfer allowed-accounts
```

#### 2. Transfer Rejected - "Already transferred"

**Problem**: One-time enforcement preventing duplicate transfer

**Solution**:
```bash
# Check transfer status to confirm
inferenced query genesistransfer transfer-status <genesis-address>

# Review transfer history for details
inferenced query genesistransfer transfer-history
```

#### 3. Insufficient Gas Error

**Problem**: Gas estimation too low

**Solution**:
```bash
# Use higher gas adjustment
--gas auto --gas-adjustment 2.0

# Or set specific gas limit
--gas 500000
```

#### 4. Account Not Found

**Problem**: Genesis account doesn't exist

**Solution**:
```bash
# Verify account exists
inferenced query auth account <genesis-address>

# Check account has balance
inferenced query bank balances <genesis-address>
```

#### 5. Authority Validation Failed

**Problem**: Incorrect authority for transfer

**Solution**:
```bash
# Verify authority key
inferenced keys list

# Ensure key has proper permissions
# For genesis accounts, authority is typically the account itself
```

### Diagnostic Commands

```bash
# Full system status check
inferenced status

# Check module is loaded
inferenced query genesistransfer params

# Verify account information
inferenced query auth account <address>

# Check transaction by hash
inferenced query tx <tx-hash>

# View recent blocks
inferenced query block <height>
```

## Examples

### Example 1: Simple Genesis Account Transfer

```bash
# 1. Validate the genesis account
inferenced query genesistransfer transfer-eligibility cosmos1genesis123abc

# 2. Check current balances
inferenced query bank balances cosmos1genesis123abc

# 3. Execute transfer
inferenced tx genesistransfer transfer-ownership \
  cosmos1genesis123abc \
  cosmos1recipient456def \
  --from genesis-key \
  --chain-id inference-1 \
  --gas auto \
  --gas-adjustment 1.5 \
  --broadcast-mode block

# 4. Verify completion
inferenced query genesistransfer transfer-status cosmos1genesis123abc
```

### Example 2: Vesting Account Transfer

```bash
# 1. Check for vesting schedule
inferenced query auth account cosmos1vesting123abc

# 2. Verify eligibility
inferenced query genesistransfer transfer-eligibility cosmos1vesting123abc

# 3. Execute transfer (same command works for vesting accounts)
inferenced tx genesistransfer transfer-ownership \
  cosmos1vesting123abc \
  cosmos1recipient789ghi \
  --from vesting-authority \
  --chain-id inference-1 \
  --gas auto \
  --gas-adjustment 1.5

# 4. Verify vesting schedule transferred
inferenced query auth account cosmos1recipient789ghi
```

### Example 3: Batch Status Check

```bash
#!/bin/bash
# batch-status.sh - Check multiple accounts

ACCOUNTS=(
  "cosmos1genesis1..."
  "cosmos1genesis2..."
  "cosmos1genesis3..."
)

for account in "${ACCOUNTS[@]}"; do
  echo "=== $account ==="
  inferenced query genesistransfer transfer-status $account --output json | \
    jq '{transferred: .is_transferred, recipient: .transfer_record.recipient_address}'
  echo
done
```

### Example 4: Complete Audit Trail

```bash
# Generate comprehensive audit report
{
  echo "Genesis Transfer Audit Report"
  echo "Generated: $(date)"
  echo "Chain ID: $(inferenced status | jq -r '.NodeInfo.network')"
  echo
  
  echo "=== Module Parameters ==="
  inferenced query genesistransfer params
  echo
  
  echo "=== Transfer History ==="
  inferenced query genesistransfer transfer-history --output json | \
    jq -r '.transfer_records[] | "[\(.transfer_height)] \(.genesis_address) → \(.recipient_address) (\(.transfer_amount) \(.transferred_denoms | join(",")))"'
  echo
  
  echo "=== Allowed Accounts ==="
  inferenced query genesistransfer allowed-accounts
  
} > genesis-transfer-audit-$(date +%Y%m%d).txt
```

## Integration with Other Tools

### Using with jq for JSON Processing

```bash
# Extract specific fields
inferenced query genesistransfer transfer-history --output json | \
  jq '.transfer_records[] | {genesis: .genesis_address, recipient: .recipient_address, amount: .transfer_amount}'

# Filter completed transfers
inferenced query genesistransfer transfer-history --output json | \
  jq '.transfer_records[] | select(.completed == true)'

# Count transfers by status
inferenced query genesistransfer transfer-history --output json | \
  jq '.transfer_records | group_by(.completed) | map({status: .[0].completed, count: length})'
```

### Scripting with Error Handling

```bash
#!/bin/bash
# robust-transfer.sh

set -e  # Exit on error

GENESIS_ADDR="$1"
RECIPIENT_ADDR="$2"
AUTHORITY_KEY="$3"

if [[ -z "$GENESIS_ADDR" || -z "$RECIPIENT_ADDR" || -z "$AUTHORITY_KEY" ]]; then
  echo "Usage: $0 <genesis-address> <recipient-address> <authority-key>"
  exit 1
fi

# Validation
echo "Validating transfer eligibility..."
if ! inferenced query genesistransfer transfer-eligibility "$GENESIS_ADDR" --output json | jq -e '.is_eligible'; then
  echo "Error: Account not eligible for transfer"
  exit 1
fi

# Execute transfer
echo "Executing transfer..."
TX_HASH=$(inferenced tx genesistransfer transfer-ownership \
  "$GENESIS_ADDR" \
  "$RECIPIENT_ADDR" \
  --from "$AUTHORITY_KEY" \
  --chain-id inference-1 \
  --gas auto \
  --gas-adjustment 1.5 \
  --broadcast-mode block \
  --output json | jq -r '.txhash')

echo "Transfer submitted: $TX_HASH"

# Verify
echo "Verifying transfer completion..."
sleep 5
if inferenced query genesistransfer transfer-status "$GENESIS_ADDR" --output json | jq -e '.is_transferred'; then
  echo "✅ Transfer completed successfully"
else
  echo "❌ Transfer verification failed"
  exit 1
fi
```

---

This CLI guide provides comprehensive coverage of all genesis transfer commands and workflows. For additional support or advanced use cases, refer to the module documentation and deployment guide.
