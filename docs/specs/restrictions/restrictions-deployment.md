# Transfer Restrictions Deployment Guide

## Overview

The Transfer Restrictions module provides temporary restrictions on user-to-user transfers during bootstrap periods while preserving essential network operations (gas payments, staking, governance, inference fees).

## Configuration

### Genesis Configuration

**For Production Networks**: Set restriction end block in genesis.json:

```json
{
  "app_state": {
    "restrictions": {
      "params": {
        "restriction_end_block": "1555000"
      }
    }
  }
}
```

**For Testing/Testnet**: Default `restriction_end_block: 0` (no restrictions)

### Timeline

- **Recommended Duration**: 90 days (~1,555,000 blocks at 5s block time)
- **Automatic Lifting**: Restrictions automatically end at configured block height
- **No Manual Intervention**: System self-manages restriction lifecycle

## What's Restricted vs Allowed

### ✅ **Always Allowed**
- Gas fee payments
- Staking operations (delegate, undelegate, redelegate)
- Governance operations (deposits, voting)
- Inference service payments
- Module reward distributions
- All module-to-account transfers

### ❌ **Restricted During Bootstrap**
- Direct user-to-user transfers
- Peer-to-peer trading
- Speculative transfers

## Emergency Exemptions

If emergency transfers are needed during restriction period:

1. **Create Governance Proposal** to add exemption template
2. **Users Execute** transfers matching approved templates

```bash
# Check available exemptions
inferenced query restrictions transfer-exemptions

# Execute emergency transfer (if exemption exists)
inferenced tx restrictions execute-emergency-transfer [exemption-id] [from] [to] [amount] [denom] --from [key]
```

## Monitoring

```bash
# Check current status
inferenced query restrictions transfer-restriction-status

# View exemption usage
inferenced query restrictions exemption-usage [exemption-id] [account]
```

## Timeline Management

- **Current Status**: Check remaining blocks until restrictions lift
- **Governance Override**: Can modify end block via parameter change proposal
- **Early Termination**: Set end block to current height to disable immediately

The module automatically handles all restriction lifecycle management with zero manual intervention required.