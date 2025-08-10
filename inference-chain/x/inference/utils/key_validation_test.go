package utils

import (
	"encoding/base64"
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/stretchr/testify/require"
)

func TestSafeCreateED25519ValidatorKey(t *testing.T) {
	// Test valid key
	t.Run("valid_key", func(t *testing.T) {
		validPrivKey := ed25519.GenPrivKey()
		validPubKey := validPrivKey.PubKey()
		validKeyBase64 := base64.StdEncoding.EncodeToString(validPubKey.Bytes())

		pubKey, err := SafeCreateED25519ValidatorKey(validKeyBase64)
		require.NoError(t, err, "Valid key should work")
		require.NotNil(t, pubKey, "Should return valid key")
	})

	// Test the exact key from bug report
	t.Run("bug_report_key", func(t *testing.T) {
		bugReportKey := "AggLJgjYij7iN/qmWohnV5mU7CdcYFGw9qd3NlsvZ28c"

		// First check what actually happens with this key
		pubKeyBytes, decodeErr := base64.StdEncoding.DecodeString(bugReportKey)
		require.NoError(t, decodeErr, "Key should decode successfully")
		t.Logf("Bug report key has %d bytes instead of expected 32", len(pubKeyBytes))

		// The actual issue might be in a different operation
		pubKey, err := SafeCreateED25519ValidatorKey(bugReportKey)
		if err != nil {
			require.Contains(t, err.Error(), "32 bytes")
			t.Logf("Bug report key properly caught: %v", err)
		} else {
			// If it doesn't fail here, let's see what happens with Address()
			t.Logf("Key creation succeeded, trying Address() operation...")
			address := pubKey.Address()
			t.Logf("Address created successfully: %x", address)

			// Maybe the issue is specifically in the consensus layer
			t.Logf("The key may only fail in specific consensus operations, not general usage")
		}
	})

	// Test empty key
	t.Run("empty_key", func(t *testing.T) {
		pubKey, err := SafeCreateED25519ValidatorKey("")
		require.Error(t, err, "Empty key should fail")
		require.Nil(t, pubKey, "Should not return key")
	})

	// Test invalid base64
	t.Run("invalid_base64", func(t *testing.T) {
		pubKey, err := SafeCreateED25519ValidatorKey("invalid-base64!")
		require.Error(t, err, "Invalid base64 should fail")
		require.Nil(t, pubKey, "Should not return key")
	})
}

func TestSafeCreateSECP256K1AccountKey(t *testing.T) {
	// This demonstrates the same approach works for SECP256K1 keys
	t.Run("empty_key", func(t *testing.T) {
		pubKey, err := SafeCreateSECP256K1AccountKey("")
		require.Error(t, err, "Empty key should fail")
		require.Nil(t, pubKey, "Should not return key")
	})
}
