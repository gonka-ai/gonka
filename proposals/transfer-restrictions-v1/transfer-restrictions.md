Native Coin Transfer Restrictions

This document proposes implementing transfer restrictions on native gonka coins during the network's initial bootstrapping phase using Cosmos SDK's SendRestriction mechanism. The restrictions will apply for the first 1,555,000 blocks while preserving essential network operations including gas payments and inference fees.

## 1. Summary of Changes

This proposal introduces temporary native coin transfer restrictions during the network's initial launch period to ensure stable economic bootstrapping while maintaining critical network functionality. **Important**: This restriction affects only user-initiated transfers of native gonka coins - all module-level operations, gas payments, and inference service payments remain fully functional.

**Core Economic Rationale**: Early-stage decentralized networks benefit from temporary transfer limitations to prevent speculative trading while core infrastructure and economic systems stabilize. This restriction creates a focus on utility (inference services) rather than speculation during the critical bootstrapping period. Additionally, the 3-month restriction period provides major cryptocurrency exchanges with sufficient time for technical integration and security audits, enabling synchronized trading launch that maximizes network value for all participants.

**Key Changes:**
- **SendRestriction Implementation**: Bank module restrictions on native coin transfers between user accounts
- **Essential Service Exemptions**: Gas payments and inference fees continue operating normally
- **Time-Based Activation**: Automatic lifting of restrictions after block height 1,555,000
- **Module Account Exemptions**: All module-to-account and account-to-module transfers remain unrestricted
- **Governance Override**: Emergency governance proposals can modify or disable restrictions

The restriction will be implemented through a custom SendRestriction function integrated into the bank module configuration, ensuring minimal impact on existing systems while providing the desired economic controls.

## 2. Current vs. Restricted Transfer System

### 2.1. Current Unrestricted System

The existing system allows unrestricted transfers of native gonka coins:
```
Any account can send gonka coins to any other account for any purpose
Gas payments: User accounts → Fee collector (for transaction fees)
Inference payments: User accounts → Inference module → MLNode operators (for AI services)
Rewards: Module accounts → User accounts (for mining/inference rewards)
```

**Current Transfer Freedom:**
- No restrictions on peer-to-peer transfers
- All transaction types permitted
- Immediate liquidity for all holders
- Potential for speculative trading during bootstrap phase

### 2.2. New Restricted Transfer System

The new system maintains operational functionality while restricting speculative transfers:
```
RESTRICTED: Direct user-to-user transfers of native gonka coins
PERMITTED: Gas payments to fee collector module
PERMITTED: User-to-module transfers (inference escrow, governance deposits, collateral)
PERMITTED: Module accounts sending rewards to users (direct or through vesting)
PERMITTED: All module-to-module transfers
PERMITTED: Emergency transfers matching governance exemption templates
```

**Benefits:**
- **Bootstrap Stability**: Prevents speculative trading during critical network development
- **Utility Focus**: Encourages use of gonka coins for intended purposes (inference services)
- **Network Security**: Reduces risk of economic attacks during vulnerable early period
- **Exchange Integration**: Provides 3-month window for major exchanges to complete technical integration and security audits
- **Synchronized Market Launch**: Enables simultaneous trading launch across multiple exchanges, maximizing network value
- **Operational Continuity**: All essential network functions remain unaffected
- **Automatic Resolution**: Restrictions automatically lift after predetermined block height

## 3. SendRestriction Implementation Mechanism

### 3.1. Cosmos SDK SendRestriction Overview

The Cosmos SDK bank module provides a `SendRestriction` interface that allows custom validation logic for coin transfers:

**SendRestriction Interface**: Standard Cosmos SDK interface for validating coin transfers before execution.

**Integration Point**: The SendRestriction function is called by the bank module before executing any `SendCoins` operation, providing a hook to validate, modify, or reject transfers.

### 3.2. Transfer Restriction Logic

**Restriction Categories:**

1. **PERMITTED - Gas Fee Payments**:
   - From: Any user account
   - To: Fee collector module account
   - Purpose: Transaction gas payments
   - Implementation: `IsGasFeePayment()` function in `inference-chain/x/restrictions/keeper/keeper.go`

2. **PERMITTED - User-to-Module Transfers**:
   - From: Any user account  
   - To: Any module account (inference, governance, etc.)
   - Purpose: All legitimate protocol interactions (inference escrow, governance deposits, collateral, etc.)
   - Implementation: `IsModuleAccount()` check for recipient in `inference-chain/x/restrictions/keeper/keeper.go`

3. **PERMITTED - Module Operations**:
   - From: Any module account (inference, streamvesting, etc.)
   - To: Any account (user or module)
   - Purpose: Direct rewards, vested rewards, refunds, administrative transfers
   - Implementation: `IsModuleAccount()` function in `inference-chain/x/restrictions/keeper/keeper.go`

4. **PERMITTED - Emergency Exemption Transfers**:
   - Transfers that match governance-approved exemption templates
   - Emergency mechanism with pre-defined criteria and usage limits
   - Implementation: `MatchesEmergencyExemption()` function in `inference-chain/x/restrictions/keeper/keeper.go`

5. **RESTRICTED - Direct User Transfers**:
   - From: User account
   - To: User account (non-module)
   - Purpose: Peer-to-peer transfers, trading
   - Action: Reject with clear error message using error types from `inference-chain/x/restrictions/types/errors.go`

### 3.3. Implementation Details

**Main SendRestriction Function**: `SendRestrictionFn(ctx context.Context, from, to sdk.AccAddress, amt sdk.Coins)` in `inference-chain/x/restrictions/keeper/send_restriction.go`
- Uses modern `context.Context` parameter signature (Cosmos SDK dependency injection standard)
- Checks if restrictions are active using block height comparison against governance parameter
- Validates transfer categories using helper functions (gas fees, module accounts, emergency exemptions)
- Returns appropriate errors for restricted user-to-user transfers
- Auto-unregisters itself when restrictions expire via EndBlocker

**Module Independence**: Transfer restrictions implemented as independent module (`x/restrictions`) that can be reused by any Cosmos SDK chain.

**Modern Integration**: Uses dependency injection through module outputs with `group:"bank-send-restrictions"` tag - bank module automatically collects and applies all registered send restrictions without manual configuration.

### 3.4. WASM Contract Bypass Prevention

**Analysis**: Upon review, WASM contracts are **not module accounts** - they are regular smart contract accounts with their own addresses, similar to user accounts.

**Current Restriction Behavior with WASM Contracts**:
1. User A → WASM contract: **RESTRICTED** (user-to-user transfer)
2. WASM contract → User B: **RESTRICTED** (user-to-user transfer)  
3. User A → User B via WASM contract: **Already prevented** by existing restrictions

**Conclusion**: The current SendRestriction design already prevents WASM contract bypass attempts because:
- WASM contracts are treated as user accounts, not module accounts
- Transfers to/from WASM contracts are subject to the same restrictions as user-to-user transfers
- No additional mitigation is required

### 3.5. Authz Delegation Analysis

**Potential Concern**: Users might attempt to bypass transfer restrictions by delegating bank send permissions to other accounts through the authz module.

**Why This Isn't a Concern**:

**1. Existing SendRestriction Applies to Authz Execution**:
- When recipients execute authz delegations via `tx authz exec`, the underlying bank transfer still goes through SendRestriction
- SendRestriction sees the original account (granter) as the sender and applies the same restrictions
- User-to-user transfers remain blocked regardless of whether they're executed directly or via authz

**2. Delegation Creation vs. Fund Movement**:
- Creating authz delegations (`MsgGrant`) doesn't move any funds - it only creates permissions
- Actual fund transfers happen during execution (`MsgExec`) and are subject to SendRestriction
- The restriction applies at the point of actual fund movement, not permission creation

**3. Revocable Nature of Delegations**:
- All standard authz delegations are revocable by the granter at any time
- This means delegations don't constitute permanent loss of control or genuine transfers
- The bootstrap period restriction goal (preventing speculative transfers) isn't compromised by revocable permissions

**Conclusion**: The existing SendRestriction mechanism already prevents authz-based bypass attempts because restrictions apply to the actual fund transfers during execution, not to the creation of revocable permissions. No additional restrictions on authz delegation are necessary.

## 4. Block Height-Based Activation

### 4.1. Restriction Timeline

**Activation Block**: Genesis block (height 0)
**End Block**: 1,555,000 (production - set via genesis configuration)
**Default Parameter**: 0 (no restrictions - for testing/testnet environments)
**Estimated Duration**: Approximately 90 days (assuming 5-second block times)

**Block Height Calculation:**
```
Restriction Duration: 1,555,000 blocks
Block Time: ~5 seconds average
Total Duration: 1,555,000 × 5 seconds = 7,775,000 seconds ≈ 90 days
```

### 4.2. Parameter Configuration Strategy

**Default Behavior**: The module defaults to `restriction_end_block: 0` to support different deployment scenarios:

- **Testing/Development**: Default `0` allows unrestricted transfers for local development and testing
- **Testnet Deployment**: Default `0` enables full functionality testing without restrictions  
- **Production Networks**: Must explicitly set `1,555,000` (or desired block height) in genesis configuration

**Genesis Configuration**: Production networks should include restriction parameters in genesis:
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

### 4.3. Automatic Restriction Lifting

**Implementation**: `IsRestrictionActive()` function in `inference-chain/x/restrictions/keeper/keeper.go`
- Compares current block height against `RestrictionEndBlock` governance parameter
- Returns `false` when current height >= end block OR when end block is 0, effectively disabling all restrictions
- Returns boolean indicating if restrictions should be enforced
- **SendRestriction Auto-Unregistration**: When restrictions expire, the module automatically unregisters the SendRestriction function to eliminate unnecessary performance overhead

**Benefits of Block Height-Based System:**
- **Predictable Timeline**: Network participants know exact end time
- **Automatic Execution**: No manual intervention required for lifting restrictions
- **Transparent Progress**: Current restriction status visible on-chain
- **Governance Independence**: Functions regardless of governance activity

## 5. Essential Service Exemptions

### 5.1. Gas Fee Payment Exemption

**Purpose**: Ensure all transaction types can pay gas fees normally
**Implementation**: Allow transfers to fee collector module account
**Module Account**: `authtypes.FeeCollectorName` ("fee_collector")

**Implementation**: `IsGasFeePayment()` function in `inference-chain/x/restrictions/keeper/keeper.go`
- Compares recipient address against fee collector module address
- Uses `authtypes.FeeCollectorName` constant for validation

### 5.2. Inference Fee Payment Exemption

**Purpose**: Maintain AI inference service functionality during restrictions
**Implementation**: Allow transfers to inference module account
**Module Account**: `inferencetypes.ModuleName` ("inference")

**Simplified Implementation**: SendRestriction cannot distinguish between different message types triggering transfers, so the approach is:
- **PERMIT**: Any transfer TO a module account (covers inference payments, governance deposits, collateral, etc.)
- **RESTRICT**: Only user-to-user transfers (non-module recipients)

**Inference Payment Flow (Unchanged):**
1. User submits inference request through API
2. API node (participant/validator) sends MsgStartInference transaction
3. Chain automatically transfers payment from user account to inference module escrow (PERMITTED - user-to-module transfer)
4. Inference module holds coins in escrow during processing
5. API node sends MsgFinishInference upon completion
6. Inference module pays MLNode operators from escrow:
   - Direct payment: inference module → participant account (PERMITTED - module account sending)
   - Vested payment: inference module → streamvesting module → participant account (PERMITTED - all module operations)
7. Inference module refunds unused escrow to user (PERMITTED - module account sending)

### 5.3. Module Account Exemptions

**Purpose**: Preserve all existing module functionality
**Implementation**: Allow all transfers involving module accounts
**Affected Operations**:
- Direct reward distributions (mining rewards, inference rewards)
- Vested reward releases from streamvesting module
- Vesting schedule transfers between modules
- Collateral deposits and withdrawals
- Bridge operations
- Governance operations

**Implementation**: `IsModuleAccount()` function in `inference-chain/x/restrictions/keeper/keeper.go`
- Checks if address corresponds to a module account using account keeper
- Validates account type against `authtypes.ModuleAccount`

## 6. Governance Emergency Transfer Exemptions

### 6.1. Exemption Template Mechanism

**Purpose**: Allow governance to pre-approve emergency transfer categories that users can execute without spamming individual requests
**Security Principle**: Governance creates exemption templates, but only account owners can execute transfers from their accounts

**Two-Step Process**:
1. **Governance creates exemption**: Governance defines transfer exemption template with specific criteria
2. **User execution**: Account owners execute transfers that match approved exemption templates

### 6.2. Emergency Exemption Structure

**Exemption Template Fields**:
- `exemption_id`: Unique identifier for the exemption
- `from_address`: Specific account (or wildcard for any account)
- `to_address`: Specific recipient OR wildcard pattern (e.g., any address, specific address patterns)
- `max_amount`: Maximum amount per transfer
- `usage_limit`: Maximum number of uses per account (prevents abuse)
- `expiry_block`: Block height when exemption expires
- `justification`: Description of emergency use case

**Exemption Types**:
1. **Specific From → Specific To**: `from: cosmos1abc...`, `to: cosmos1def...` (targeted exemption)
2. **Specific From → Any To**: `from: cosmos1abc...`, `to: "*"` (account recovery scenarios)
3. **Any From → Specific To**: `from: "*"`, `to: cosmos1def...` (critical infrastructure payments)

### 6.3. Exemption Execution Process

**Step 1 - Governance Creates Exemption**:
- **Governance Proposal**: Standard parameter change proposal adding new exemption template
- **Storage**: Exemption stored in `EmergencyTransferExemptions` parameter
- **Activation**: Becomes effective when proposal passes

**Step 2 - User Executes Transfer**:
- **Message Type**: `MsgExecuteEmergencyTransfer` in `inference-chain/x/restrictions/types/tx.proto`
- **Required Fields**: exemption_id, to_address, amount
- **Validation**: Must be signed by from_address specified in exemption (or any address if wildcard)
- **Usage Tracking**: Track usage count per account to enforce limits

### 6.4. Security Safeguards

**Account Ownership**: Only account owners can execute transfers from their accounts (enforced by transaction signing)
**Template Matching**: Transfers must exactly match exemption template criteria
**Usage Limits**: Each account limited to specified number of uses per exemption
**Amount Limits**: Each transfer cannot exceed max_amount specified in exemption
**Time Limits**: Exemptions automatically expire at specified block height
**Governance Control**: All exemption templates require full governance proposal process

**Example Exemption Templates**:
```
Emergency Infrastructure Payment:
- from: "*" (any account)
- to: "cosmos1infrastructure..." 
- max_amount: 100000ugonka
- usage_limit: 1 per account
- justification: "Critical infrastructure maintenance payment"

Account Recovery:
- from: "cosmos1user123..."
- to: "*" (any address)
- max_amount: 50000ugonka  
- usage_limit: 3 total
- justification: "User wallet recovery assistance"
```

## 7. Implementation Details

### 7.1. Independent Module Architecture

**New Module Structure**: `inference-chain/x/restrictions/`
- **Purpose**: General-purpose economic transfer restriction mechanism
- **Independence**: No dependencies on inference module functionality
- **Reusability**: Can be used by any Cosmos SDK chain requiring transfer restrictions

**Module Files**:
- `inference-chain/x/restrictions/keeper/keeper.go` - Core restriction logic
- `inference-chain/x/restrictions/types/params.go` - Module parameters
- `inference-chain/x/restrictions/types/errors.go` - Error definitions
- `inference-chain/x/restrictions/types/msgs.go` - Emergency transfer messages

### 7.2. Integration Points

**Modern Dependency Injection Integration**: The restrictions module uses Cosmos SDK's modern dependency injection pattern for SendRestriction registration:

**Module Output Configuration** in `inference-chain/x/restrictions/module/module.go`:
- `SendRestrictionFn` provided through module outputs with `group:"bank-send-restrictions"` tag
- Bank module automatically collects all registered send restrictions via dependency injection
- No manual configuration needed in `app.go` - fully automated through app wiring

**SendRestriction Function Signature**: `func(ctx context.Context, from, to sdk.AccAddress, amt sdk.Coins) (sdk.AccAddress, error)`
- Uses `context.Context` parameter (modern Cosmos SDK standard)
- Internally converts to `sdk.Context` using `sdk.UnwrapSDKContext(ctx)` for module operations
- **Dynamic Unregistration**: Module automatically unregisters SendRestriction when deadline passes

**Core Restriction Functions**:
- `SendRestrictionFn(ctx context.Context, from, to, amt)` - Main restriction function registered with bank module
- `IsRestrictionActive(ctx)` - Check if restrictions are currently enabled using governance parameter
- `IsModuleAccount(addr)` - Validate if address is a module account
- `MatchesEmergencyExemption(ctx, from, to, amount)` - Check emergency exemption templates
- `GetRestrictionStatus(ctx)` - Query current restriction status and remaining blocks
- `CheckAndUnregisterRestriction(ctx)` - Auto-remove SendRestriction when deadline passes (called in EndBlocker)

### 7.3. Governance Parameters

**Configurable Parameters in `inference-chain/x/restrictions/types/params.go`:**
- `RestrictionEndBlock`: Block height when restrictions end (governance-modifiable, default: 1,555,000)
- `EmergencyTransferExemptions`: Array of governance-approved exemption templates
- `ExemptionUsageTracking`: Map tracking usage counts per account per exemption

**Parameter Updates**: Governance can modify `RestrictionEndBlock` to extend or shorten restriction period through standard parameter change proposals

### 7.4. Error Handling and User Experience

**Error Definitions**: Add new error types to `inference-chain/x/restrictions/types/errors.go`
- `ErrTransferRestricted`: Main restriction error with block height information
- `ErrInvalidExemption`: Emergency exemption validation errors
- `ErrExemptionExpired`: Exemption past expiry block
- `ErrExemptionUsageLimitExceeded`: Account exceeded usage limit for exemption

**User-Friendly Error Response:**
- Clear explanation of restriction reason
- Current block height and restriction end block
- List of permitted transaction types
- Estimated time remaining until restriction lifts

### 7.5. SendRestriction Auto-Unregistration

**Performance Optimization**: Once the restriction deadline passes, the module automatically unregisters the SendRestriction function to eliminate unnecessary overhead on every transfer.

**Implementation**: 
- `CheckAndUnregisterRestriction()` function called in EndBlocker
- Compares current block height against `RestrictionEndBlock` parameter
- Calls `UnregisterSendRestriction()` when deadline reached
- Logs restriction lifting event for transparency

**Benefits**:
- **Zero Performance Impact**: No restriction checks after deadline
- **Automatic Process**: No governance action required
- **Clean Architecture**: Module cleanly removes itself when no longer needed

### 7.6. Testing Strategy

**Unit Tests in `inference-chain/x/restrictions/keeper/keeper_test.go`:**
- Restriction enforcement for user-to-user transfers
- Gas fee payment exemption validation
- Inference fee payment exemption validation
- Module account transfer exemption validation
- Block height-based restriction lifting
- Governance exemption mechanism

**Integration Tests:**
- End-to-end transaction testing with restrictions active
- Inference service functionality during restriction period
- Automatic restriction lifting at target block height
- Governance emergency exemption workflow

## 8. Economic Impact and Rationale

### 8.1. Bootstrap Stability Benefits

**Network Development Focus**: Restrictions encourage building core infrastructure rather than speculative trading during critical early development phases.

**Economic Stabilization**: Prevents large token movements that could destabilize the network's economic systems before they mature.

**Utility-Driven Adoption**: Users focus on the network's core value proposition (AI inference services) rather than token speculation.

### 8.2. Risk Mitigation

**Speculative Attack Prevention**: Reduces risk of large-scale buying/selling that could manipulate early token prices and network incentives.

**Infrastructure Protection**: Ensures network resources are allocated to building robust systems rather than managing speculative volatility.

**Community Building**: Encourages long-term participants and builders rather than short-term speculators.

### 8.3. Exchange Integration and Listing Benefits

**Exchange Integration Timeline**: Top-tier cryptocurrency exchanges prefer projects where trading begins simultaneously with listing announcements. The 3-month restriction period provides exchanges with sufficient time to:
- Complete technical integration with the gonka network
- Perform comprehensive security audits of the blockchain
- Establish proper custody and operational procedures
- Prepare marketing and listing announcements

**Synchronized Market Launch**: When transfer restrictions lift at block 1,555,000, exchanges can immediately enable trading, creating:
- **Professional Market Entry**: Simultaneous listing across multiple major exchanges
- **Enhanced Legitimacy**: Association with reputable exchanges increases project credibility
- **Increased Subsidy Value**: Higher token liquidity and price discovery benefits all network participants receiving gonka rewards
- **Broader Adoption**: Exchange listings dramatically expand user access and network growth potential

**Strategic Advantage**: This approach transforms the restriction period from a limitation into a strategic advantage, using the time to build institutional relationships that benefit the entire network ecosystem.

### 8.4. Post-Restriction Economic Effects

**Gradual Market Development**: After block 1,555,000, natural price discovery and trading can develop organically with mature infrastructure supporting it.

**Established Utility Value**: By restriction end, gonka coins will have demonstrated clear utility value through inference services, creating fundamental value basis.

**Network Maturity**: Economic systems, governance, and technical infrastructure will be battle-tested and stable before unrestricted trading begins.

## 9. Monitoring and Observability

### 9.1. Restriction Status Queries

**Chain Queries:**
- Current restriction status (active/inactive)
- Remaining blocks until restriction lifts
- List of current governance exemptions
- Transfer attempt statistics and rejections

**New Query Endpoints**: Add to `inference-chain/proto/inference/restrictions/query.proto`
- `TransferRestrictionStatus`: Query current restriction status and remaining blocks
- `TransferExemptions`: Query active emergency exemption templates
- `ExemptionUsage`: Query usage statistics for emergency exemptions

**Query Implementation**: Add corresponding query handlers in `inference-chain/x/restrictions/keeper/query.go`

### 9.2. Metrics and Analytics

**Restriction Impact Metrics:**
- Number of transfer attempts blocked per day
- Gas fee payments processed during restriction period
- Inference fee payments processed during restriction period
- Module-to-user transfers (rewards) during restriction period

**User Experience Monitoring:**
- Failed transaction analysis and patterns
- User education effectiveness
- Support request volume related to restrictions

## 10. Migration and Deployment Strategy

### 10.1. Deployment Plan

**Phase 1: Code Implementation**
- Implement new `x/transfer-restrictions` module
- Implement SendRestriction function and integration
- Add module parameters and governance integration
- Create comprehensive test suite

**Phase 2: Network Upgrade**
- Deploy via governance upgrade proposal
- Configure restriction parameters (end block: 1,555,000)
- Activate restrictions starting from upgrade block

**Phase 3: Monitoring and Support**
- Monitor restriction effectiveness and user experience
- Provide user education and support documentation
- Prepare for automatic restriction lifting

### 10.2. Rollback and Emergency Procedures

**Emergency Governance Options:**
- Modify `RestrictionEndBlock` parameter (extend or shorten restriction period)
- Add specific emergency transfer exemption templates for critical operations
- Completely disable restrictions by setting `RestrictionEndBlock` to current block height

**Emergency Override Process:**
1. Governance proposal to modify `RestrictionEndBlock` parameter (in transfer-restrictions module)
2. Fast-track voting for critical situations
3. Automatic activation upon proposal passage
4. SendRestriction automatically unregisters if deadline set to current/past block

## 11. Documentation and User Communication

### 11.1. User Education Materials

**Clear Communication Strategy:**
- Explanation of restriction purpose and benefits
- List of permitted vs. restricted transaction types
- Timeline and automatic lifting mechanism
- How to perform essential operations (gas payments, inference fees)

**Documentation Updates:**
- Update transaction documentation with restriction information
- Add FAQ section addressing common user questions
- Provide examples of permitted transaction types

### 11.2. Developer Integration

**SDK Integration Notes:**
- Transfer-restrictions module integration patterns
- SendRestriction function behavior and error handling
- Test environment configuration for restriction testing
- Query endpoints for checking restriction status
- Emergency exemption request procedures

This comprehensive transfer restriction system ensures network stability during the critical bootstrap period while maintaining all essential functionality. The automatic lifting mechanism provides certainty for users while the governance override ensures flexibility for unforeseen circumstances.
