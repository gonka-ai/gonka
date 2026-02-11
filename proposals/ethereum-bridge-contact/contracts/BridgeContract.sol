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
        bytes signature;         // 128 bytes - BLS threshold signature (G1 point, uncompressed)
    }

    struct MintCommand {
        uint64 epochId;           // 8 bytes - epoch for signature validation
        bytes32 requestId;        // 32 bytes - unique request identifier from source chain
        address recipient;        // 20 bytes - Ethereum address to receive WGNK
        uint256 amount;          // 32 bytes - WGNK amount to mint
        bytes signature;         // 128 bytes - BLS threshold signature (G1 point, uncompressed)
    }

    // =============================================================================
    // STATE VARIABLES
    // =============================================================================

    // Optimized group key storage (3 slots instead of 4)
    struct GroupKey {
        // Uncompressed G2 point: X.c0, X.c1, Y.c0, Y.c1 (each 64 bytes → 8 x bytes32)
        bytes32 part0;
        bytes32 part1;
        bytes32 part2;
        bytes32 part3;
        bytes32 part4;
        bytes32 part5;
        bytes32 part6;
        bytes32 part7;
    }
    
    /**
     * @dev Convert bytes to GroupKey struct
     */
    function _bytesToGroupKey(bytes memory data) internal pure returns (GroupKey memory) {
        require(data.length == 256, "Invalid group key length");
        
        bytes32 part0;
        bytes32 part1; 
        bytes32 part2; 
        bytes32 part3;
        bytes32 part4;
        bytes32 part5;
        bytes32 part6;
        bytes32 part7;
        
        assembly {
            part0 := mload(add(data, 0x20))
            part1 := mload(add(data, 0x40))
            part2 := mload(add(data, 0x60))
            part3 := mload(add(data, 0x80))
            part4 := mload(add(data, 0xA0))
            part5 := mload(add(data, 0xC0))
            part6 := mload(add(data, 0xE0))
            part7 := mload(add(data, 0x100))
        }
        
        return GroupKey(part0, part1, part2, part3, part4, part5, part6, part7);
    }
    
    /**
     * @dev Convert GroupKey struct to bytes
     */
    function _groupKeyToBytes(GroupKey memory key) internal pure returns (bytes memory) {
        bytes memory result = new bytes(256);
        
        assembly {
            mstore(add(result, 0x20), mload(key))              // part0
            mstore(add(result, 0x40), mload(add(key, 0x20)))   // part1  
            mstore(add(result, 0x60), mload(add(key, 0x40)))   // part2
            mstore(add(result, 0x80), mload(add(key, 0x60)))   // part3
            mstore(add(result, 0xA0), mload(add(key, 0x80)))   // part4
            mstore(add(result, 0xC0), mload(add(key, 0xA0)))   // part5
            mstore(add(result, 0xE0), mload(add(key, 0xC0)))   // part6
            mstore(add(result, 0x100), mload(add(key, 0xE0)))  // part7
        }
        
        return result;
    }
    
    /**
     * @dev Check if GroupKey is empty (all zeros)
     */
    function _isGroupKeyEmpty(GroupKey memory key) internal pure returns (bool) {
        return key.part0 == bytes32(0) && key.part1 == bytes32(0) && key.part2 == bytes32(0)
            && key.part3 == bytes32(0) && key.part4 == bytes32(0) && key.part5 == bytes32(0)
            && key.part6 == bytes32(0) && key.part7 == bytes32(0);
    }
    
    // Efficient storage: only what's needed
    mapping(uint64 => GroupKey) public epochGroupKeys;  // epochId => 256-byte G2 public key (8 slots)
    mapping(uint64 => mapping(bytes32 => bool)) public processedRequests;  // epochId => requestId => processed

    // Packed metadata (single 32-byte storage slot)
    EpochMetadata public epochMeta;

    // Constants
    uint64 public constant MAX_STORED_EPOCHS = 365;  // 365 epochs = 365 days
    uint64 public constant TIMEOUT_DURATION = 30 days;

    // Chain ID constants for cross-chain replay protection
    bytes32 public immutable GONKA_CHAIN_ID;    // Source chain identifier (e.g., keccak256("gonka-mainnet-v1"))
    bytes32 public immutable ETHEREUM_CHAIN_ID; // This chain identifier (e.g., bytes32(uint256(1)))

    // EIP-2537 precompiles
    address constant BLS12_PAIRING = 0x000000000000000000000000000000000000000F;       // BLS12_PAIRING_CHECK
    address constant BLS12_MAP_FP_TO_G1 = 0x0000000000000000000000000000000000000010;  // BLS12_MAP_FP_TO_G1
    
    // Operation type identifiers for message hash domain separation
    bytes32 constant WITHDRAW_OPERATION = keccak256("WITHDRAW_OPERATION");
    bytes32 constant MINT_OPERATION = keccak256("MINT_OPERATION");
    
    // Field modulus p for BLS12-381 (64-byte big-endian, padded)
    bytes constant FP_MODULUS = hex"000000000000000000000000000000001a0111ea397fe69a4b1ba7b6434bacd764774b84f38512bf6730d2a0f6b0f6241eabfffeb153ffffb9feffffffffaaab";
    
    // Uncompressed G2 generator (X.c0, X.c1, Y.c0, Y.c1), each 64-byte big-endian (padded)
    bytes constant G2_GENERATOR = hex"00000000000000000000000000000000024aa2b2f08f0a91260805272dc51051c6e47ad4fa403b02b4510b647ae3d1770bac0326a805bbefd48056c8c121bdb80000000000000000000000000000000013e02b6052719f607dacd3a088274f65596bd0d09920b61ab5da61bbdc7f5049334cf11213945d57e5ac7d055d042b7e000000000000000000000000000000000ce5d527727d6e118cc9cdc6da2e351aadfd9baa8cbdd3a76d429a695160d12c923ac9cc3baca289e193548608b82801000000000000000000000000000000000606c4a02ea734cc32acd2b02bc28b99cb3e287e85a763af267492ab572e99ab3f370d275cec1da1aaa9075ff05f79be";

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

    constructor(bytes32 _gonkaChainId, bytes32 _ethereumChainId) ERC20("Wrapped Gonka", "WGNK") {
        // Set immutable chain IDs for cross-chain replay protection
        GONKA_CHAIN_ID = _gonkaChainId;
        ETHEREUM_CHAIN_ID = _ethereumChainId;
        
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
     * @dev Set a new group public key for an epoch (admin only during ADMIN_CONTROL)
     * @param epochId The epoch ID (must be sequential)
     * @param groupPublicKey The 256-byte G2 public key (uncompressed) for the epoch
     */
    function setGroupKey(
        uint64 epochId,
        bytes calldata groupPublicKey
    ) external onlyOwner onlyAdminControl {
        // Verify sequential submission
        if (epochId < epochMeta.latestEpochId + 1) {
            revert InvalidEpochSequence();
        }

        // Verify group public key is 256 bytes (G2 point uncompressed)
        require(groupPublicKey.length == 256, "Invalid group key length");

        // Verify validation signature against previous epoch (if not genesis)
        GroupKey memory newGroupKeyStruct = _bytesToGroupKey(groupPublicKey);

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
     * @dev Submit a new group public key for the next epoch
     * @param epochId The epoch ID (must be sequential)
     * @param groupPublicKey The 256-byte G2 public key (uncompressed) for the epoch
     * @param validationSig The validation signature from previous epoch (not stored)
     */
    function submitGroupKey(
        uint64 epochId,
        bytes calldata groupPublicKey,
        bytes calldata validationSig
    ) public {
        // Verify sequential submission
        if (epochId != epochMeta.latestEpochId + 1) {
            revert InvalidEpochSequence();
        }

        // Verify group public key is 256 bytes (G2 point uncompressed)
        require(groupPublicKey.length == 256, "Invalid group key length");

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

        // 3. Signature Verification: Use cached group key with dual chain ID protection
        // Message format: [epochId, gonkaChainId, requestId, ethereumChainId, WITHDRAW_OPERATION, recipient, tokenContract, amount]
        bytes32 messageHash = keccak256(
            abi.encodePacked(
                cmd.epochId,        // Gonka epoch
                GONKA_CHAIN_ID,     // Gonka chain identifier (prevents cross-Gonka-chain replays)
                cmd.requestId,      // Unique request ID
                ETHEREUM_CHAIN_ID,  // This Ethereum chain ID (prevents cross-Ethereum-chain replays)
                WITHDRAW_OPERATION, // Operation type
                cmd.recipient,      // Withdrawal details
                cmd.tokenContract,
                cmd.amount
            )
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

        // 3. Signature Verification: Use cached group key with dual chain ID protection
        // Message format: [epochId, gonkaChainId, requestId, ethereumChainId, MINT_OPERATION, recipient, amount]
        bytes32 messageHash = keccak256(
            abi.encodePacked(
                cmd.epochId,        // Gonka epoch
                GONKA_CHAIN_ID,     // Gonka chain identifier (prevents cross-Gonka-chain replays)
                cmd.requestId,      // Unique request ID
                ETHEREUM_CHAIN_ID,  // This Ethereum chain ID (prevents cross-Ethereum-chain replays)
                MINT_OPERATION,     // Operation type
                cmd.recipient,      // Mint details
                cmd.amount
            )
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

    /**
     * @dev Override default decimals (18) to match Nano/Gonka (9)
     */
    function decimals() public view virtual override returns (uint8) {
        return 9;
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
     * @dev Verify BLS signature using EIP-2537 precompiles (pairing check)
     * @param groupPublicKey The 256-byte G2 public key (uncompressed)
     * @param messageHash The 32-byte message hash
     * @param signature The 128-byte G1 signature (uncompressed)
     */
    function _verifyBLSSignature(
        bytes memory groupPublicKey,
        bytes32 messageHash,
        bytes memory signature
    ) internal view returns (bool) {
        require(groupPublicKey.length == 256, "Invalid group key length");
        require(signature.length == 128, "Invalid signature length");

        // 1) Map message hash (interpreted as Fp element) to a G1 point via precompile
        bytes memory hG1 = _mapMessageToG1(messageHash);

        // 2) Negate mapped G1 point for product-of-pairings check
        bytes memory negHG1 = _negateG1(hG1);

        // 3) Build input for pairing check:
        //    e(signature, G2) * e(-H(m), pubkey) == 1
        bytes memory pairingInput = abi.encodePacked(
            signature,            // 128
            G2_GENERATOR,         // 256
            negHG1,               // 128
            groupPublicKey        // 256
        );

        // 4) Pairing precompile returns true if product equals 1
        (bool success, bytes memory result) = BLS12_PAIRING.staticcall(pairingInput);
        return success && result.length == 32 && abi.decode(result, (bool));
    }

    /**
     * @dev Map a 32-byte message hash (as field element) to a G1 point using EIP-2537 MAP_FP_TO_G1
     *      Input must be a 64-byte big-endian field element; we left-pad the 32-byte hash.
     *      Returns 128-byte uncompressed G1 point (x||y), big-endian coords (64-bytes each).
     */
    function _mapMessageToG1(bytes32 messageHash) internal view returns (bytes memory) {
        bytes memory fp = new bytes(64);
        // Left-pad 32 zero bytes, then place messageHash as big-endian
        assembly {
            mstore(add(fp, 0x40), messageHash)
        }
        (bool ok, bytes memory out) = BLS12_MAP_FP_TO_G1.staticcall(fp);
        require(ok && out.length == 128, "MAP_FP_TO_G1 failed");
        return out;
    }

    /**
     * @dev Negate a G1 point (x, y) by computing (x, p - y) modulo field p.
     *      Expects/unpacks 128-byte uncompressed G1 encoding (x||y), 64-byte big-endian coords.
     */
    function _negateG1(bytes memory g1) internal pure returns (bytes memory) {
        require(g1.length == 128, "Invalid G1 length");
        bytes memory out = new bytes(128);
        // Copy x as-is
        for (uint256 i = 0; i < 64; i++) {
            out[i] = g1[i];
        }
        // Compute y' = p - y (big-endian, byte-by-byte with borrow)
        uint8 borrow = 0;
        for (uint256 i = 0; i < 64; i++) {
            uint256 idx = 63 - i;
            uint8 pi = uint8(FP_MODULUS[idx]);
            uint8 yi = uint8(g1[64 + idx]);
            uint16 subtrahend = uint16(yi) + uint16(borrow);
            if (pi >= subtrahend) {
                out[64 + idx] = bytes1(uint8(pi - subtrahend));
                borrow = 0;
            } else {
                out[64 + idx] = bytes1(uint8(uint16(pi) + 256 - subtrahend));
                borrow = 1;
            }
        }
        // If borrow == 1 here, input y >= p, which should not happen for valid points.
        return out;
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
        require(validationSignature.length == 128, "Invalid validation signature length");

        // Compute validation message hash following the format:
        // abi.encodePacked(previous_epoch_id, chain_id, data[0], data[1], data[2])
        // where data[0], data[1], data[2] are the 3 parts of the new group public key
        
        // Use GONKA chain id (source chain) for transition binding
        bytes32 chainId = GONKA_CHAIN_ID;
        
        // Encode message: abi.encodePacked(previousEpochId, chainId, part0..part7)
        bytes memory encodedMessage = abi.encodePacked(
            previousEpochId,        // 8 bytes
            chainId,                // 32 bytes
            newGroupKey.part0,      // 32 bytes - direct access, no intermediate variables
            newGroupKey.part1,      // 32 bytes
            newGroupKey.part2,      // 32 bytes
            newGroupKey.part3,      // 32 bytes
            newGroupKey.part4,      // 32 bytes
            newGroupKey.part5,      // 32 bytes
            newGroupKey.part6,      // 32 bytes
            newGroupKey.part7       // 32 bytes
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