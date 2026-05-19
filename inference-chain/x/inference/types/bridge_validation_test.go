package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecimalStringWithoutLeadingZeros(t *testing.T) {
	require.True(t, decimalStringWithoutLeadingZeros("0"))
	require.True(t, decimalStringWithoutLeadingZeros("1000"))
	require.False(t, decimalStringWithoutLeadingZeros("01000"))
	require.False(t, decimalStringWithoutLeadingZeros(""))
}

func TestIsValidReceiptsRootHex(t *testing.T) {
	valid := "0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	require.True(t, isValidReceiptsRootHex(valid))
	require.True(t, isValidReceiptsRootHex("0xABCDEF1234567890ABCDEF1234567890ABCDEF1234567890ABCDEF1234567890"))
	require.False(t, isValidReceiptsRootHex("0xabc"))
	require.False(t, isValidReceiptsRootHex("abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"))
}
