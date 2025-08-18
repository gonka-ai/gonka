# Genesis Account Ownership Transfer - Deployment Guide

## Overview

This guide covers deploying and using the genesis account ownership transfer system. The module enables secure, atomic transfer of genesis accounts including all liquid balances and vesting schedules from placeholder accounts to their intended recipients.

## Prerequisites

- Active blockchain network with the genesistransfer module enabled
- Private keys for genesis accounts that need to be transferred
- CLI access to the blockchain node

## Basic Usage

### 1. Check Transfer Eligibility

Before transferring, verify the account is eligible:

```bash
# Check if genesis account can be transferred
inferenced query genesistransfer transfer-eligibility [genesis-address]

# Check current transfer status
inferenced query genesistransfer transfer-status [genesis-address]
```

### 2. Execute Ownership Transfer

Transfer complete ownership of a genesis account:

```bash
# Transfer ownership (must be signed by genesis account owner)
inferenced tx genesistransfer transfer-ownership [genesis-address] [recipient-address] \
  --from [genesis-account-key-name] \
  --gas 2000000 \
  --yes
```

**Important**: The transaction must be signed by the genesis account owner (the account being transferred from).

### 3. Verify Transfer Completion

After transfer, verify it completed successfully:

```bash
# Check transfer status
inferenced query genesistransfer transfer-status [genesis-address]

# View transfer history
inferenced query genesistransfer transfer-history

# Check recipient received the assets
inferenced query bank balances [recipient-address]
```

## Module Configuration

### Parameters

```bash
# View current module parameters
inferenced query genesistransfer params
```

### Account Whitelist (Optional)

If account restrictions are enabled:

```bash
# Check which accounts are allowed to transfer
inferenced query genesistransfer allowed-accounts
```

## Security Considerations

### Key Management
- **Genesis Account Keys**: Must be securely stored until transfer completion
- **One-Time Transfer**: Each genesis account can only be transferred once
- **Irreversible**: Transfers cannot be undone once completed

### Validation
- **Address Verification**: Double-check recipient addresses before transfer
- **Balance Confirmation**: Verify expected balances before and after transfer
- **Vesting Preservation**: Vesting schedules transfer intact with original timelines
