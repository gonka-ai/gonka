package keeper_test

import (
	"encoding/base64"
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/types"
	"github.com/productscience/inference/x/inference/utils"
	"github.com/stretchr/testify/require"
)

func TestMsgServer_SubmitNewParticipant(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)

	// Create test secp256k1 keys for ValidatorKey and WorkerKey
	validatorPrivKey := secp256k1.GenPrivKey()
	validatorPubKey := validatorPrivKey.PubKey()
	validatorKeyString := base64.StdEncoding.EncodeToString(validatorPubKey.Bytes())

	workerPrivKey := secp256k1.GenPrivKey()
	workerPubKey := workerPrivKey.PubKey()
	workerKeyString := base64.StdEncoding.EncodeToString(workerPubKey.Bytes())

	_, err := ms.SubmitNewParticipant(ctx, &types.MsgSubmitNewParticipant{
		Creator:      testutil.Executor,
		Url:          "url",
		ValidatorKey: validatorKeyString,
		WorkerKey:    workerKeyString,
	})
	require.NoError(t, err)

	savedParticipant, found := k.GetParticipant(ctx, testutil.Executor)
	require.True(t, found)
	ctx2 := sdk.UnwrapSDKContext(ctx)
	require.Equal(t, types.Participant{
		Index:             testutil.Executor,
		Address:           testutil.Executor,
		Weight:            -1,
		JoinTime:          ctx2.BlockTime().UnixMilli(),
		JoinHeight:        ctx2.BlockHeight(),
		LastInferenceTime: 0,
		InferenceUrl:      "url",
		Status:            types.ParticipantStatus_ACTIVE,
		ValidatorKey:      validatorKeyString, // Verify secp256k1 public key is stored
		WorkerPublicKey:   workerKeyString,    // Verify worker key is stored
		CurrentEpochStats: types.NewCurrentEpochStats(),
	}, savedParticipant)
}

func TestMsgServer_SubmitNewParticipant_WithEmptyKeys(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)

	_, err := ms.SubmitNewParticipant(ctx, &types.MsgSubmitNewParticipant{
		Creator:      testutil.Executor,
		Url:          "url",
		ValidatorKey: "", // Test with empty validator key
		WorkerKey:    "", // Test with empty worker key
	})
	require.NoError(t, err)

	savedParticipant, found := k.GetParticipant(ctx, testutil.Executor)
	require.True(t, found)
	require.Equal(t, "", savedParticipant.ValidatorKey) // Should handle empty key gracefully
	require.Equal(t, "", savedParticipant.WorkerPublicKey)
}

func TestMsgServer_SubmitNewParticipant_ValidateSecp256k1Key(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)

	// Create a valid secp256k1 key
	privKey := secp256k1.GenPrivKey()
	pubKey := privKey.PubKey()
	validatorKeyString := base64.StdEncoding.EncodeToString(pubKey.Bytes())

	_, err := ms.SubmitNewParticipant(ctx, &types.MsgSubmitNewParticipant{
		Creator:      testutil.Executor,
		Url:          "url",
		ValidatorKey: validatorKeyString,
		WorkerKey:    "worker-key",
	})
	require.NoError(t, err)

	savedParticipant, found := k.GetParticipant(ctx, testutil.Executor)
	require.True(t, found)

	// Verify the key was stored correctly
	require.Equal(t, validatorKeyString, savedParticipant.ValidatorKey)

	// Decode and verify it's a valid secp256k1 key
	decodedBytes, err := base64.StdEncoding.DecodeString(savedParticipant.ValidatorKey)
	require.NoError(t, err)
	require.Equal(t, 33, len(decodedBytes)) // secp256k1 compressed public key is 33 bytes

	// Verify we can reconstruct the public key
	reconstructedPubKey := &secp256k1.PubKey{Key: decodedBytes}
	require.Equal(t, pubKey.Bytes(), reconstructedPubKey.Bytes())
}

// TestMsgServer_SubmitNewParticipant_InvalidED25519Keys reproduces the consensus failure
// described in the bug report where invalid ED25519 validator keys cause
// "pubkey is incorrect size" error during consensus processing
func TestMsgServer_SubmitNewParticipant_InvalidED25519Keys(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)

	// Generate a valid ED25519 key for comparison
	validPrivKey := ed25519.GenPrivKey()
	validPubKey := validPrivKey.PubKey()
	validKeyBytes := validPubKey.Bytes()
	validKeyBase64 := base64.StdEncoding.EncodeToString(validKeyBytes)

	testCases := []struct {
		name         string
		validatorKey string
		expectError  bool
		description  string
	}{
		{
			name:         "valid_ed25519_key",
			validatorKey: validKeyBase64,
			expectError:  false,
			description:  "Valid 32-byte ED25519 key should work",
		},
		{
			name:         "invalid_key_from_bug_report",
			validatorKey: "AggLJgjYij7iN/qmWohnV5mU7CdcYFGw9qd3NlsvZ28c",
			expectError:  true,
			description:  "Exact invalid key from bug report that caused consensus failure",
		},
		{
			name:         "wrong_size_too_short",
			validatorKey: base64.StdEncoding.EncodeToString([]byte("short")),
			expectError:  true,
			description:  "Key with wrong size (too short)",
		},
		{
			name:         "wrong_size_too_long",
			validatorKey: base64.StdEncoding.EncodeToString(make([]byte, 64)), // 64 bytes instead of 32
			expectError:  true,
			description:  "Key with wrong size (too long)",
		},
		{
			name:         "invalid_base64",
			validatorKey: "invalid-base64-string!!!",
			expectError:  true,
			description:  "Invalid base64 encoding",
		},
		{
			name:         "null_bytes",
			validatorKey: base64.StdEncoding.EncodeToString(make([]byte, 32)), // All zeros
			expectError:  false,                                               // This should pass validation but might not be a good key
			description:  "All zero bytes (technically valid but poor key)",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Test our validation utility
			_, err := utils.SafeCreateED25519ValidatorKey(tc.validatorKey)
			if tc.expectError {
				require.Error(t, err, "Validation should fail for: %s", tc.description)
				t.Logf("Expected validation error: %v", err)
			} else {
				require.NoError(t, err, "Validation should pass for: %s", tc.description)
			}

			// Test submitting participant with this key
			_, submitErr := ms.SubmitNewParticipant(ctx, &types.MsgSubmitNewParticipant{
				Creator:      testutil.Executor,
				Url:          "http://test.url",
				ValidatorKey: tc.validatorKey,
				WorkerKey:    validKeyBase64, // Use valid worker key
			})

			// Currently, submission should succeed because there's no validation yet
			// This test documents the current behavior before we add validation
			require.NoError(t, submitErr, "Submission currently succeeds without validation")

			// Check that participant was stored
			savedParticipant, found := k.GetParticipant(ctx, testutil.Executor)
			require.True(t, found, "Participant should be stored")
			require.Equal(t, tc.validatorKey, savedParticipant.ValidatorKey, "Validator key should be stored as-is")
		})
	}
}

// TestReproduceConsensusFailure simulates the exact scenario from the bug report
// where an invalid ED25519 key causes consensus failure during epoch processing
func TestReproduceConsensusFailure(t *testing.T) {
	// This test reproduces the exact key from the bug report that caused consensus failure
	invalidKeyFromBugReport := "AggLJgjYij7iN/qmWohnV5mU7CdcYFGw9qd3NlsvZ28c"

	// First, verify that our validation detects this invalid key
	_, err := utils.SafeCreateED25519ValidatorKey(invalidKeyFromBugReport)
	require.Error(t, err, "Validation should detect the invalid key from bug report")
	require.Contains(t, err.Error(), "ED25519 validator key must be exactly 32 bytes")

	// Decode the key to check its actual size (this is what happens in epoch_group.go)
	pubKeyBytes, decodeErr := base64.StdEncoding.DecodeString(invalidKeyFromBugReport)
	require.NoError(t, decodeErr, "Key should decode successfully")
	require.NotEqual(t, 32, len(pubKeyBytes), "Key should not be 32 bytes")
	t.Logf("Invalid key has %d bytes instead of required 32 bytes", len(pubKeyBytes))

	// This is where the consensus failure occurs in epoch_group.go line 366:
	// pubKey := ed25519.PubKey{Key: pubKeyBytes}
	// The ed25519 library expects exactly 32 bytes

	// Attempting to create the pubkey should panic or fail
	defer func() {
		if r := recover(); r != nil {
			t.Logf("Creating ED25519 key with invalid size panicked as expected: %v", r)
		}
	}()

	// This would cause the consensus failure described in the bug report
	pubKey := &ed25519.PubKey{Key: pubKeyBytes}
	address := pubKey.Address() // This operation might fail with wrong size key
	t.Logf("If we got here, key created address: %v", address)
}
