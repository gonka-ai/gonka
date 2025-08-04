package apiconfig

import (
	"encoding/base64"
	"testing"

	"github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/ignite/cli/v28/ignite/pkg/cosmosaccount"
	"github.com/stretchr/testify/require"
)

const (
	// Test data from your previous debugging sessions
	testPubKeyStr     = "Au5ZQav3E36PZpGta2xUa8r9xEEo9Biph3fG5i3qaeSG"
	testAddressPrefix = "gonka"
	testExpectedAddr  = "gonka1jwrv4q8hpxc354pr87pt0pkulaep67e9s4z0ym"
)

// TestApiAccount_AccountAddress verifies that the AccountAddress method correctly
// converts the public key into the expected bech32 address string.
func TestApiAccount_AccountAddress(t *testing.T) {
	pubKeyBytes, err := base64.StdEncoding.DecodeString(testPubKeyStr)
	require.NoError(t, err, "Failed to decode test public key string")

	pubKey := &secp256k1.PubKey{Key: pubKeyBytes}
	apiAccount := &ApiAccount{
		AccountKey:    pubKey,
		AddressPrefix: testAddressPrefix,
	}

	// Act
	actualAddress, err := apiAccount.AccountAddressBech32()
	require.NoError(t, err)

	// Assert
	require.Equal(t, testExpectedAddr, actualAddress)
}

// TestApiAccount_IsSignerTheMainAccount verifies the logic for comparing
// the signer's public key with the main account's public key.
func TestApiAccount_IsSignerTheMainAccount(t *testing.T) {
	mainPubKeyBytes, err := base64.StdEncoding.DecodeString(testPubKeyStr)
	require.NoError(t, err)
	mainPubKey := &secp256k1.PubKey{Key: mainPubKeyBytes}

	// Create a different public key for the mismatch case
	otherPubKeyBytes := make([]byte, 33)
	otherPubKeyBytes[0] = 0x02 // A different valid compressed key
	otherPubKey := &secp256k1.PubKey{Key: otherPubKeyBytes}

	// --- Test Case 1: The signer key IS the main account key ---
	t.Run("should return true when keys are the same", func(t *testing.T) {
		// Arrange
		// In Ignite v28, the account record is a `keyring.Record`, and its PubKey is a `*types.Any`.
		// We must convert our `cryptotypes.PubKey` to a `*types.Any` to create the mock.
		mainPubKeyAny, err := types.NewAnyWithValue(mainPubKey)
		require.NoError(t, err)

		signerAccountWithSameKey := &cosmosaccount.Account{
			Record: &keyring.Record{
				PubKey: mainPubKeyAny, // Use the same key, wrapped in Any
			},
		}
		apiAccount := &ApiAccount{
			AccountKey:    mainPubKey,
			SignerAccount: signerAccountWithSameKey,
		}

		require.True(t, apiAccount.IsSignerTheMainAccount(), "Expected keys to be equal")
	})

	// --- Test Case 2: The signer key is NOT the main account key ---
	t.Run("should return false when keys are different", func(t *testing.T) {
		// Arrange
		otherPubKeyAny, err := types.NewAnyWithValue(otherPubKey)
		require.NoError(t, err)

		signerAccountWithDifferentKey := &cosmosaccount.Account{
			Record: &keyring.Record{
				PubKey: otherPubKeyAny, // Use the different key, wrapped in Any
			},
		}
		apiAccount := &ApiAccount{
			AccountKey:    mainPubKey,
			SignerAccount: signerAccountWithDifferentKey,
		}

		// Act & Assert
		// NOTE: See comment in the test case above.
		require.False(t, apiAccount.IsSignerTheMainAccount(), "Expected keys to be different")
	})
}
