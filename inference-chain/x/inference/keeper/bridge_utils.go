package keeper

import (
	"crypto/sha256"
	"fmt"

	"cosmossdk.io/math"
	"github.com/productscience/inference/x/inference/types"
)

// Bridge signature data helper functions shared between mint and withdrawal operations

// ethereumAddressToBytes converts an Ethereum hex address string to 20 bytes
// Handles addresses with or without 0x prefix and preserves case (matching Solidity abi.encodePacked behavior)
func ethereumAddressToBytes(address string) []byte {
	// Remove 0x prefix if present
	addr := address
	if len(addr) >= 2 && addr[:2] == "0x" {
		addr = addr[2:]
	}

	// Convert hex string to 20 bytes
	addrBytes := make([]byte, 20)
	for i := 0; i < 40 && i < len(addr); i += 2 {
		if i+1 < len(addr) {
			high := hexCharToByte(addr[i])
			low := hexCharToByte(addr[i+1])
			addrBytes[i/2] = (high << 4) | low
		}
	}

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

// hexCharToByte converts a hex character to its byte value
func hexCharToByte(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	default:
		return 0
	}
}

// Bridge transaction content hashing for secure validation

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
