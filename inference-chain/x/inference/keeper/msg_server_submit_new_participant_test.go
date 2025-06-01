package keeper_test

import (
	"encoding/base64"
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
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
		Creator:      "creator",
		Url:          "url",
		ValidatorKey: validatorKeyString,
		WorkerKey:    workerKeyString,
	})
	require.NoError(t, err)

	savedParticipant, found := k.GetParticipant(ctx, "creator")
	require.True(t, found)
	ctx2 := sdk.UnwrapSDKContext(ctx)
	require.Equal(t, types.Participant{
		Index:             "creator",
		Address:           "creator",
		Weight:            -1,
		JoinTime:          ctx2.BlockTime().UnixMilli(),
		JoinHeight:        ctx2.BlockHeight(),
		LastInferenceTime: 0,
		InferenceUrl:      "url",
		Status:            types.ParticipantStatus_ACTIVE,
		ValidatorKey:      validatorKeyString, // Verify secp256k1 public key is stored
		WorkerPublicKey:   workerKeyString,    // Verify worker key is stored
		CurrentEpochStats: &types.CurrentEpochStats{},
	}, savedParticipant)
}

func TestMsgServer_SubmitNewParticipant_WithEmptyKeys(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)

	_, err := ms.SubmitNewParticipant(ctx, &types.MsgSubmitNewParticipant{
		Creator:      "creator",
		Url:          "url",
		ValidatorKey: "", // Test with empty validator key
		WorkerKey:    "", // Test with empty worker key
	})
	require.NoError(t, err)

	savedParticipant, found := k.GetParticipant(ctx, "creator")
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
		Creator:      "creator",
		Url:          "url",
		ValidatorKey: validatorKeyString,
		WorkerKey:    "worker-key",
	})
	require.NoError(t, err)

	savedParticipant, found := k.GetParticipant(ctx, "creator")
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
