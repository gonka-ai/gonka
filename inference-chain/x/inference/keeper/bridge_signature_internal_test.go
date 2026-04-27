package keeper

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
)

// Test exactly what the user requested: exact signed field ordering including bridge contract address.
func TestPrepareBridgeMintSignatureData_Ordering(t *testing.T) {
	k := msgServer{} // we only need the receiver for the helper

	chainId := "1" // ethereum numeric string
	recipient := "0x1111111111111111111111111111111111111111"
	bridgeContract := "0x2222222222222222222222222222222222222222"
	amount := "100"

	payload, err := k.prepareBridgeMintSignatureData(chainId, recipient, bridgeContract, amount)
	require.NoError(t, err)

	// Expected exact ordering of the 5 elements:
	// 1. EthereumChainId (32 bytes)
	// 2. MINT_OPERATION hash (32 bytes)
	// 3. Recipient (20 bytes)
	// 4. BridgeContract (20 bytes)
	// 5. Amount (32 bytes)
	require.Len(t, payload, 5)

	// Check lengths of the packed fields
	require.Len(t, payload[0], 32, "ChainID must be 32 bytes")
	require.Len(t, payload[1], 32, "MintOperationHash must be 32 bytes")
	require.Len(t, payload[2], 20, "Recipient must be 20 bytes")
	require.Len(t, payload[3], 20, "BridgeContract must be 20 bytes")
	require.Len(t, payload[4], 32, "Amount must be 32 bytes")

	// Ensure the bridge address specifically was packed at index 3
	expectedBridgeBytes, _ := hex.DecodeString("2222222222222222222222222222222222222222")
	require.Equal(t, expectedBridgeBytes, payload[3])
}

func TestPrepareBridgeWithdrawalSignatureData_Ordering(t *testing.T) {
	k := msgServer{}

	chainId := "1"
	recipient := "0x1111111111111111111111111111111111111111"
	bridgeContract := "0x2222222222222222222222222222222222222222"
	tokenContract := "0x3333333333333333333333333333333333333333"
	amount := "500"

	payload, err := k.prepareBridgeWithdrawalSignatureData(chainId, recipient, bridgeContract, tokenContract, amount)
	require.NoError(t, err)

	// Expected exact ordering of the 6 elements:
	// 1. EthereumChainId (32 bytes)
	// 2. WITHDRAW_OPERATION hash (32 bytes)
	// 3. Recipient (20 bytes)
	// 4. BridgeContract (20 bytes)
	// 5. TokenContract (20 bytes)
	// 6. Amount (32 bytes)
	require.Len(t, payload, 6)

	require.Len(t, payload[0], 32)
	require.Len(t, payload[1], 32)
	require.Len(t, payload[2], 20)
	require.Len(t, payload[3], 20)
	require.Len(t, payload[4], 20)
	require.Len(t, payload[5], 32)

	// Ensure bridge address is exactly at index 3
	expectedBridgeBytes, _ := hex.DecodeString("2222222222222222222222222222222222222222")
	require.Equal(t, expectedBridgeBytes, payload[3])
}
