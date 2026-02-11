# [IMPLEMENTED]: Ethereum Bridge Smart Contract

Ethereum smart contract implementation for cross-chain bridge using BLS threshold signatures. The contract serves dual purposes: bridge operations for ERC-20 tokens/ETH and WGNK (Wrapped Gonka) token issuance.

## Contract Overview

The `BridgeContract` is both:
- **Bridge**: Enables cross-chain token transfers with BLS threshold signature validation
- **WGNK Token**: ERC-20 token representing bridged Gonka native tokens

### Core Functionality

- Receive ERC-20 token and ETH deposits via standard transfers
- Process withdrawals signed with BLS threshold signatures
- Mint WGNK tokens with BLS signature validation
- Manage epoch-based validator sets with automatic cleanup
- Auto-burn WGNK when sent to contract address

## Storage Optimization

- Packed metadata struct - single 32-byte storage slot
- 365-epoch sliding window prevents unbounded growth
- Gas-efficient epoch submission

## Security Features

- BLS signature verification using Ethereum's native precompiles
- Dual chain ID replay protection (Gonka + Ethereum chain IDs)
- Per-epoch request ID tracking
- Sequential epoch validation with cryptographic chain of trust
- Admin failsafe with automatic timeout detection


### Constructor
```solidity
constructor(bytes32 _gonkaChainId, bytes32 _ethereumChainId)
```

**Parameters:**
- `_gonkaChainId`: Source chain identifier (e.g., `0x27c767b59757cbc34932bb1e316f012a7360878c0b48ddd99ea1db3e6b3a63fb`)
- `_ethereumChainId`: Target Ethereum chain ID for cross-chain replay protection

Contract starts in `ADMIN_CONTROL` state requiring genesis epoch setup.

### Initial Setup
1. Deploy contract with chain IDs
2. Admin calls `setGroupKey(1, genesisGroupKey)` for epoch 1
3. Admin calls `resetToNormalOperation()` to enable operations

## Usage

### Withdrawals (ERC-20 and ETH)

```solidity
function withdraw(WithdrawalCommand calldata cmd) external
```

**WithdrawalCommand Structure:**
```solidity
struct WithdrawalCommand {
    uint64 epochId;           // Epoch for signature validation
    bytes32 requestId;        // Unique request identifier from source chain
    address recipient;        // Ethereum address to receive tokens
    address tokenContract;    // ERC-20 contract address (or address(this) for ETH)
    uint256 amount;          // Token amount to withdraw
    bytes signature;         // 128-byte BLS threshold signature (G1 point, uncompressed)
}
```

**Message Hash Format:**
```solidity
bytes32 messageHash = keccak256(
    abi.encodePacked(
        epochId,
        GONKA_CHAIN_ID,
        requestId,
        ETHEREUM_CHAIN_ID,
        WITHDRAW_OPERATION,  // keccak256("WITHDRAW_OPERATION")
        recipient,
        tokenContract,
        amount
    )
);
```

**ETH Withdrawals:** Use `tokenContract = address(this)` to withdraw ETH.

### WGNK Minting

```solidity
function mintWithSignature(MintCommand calldata cmd) external
```

**MintCommand Structure:**
```solidity
struct MintCommand {
    uint64 epochId;           // Epoch for signature validation
    bytes32 requestId;        // Unique request identifier from source chain
    address recipient;        // Ethereum address to receive WGNK
    uint256 amount;          // WGNK amount to mint (9 decimals)
    bytes signature;         // 128-byte BLS threshold signature (G1 point, uncompressed)
}
```

**Message Hash Format:**
```solidity
bytes32 messageHash = keccak256(
    abi.encodePacked(
        epochId,
        GONKA_CHAIN_ID,
        requestId,
        ETHEREUM_CHAIN_ID,
        MINT_OPERATION,  // keccak256("MINT_OPERATION")
        recipient,
        amount
    )
);
```

### WGNK Token Operations

**Auto-Burn Transfers:**
```solidity
// Sending WGNK to contract address automatically burns tokens
token.transfer(address(bridgeContract), amount);  // Burns tokens
```

Enhanced `transfer()` and `transferFrom()` automatically burn tokens when sent to the contract address.

**Token Info:**
- Name: "Wrapped Gonka"
- Symbol: "WGNK"
- Decimals: 9 (matches Gonka native token)

### Epoch Management

**Public Submission (with validation):**
```solidity
function submitGroupKey(
    uint64 epochId,
    bytes calldata groupPublicKey,    // 256-byte G2 public key (uncompressed)
    bytes calldata validationSig      // 128-byte signature from previous epoch
) external
```

Anyone can submit the next sequential epoch with valid signature from previous epoch validators.

**Admin Setup (during ADMIN_CONTROL):**
```solidity
function setGroupKey(
    uint64 epochId,
    bytes calldata groupPublicKey     // 256-byte G2 public key (uncompressed)
) external onlyOwner onlyAdminControl
```

Admin can set group keys without validation signature during ADMIN_CONTROL state.

### State Management

```solidity
function resetToNormalOperation() external onlyOwner
function checkAndHandleTimeout() external  // Callable by anyone
```

**States:**
- `ADMIN_CONTROL` - Initial state, conflict resolution, timeout recovery
- `NORMAL_OPERATION` - Standard bridge operations

**Timeout:** 30 days without new epochs triggers automatic admin control.

### View Functions

```solidity
function isValidEpoch(uint64 epochId) external view returns (bool)
function isRequestProcessed(uint64 epochId, bytes32 requestId) external view returns (bool)
function getCurrentState() external view returns (ContractState)
function getLatestEpochInfo() external view returns (uint64 epochId, uint64 timestamp, bytes memory groupKey)
function isTimeoutReached() external view returns (bool)
function getContractBalance(address tokenContract) external view returns (uint256)  // address(this) for ETH
function getWGNKInfo() external view returns (string memory name, string memory symbol, uint8 decimals, uint256 totalSupply)
function decimals() external view returns (uint8)  // Returns 9
```

## Events

```solidity
event GroupKeySubmitted(uint64 indexed epochId, bytes groupPublicKey, uint256 timestamp)
event WithdrawalProcessed(uint64 indexed epochId, bytes32 indexed requestId, address indexed recipient, address tokenContract, uint256 amount)
event WGNKMinted(uint64 indexed epochId, bytes32 indexed requestId, address indexed recipient, uint256 amount)
event WGNKBurned(address indexed from, uint256 amount, uint256 timestamp)
event AdminControlActivated(uint256 timestamp, string reason)
event NormalOperationRestored(uint64 epochId, uint256 timestamp)
event EpochCleaned(uint64 indexed epochId)
```

## Error Handling

```solidity
error BridgeNotOperational()      // Contract not in NORMAL_OPERATION
error InvalidEpoch()              // Epoch doesn't exist
error RequestAlreadyProcessed()   // Replay attack prevention
error InvalidSignature()          // BLS signature verification failed
error MustBeInAdminControl()      // Operation requires ADMIN_CONTROL state
error InvalidEpochSequence()      // Epoch not sequential
error NoValidGenesisEpoch()       // Cannot enable operations without epoch 1
error TimeoutNotReached()         // Timeout check called too early
```

## Gas Costs

**Typical Operations:**
- Withdraw: ~100,000-150,000 gas (varies with token transfer)
- Mint WGNK: ~80,000-120,000 gas
- Submit Group Key: ~80,000-120,000 gas
- State transitions: ~30,000-50,000 gas

## Security Considerations

### BLS Signature Verification
- Uses Ethereum's native BLS12-381 precompiled contracts (EIP-2537)
- 128-byte uncompressed G1 signatures
- 256-byte uncompressed G2 public keys
- Message encoding follows strict `abi.encodePacked` format

### Replay Protection
- Dual chain ID binding prevents cross-chain replays
- Operation type constants prevent cross-operation replays
- Per-epoch request ID tracking prevents double-spending

### Admin Controls
- Admin cannot arbitrarily pause operations
- Timeout detection (30 days) automatically triggers admin control
- Clear conditions for returning to normal operation

## Integration

### Deposit Detection
Monitor standard ERC-20 `Transfer` events to bridge contract address:
```solidity
event Transfer(address indexed from, address indexed to, uint256 value)
```

### Off-chain Components
- **Validator Network**: Signs withdrawal/mint requests with BLS threshold signatures
- **Bridge Monitor**: Watches for deposits and submits mint requests to source chain
- **Epoch Manager**: Handles validator set transitions and group key updates
