package utils_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"common/utils"
)

// Test vector from decentralized-api/apiconfig/accounts_test.go
const (
	testPubKeyBase64  = "Au5ZQav3E36PZpGta2xUa8r9xEEo9Biph3fG5i3qaeSG"
	testPubKeyHex     = "02ee5941abf7137e8f6691ad6b6c546bcafdc44128f418a98777c6e62dea69e486"
	testExpectedAddr  = "gonka1jwrv4q8hpxc354pr87pt0pkulaep67e9s4z0ym"
	testAddressPrefix = "gonka"
)

func TestPubKeyBase64ToAddress(t *testing.T) {
	addr, err := utils.PubKeyBase64ToAddress(testPubKeyBase64)
	require.NoError(t, err)
	assert.Equal(t, testExpectedAddr, addr)
}

func TestPubKeyHexToAddress(t *testing.T) {
	addr, err := utils.PubKeyHexToAddress(testPubKeyHex)
	require.NoError(t, err)
	assert.Equal(t, testExpectedAddr, addr)
}

func TestPubKeyToAddress_Base64(t *testing.T) {
	addr, err := utils.PubKeyToAddress(testPubKeyBase64)
	require.NoError(t, err)
	assert.Equal(t, testExpectedAddr, addr)
}

func TestPubKeyToAddress_Hex(t *testing.T) {
	addr, err := utils.PubKeyToAddress(testPubKeyHex)
	require.NoError(t, err)
	assert.Equal(t, testExpectedAddr, addr)
}

func TestPubKeyToAddress_Invalid(t *testing.T) {
	_, err := utils.PubKeyToAddress("not-valid!!!")
	require.Error(t, err)
}

func TestPubKeyBase64ToAddress_Invalid(t *testing.T) {
	_, err := utils.PubKeyBase64ToAddress("not-base64!!!")
	require.Error(t, err)
}

func TestPubKeyHexToAddress_Invalid(t *testing.T) {
	_, err := utils.PubKeyHexToAddress("not-hex!!!")
	require.Error(t, err)
}

// TestValidatorKeyToHexAddress_MatchesPubKeyToAddress3 verifies that
// ValidatorKeyToHexAddress produces output identical to the removed
// decentralized-api pubKeyToAddress3 function:
//
//	base64.StdEncoding.DecodeString → tmhash.SumTruncated → strings.ToUpper(hex.EncodeToString)
func TestValidatorKeyToHexAddress_MatchesPubKeyToAddress3(t *testing.T) {
	const expected = "CF90516F2CD1EA1AA8132A6B1BC451C726EEE898"

	got, err := utils.ValidatorKeyToHexAddress(testPubKeyBase64)
	require.NoError(t, err)
	assert.Equal(t, expected, got)
}

func TestValidatorKeyToHexAddress_InvalidBase64(t *testing.T) {
	_, err := utils.ValidatorKeyToHexAddress("not-base64!!!")
	require.Error(t, err)
}
