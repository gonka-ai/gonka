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
// Handles addresses with or without 0x prefix and preserves case (matching Solidity abi.encodePacked behavior)
func ethereumAddressToBytes(address string) []byte {
	// Remove 0x prefix if present
	addr := address
	if len(addr) >= 2 && addr[:2] == "0x" {
		addr = addr[2:]
	}

	// Convert hex string to 20 bytes using encoding/hex
	// Truncate to at most 40 hex chars and ensure even length (ignore dangling nibble)
	maxLen := 40
	n := len(addr)
	if n > maxLen {
		n = maxLen
	}
	if n%2 == 1 {
		n--
	}

	addrBytes := make([]byte, 20)
	if n <= 0 {
		return addrBytes
	}
	decoded := make([]byte, n/2)
	if _, err := hex.Decode(decoded, []byte(addr[:n])); err != nil {
		return addrBytes
	}
	copy(addrBytes, decoded)
	return addrBytes
}

// chainIdToBytes32 converts a numeric chain ID string to bytes32 format (uint256)
func chainIdToBytes32(chainId string) []byte {
	chainIdBytes := make([]byte, 32)
	if chainIdInt, ok := math.NewIntFromString(chainId); ok {
		chainIdBigInt := chainIdInt.BigInt()
		chainIdBigInt.FillBytes(chainIdBytes) // Big endian format
	}
	return chainIdBytes
}

// amountToBytes32 converts an amount string to bytes32 format (uint256)
func amountToBytes32(amount string) []byte {
	amountBytes := make([]byte, 32)
	if amountInt, ok := math.NewIntFromString(amount); ok {
		amountBigInt := amountInt.BigInt()
		amountBigInt.FillBytes(amountBytes) // Big endian format
	}
	return amountBytes
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
