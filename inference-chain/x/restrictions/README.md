# Transfer Restrictions Module (x/restrictions)

## Overview

The Transfer Restrictions module provides temporary restrictions on user-to-user token transfers during blockchain bootstrap periods while preserving essential network operations. This module is designed to be reusable across any Cosmos SDK chain.

## Architecture

### Core Components

- **SendRestriction Function**: Intercepts all bank transfers and applies restriction logic
- **Parameter System**: Governance-controlled configuration for restriction behavior  
- **Emergency Exemptions**: Governance-approved templates for critical transfers
- **Auto-Unregistration**: Automatic cleanup when restrictions expire
- **Query Interface**: APIs for checking restriction status and exemptions

### Module Integration

The module integrates with the Cosmos SDK using modern dependency injection patterns:

```go
// Module automatically provides SendRestriction to bank module
func (app *App) RegisterModules() {
    // SendRestriction is automatically registered via DI
    // No manual configuration needed in app.go
}
```

## Parameters

### Restriction End Block
- **Key**: `restriction_end_block`
- **Type**: `uint64`
- **Default**: `0` (no restrictions - for testing/testnet)
- **Production**: `1,555,000` (approximately 90 days) - set via genesis configuration
- **Description**: Block height when transfer restrictions automatically end. Default 0 allows unrestricted transfers for testing environments.

### Emergency Transfer Exemptions
- **Key**: `emergency_transfer_exemptions`
- **Type**: `[]EmergencyTransferExemption`
- **Default**: `[]` (empty array)
- **Description**: Array of governance-approved emergency transfer templates

### Exemption Usage Tracking
- **Key**: `exemption_usage_tracking`
- **Type**: `[]ExemptionUsage`
- **Default**: `[]` (empty array)
- **Description**: Per-account usage tracking for emergency exemptions

## Emergency Transfer Exemptions

Emergency exemptions allow specific transfers during restriction periods:

```go
type EmergencyTransferExemption struct {
    ExemptionId   string  // Unique identifier
    FromAddress   string  // Source address or "*" for wildcard
    ToAddress     string  // Destination address or "*" for wildcard
    MaxAmount     string  // Maximum transfer amount per use
    UsageLimit    uint64  // Maximum uses per account
    ExpiryBlock   uint64  // Block height when exemption expires
    Justification string  // Human-readable explanation
}
```

### Wildcard Support

- `FromAddress: "*"` - Allow any source address
- `ToAddress: "*"` - Allow any destination address
- Useful for general exemption categories

## Transfer Restriction Logic

The module applies the following logic to all bank transfers:

### Allowed Transfers (Always Pass)
1. **Gas Fee Payments** - Transfers to fee collector module
2. **User-to-Module** - Transfers to any module account (inference escrow, governance deposits, staking)
3. **Module-to-User** - Transfers from any module account (rewards, refunds)
4. **Emergency Exemptions** - Transfers matching governance-approved templates

### Restricted Transfers (Blocked During Bootstrap)
1. **User-to-User** - Direct transfers between user accounts

### Automatic Lifecycle
- Restrictions automatically lift when `restriction_end_block` is reached
- SendRestriction function is unregistered to eliminate performance overhead
- Event emitted for transparency: `restriction_lifted`

## Query Endpoints

### Transfer Restriction Status
```bash
inferenced query restrictions transfer-restriction-status
```

Response:
```json
{
  "is_active": true,
  "restriction_end_block": "1555000",
  "current_block_height": "125000",
  "remaining_blocks": "1430000"
}
```

### Transfer Exemptions
```bash
inferenced query restrictions transfer-exemptions
```

Response:
```json
{
  "exemptions": [
    {
      "exemption_id": "emergency-infrastructure-001",
      "from_address": "*",
      "to_address": "cosmos1...",
      "max_amount": "1000000",
      "usage_limit": "5",
      "expiry_block": "1500000",
      "justification": "Critical infrastructure maintenance"
    }
  ]
}
```

### Exemption Usage
```bash
inferenced query restrictions exemption-usage [exemption-id] [account-address]
```

## Transaction Messages

### Execute Emergency Transfer
```bash
inferenced tx restrictions execute-emergency-transfer \
  --exemption-id="emergency-infrastructure-001" \
  --from-address="cosmos1..." \
  --to-address="cosmos1..." \
  --amount="100000" \
  --denom="uatom" \
  --from="cosmos1..."
```

### Update Parameters (Governance Only)
```bash
inferenced tx gov submit-proposal update-restrictions-params \
  --restriction-end-block=1600000 \
  --emergency-exemptions='[{...}]' \
  --title="Update Transfer Restrictions" \
  --summary="Extend restrictions and add emergency exemption" \
  --deposit="10000uatom" \
  --from="cosmos1..."
```

## Governance Integration

### Parameter Changes
All module parameters can be modified through governance proposals:

```go
type MsgUpdateParams struct {
    Authority string // Governance module address
    Params    Params // New parameter values
}
```

### Emergency Exemption Workflow
1. **Proposal**: Community submits parameter change proposal
2. **Voting**: Validators and delegators vote on proposal
3. **Execution**: If passed, exemptions are added to module parameters
4. **Usage**: Users can execute transfers using exemption templates

## Integration Guide

### For Chain Developers

1. **Add Module to Chain**:
```go
// app_config.go
ModuleConfig{
    Name: "restrictions",
    Config: restrictionsmodule.Module{},
}
```

2. **Configure Genesis**:
```json
{
  "restrictions": {
    "params": {
      "restriction_end_block": "1555000",
      "emergency_transfer_exemptions": [],
      "exemption_usage_tracking": []
    }
  }
}
```

3. **Module Dependencies**:
- Bank Keeper (for transfer interception)
- Governance Module (for parameter changes)
- Account Keeper (for module account detection)

### For Other Modules

The restrictions module automatically intercepts bank transfers. No special integration needed:

```go
// This transfer will be subject to restrictions
err := bankKeeper.SendCoins(ctx, fromAddr, toAddr, coins)
```

## Events

### Emergency Transfer
```json
{
  "type": "emergency_transfer",
  "attributes": [
    {"key": "exemption_id", "value": "emergency-001"},
    {"key": "from_address", "value": "cosmos1..."},
    {"key": "to_address", "value": "cosmos1..."},
    {"key": "amount", "value": "100000uatom"},
    {"key": "remaining_uses", "value": "4"}
  ]
}
```

### Restriction Lifted
```json
{
  "type": "restriction_lifted",
  "attributes": [
    {"key": "current_block", "value": "1555000"},
    {"key": "restriction_end_block", "value": "1555000"}
  ]
}
```

## Error Codes

- `1108`: Transfer restricted during bootstrap period
- `1109`: Emergency exemption not found
- `1110`: Emergency exemption expired
- `1111`: Emergency exemption usage limit exceeded
- `1112`: Transfer amount exceeds exemption limit

## Security Considerations

### Attack Vectors
1. **Module Account Bypass**: Only legitimate module accounts can bypass restrictions
2. **Emergency Exemption Abuse**: Governance controls exemption creation and usage limits
3. **Parameter Manipulation**: Only governance can modify restriction parameters

### Best Practices
1. **Conservative Exemptions**: Limit exemption scope and usage
2. **Regular Review**: Monitor exemption usage through queries
3. **Clear Justification**: Always provide clear reasoning for exemptions
4. **Time-bounded**: Set appropriate expiry blocks for exemptions

## Performance Impact

### During Restrictions
- Minimal overhead: Single block height comparison + address lookups
- Module account detection: Fast map lookup in account keeper
- Emergency exemption matching: Linear scan (expected small list)

### After Restrictions
- Zero overhead: SendRestriction automatically unregistered
- Clean removal: No ongoing performance impact

## Testing

The module includes comprehensive test coverage:

- **Unit Tests**: Core restriction logic and helper functions
- **Integration Tests**: Bank module interaction and emergency transfers  
- **End-to-End Tests**: Complete governance workflow and lifecycle testing

### Test Scenarios
1. User-to-user transfers blocked during restrictions
2. Gas payments and module transfers work normally
3. Emergency exemptions function correctly
4. Governance parameter changes work
5. Automatic restriction lifting at deadline
6. Usage tracking and limits enforced

## Migration Guide

### From Legacy Transfer Restrictions
If migrating from a custom transfer restriction implementation:

1. **Parameter Migration**: Map existing configuration to module parameters
2. **State Migration**: Convert existing exemptions to module format
3. **Event Migration**: Update event listeners for new event types
4. **API Migration**: Update client code to use new query endpoints

### Version Compatibility
- Requires Cosmos SDK v0.47+
- Compatible with standard bank module
- Uses modern dependency injection patterns

## Support

For issues and questions:
- Review the comprehensive test suite for usage examples
- Check the proposal document for design rationale
- Examine the E2E test for complete workflow examples

## License

This module is part of the inference blockchain project and follows the same licensing terms.
