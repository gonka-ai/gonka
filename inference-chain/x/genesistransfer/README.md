# Genesis Transfer Module (x/genesistransfer)

## Overview

The Genesis Transfer module provides secure, atomic, and irreversible transfer of genesis account ownership including all liquid balances and vesting schedules. This module enables the safe handover of pre-allocated tokens to their intended recipients while maintaining comprehensive validation, audit trails, and optional account restrictions.

## Architecture

### Core Components

- **Atomic Transfer System**: Complete ownership transfer including liquid balances and vesting schedules
- **One-Time Enforcement**: Prevents duplicate transfers with transfer record tracking
- **Vesting Integration**: Preserves vesting timelines during ownership transfer
- **Transfer Restrictions Compliance**: Uses module account intermediary to bypass bootstrap restrictions
- **Parameter System**: Optional account whitelist via governance parameters
- **Query Interface**: Comprehensive APIs for transfer status, history, and eligibility
- **Audit Trail**: Complete transfer record management for compliance and monitoring

### Module Integration

The module integrates with the Cosmos SDK using modern dependency injection patterns:

```go
// Module automatically registers with app wiring
func ProvideModule(in ModuleInputs) ModuleOutputs {
    // Automatic dependency injection for AccountKeeper, BankKeeper, etc.
}
```

## Transfer Workflow and Security Model

### Complete Ownership Transfer Process

1. **Pre-Transfer Validation**:
   - Address format validation and ownership verification
   - One-time enforcement check (prevents duplicate transfers)
   - Account existence and balance verification
   - Optional whitelist validation (if enabled)

2. **Atomic Transfer Execution**:
   - **Step 1**: Genesis account → GenesisTransfer module account (user-to-module: bypasses restrictions)
   - **Step 2**: GenesisTransfer module account → Recipient (module-to-user: bypasses restrictions)
   - **Step 3**: Vesting schedule transfer with timeline preservation (if applicable)

3. **Post-Transfer Operations**:
   - Transfer record creation and storage
   - Event emission for audit trail
   - Transfer completion validation

### Security Model

- **Authorization**: Only account owners can initiate transfers (enforced by transaction signing)
- **One-Time Enforcement**: Transfer records prevent duplicate transfers
- **Atomic Operations**: All-or-nothing execution with automatic rollback on failure
- **Whitelist Support**: Optional governance-controlled account restrictions
- **Transfer Restrictions Compliance**: Bypasses bootstrap restrictions using secure module account intermediary

## Vesting Integration and Timeline Preservation

### Supported Vesting Account Types

1. **PeriodicVestingAccount**: Multiple vesting periods with specific amounts and durations
2. **ContinuousVestingAccount**: Linear vesting over time
3. **DelayedVestingAccount**: Cliff vesting with single release date
4. **BaseVestingAccount**: Basic vesting functionality

### Timeline Preservation Logic

- **Proportional Calculations**: Remaining vesting amounts calculated based on elapsed time
- **Period Adjustment**: Partial periods properly handled for PeriodicVesting
- **Timeline Integrity**: Original vesting schedules maintained for recipients
- **Expired Handling**: Graceful handling of expired vesting periods

### Vesting Transfer Process

```go
// Example: PeriodicVesting transfer
1. Analyze remaining vesting periods from current time
2. Calculate proportional amounts for partial periods
3. Create new PeriodicVestingAccount for recipient
4. Transfer remaining vesting schedule with preserved timeline
5. Register new vesting account with account keeper
```

## Query Endpoint Documentation

### Transfer Status Queries

#### `TransferStatus`
- **Purpose**: Check completion status for specific genesis accounts
- **Usage**: `inferenced query genesistransfer transfer-status [genesis-address]`
- **Response**: Transfer record with completion status, recipient, and block height

#### `TransferHistory`
- **Purpose**: Retrieve historical transfer records with pagination
- **Usage**: `inferenced query genesistransfer transfer-history`
- **Response**: List of all transfer records with pagination support

#### `TransferEligibility`
- **Purpose**: Validate whether specific accounts can be transferred
- **Usage**: `inferenced query genesistransfer transfer-eligibility [genesis-address]`
- **Response**: Eligibility status with detailed reason if ineligible

### Parameter Queries

#### `Params`
- **Purpose**: Query current module parameters
- **Usage**: `inferenced query genesistransfer params`
- **Response**: Current whitelist settings and restrictions

#### `AllowedAccounts`
- **Purpose**: Query whitelist of accounts eligible for transfer (if enabled)
- **Usage**: `inferenced query genesistransfer allowed-accounts`
- **Response**: List of whitelisted account addresses

### Transaction Commands

#### `TransferOwnership`
- **Purpose**: Execute complete ownership transfer
- **Usage**: `inferenced tx genesistransfer transfer-ownership [genesis-address] [recipient-address]`
- **Requirements**: Must be signed by genesis account owner

## Integration Patterns for Genesis Account Management

### Basic Integration

```go
// Import the module
import genesistransferkeeper "github.com/productscience/inference/x/genesistransfer/keeper"

// Execute ownership transfer
err := genesisTransferKeeper.ExecuteOwnershipTransfer(ctx, genesisAddr, recipientAddr)
if err != nil {
    // Handle transfer error
}
```

### Query Integration

```go
// Check transfer eligibility
eligible, reason, alreadyTransferred, err := keeper.ValidateTransferEligibility(ctx, genesisAddr)

// Get transfer status
transferRecord, found, err := keeper.GetTransferRecord(ctx, genesisAddr)

// Get transfer history
records, pagination, err := keeper.GetAllTransferRecords(ctx, pageRequest)
```

### Parameter Management

```go
// Get current parameters
params := keeper.GetParams(ctx)

// Check if account is whitelisted (if whitelist enabled)
isTransferable := keeper.IsTransferableAccount(ctx, accountAddr)

// Check whitelist settings
allowedAccounts := keeper.GetAllowedAccounts(ctx)
whitelistEnabled := keeper.GetRestrictToList(ctx)
```

## Module Configuration

### Module Account Permissions

The module requires the following permissions in `app_config.go`:

```go
{Account: genesistransfermoduletypes.ModuleName, Permissions: []string{authtypes.Minter, authtypes.Burner}},
```

### Module Ordering

The module should be positioned after core modules in initialization order:

```go
// Genesis initialization order
genesisModuleOrder = []string{
    // ... core modules ...
    genesistransfermoduletypes.ModuleName,
}
```

## CLI Command Examples

### Query Commands

```bash
# Check if account can be transferred
inferenced query genesistransfer transfer-eligibility gonka1abc...

# Check transfer status
inferenced query genesistransfer transfer-status gonka1abc...

# View transfer history
inferenced query genesistransfer transfer-history

# View module parameters
inferenced query genesistransfer params

# View allowed accounts
inferenced query genesistransfer allowed-accounts
```

### Transaction Commands

```bash
# Execute ownership transfer (requires genesis account owner signature)
inferenced tx genesistransfer transfer-ownership gonka1genesis... gonka1recipient... \
  --from genesis-account-key \
  --keyring-backend test \
  --gas 2000000 \
  --yes
```

## Error Handling

### Common Error Types

- `ErrAccountNotFound`: Genesis account doesn't exist
- `ErrAlreadyTransferred`: Account has already been transferred
- `ErrInsufficientBalance`: Account has no transferable assets
- `ErrNotInAllowedList`: Account not in whitelist (when enabled)
- `ErrInvalidTransfer`: Invalid transfer parameters or state

### Error Response Format

```json
{
  "code": 1,
  "codespace": "genesistransfer",
  "log": "genesis account gonka1abc... has already been transferred to gonka1def... at height 12345: already transferred"
}
```

## Security Considerations

### Access Control
- **Genesis Account Owner Execution**: Transfers require signature from the genesis account owner
- **Account Ownership**: Only account owners control their accounts (via transaction signing)
- **One-Time Enforcement**: Prevents accidental or malicious duplicate transfers

### Transfer Restrictions Integration
- **Bootstrap Compliance**: Automatically bypasses transfer restrictions using module account
- **Secure Intermediary**: Two-step process maintains security while enabling functionality
- **Audit Trail**: All transfers logged for compliance and monitoring

### Validation Layers
- **Address Validation**: Comprehensive bech32 format checking
- **Balance Validation**: Ensures sufficient transferable assets
- **Whitelist Validation**: Optional governance-controlled restrictions
- **State Validation**: Transfer record integrity and consistency checks

## Testing

### Unit Tests
- Transfer logic with various account scenarios
- Vesting account integration with all vesting types
- Validation functions with edge cases and error conditions
- Parameter management and whitelist functionality

### Integration Tests
- Complete ownership transfer workflows
- Vesting timeline preservation verification
- Transfer restrictions integration
- One-time enforcement validation

### End-to-End Tests
- Full network environment testing via testermint
- CLI command verification
- Real balance transfer validation
- Transfer restrictions bypass verification

## Monitoring and Observability

### Events
- `ownership_transfer_completed`: Emitted on successful transfers
- `transfer_record_created`: Emitted when transfer records are stored

### Logs
- Transfer execution progress and completion
- Validation failures with detailed reasons
- Balance transfer amounts and accounts
- Vesting schedule transfer details

### Metrics
- Transfer completion rates
- Account types transferred (vesting vs non-vesting)
- Transfer amounts and denominations
- Whitelist enforcement statistics

## Governance Integration

### Parameter Updates
- `AllowedAccounts`: Account whitelist configured via governance proposals
- `RestrictToList`: Enable/disable whitelist enforcement

### Emergency Procedures
- Parameter updates for urgent whitelist changes
- Transfer record queries for audit and compliance

## Version Compatibility

- **Cosmos SDK**: v0.50+ (uses modern dependency injection)
- **Protocol**: Consensus version 1
- **Backwards Compatibility**: Maintains compatibility with existing account types

## Migration Notes

### From Previous Versions
- No migration required for new installations
- Existing accounts fully supported
- Vesting schedules preserved during transfers

### Upgrade Considerations
- Module state is preserved during chain upgrades
- Transfer records maintained for audit continuity
- Parameter settings retained across versions
