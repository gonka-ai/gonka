# [IMPLEMENTED]: Ethereum Bridge Smart Contract

This directory contains the Ethereum smart contract implementation for the cross-chain bridge using BLS threshold signatures.

## Files

- `ethereum-bridge-contract.md` - Comprehensive specification document
- `BridgeContract.sol` - Smart contract implementation
- `README.md` - This file

## Contract Overview

The `BridgeContract` enables secure cross-chain token transfers by:

1. **Receiving ERC-20 token deposits** via standard transfers
2. **Processing withdrawals** signed with BLS threshold signatures
3. **Managing epoch-based validator sets** with automatic cleanup
4. **Providing admin failsafe mechanisms** for conflict resolution

## Key Features

### Storage Optimization
- **Packed metadata struct** - Single 32-byte storage slot for all metadata
- **Gas-efficient operations** - ~27,000+ gas savings per epoch submission
- **Automatic cleanup** - 365-epoch sliding window prevents unbounded growth

### Security Features
- **BLS signature verification** using Ethereum's native precompiles
- **Replay protection** via per-epoch request ID tracking
- **Admin failsafe system** with automatic timeout detection
- **Sequential epoch validation** with cryptographic chain of trust

### Multi-Asset Support
- **ERC-20 tokens** - Standard token withdrawals using `safeTransfer`
- **ETH withdrawals** - Use `tokenContract = address(this)` for ETH transfers
- **Unified interface** - Same withdrawal flow for all asset types

### Operational States
- **ADMIN_CONTROL** - Initial state, conflict resolution, timeout recovery
- **NORMAL_OPERATION** - Standard bridge operations

## Deployment

### Prerequisites
```bash
npm install @openzeppelin/contracts
```

### Constructor
```solidity
constructor()
```
- Contract starts in `ADMIN_CONTROL` state
- Requires admin to submit genesis epoch before normal operations

### Initial Setup
1. Deploy contract
2. Admin calls `submitGroupKey(1, genesisGroupKey, emptySignature)` for epoch 1
3. Admin calls `resetToNormalOperation()` to enable withdrawals

## Usage

### For Users - Withdrawals

```solidity
function withdraw(WithdrawalCommand calldata cmd) external
```

**WithdrawalCommand Structure:**
```solidity
struct WithdrawalCommand {
    uint64 epochId;           // Epoch for signature validation
    bytes32 requestId;        // Unique request identifier from source chain
    address recipient;        // Ethereum address to receive tokens
    address tokenContract;    // ERC-20 contract address
    uint256 amount;          // Token amount to withdraw
    bytes signature;         // 48-byte BLS threshold signature
}
```

**Message Format for Signing:**
```solidity
bytes32 messageHash = keccak256(
    abi.encodePacked(epochId, requestId, recipient, tokenContract, amount)
);
```

### For Validators - Epoch Management

```solidity
function submitGroupKey(
    uint64 epochId,
    bytes calldata groupPublicKey,    // 96-byte G2 public key
    bytes calldata validationSig     // Signature from previous epoch
) external onlyOwner
```

### For Monitoring - State Queries

```solidity
function isValidEpoch(uint64 epochId) external view returns (bool)
function isRequestProcessed(uint64 epochId, bytes32 requestId) external view returns (bool)
function getCurrentState() external view returns (ContractState)
function getLatestEpochInfo() external view returns (uint64, uint64, bytes memory)
function getContractBalance(address tokenContract) external view returns (uint256)  // Use address(this) for ETH
```

## Admin Functions

### Normal Admin Operations
```solidity
function resetToNormalOperation() external onlyOwner
function checkAndHandleTimeout() external  // Callable by anyone
```

### Security Design
```solidity
// No arbitrary emergency controls - admin control only activated by predefined conditions:
// 1. Timeout (30 days without new epochs)
// 2. Epoch conflicts (automatic detection) 
// 3. Initial deployment state
```

## Events

```solidity
event GroupKeySubmitted(uint64 indexed epochId, bytes groupPublicKey, uint256 timestamp)
event WithdrawalProcessed(uint64 indexed epochId, bytes32 indexed requestId, address indexed recipient, address tokenContract, uint256 amount)
event AdminControlActivated(uint256 timestamp, string reason)
event NormalOperationRestored(uint64 epochId, uint256 timestamp)
```

## Error Handling

The contract uses custom errors for gas efficiency:

```solidity
error BridgeNotOperational()      // Contract not in NORMAL_OPERATION
error InvalidEpoch()              // Epoch doesn't exist
error RequestAlreadyProcessed()   // Replay attack prevention
error InvalidSignature()         // BLS signature verification failed
error InsufficientBalance()      // Not enough tokens in contract
```

## Gas Costs

**Typical Operations:**
- Withdraw: ~100,000-150,000 gas (depends on token transfer costs)
- Submit Group Key: ~80,000-120,000 gas
- State transitions: ~30,000-50,000 gas

**Optimizations:**
- Packed storage reduces epoch submission costs by ~27,000 gas
- Request ID tracking uses single mapping for efficiency
- Automatic cleanup prevents storage bloat

## Security Considerations

### BLS Signature Verification
- Uses Ethereum's native BLS12-381 precompiled contracts
- Message encoding follows strict `abi.encodePacked` format
- Prevents signature malleability through canonical encoding

### Admin Controls
- Admin cannot arbitrarily pause operations
- Automatic timeout detection (30 days) triggers admin control
- Clear conditions for returning to normal operation

### Replay Protection
- Per-epoch request ID tracking prevents double-spending
- Request IDs are unique identifiers from source chain
- Failed withdrawals don't mark request IDs as processed

## Integration

### Bridge Monitoring
Monitor `Transfer` events to detect token deposits:
```solidity
// Standard ERC-20 Transfer event when tokens sent to bridge
event Transfer(address indexed from, address indexed to, uint256 value)
```

### Off-chain Components
1. **Validator Network** - Signs withdrawal requests with BLS threshold signatures
2. **Bridge Monitor** - Watches for deposits and submits mint requests to source chain
3. **Epoch Manager** - Handles validator set transitions and group key updates

## Testing

The contract includes comprehensive error handling and state validation. Key test scenarios:

1. **Epoch Management** - Sequential submission, validation signatures, cleanup
2. **Withdrawal Processing** - Valid/invalid signatures, replay protection, insufficient balance
3. **State Transitions** - Admin control triggers, timeout detection, normal operation restoration
4. **Edge Cases** - Genesis epoch handling, storage limits, emergency functions

## Deployment Checklist

- [ ] Deploy contract with proper admin address
- [ ] Submit genesis epoch (epoch 1) group key
- [ ] Verify BLS precompile integration on target network
- [ ] Test withdrawal with small amounts
- [ ] Set up monitoring for events and timeouts
- [ ] Document validator public keys and setup
- [ ] Establish admin key security procedures

## Future Enhancements

1. **Multi-signature admin controls** - Reduce centralization risk
2. **Fee mechanism** - Configurable withdrawal fees
3. **Withdrawal limits** - Rate limiting and maximum amounts
4. **Token whitelist** - Approved token management
5. **Upgrade mechanism** - Proxy pattern for non-disruptive updates