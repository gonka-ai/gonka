package keeper

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"cosmossdk.io/math"
	"github.com/productscience/inference/x/inference/types"
	"golang.org/x/crypto/sha3"
)

// Bridge signature data helper functions shared between mint and withdrawal operations

// keccak256Hash computes Ethereum-compatible keccak256 hash and returns a fixed 32-byte array
func keccak256Hash(data []byte) [32]byte {
	hash := sha3.NewLegacyKeccak256()
	hash.Write(data)
	var result [32]byte
	copy(result[:], hash.Sum(nil))
	return result
}

// ethereumAddressToBytes converts an Ethereum hex address string to 20 bytes
// Enforces strict validation: strips 0x if present, requires exactly 40 hex characters,
// decodes with hex.DecodeString, and returns an error on any length/hex failure.
func ethereumAddressToBytes(address string) ([]byte, error) {
	// Remove 0x or 0X prefix if present
	addr := address
	if len(addr) >= 2 && (addr[:2] == "0x" || addr[:2] == "0X") {
		addr = addr[2:]
	}

	// Must be exactly 40 hex characters (20 bytes)
	if len(addr) != 40 {
		return nil, fmt.Errorf("invalid ethereum address length: expected 40 hex characters, got %d", len(addr))
	}

	// Convert hex string to 20 bytes using encoding/hex
	addrBytes, err := hex.DecodeString(addr)
	if err != nil {
		return nil, fmt.Errorf("invalid ethereum address hex format: %v", err)
	}

	return addrBytes, nil
}

// chainIdToBytes32 converts a numeric chain ID string to bytes32 format (uint256)
func chainIdToBytes32(chainId string) ([]byte, error) {
	chainIdBytes := make([]byte, 32)
	chainIdInt, ok := math.NewIntFromString(chainId)
	if !ok {
		return nil, fmt.Errorf("invalid chain ID format: %s", chainId)
	}
	if chainIdInt.IsNegative() {
		return nil, fmt.Errorf("chain ID cannot be negative: %s", chainId)
	}
	bigInt := chainIdInt.BigInt()
	if bigInt.BitLen() > 256 {
		return nil, fmt.Errorf("chain ID exceeds 256 bits: %s", chainId)
	}
	bigInt.FillBytes(chainIdBytes) // Big endian format
	return chainIdBytes, nil
}

// amountToBytes32 converts an amount string to bytes32 format (uint256)
func amountToBytes32(amount string) ([]byte, error) {
	amountBytes := make([]byte, 32)
	amountInt, ok := math.NewIntFromString(amount)
	if !ok {
		return nil, fmt.Errorf("invalid amount format: %s", amount)
	}
	if amountInt.IsNegative() {
		return nil, fmt.Errorf("amount cannot be negative: %s", amount)
	}
	bigInt := amountInt.BigInt()
	if bigInt.BitLen() > 256 {
		return nil, fmt.Errorf("amount exceeds 256 bits: %s", amount)
	}
	bigInt.FillBytes(amountBytes) // Big endian format
	return amountBytes, nil
}

// generateSecureBridgeTransactionKey creates a content-based key for bridge transactions
// This ensures validators can only vote on identical transaction data
// Format: chainId_blockNumber_contentHash (keeps block number for efficient cleanup)
func generateSecureBridgeTransactionKey(tx *types.BridgeTransaction) string {
	// Hash all the critical transaction data to ensure content integrity
	contentData := fmt.Sprintf(
		"%s|%s|%s|%s|%s|%s|%s",
		tx.ChainId,
		tx.BlockNumber,
		tx.ReceiptIndex,
		tx.ContractAddress,
		tx.OwnerAddress,
		tx.Amount,
		tx.ReceiptsRoot,
	)

	contentHash := sha256.Sum256([]byte(contentData))

	// Include block number in key for efficient cleanup, plus content hash for security
	// Format: chainId_blockNumber_contentHash
	return fmt.Sprintf("%s_%s_%x", tx.ChainId, tx.BlockNumber, contentHash[:12]) // Use first 12 bytes of hash
}

// bridgeTransactionsEqual compares all critical fields of two bridge transactions
func bridgeTransactionsEqual(tx1, tx2 *types.BridgeTransaction) bool {
	return tx1.ChainId == tx2.ChainId &&
		tx1.BlockNumber == tx2.BlockNumber &&
		tx1.ReceiptIndex == tx2.ReceiptIndex &&
		tx1.ContractAddress == tx2.ContractAddress &&
		tx1.OwnerAddress == tx2.OwnerAddress &&
		tx1.Amount == tx2.Amount &&
		tx1.ReceiptsRoot == tx2.ReceiptsRoot
}
