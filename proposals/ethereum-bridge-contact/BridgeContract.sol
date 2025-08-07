// SPDX-License-Identifier: MIT
pragma solidity ^0.8.19;

import "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import "@openzeppelin/contracts/token/ERC20/ERC20.sol";
import "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import "@openzeppelin/contracts/access/Ownable.sol";
import "@openzeppelin/contracts/security/ReentrancyGuard.sol";

/**
 * @title BridgeContract
 * @dev Ethereum bridge contract and WGNK (Wrapped Gonka) ERC-20 token with BLS threshold signatures
 * @notice This contract serves as both the bridge and the WGNK token, enabling seamless cross-chain transfers
 */
contract BridgeContract is ERC20, Ownable, ReentrancyGuard {
    using SafeERC20 for IERC20;

    // =============================================================================
    // ENUMS AND STRUCTS
    // =============================================================================

    enum ContractState {
        ADMIN_CONTROL,      // 0 - Initial state, conflict resolution, and timeout recovery
        NORMAL_OPERATION    // 1 - Standard bridge operations
    }

    // Packed metadata (single 32-byte storage slot)
    struct EpochMetadata {
        uint64 latestEpochId;        // 8 bytes
        uint64 submissionTimestamp;  // 8 bytes (sufficient until year 2554)
        ContractState currentState;  // 1 byte (enum compiles to uint8)
        uint120 reserved;            // 15 bytes for future use
    }

    struct WithdrawalCommand {
        uint64 epochId;           // 8 bytes - epoch for signature validation
        bytes32 requestId;        // 32 bytes - unique request identifier from source chain
        address recipient;        // 20 bytes - Ethereum address to receive tokens
        address tokenContract;    // 20 bytes - ERC-20 contract address
        uint256 amount;          // 32 bytes - token amount to withdraw
        bytes signature;         // 48 bytes - BLS threshold signature (G1 point)
    }

    struct MintCommand {
        uint64 epochId;           // 8 bytes - epoch for signature validation
        bytes32 requestId;        // 32 bytes - unique request identifier from source chain
        address recipient;        // 20 bytes - Ethereum address to receive WGNK
        uint256 amount;          // 32 bytes - WGNK amount to mint
        bytes signature;         // 48 bytes - BLS threshold signature (G1 point)
    }

    // =============================================================================
    // STATE VARIABLES
    // =============================================================================

    // Optimized group key storage (3 slots instead of 4)
    struct GroupKey {
        bytes32 part0;  // bytes 0-31
        bytes32 part1;  // bytes 32-63  
        bytes32 part2;  // bytes 64-95
    }
    
    /**
     * @dev Convert bytes to GroupKey struct
     */
    function _bytesToGroupKey(bytes memory data) internal pure returns (GroupKey memory) {
        require(data.length == 96, "Invalid group key length");
        
        bytes32 part0;
        bytes32 part1; 
        bytes32 part2;
        
        assembly {
            part0 := mload(add(data, 0x20))        // bytes 0-31
            part1 := mload(add(data, 0x40))        // bytes 32-63  
            part2 := mload(add(data, 0x60))        // bytes 64-95
        }
        
        return GroupKey(part0, part1, part2);
    }
    
    /**
     * @dev Convert GroupKey struct to bytes
     */
    function _groupKeyToBytes(GroupKey memory key) internal pure returns (bytes memory) {
        bytes memory result = new bytes(96);
        
        assembly {
            mstore(add(result, 0x20), mload(key))         // part0
            mstore(add(result, 0x40), mload(add(key, 0x20))) // part1  
            mstore(add(result, 0x60), mload(add(key, 0x40))) // part2
        }
        
        return result;
    }
    
    /**
     * @dev Check if GroupKey is empty (all zeros)
     */
    function _isGroupKeyEmpty(GroupKey memory key) internal pure returns (bool) {
        return key.part0 == bytes32(0) && key.part1 == bytes32(0) && key.part2 == bytes32(0);
    }
    
    // Efficient storage: only what's needed
    mapping(uint64 => GroupKey) public epochGroupKeys;  // epochId => 96-byte G2 public key (3 slots)
    mapping(uint64 => mapping(bytes32 => bool)) public processedRequests;  // epochId => requestId => processed

    // Packed metadata (single 32-byte storage slot)
    EpochMetadata public epochMeta;

    // Constants
    uint64 public constant MAX_STORED_EPOCHS = 365;  // 365 epochs = 365 days
    uint64 public constant TIMEOUT_DURATION = 30 days;

    // BLS signature verification (using Ethereum's native precompiles)
    address constant BLS_PRECOMPILE = 0x000000000000000000000000000000000000000f;
    
    // Operation type identifiers for message hash domain separation
    bytes32 constant WITHDRAW_OPERATION = keccak256("WITHDRAW_OPERATION");
    bytes32 constant MINT_OPERATION = keccak256("MINT_OPERATION");

    // =============================================================================
    // EVENTS
    // =============================================================================

    event GroupKeySubmitted(uint64 indexed epochId, bytes groupPublicKey, uint256 timestamp);
    event AdminControlActivated(uint256 timestamp, string reason);
    event NormalOperationRestored(uint64 epochId, uint256 timestamp);
    event WithdrawalProcessed(
        uint64 indexed epochId,
        bytes32 indexed requestId,
        address indexed recipient,
        address tokenContract,
        uint256 amount
    );
    event EpochCleaned(uint64 indexed epochId);
    
    // WGNK-specific events
    event WGNKMinted(uint64 indexed epochId, bytes32 indexed requestId, address indexed recipient, uint256 amount);
    event WGNKBurned(address indexed from, uint256 amount, uint256 timestamp);

    // =============================================================================
    // ERRORS
    // =============================================================================

    error BridgeNotOperational();
    error InvalidEpoch();
    error RequestAlreadyProcessed();
    error InvalidSignature();
    error MustBeInAdminControl();
    error InvalidEpochSequence();
    error NoValidGenesisEpoch();
    error TimeoutNotReached();

    // =============================================================================
    // CONSTRUCTOR
    // =============================================================================

    constructor() ERC20("Wrapped Gonka", "WGNK") {
        // Start in admin control state - requires genesis epoch setup
        epochMeta.currentState = ContractState.ADMIN_CONTROL;
        epochMeta.latestEpochId = 0;
        epochMeta.submissionTimestamp = uint64(block.timestamp);
    }

    // =============================================================================
    // MODIFIERS
    // =============================================================================

    modifier onlyNormalOperation() {
        if (epochMeta.currentState != ContractState.NORMAL_OPERATION) {
            revert BridgeNotOperational();
        }
        _;
    }

    modifier onlyAdminControl() {
        if (epochMeta.currentState != ContractState.ADMIN_CONTROL) {
            revert MustBeInAdminControl();
        }
        _;
    }

    // =============================================================================
    // ADMIN FUNCTIONS
    // =============================================================================

    /**
     * @dev Submit a new group public key for an epoch (admin only during ADMIN_CONTROL)
     * @param epochId The epoch ID (must be sequential)
     * @param groupPublicKey The 96-byte G2 public key for the epoch
     * @param validationSig The validation signature from previous epoch (not stored)
     */
    function submitGroupKey(
        uint64 epochId,
        bytes calldata groupPublicKey,
        bytes calldata validationSig
    ) external onlyOwner onlyAdminControl {
        // Verify sequential submission
        if (epochId != epochMeta.latestEpochId + 1) {
            revert InvalidEpochSequence();
        }

        // Verify group public key is 96 bytes (G2 point compressed)
        require(groupPublicKey.length == 96, "Invalid group key length");

        // Verify validation signature against previous epoch (if not genesis)
        GroupKey memory newGroupKeyStruct = _bytesToGroupKey(groupPublicKey);
        if (epochId > 1) {
            GroupKey memory prevGroupKeyStruct = epochGroupKeys[epochId - 1];
            require(!_isGroupKeyEmpty(prevGroupKeyStruct), "Previous epoch not found");
            
            require(_verifyTransitionSignature(prevGroupKeyStruct, newGroupKeyStruct, validationSig, epochId - 1), "Invalid transition signature");
        }

        // Store only the group public key
        epochGroupKeys[epochId] = newGroupKeyStruct;

        // Update metadata in packed storage (single SSTORE)
        epochMeta = EpochMetadata({
            latestEpochId: epochId,
            submissionTimestamp: uint64(block.timestamp),
            currentState: epochMeta.currentState,  // Preserve current state
            reserved: 0
        });

        // Clean up old epochs (keep last 365 epochs = 365 days)
        _cleanupOldEpochs(epochId);

        emit GroupKeySubmitted(epochId, groupPublicKey, block.timestamp);
    }

    /**
     * @dev Reset contract to normal operation (admin only)
     */
    function resetToNormalOperation() external onlyOwner onlyAdminControl {
        if (epochMeta.latestEpochId == 0) {
            revert NoValidGenesisEpoch();
        }

        // Update state in packed storage
        epochMeta.currentState = ContractState.NORMAL_OPERATION;

        emit NormalOperationRestored(epochMeta.latestEpochId, block.timestamp);
    }

    // =============================================================================
    // PUBLIC FUNCTIONS
    // =============================================================================

    /**
     * @dev Process a withdrawal command with BLS threshold signature
     * @param cmd The withdrawal command containing all necessary data
     */
    function withdraw(WithdrawalCommand calldata cmd) external nonReentrant onlyNormalOperation {
        // 1. Epoch Validation: Cache group key to avoid double SLOAD
        GroupKey memory groupKeyStruct = epochGroupKeys[cmd.epochId];
        if (_isGroupKeyEmpty(groupKeyStruct)) {
            revert InvalidEpoch();
        }
        bytes memory groupKey = _groupKeyToBytes(groupKeyStruct);

        // 2. Replay Protection: Check requestId hasn't been processed for this epochId
        if (processedRequests[cmd.epochId][cmd.requestId]) {
            revert RequestAlreadyProcessed();
        }

        // 3. Signature Verification: Use cached group key with operation domain separation
        bytes32 messageHash = keccak256(
            abi.encodePacked(cmd.epochId, cmd.requestId, WITHDRAW_OPERATION, cmd.recipient, cmd.tokenContract, cmd.amount)
        );
        
        if (!_verifyBLSSignature(groupKey, messageHash, cmd.signature)) {
            revert InvalidSignature();
        }

        // 4. Execution: Transfer tokens or ETH to recipient address
        if (cmd.tokenContract == address(this)) {
            // ETH withdrawal: tokenContract == address(this) indicates ETH
            require(address(this).balance >= cmd.amount, "Insufficient ETH balance");
            
            // Use call{value:} for better gas compatibility (no 2300 gas limit)
            (bool success, ) = cmd.recipient.call{value: cmd.amount}("");
            require(success, "ETH transfer failed");
        } else {
            // ERC-20 withdrawal: standard token transfer
            IERC20(cmd.tokenContract).safeTransfer(cmd.recipient, cmd.amount);
        }

        // 5. Record Processing: Mark requestId as processed (only after successful transfer)
        processedRequests[cmd.epochId][cmd.requestId] = true;

        emit WithdrawalProcessed(
            cmd.epochId,
            cmd.requestId,
            cmd.recipient,
            cmd.tokenContract,
            cmd.amount
        );
    }

    /**
     * @dev Mint WGNK tokens with BLS threshold signature validation
     * @param cmd The mint command containing all necessary data
     */
    function mintWithSignature(MintCommand calldata cmd) external nonReentrant onlyNormalOperation {
        // 1. Epoch Validation: Cache group key to avoid double SLOAD
        GroupKey memory groupKeyStruct = epochGroupKeys[cmd.epochId];
        if (_isGroupKeyEmpty(groupKeyStruct)) {
            revert InvalidEpoch();
        }
        bytes memory groupKey = _groupKeyToBytes(groupKeyStruct);

        // 2. Replay Protection: Check requestId hasn't been processed for this epochId
        if (processedRequests[cmd.epochId][cmd.requestId]) {
            revert RequestAlreadyProcessed();
        }

        // 3. Signature Verification: Use cached group key with operation domain separation
        bytes32 messageHash = keccak256(
            abi.encodePacked(cmd.epochId, cmd.requestId, MINT_OPERATION, cmd.recipient, cmd.amount)
        );
        
        if (!_verifyBLSSignature(groupKey, messageHash, cmd.signature)) {
            revert InvalidSignature();
        }

        // 4. Execution: Mint WGNK tokens to recipient
        _mint(cmd.recipient, cmd.amount);

        // 5. Record Processing: Mark requestId as processed (only after successful mint)
        processedRequests[cmd.epochId][cmd.requestId] = true;

        emit WGNKMinted(cmd.epochId, cmd.requestId, cmd.recipient, cmd.amount);
    }

    /**
     * @dev Enhanced transfer function with auto-burn when sending to contract address
     * @param to The recipient address (if contract address, tokens are burned)
     * @param amount The amount to transfer or burn
     */
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

    /**
     * @dev Enhanced transferFrom function with auto-burn when sending to contract address
     * @param from The sender address
     * @param to The recipient address (if contract address, tokens are burned)
     * @param amount The amount to transfer or burn
     */
    function transferFrom(address from, address to, uint256 amount) public override returns (bool) {
        if (to == address(this)) {
            // Auto-burn: sending tokens to contract burns them
            _spendAllowance(from, msg.sender, amount);
            _burn(from, amount);
            emit WGNKBurned(from, amount, block.timestamp);
            return true;
        } else {
            // Standard ERC-20 transferFrom
            return super.transferFrom(from, to, amount);
        }
    }

    /**
     * @dev Check for timeout and trigger admin control if needed (callable by anyone)
     */
    function checkAndHandleTimeout() external {
        if (epochMeta.currentState != ContractState.NORMAL_OPERATION) {
            return; // Already in admin control
        }

        if (block.timestamp - epochMeta.submissionTimestamp <= TIMEOUT_DURATION) {
            revert TimeoutNotReached();
        }

        _triggerAdminControl("Timeout: No new epochs for 30 days");
    }

    // =============================================================================
    // VIEW FUNCTIONS
    // =============================================================================

    /**
     * @dev Check if an epoch has a valid group key
     */
    function isValidEpoch(uint64 epochId) external view returns (bool) {
        return !_isGroupKeyEmpty(epochGroupKeys[epochId]);
    }

    /**
     * @dev Check if a request has been processed for a given epoch
     */
    function isRequestProcessed(uint64 epochId, bytes32 requestId) external view returns (bool) {
        return processedRequests[epochId][requestId];
    }

    /**
     * @dev Get current contract state
     */
    function getCurrentState() external view returns (ContractState) {
        return epochMeta.currentState;
    }

    /**
     * @dev Get latest epoch information
     */
    function getLatestEpochInfo() external view returns (uint64 epochId, uint64 timestamp, bytes memory groupKey) {
        epochId = epochMeta.latestEpochId;
        timestamp = epochMeta.submissionTimestamp;
        groupKey = _groupKeyToBytes(epochGroupKeys[epochId]);
    }

    /**
     * @dev Check if timeout has been reached
     */
    function isTimeoutReached() external view returns (bool) {
        return block.timestamp - epochMeta.submissionTimestamp > TIMEOUT_DURATION;
    }

    /**
     * @dev Get contract's balance for any token or ETH
     * @param tokenContract Address of the ERC-20 token, or address(this) for ETH
     * @return balance The balance of the specified token or ETH
     */
    function getContractBalance(address tokenContract) external view returns (uint256 balance) {
        if (tokenContract == address(this)) {
            return address(this).balance;  // ETH balance
        } else {
            return IERC20(tokenContract).balanceOf(address(this));  // ERC-20 balance
        }
    }

    /**
     * @dev Get WGNK token information
     * @return tokenName The token name
     * @return tokenSymbol The token symbol
     * @return tokenDecimals The number of decimals
     * @return tokenTotalSupply The total supply of WGNK
     */
    function getWGNKInfo() external view returns (
        string memory tokenName,
        string memory tokenSymbol,
        uint8 tokenDecimals,
        uint256 tokenTotalSupply
    ) {
        return (name(), symbol(), decimals(), totalSupply());
    }

    // =============================================================================
    // INTERNAL FUNCTIONS
    // =============================================================================

    /**
     * @dev Trigger admin control state with reason
     */
    function _triggerAdminControl(string memory reason) internal {
        epochMeta.currentState = ContractState.ADMIN_CONTROL;
        emit AdminControlActivated(block.timestamp, reason);
    }

    /**
     * @dev Clean up old epochs if we exceed the limit
     */
    function _cleanupOldEpochs(uint64 newEpochId) internal {
        // Only cleanup if we exceed the limit
        if (newEpochId <= MAX_STORED_EPOCHS) {
            return; // Keep all epochs if we haven't reached the limit yet
        }

        // Calculate which epoch to delete: keep latest MAX_STORED_EPOCHS
        // When adding epoch 366, delete epoch 1 (366 - 365 = 1)
        // When adding epoch 367, delete epoch 2 (367 - 365 = 2)
        uint64 epochToDelete = newEpochId - MAX_STORED_EPOCHS;
        
        delete epochGroupKeys[epochToDelete];
        
        // Note: processedRequests cleanup is expensive for individual deletions
        // In production, consider using a different storage pattern for large request sets
        
        emit EpochCleaned(epochToDelete);
    }

    /**
     * @dev Verify BLS signature using Ethereum's native precompiles
     * @param groupPublicKey The 96-byte G2 public key
     * @param messageHash The 32-byte message hash
     * @param signature The 48-byte G1 signature
     */
    function _verifyBLSSignature(
        bytes memory groupPublicKey,
        bytes32 messageHash,
        bytes memory signature
    ) internal view returns (bool) {
        require(groupPublicKey.length == 96, "Invalid group key length");
        require(signature.length == 48, "Invalid signature length");

        // Prepare data for BLS precompile
        // Format: message_hash (32) + signature (48) + public_key (96)
        bytes memory input = abi.encodePacked(messageHash, signature, groupPublicKey);

        // Call BLS precompile for signature verification
        (bool success, bytes memory result) = BLS_PRECOMPILE.staticcall(input);
        
        return success && result.length == 32 && abi.decode(result, (bool));
    }

    /**
     * @dev Verify transition signature - validates that new group key is signed by previous epoch
     * @param previousGroupKey The previous epoch's group key struct
     * @param newGroupKey The new epoch's group key struct  
     * @param validationSignature The 48-byte G1 signature from previous epoch validators
     * @param previousEpochId The epoch ID that signed this transition
     */
    function _verifyTransitionSignature(
        GroupKey memory previousGroupKey,
        GroupKey memory newGroupKey,
        bytes memory validationSignature,
        uint64 previousEpochId
    ) internal view returns (bool) {
        require(validationSignature.length == 48, "Invalid validation signature length");

        // Compute validation message hash following the format:
        // abi.encodePacked(previous_epoch_id, chain_id, data[0], data[1], data[2])
        // where data[0], data[1], data[2] are the 3 parts of the new group public key
        
        // Chain ID as bytes32 (constant for this contract deployment)
        bytes32 chainId = bytes32(block.chainid);
        
        // Encode message: abi.encodePacked(previousEpochId, chainId, part0, part1, part2)
        bytes memory encodedMessage = abi.encodePacked(
            previousEpochId,        // 8 bytes
            chainId,                // 32 bytes
            newGroupKey.part0,      // 32 bytes - direct access, no intermediate variables
            newGroupKey.part1,      // 32 bytes
            newGroupKey.part2       // 32 bytes
        );
        
        // Compute message hash
        bytes32 messageHash = keccak256(encodedMessage);
        
        // Verify BLS signature using previous epoch's group public key
        bytes memory previousGroupKeyBytes = _groupKeyToBytes(previousGroupKey);
        return _verifyBLSSignature(previousGroupKeyBytes, messageHash, validationSignature);
    }

    // =============================================================================
    // RECEIVE FUNCTION
    // =============================================================================

    /**
     * @dev Contract can receive ETH deposits
     */
    receive() external payable {
        // ETH deposits are allowed but not actively processed
        // Users should monitor Transfer events for bridge detection
    }
}