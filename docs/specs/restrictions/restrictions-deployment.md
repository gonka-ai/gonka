# Transfer Restrictions Deployment Guide

## Overview

This guide covers deploying and managing the Transfer Restrictions module (x/restrictions) in production blockchain environments. The module provides temporary restrictions on user-to-user transfers during bootstrap periods while preserving essential network operations.

## Pre-Deployment Planning

### Timeline Considerations

**Recommended Bootstrap Period**: 90 days (1,555,000 blocks)
- Allows sufficient time for ecosystem development
- Provides window for exchange integrations and audits
- Enables community growth and governance establishment

**Key Milestones**:
- **Days 1-15**: Initial network stability and core operations
- **Days 16-45**: Exchange partnerships and integration testing
- **Days 46-75**: Community governance activation and testing
- **Days 76-90**: Pre-launch preparation and final audits
- **Day 90+**: Full transfer functionality enabled

### Stakeholder Communication

**Exchange Partners**:
- Notify of transfer restrictions during bootstrap period
- Provide timeline for full transfer activation
- Share governance parameters and emergency procedures
- Coordinate integration testing during restriction period

**Community**:
- Publish clear documentation about transfer restrictions
- Explain benefits: coordinated launch, exchange partnerships
- Detail governance procedures for emergency exemptions
- Provide regular updates on timeline and milestones

## Genesis Configuration

### Basic Configuration

**Important**: The module defaults to `restriction_end_block: 0` (no restrictions) for testing and testnet environments. For production deployment, you **must** configure the restriction end block in genesis.

Add to `genesis.json`:

```json
{
  "app_state": {
    "restrictions": {
      "params": {
        "restriction_end_block": "1555000",
        "emergency_transfer_exemptions": [],
        "exemption_usage_tracking": []
      }
    }
  }
}
```

**Configuration Notes**:
- **Testing/Testnet**: Default `0` allows unrestricted transfers for development
- **Production**: Set `1555000` (90 days) or desired block height in genesis
- **Block Height**: Must be set at genesis - cannot be 0 in production networks

### Production Parameters

```json
{
  "app_state": {
    "restrictions": {
      "params": {
        "restriction_end_block": "1555000",
        "emergency_transfer_exemptions": [
          {
            "exemption_id": "validator-operations-001",
            "from_address": "*",
            "to_address": "cosmos1validator_rewards_address",
            "max_amount": "1000000",
            "usage_limit": "100",
            "expiry_block": "1500000",
            "justification": "Validator operational expenses and infrastructure"
          },
          {
            "exemption_id": "critical-infrastructure-001", 
            "from_address": "cosmos1foundation_address",
            "to_address": "*",
            "max_amount": "5000000",
            "usage_limit": "50",
            "expiry_block": "1400000",
            "justification": "Critical infrastructure and ecosystem development"
          }
        ],
        "exemption_usage_tracking": []
      }
    }
  }
}
```

### Parameter Tuning Guidelines

**Restriction End Block**:
- **Conservative**: 2,200,000 blocks (~127 days)
- **Standard**: 1,555,000 blocks (~90 days) 
- **Aggressive**: 1,100,000 blocks (~64 days)

**Emergency Exemptions**:
- **Limit scope**: Use specific addresses when possible
- **Set usage limits**: Prevent abuse with reasonable limits
- **Time-bound**: Set expiry blocks before restriction end
- **Clear justification**: Document the business case

## Deployment Checklist

### Pre-Launch (1-2 weeks before)

- [ ] **Genesis Configuration**
  - [ ] Restriction end block calculated and verified
  - [ ] Emergency exemptions defined and reviewed
  - [ ] Genesis file validated and tested

- [ ] **Infrastructure Preparation**
  - [ ] Monitoring dashboards configured
  - [ ] Alert thresholds set for restriction events
  - [ ] Log aggregation configured for restriction logs

- [ ] **Documentation**
  - [ ] User-facing documentation published
  - [ ] Exchange integration guides distributed
  - [ ] Emergency procedures documented

- [ ] **Testing**
  - [ ] Testnet deployment with restrictions tested
  - [ ] Emergency exemption workflows verified
  - [ ] Governance parameter changes tested

### Launch Day

- [ ] **Network Launch**
  - [ ] Genesis file deployed with restrictions active
  - [ ] Initial block production verified
  - [ ] Restriction status confirmed via queries

- [ ] **Monitoring Activation**
  - [ ] Real-time monitoring dashboards active
  - [ ] Alert systems operational
  - [ ] Log collection verified

- [ ] **Community Communication**
  - [ ] Launch announcement with restriction details
  - [ ] Status page updated with restriction information
  - [ ] Support channels prepared for questions

### Post-Launch (First 24-48 hours)

- [ ] **System Verification**
  - [ ] User-to-user transfers confirmed blocked
  - [ ] Gas payments and module operations verified working
  - [ ] Emergency exemptions tested (if applicable)

- [ ] **Performance Monitoring**
  - [ ] Block times and network performance normal
  - [ ] No unexpected restriction-related errors
  - [ ] Memory and CPU usage within expected ranges

## Operational Procedures

### Monitoring and Observability

#### Key Metrics to Track

1. **Restriction Status**
   ```bash
   # Check restriction status
   inferenced query restrictions transfer-restriction-status
   
   # Monitor remaining blocks
   watch -n 60 'inferenced query restrictions transfer-restriction-status | jq .remaining_blocks'
   ```

2. **Blocked Transfer Attempts**
   ```bash
   # Search logs for restricted transfers
   grep "transfer restricted during bootstrap" /var/log/inference/node.log
   
   # Count blocked transfers per hour
   grep "transfer restricted" /var/log/inference/node.log | grep "$(date '+%Y-%m-%d %H')" | wc -l
   ```

3. **Emergency Exemption Usage**
   ```bash
   # Monitor exemption usage
   inferenced query restrictions transfer-exemptions
   
   # Check specific exemption usage
   inferenced query restrictions exemption-usage emergency-001 cosmos1...
   ```

#### Alerting Thresholds

- **High blocked transfer rate**: >1000 blocked transfers per hour
- **Emergency exemption overuse**: >80% of usage limit consumed
- **Approaching deadline**: <30 days remaining
- **Unexpected restriction lifting**: Restriction becomes inactive before deadline

#### Dashboard Metrics

```yaml
# Example Prometheus/Grafana metrics
- restriction_active: Boolean indicator
- restriction_remaining_blocks: Blocks until automatic lifting
- blocked_transfers_total: Counter of blocked transfers
- emergency_transfers_total: Counter of emergency transfers
- exemption_usage_ratio: Usage percentage per exemption
```

### Emergency Procedures

#### Creating Emergency Exemptions

1. **Immediate Response** (for critical situations):
   ```bash
   # Submit governance proposal for emergency exemption
   inferenced tx gov submit-proposal update-restrictions-params \
     --restriction-end-block=1555000 \
     --emergency-exemptions='[{
       "exemption_id": "emergency-'$(date +%s)'",
       "from_address": "cosmos1critical_address",
       "to_address": "cosmos1destination_address", 
       "max_amount": "1000000",
       "usage_limit": "10",
       "expiry_block": "1400000",
       "justification": "Critical system failure requiring immediate intervention"
     }]' \
     --title="Emergency Transfer Exemption" \
     --summary="Critical exemption for system recovery" \
     --deposit="10000uatom" \
     --from=governance_account
   ```

2. **Expedited Voting**:
   ```bash
   # Vote yes on emergency proposal
   inferenced tx gov vote [proposal-id] yes --from=validator_key
   
   # Check proposal status
   inferenced query gov proposal [proposal-id]
   ```

#### Modifying Restriction Timeline

```bash
# Extend restrictions (emergency extension)
inferenced tx gov submit-proposal update-restrictions-params \
  --restriction-end-block=1800000 \
  --title="Extend Transfer Restrictions" \
  --summary="Extend restrictions due to unforeseen circumstances" \
  --deposit="10000uatom" \
  --from=governance_account

# Shorten restrictions (early removal)
inferenced tx gov submit-proposal update-restrictions-params \
  --restriction-end-block=1200000 \
  --title="Early Restriction Removal" \
  --summary="Remove restrictions ahead of schedule" \
  --deposit="10000uatom" \
  --from=governance_account
```

### Routine Maintenance

#### Weekly Health Checks

```bash
#!/bin/bash
# Weekly restriction health check script

echo "=== Transfer Restrictions Health Check ==="
echo "Date: $(date)"
echo

# Check restriction status
echo "1. Restriction Status:"
inferenced query restrictions transfer-restriction-status
echo

# Check exemption usage
echo "2. Emergency Exemption Usage:"
inferenced query restrictions transfer-exemptions | jq '.exemptions[] | {id: .exemption_id, remaining: (.usage_limit - .usage_count)}'
echo

# Check recent blocked transfers
echo "3. Recent Blocked Transfers (last 24h):"
grep "transfer restricted" /var/log/inference/node.log | grep "$(date -d '24 hours ago' '+%Y-%m-%d')" | wc -l
echo

# Check system performance
echo "4. System Performance:"
echo "Block height: $(inferenced status | jq -r .sync_info.latest_block_height)"
echo "Block time: $(inferenced status | jq -r .sync_info.latest_block_time)"
echo

echo "=== Health Check Complete ==="
```

#### Monthly Reviews

1. **Exemption Analysis**:
   - Review usage patterns for all exemptions
   - Identify any abuse or unexpected usage
   - Plan adjustments for upcoming month

2. **Timeline Assessment**:
   - Confirm timeline alignment with ecosystem milestones
   - Assess readiness for full transfer activation
   - Coordinate with exchanges and partners

3. **Performance Impact**:
   - Analyze restriction performance overhead
   - Review error rates and system stability
   - Plan optimizations if needed

## Troubleshooting

### Common Issues

#### 1. Users Unable to Pay Gas Fees

**Symptoms**: Users report inability to submit transactions
**Cause**: Gas payment detection failure
**Solution**:
```bash
# Verify fee collector address
inferenced query auth module-account fee_collector

# Check if transfers to fee collector are working
inferenced tx bank send user_address $(inferenced query auth module-account fee_collector | jq -r .account.base_account.address) 1000uatom --gas=auto
```

#### 2. Module Operations Failing

**Symptoms**: Staking, governance, or inference operations blocked
**Cause**: Module account detection issues
**Solution**:
```bash
# List all module accounts
inferenced query auth module-accounts

# Verify specific module account
inferenced query auth module-account [module-name]

# Test transfer to module
inferenced tx bank send user_address module_address 1000uatom --gas=auto
```

#### 3. Emergency Exemptions Not Working

**Symptoms**: Emergency transfers still blocked despite exemptions
**Cause**: Exemption configuration or matching issues
**Solution**:
```bash
# Check exemption configuration
inferenced query restrictions transfer-exemptions

# Verify exemption details match transfer exactly
# - From/to addresses must match or use wildcards
# - Amount must be within max_amount limit
# - Usage limit must not be exceeded
# - Expiry block must be in the future

# Check exemption usage
inferenced query restrictions exemption-usage [exemption-id] [account-address]
```

#### 4. Performance Degradation

**Symptoms**: Slower block times or high CPU usage
**Cause**: Restriction overhead or configuration issues
**Solution**:
```bash
# Monitor restriction function performance
grep "SendRestriction" /var/log/inference/node.log | tail -100

# Check for excessive exemption list size
inferenced query restrictions transfer-exemptions | jq '.exemptions | length'

# Consider optimizations:
# - Reduce exemption list size
# - Use specific addresses instead of wildcards
# - Remove expired exemptions
```

### Log Analysis

#### Key Log Patterns

```bash
# Successful restriction blocks
grep "transfer restricted during bootstrap period" /var/log/inference/node.log

# Emergency transfer executions
grep "emergency_transfer" /var/log/inference/node.log

# Automatic restriction lifting
grep "restriction_lifted" /var/log/inference/node.log

# Performance issues
grep "SendRestriction.*slow" /var/log/inference/node.log
```

#### Log Aggregation Queries

```sql
-- Example Splunk/ELK queries

-- Blocked transfers by hour
index=inference "transfer restricted" | timechart span=1h count

-- Emergency exemption usage
index=inference "emergency_transfer" | stats count by exemption_id

-- Performance monitoring
index=inference "SendRestriction" | eval duration=tonumber(duration) | stats avg(duration) p95(duration) by _time span=1h
```

## Security Considerations

### Access Controls

- **Governance Keys**: Secure multi-sig setup for parameter changes
- **Emergency Procedures**: Clear escalation paths for critical issues
- **Monitoring Access**: Restrict access to restriction status and logs

### Audit Trails

- **Parameter Changes**: All governance proposals logged and tracked
- **Emergency Usage**: Complete audit trail of exemption usage
- **System Events**: Comprehensive logging of restriction events

### Risk Management

- **Backup Plans**: Procedures for emergency restriction removal
- **Communication Plans**: Stakeholder notification procedures
- **Recovery Procedures**: Steps for handling system failures

## Post-Restriction Transition

### Automatic Lifting Preparation

**30 Days Before**:
- [ ] Verify automatic lifting mechanism working in testnet
- [ ] Prepare community announcement
- [ ] Coordinate with exchange partners
- [ ] Update documentation and support materials

**7 Days Before**:
- [ ] Final system health check
- [ ] Confirm no outstanding emergency exemptions needed
- [ ] Prepare monitoring for post-restriction period
- [ ] Brief support teams on expected behavior changes

**Lifting Day**:
- [ ] Monitor automatic restriction lifting
- [ ] Verify user-to-user transfers working
- [ ] Confirm performance impact removal
- [ ] Communicate successful transition to community

**Post-Lifting**:
- [ ] Monitor network for any issues
- [ ] Collect metrics on transfer volume increase
- [ ] Document lessons learned
- [ ] Plan post-mortem review

## Success Metrics

### Network Health
- Block production remains stable throughout restriction period
- No significant performance degradation from restrictions
- Automatic lifting occurs smoothly at designated block

### Ecosystem Development
- Exchange integrations completed during restriction period
- Community governance participation increased
- Emergency exemption system used appropriately (not abused)

### User Experience
- Clear understanding of restriction timeline and purpose
- Minimal user complaints about essential operations blocked
- Smooth transition to unrestricted transfers

## Support and Resources

### Documentation
- Module README: `inference-chain/x/restrictions/README.md`
- CLI Reference: Run `inferenced query restrictions --help`
- API Documentation: gRPC/REST endpoint specifications

### Monitoring Tools
- Prometheus metrics for restriction status
- Grafana dashboards for visualization
- Log aggregation for troubleshooting

### Emergency Contacts
- Technical Team: For system issues and configuration problems
- Governance Team: For emergency exemption proposals
- Community Team: For user communication and support

This deployment guide provides comprehensive coverage for successfully deploying and managing transfer restrictions in production environments. Regular review and updates of procedures ensure continued effectiveness and smooth operations.
