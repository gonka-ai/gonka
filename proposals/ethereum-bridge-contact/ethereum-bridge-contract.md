# [IMPLEMENTED]: Ethereum Bridge Smart Contract Specification

## Overview

This document describes the Ethereum smart contract that serves as the bridge endpoint for cross-chain token transfers. The contract leverages BLS threshold signatures (natively supported by Ethereum) for secure, decentralized withdrawal validation while maintaining admin failsafe mechanisms for emergency situations.

## Core Functionality

### 1. Token and ETH Reception and Detection

**Token Deposits:**

- Contract can receive transfers of any ERC-20 tokens through direct transfers (without triggering contract execution)
- Contract can receive ETH deposits through the receive() function
- Off-chain bridge monitors contract address for incoming token transfers via event logs
- Supports multiple ERC-20 tokens and ETH simultaneously
- No explicit deposit function required - relies on standard ERC-20 transfer and ETH transfer detection

**Bridge Detection Mechanism:**

- Off-chain bridge service monitors `Transfer` events where `to` address matches bridge contract
- Parses transfer details: token contract address, sender, amount, transaction hash
- Triggers corresponding mint operations on the source blockchain

### 2. BLS Threshold Signature System

**Native Ethereum BLS Support:**

- Utilizes Ethereum's native BLS12-381 curve support for signature verification
- Implements threshold signature validation using precompiled contracts
- Group public keys stored as G2 points (96 bytes compressed format)
- Signatures verified as G1 points (48 bytes compressed format)

**Signature Verification Process:**

```text
1. Parse withdrawal command: (epoch_id, request_id, recipient, token, amount)
2. Encode data using abi.encodePacked for consistent hashing
3. Compute message hash: keccak256(encoded_data)
4. Retrieve group public key for specified epoch_id
5. Verify BLS signature against group public key using precompiled contract (with operation domain separation)
6. Check request_id hasn't been processed for this epoch_id
7. Execute withdrawal if signature valid and request not processed
8. Record request_id as processed for this epoch_id
```

### 3. Epoch-Based Group Key Management

**Group Key Transitions:**

- Each epoch has an associated BLS group public key (G2 point, 96 bytes)
- Group keys represent the consensus validator set for that epoch
- Transitions must be submitted sequentially by epoch number
- Each transition includes validation signature from previous epoch (chain of trust)

**Optimized Storage:**

```solidity
// Optimized group key storage (3 slots instead of 4)
struct GroupKey {
    bytes32 part0;  // bytes 0-31
    bytes32 part1;  // bytes 32-63  
    bytes32 part2;  // bytes 64-95
}

// Simple mapping: epochId => groupPublicKey (96 bytes G2 point, 3 storage slots)
mapping(uint64 => GroupKey) public epochGroupKeys;

enum ContractState {
    ADMIN_CONTROL,      // 0 - Initial state, conflict resolution, and timeout recovery
    NORMAL_OPERATION    // 1 - Standard bridge operations
}
// Note: Enums compile to uint8 - same efficiency as constants but with type safety

// Packed storage for metadata (fits in one 32-byte slot)
struct EpochMetadata {
    uint64 latestEpochId;        // 8 bytes
    uint64 submissionTimestamp;  // 8 bytes (sufficient until year 2554)
    ContractState currentState;  // 1 byte (enum compiles to uint8)
    uint120 reserved;            // 15 bytes for future use
}
EpochMetadata public epochMeta;

// Note: validationSignature only used during submission, not stored
```

**Sequential Submission Requirements:**

- New transitions must have `epochId = lastEpochId + 1`
- Cannot skip epochs or submit out-of-order transitions
- Each transition must include valid signature from previous epoch's group key
- Genesis epoch (epoch 1) requires admin submission without validation signature

**Use of `submissionTimestamp`:**

Since **1 epoch = 1 day**, the timestamp is needed for:

1. **Timeout detection**: Detect when no new transitions arrive (e.g., if validators stop submitting)
2. **Not needed for cleanup**: Since epochs naturally correspond to days, we can keep last 365 epochs

**Optimized submission and cleanup:**

```solidity
uint64 public constant MAX_STORED_EPOCHS = 365;  // 365 epochs = 365 days
uint64 public oldestStoredEpoch = 1;

function submitGroupKey(uint64 epochId, bytes calldata groupPublicKey, bytes calldata validationSig) external {
    // Verify sequential submission
    require(epochId == epochMeta.latestEpochId + 1, "Must be next epoch");
    
    // Verify validation signature against previous epoch (if not genesis)
    if (epochId > 1) {
        bytes memory prevGroupKey = epochGroupKeys[epochId - 1];
        require(_verifyTransitionSignature(prevGroupKey, groupPublicKey, validationSig), "Invalid signature");
    }
    
    // Store only the group public key
    epochGroupKeys[epochId] = groupPublicKey;
    
    // Update metadata in packed storage (single SSTORE)
    epochMeta = EpochMetadata({
        latestEpochId: epochId,
        submissionTimestamp: uint64(block.timestamp),
        currentState: epochMeta.currentState,  // Preserve current state
        reserved: 0
    });
    
    // Clean up old epochs (keep last 365 epochs = 365 days)
    if (epochId - oldestStoredEpoch >= MAX_STORED_EPOCHS) {
        delete epochGroupKeys[oldestStoredEpoch];
        oldestStoredEpoch++;
    }
    
    emit GroupKeySubmitted(epochId, block.timestamp);
}

// Check for timeout (no new transitions)
function checkTimeout() external view returns (bool) {
    return block.timestamp - epochMeta.submissionTimestamp > 30 days;
}
```

### 4. Admin Failsafe System

**Contract Initialization:**

- Contract deploys in `ADMIN_CONTROL` state
- Admin must submit genesis epoch (epoch 1) group key to initialize bridge
- No validation signature required for genesis epoch (no previous epoch exists)
- Only after genesis setup can contract transition to `NORMAL_OPERATION`

**Conflict Resolution:**

- If two different group keys submitted for same epoch → contract returns to `ADMIN_CONTROL` state
- Admin gains temporary control to resolve conflicts
- Admin can submit correct group key and reset to `NORMAL_OPERATION` state
- All withdrawal operations suspended during admin control

**Stuck Epoch Recovery:**

- **Timeout mechanism**: If no new group key transitions for 30 days, automatic `ADMIN_CONTROL` activation
- **Epoch-based approach**: Since 1 epoch = 1 day, timeout detection uses submission timestamps
- Admin must submit missing transitions to restore normal operation
- Prevents bridge from becoming permanently stuck due to validator issues

**Operational Behavior:**

- **ADMIN_CONTROL state automatically suspends all bridge operations** (withdrawals, group key transitions by validators)
- Only admin functions available: `submitGroupKey()`, `resetToNormalOperation()`
- **No arbitrary pause power** - admin cannot suspend operations outside of predefined failure scenarios
- Bridge operations only resume when admin resolves conflicts and transitions to `NORMAL_OPERATION`

**Admin Capabilities in Control State:**

```solidity
// Admin functions (only available in ADMIN_CONTROL state):
function submitGroupKey(uint64 epochId, bytes calldata groupPublicKey) external onlyAdmin;
function resetToNormalOperation() external onlyAdmin;
// Note: No arbitrary emergency triggers - admin control only activated by predefined conditions
```

**State Transitions and Permissions:**

```solidity
// ADMIN_CONTROL → NORMAL_OPERATION (only admin transition)
function resetToNormalOperation() external onlyAdmin {
    require(epochMeta.currentState == ContractState.ADMIN_CONTROL, "Must be in admin control");
    require(hasValidGenesisEpoch(), "Genesis epoch required");
    
    // Update state in packed storage
    epochMeta.currentState = ContractState.NORMAL_OPERATION;
}

// NORMAL_OPERATION → ADMIN_CONTROL (automatic triggers only)
// Triggered by:
// 1. Epoch conflict detection (two different keys for same epoch)
// 2. Timeout (30 days without new group key transition)
// 3. Initial contract deployment state
function _triggerAdminControl(string memory reason) internal {
    epochMeta.currentState = ContractState.ADMIN_CONTROL;
    emit AdminControlActivated(block.timestamp, reason);
}

// Timeout check function
function checkAndHandleTimeout() external {
    require(epochMeta.currentState == ContractState.NORMAL_OPERATION, "Already in admin control");
    
    if (block.timestamp - epochMeta.submissionTimestamp > 30 days) {
        _triggerAdminControl("Timeout: No new epochs for 30 days");
    }
}
```

**Operations Allowed by State:**

- **ADMIN_CONTROL**: Only admin functions (submitGroupKey, resetToNormalOperation) - **ALL user operations suspended**
- **NORMAL_OPERATION**: All functions (withdrawals, group key transitions from validators, admin functions)

**State Usage:**

```solidity
// Example usage in withdrawal function
function withdraw(WithdrawalCommand calldata cmd) external {
    require(epochMeta.currentState == ContractState.NORMAL_OPERATION, "Bridge not operational");
    // ... rest of withdrawal logic
}

// Contract deployment initialization
constructor() {
    epochMeta.currentState = ContractState.ADMIN_CONTROL;  // Start in admin control
}
```

**Key Security Features:**

- **No arbitrary admin pause power** - admin cannot suspend operations at will or trigger emergency stops
- **Automatic protection only** - system enters admin control only for predefined failure conditions:
  - Epoch conflicts (two different keys for same epoch)
  - Timeout conditions (30 days without new epochs)
  - Initial deployment state
- **Limited admin scope** - admin can only resolve conflicts and restore normal operation
- **Transparent triggers** - all transitions to admin control are event-logged with clear reasons
- **Trustless design** - no manual override mechanisms that could be abused

### 5. Storage and Lifecycle Management

**365-Day Retention Policy:**

- Keep only the most recent 365 epochs (365 epochs = 365 days naturally)
- Processed request IDs cleaned up when their corresponding epochs are removed
- Epoch-based cleanup: simple and deterministic since 1 epoch = 1 day
- Maintains sliding window of valid epochs for withdrawal validation

**Optimized Storage Structure:**

```solidity
// Efficient storage: only what's needed
mapping(uint64 => bytes) public epochGroupKeys;  // epochId => 96-byte G2 public key
mapping(uint64 => mapping(bytes32 => bool)) public processedRequests;  // epochId => requestId => processed

// Packed metadata (single 32-byte storage slot)
struct EpochMetadata {
    uint64 latestEpochId;        // 8 bytes
    uint64 submissionTimestamp;  // 8 bytes
    ContractState currentState;  // 1 byte (enum compiles to uint8)
    uint120 reserved;            // 15 bytes for future use
}
EpochMetadata public epochMeta;

// Constants
uint64 public constant MAX_STORED_EPOCHS = 365;  // 365 epochs = 365 days
uint64 public oldestStoredEpoch = 1;

function cleanupOldEpochs(uint64 newEpochId) internal {
    // Remove old epochs if we exceed the 365 epoch limit
    while (newEpochId - oldestStoredEpoch >= MAX_STORED_EPOCHS) {
        // Clean up group key (96 bytes freed)
        delete epochGroupKeys[oldestStoredEpoch];
        
        // Clean up processed requests for this epoch
        // Note: Individual request deletions would be expensive
        // Better to use a different storage pattern for large request sets
        
        oldestStoredEpoch++;
    }
}
```

**Benefits of optimized storage:**

- **Storage efficient**: GroupKey struct uses 3 slots instead of 4 (25% savings per epoch)
- **Gas efficient**: Single SSTORE for metadata update (latestEpochId + timestamp + state)
- **Contract state included**: Current state stored in same slot for free
- **Minimal storage**: Only stores essential data (group keys, not validation signatures)
- **Natural time alignment**: 365 epochs = 365 days exactly
- **Predictable gas costs**: Cleanup happens at known intervals  
- **Simple logic**: Count-based, but corresponds to time periods
- **Future-proof**: 15 bytes reserved space in packed struct for additional metadata

### 6. Withdrawal Command Processing

**Withdrawal Structure:**

```solidity
struct WithdrawalCommand {
    uint64 epochId;           // 8 bytes - epoch for signature validation
    bytes32 requestId;        // 32 bytes - unique request identifier from source chain
    address recipient;        // 20 bytes - Ethereum address to receive tokens
    address tokenContract;    // 20 bytes - ERC-20 contract address
    uint256 amount;          // 32 bytes - token amount to withdraw
    bytes signature;         // 48 bytes - BLS threshold signature (G1 point)
}
```

**Validation Flow:**

1. **Epoch Validation**: Verify group key exists for specified `epochId` (`epochGroupKeys[epochId].length > 0`)
2. **Replay Protection**: Check `requestId` hasn't been processed for this `epochId`
3. **Signature Verification**: Validate BLS signature against epoch's group public key using message: `abi.encodePacked(epochId, requestId, WITHDRAW_OPERATION, recipient, tokenContract, amount)`
4. **Balance Check**: Ensure contract has sufficient token or ETH balance
5. **Execution**: Transfer tokens or ETH to recipient address
   - **ETH withdrawals**: When `tokenContract == address(this)`, transfer ETH using `call{value:}`
   - **ERC-20 withdrawals**: When `tokenContract != address(this)`, transfer tokens using `safeTransfer`
6. **Record Processing**: Mark `requestId` as processed for this `epochId`

**Request ID Management:**

```solidity
// Track processed request IDs per epoch
mapping(uint64 => mapping(bytes32 => bool)) public processedRequests;  // epochId => requestId => processed

// Track active epochs for cleanup
uint64[] public activeRequestEpochs;  // Ordered list of epochs with processed requests
```

## Security Considerations

### 1. Signature Validation

- All BLS signatures verified using Ethereum precompiled contracts
- Message encoding follows strict `abi.encodePacked` format for consistency
- Prevents signature malleability through canonical encoding

### 2. Epoch Continuity

- Group key transitions form cryptographic chain of trust
- Each epoch validates previous epoch's transition
- Breaks in chain trigger admin intervention

### 3. Admin Safeguards

- Admin control only activated in specific failure scenarios
- Clear conditions for returning to normal operation
- Time-locked admin functions to prevent immediate misuse

### 4. Replay Protection

- Per-epoch request ID tracking prevents double-spending
- Request IDs are unique identifiers from the source chain
- Failed withdrawals don't mark request IDs as processed
- Request ID mappings cleaned up when epochs are removed (epoch-based, not time-based)
- Each epoch maintains independent request ID namespace

## Missing Components and Recommendations

### 1. Multi-Signature Admin Controls

**Current Gap**: Single admin key presents centralization risk

**Recommendation**:

- Multi-signature admin wallet requirement
- Timelock contract for critical admin functions
- Gradual admin privilege reduction as system matures

### 2. Upgrade Mechanism

**Current Gap**: No contract upgrade path defined

**Recommendation**:

- Proxy pattern for non-disruptive upgrades
- Version management for compatibility
- Migration path for stored data

### 3. Monitoring and Analytics

**Current Gap**: Limited visibility into bridge operations

**Recommendation**:

- Comprehensive event logging for all operations
- Bridge health metrics and status endpoints
- Integration with monitoring infrastructure

## Implementation Priority

**Phase 1 (Core Functionality):**

- BLS signature verification system
- Epoch-based group key management
- Basic withdrawal processing
- Admin failsafe mechanisms

**Phase 2 (Security Enhancements):**

- Multi-signature admin controls
- Enhanced monitoring and analytics

**Phase 3 (Operational Improvements):**

- Upgrade mechanism
- Performance optimizations

## Extended Functionality: WGNK ERC-20 Token Integration

### 7. WGNK (Wrapped Gonka) ERC-20 Implementation

**Dual Purpose Contract:**

This contract serves as both the Ethereum bridge and the ERC-20 token contract for WGNK (Wrapped Gonka). This unified design creates a seamless bridge experience where the contract itself is the wrapped token, eliminating the need for separate token and bridge contracts.

**ERC-20 Standard Compliance:**

- **Token Metadata**: Name "Wrapped Gonka", Symbol "WGNK", Decimals matching native Gonka
- **Standard Functions**: `totalSupply`, `balanceOf`, `transfer`, `approve`, `transferFrom`, `allowance`
- **Events**: Standard `Transfer` and `Approval` events for full ERC-20 compatibility
- **Storage**: Standard ERC-20 mappings for balances and allowances

**Auto-Burn Mechanism:**

When WGNK tokens are transferred to the contract's own address, they are automatically burned instead of being credited to the contract's balance. This provides an intuitive UX where users "send tokens to the bridge" to initiate burning for bridge-back operations.

```solidity
// Enhanced transfer logic with auto-burn
function transfer(address to, uint256 amount) public override returns (bool) {
    if (to == address(this)) {
        // Auto-burn: sending tokens to contract burns them
        _burn(msg.sender, amount);
        emit WGNKBurned(msg.sender, amount, block.timestamp);
        return true;
    } else {
        // Standard ERC-20 transfer
        return super.transfer(to, amount);
    }
}
```

**BLS-Validated Minting:**

New minting operation that accepts BLS threshold signatures to mint WGNK tokens directly to recipients. This enables validators to mint WGNK when users bridge from the native chain.

```solidity
struct MintCommand {
    uint64 epochId;           // 8 bytes - epoch for signature validation
    bytes32 requestId;        // 32 bytes - unique request identifier from source chain
    address recipient;        // 20 bytes - Ethereum address to receive WGNK
    uint256 amount;          // 32 bytes - WGNK amount to mint
    bytes signature;         // 48 bytes - BLS threshold signature (G1 point)
}

function mintWithSignature(MintCommand calldata cmd) external;
```

**Minting Validation Flow:**

1. **State Check**: Only allowed in `NORMAL_OPERATION` state
2. **Epoch Validation**: Verify group key exists for specified `epochId`
3. **Replay Protection**: Check `requestId` hasn't been processed for this `epochId`
4. **Signature Verification**: Validate BLS signature against epoch's group public key using message: `abi.encodePacked(epochId, requestId, MINT_OPERATION, recipient, amount, )`
5. **Execution**: Mint WGNK tokens to recipient's balance, increase total supply
6. **Record Processing**: Mark `requestId` as processed for this `epochId`

**Integration with Existing Systems:**

- **Bridge Operations**: All existing withdrawal functionality preserved for other ERC-20 tokens and ETH
- **BLS Infrastructure**: Reuses epoch-based group key management and signature verification
- **Request Tracking**: Uses same request ID system to prevent double-minting
- **State Management**: WGNK operations suspended during admin control (except standard transfers between users)

**Enhanced Event System:**

```solidity
// New events for WGNK operations
event WGNKMinted(uint64 indexed epochId, bytes32 indexed requestId, address indexed recipient, uint256 amount);
event WGNKBurned(address indexed from, uint256 amount, uint256 timestamp);

// Standard ERC-20 events
event Transfer(address indexed from, address indexed to, uint256 value);
event Approval(address indexed owner, address indexed spender, uint256 value);
```

**Complete Bridge Flow Integration:**

1. **Native to Ethereum**:
   - User locks native Gonka on source chain
   - Validators generate BLS signature for mint command
   - Anyone calls `mintWithSignature` to mint WGNK to user's Ethereum address

2. **Ethereum to Native**:
   - User transfers WGNK to contract address (auto-burn triggered)
   - Off-chain bridge detects `WGNKBurned` event
   - Native Gonka unlocked on source chain

**Storage Optimization:**

```solidity
// Additional ERC-20 storage alongside existing bridge storage
mapping(address => uint256) private _balances;
mapping(address => mapping(address => uint256)) private _allowances;
uint256 private _totalSupply;

// Reuse existing structures
// - epochGroupKeys: for both withdrawal and mint signature verification
// - processedRequests: for both withdrawal and mint replay protection
// - epochMeta: state management applies to both bridge and WGNK operations
```

**Security Considerations:**

- **Operation Domain Separation**: Each operation type (withdraw/mint) includes a unique constant in the message hash to prevent signature reuse between different operations
- **Dual Validation**: Both minting and withdrawal operations use same BLS security model
- **State Isolation**: Admin control suspends bridge operations but allows standard ERC-20 transfers
- **Burn Safety**: Auto-burn is permissionless and irreversible by design (intended behavior)
- **Request Isolation**: Separate request ID namespaces for minting vs withdrawal prevent cross-operation replay attacks

This specification provides a comprehensive foundation for a secure, decentralized bridge contract while maintaining necessary administrative controls for emergency situations.

## Native Gonka Bridge Integration

### 8. Special Bridge Module Account for Native Gonka

**Bridge Module Account Creation:**

A special module account needs to be created within the inference module to handle native Gonka token bridging to Ethereum as WGNK. This account serves as the escrow mechanism for native tokens being bridged.

**Module Account Properties:**
- **Account Name**: `"bridge_escrow"` - dedicated sub-account within the inference module
- **Purpose**: Hold native Gonka tokens that are being bridged to Ethereum as WGNK
- **Access Control**: Only the inference module can transfer tokens to/from this account
- **Balance Tracking**: Standard Cosmos SDK coin tracking for native token amounts

**Files to Create/Modify:**
- `inference-chain/x/inference/keeper/bridge_native.go` - Native bridge operations
- `inference-chain/x/inference/types/keys.go` - Add bridge escrow account constant
- `inference-chain/x/inference/module/module.go` - Register the bridge escrow account

### 9. MsgRequestBridgeMint Implementation

**New Message Type:**

Create `MsgRequestBridgeMint` to handle native Gonka bridging to WGNK. Users call this message directly to initiate the bridge process, which atomically transfers their native tokens to the bridge escrow account and generates the corresponding WGNK mint request.

**Message Structure:**
- **Creator**: User address sending native tokens
- **Amount**: Amount of native Gonka tokens to bridge
- **DestinationAddress**: Ethereum address to receive WGNK tokens
- **ChainId**: Target chain identifier (e.g., "ethereum", "sepolia")

**Processing Flow:**
1. **Message Validation**: Verify sender has sufficient balance and destination address format is valid
2. **Atomic Transfer**: Transfer native tokens from user to bridge escrow account using bank module
3. **BLS Signature Request**: Generate BLS signature request for WGNK minting on Ethereum
4. **Event Emission**: Emit bridge mint request event for off-chain monitoring
5. **Response**: Return request ID and epoch information to user

**Files to Create/Modify:**
- `inference-chain/proto/inference/inference/tx.proto` - Add MsgRequestBridgeMint protobuf definition
- `inference-chain/x/inference/types/message_request_bridge_mint.go` - Message validation logic
- `inference-chain/x/inference/keeper/msg_server_request_bridge_mint.go` - Message handler implementation

### 10. Bridge Transaction Processing for Native Tokens

**Enhanced Bridge Transaction Handling:**

Modify the existing bridge transaction processing to handle reverse direction - when BridgeTransaction is registered with a contract address matching the registered bridge address, it should release native tokens from escrow.

**Processing Logic:**
1. **Bridge Address Matching**: Check if `bridgeTx.ContractAddress` equals any registered bridge contract address
2. **Validation**: Verify the transaction represents a valid WGNK burn on Ethereum
3. **Token Release**: Transfer native Gonka tokens from bridge escrow account to `bridgeTx.OwnerAddress`
4. **Balance Management**: Ensure sufficient escrow balance exists for the withdrawal

**Owner Address Resolution:**
- Use the same address derivation mechanism as wrapped tokens
- `bridgeTx.OwnerAddress` should be a valid Cosmos bech32 address
- Support address format conversion if needed (Ethereum → Cosmos)

**Files to Modify:**
- `inference-chain/x/inference/keeper/bridge_wrapped_token.go` - Extend `handleCompletedBridgeTransaction` function
- `inference-chain/x/inference/keeper/bridge_native.go` - Add native token release logic
- `inference-chain/x/inference/keeper/bridge.go` - Add bridge address matching utilities

### 11. Integration with Existing Bridge Infrastructure

**Bridge Address Registration:**

Utilize the existing `MsgRegisterBridgeAddresses` mechanism to register Ethereum bridge contract addresses. These addresses will be used to identify when bridge transactions represent WGNK burns (reverse direction).

**BLS Signature Integration:**

Leverage the existing BLS signature infrastructure for both directions:
- **Forward Direction**: Native → WGNK (mint signatures)
- **Reverse Direction**: WGNK → Native (withdrawal validation)

**Event System Enhancement:**

Extend the existing event system to include native bridge operations:
- `BridgeMintRequested`: When native tokens are escrowed for WGNK minting
- `NativeTokensReleased`: When native tokens are released from escrow
- `BridgeEscrowBalanceChanged`: For escrow account balance tracking

**Files to Modify:**
- `inference-chain/x/inference/keeper/msg_server_register_bridge_addresses.go` - Ensure bridge addresses are properly registered
- `inference-chain/x/inference/keeper/msg_server_bridge_exchange.go` - Add native token handling to existing bridge exchange logic
- `inference-chain/x/inference/types/events.go` - Add new event types for native bridge operations

### 12. Complete Bridge Flow for Native Gonka ↔ WGNK

**Native to WGNK (Forward Direction):**
1. User calls `MsgRequestBridgeMint` with native Gonka amount and Ethereum destination
2. Native tokens transferred to bridge escrow module account
3. BLS signature request generated for WGNK minting on Ethereum
4. Validators sign the mint command using existing BLS infrastructure
5. Anyone can call `mintWithSignature` on Ethereum BridgeContract to mint WGNK

**WGNK to Native (Reverse Direction):**
1. User transfers WGNK to BridgeContract address on Ethereum (auto-burn triggered)
2. Off-chain bridge detects `WGNKBurned` event
3. Bridge submits `BridgeExchange` message with bridge contract address
4. When majority validation reached, `handleCompletedBridgeTransaction` detects bridge address match
5. Native Gonka tokens released from escrow to user's Cosmos address

**Security Considerations:**
- **Escrow Balance Monitoring**: Ensure escrow account always has sufficient balance for withdrawals
- **Address Validation**: Strict validation of Cosmos ↔ Ethereum address conversions
- **Double-Spend Prevention**: Prevent same Ethereum burn from being processed multiple times
- **Emergency Controls**: Admin controls to pause native bridge operations if needed

This integration completes the bidirectional bridge mechanism, allowing seamless conversion between native Gonka tokens and WGNK on Ethereum while maintaining the security guarantees of the existing BLS threshold signature system.
