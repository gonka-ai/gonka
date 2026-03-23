package keeper

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEthereumAddressToBytes_ValidAddress(t *testing.T) {
	// Standard 40-char hex address without prefix
	addr := "d8dA6BF26964aF9D7eEd9e03E53415D37aA96045"
	result, err := ethereumAddressToBytes(addr)
	require.NoError(t, err)
	require.Len(t, result, 20)

	expected, _ := hex.DecodeString(addr)
	require.Equal(t, expected, result)
}

func TestEthereumAddressToBytes_WithPrefix(t *testing.T) {
	addr := "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045"
	result, err := ethereumAddressToBytes(addr)
	require.NoError(t, err)
	require.Len(t, result, 20)
}

func TestEthereumAddressToBytes_EmptyAddress(t *testing.T) {
	_, err := ethereumAddressToBytes("")
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty ethereum address")
}

func TestEthereumAddressToBytes_EmptyAfterPrefix(t *testing.T) {
	_, err := ethereumAddressToBytes("0x")
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty ethereum address")
}

func TestEthereumAddressToBytes_InvalidHex(t *testing.T) {
	// 40 chars but contains non-hex character 'Z'
	_, err := ethereumAddressToBytes("ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid hex")
}

func TestEthereumAddressToBytes_TooShort(t *testing.T) {
	_, err := ethereumAddressToBytes("0xdead")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid ethereum address length")
}

func TestEthereumAddressToBytes_TooLong(t *testing.T) {
	_, err := ethereumAddressToBytes("0xd8dA6BF26964aF9D7eEd9e03E53415D37aA960450000")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid ethereum address length")
}

func TestEthereumAddressToBytes_ZeroAddress(t *testing.T) {
	// Valid zero address (40 zeros)
	result, err := ethereumAddressToBytes("0x0000000000000000000000000000000000000000")
	require.NoError(t, err)
	require.Equal(t, make([]byte, 20), result)
}
