package keeper

import (
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEthereumAddressToBytes(t *testing.T) {
	// Valid address with 0x prefix
	validAddr := "0x1234567890123456789012345678901234567890"
	expectedBytes, _ := hex.DecodeString("1234567890123456789012345678901234567890")

	// 1. Valid address with prefix
	bz, err := ethereumAddressToBytes(validAddr)
	require.NoError(t, err)
	require.Equal(t, expectedBytes, bz)

	// 2. Valid address with uppercase 0X prefix
	bz, err = ethereumAddressToBytes("0X" + validAddr[2:])
	require.NoError(t, err)
	require.Equal(t, expectedBytes, bz)

	// 3. Valid address without prefix
	bz, err = ethereumAddressToBytes(validAddr[2:])
	require.NoError(t, err)
	require.Equal(t, expectedBytes, bz)

	// 3. Invalid address length (too short)
	_, err = ethereumAddressToBytes("0x123456")
	require.ErrorContains(t, err, "invalid ethereum address length")

	// 4. Invalid address length (too long)
	_, err = ethereumAddressToBytes("0x123456789012345678901234567890123456789012")
	require.ErrorContains(t, err, "invalid ethereum address length")

	// 5. Invalid hex characters
	_, err = ethereumAddressToBytes("0x12345678901234567890123456789012345678GG")
	require.ErrorContains(t, err, "invalid ethereum address hex format")
}

func TestChainIdToBytes32(t *testing.T) {
	// 1. Valid chain ID
	bz, err := chainIdToBytes32("1")
	require.NoError(t, err)
	require.Len(t, bz, 32)
	require.Equal(t, byte(1), bz[31])

	// 2. Invalid chain ID (empty string)
	_, err = chainIdToBytes32("")
	require.ErrorContains(t, err, "invalid chain ID format")

	// 3. Invalid chain ID (non-numeric string)
	_, err = chainIdToBytes32("eth")
	require.ErrorContains(t, err, "invalid chain ID format")

	// 4. Invalid chain ID (negative)
	_, err = chainIdToBytes32("-1")
	require.ErrorContains(t, err, "chain ID cannot be negative")

	// 5. Invalid chain ID (> 256 bits)
	// Create string for 2^256
	tooBig := new(big.Int).Lsh(big.NewInt(1), 256)
	_, err = chainIdToBytes32(tooBig.String())
	require.Error(t, err) // Either invalid format or exceeds 256 bits depending on math.Int bounds
}

func TestAmountToBytes32(t *testing.T) {
	// 1. Valid amount
	bz, err := amountToBytes32("1000")
	require.NoError(t, err)
	require.Len(t, bz, 32)
	// 1000 = 0x3E8 (so bz[30] = 0x03, bz[31] = 0xE8)
	require.Equal(t, byte(0x03), bz[30])
	require.Equal(t, byte(0xE8), bz[31])

	// 2. Invalid amount (empty string)
	_, err = amountToBytes32("")
	require.ErrorContains(t, err, "invalid amount format")

	// 3. Invalid amount (non-numeric string)
	_, err = amountToBytes32("abc")
	require.ErrorContains(t, err, "invalid amount format")

	// 4. Invalid amount (negative)
	_, err = amountToBytes32("-100")
	require.ErrorContains(t, err, "amount cannot be negative")

	// 5. Invalid amount (> 256 bits)
	tooBig := new(big.Int).Lsh(big.NewInt(1), 256)
	_, err = amountToBytes32(tooBig.String())
	require.Error(t, err) // Either invalid format or exceeds 256 bits depending on math.Int bounds
}
