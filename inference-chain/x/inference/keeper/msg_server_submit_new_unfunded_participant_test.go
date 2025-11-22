package keeper_test

import (
	"encoding/base64"
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestMsgServer_SubmitNewUnfundedParticipant(t *testing.T) {
	k, ms, ctx, mocks := setupKeeperWithMocks(t)

	// Create a test address and public key
	privKey := secp256k1.GenPrivKey()
	pubKey := privKey.PubKey()
	pubKeyBytes := pubKey.Bytes()
	encodedPubKey := base64.StdEncoding.EncodeToString(pubKeyBytes)
	testAddress := sdk.AccAddress(pubKey.Address()).String()

	// Setup expectations for account keeper
	mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	mocks.AccountKeeper.EXPECT().NewAccountWithAddress(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx sdk.Context, addr sdk.AccAddress) sdk.AccountI {
			// Return a BaseAccount that can handle SetPubKey
			return &authtypes.BaseAccount{Address: addr.String()}
		}).AnyTimes()
	mocks.AccountKeeper.EXPECT().SetAccount(gomock.Any(), gomock.Any()).AnyTimes()

	// Setup expectations for bank keeper (for funding)
	mocks.BankKeeper.EXPECT().MintCoins(gomock.Any(), types.ModuleName, gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	// Call the function under test
	_, err := ms.SubmitNewUnfundedParticipant(ctx, &types.MsgSubmitNewUnfundedParticipant{
		Creator: testutil.Creator,
		Address: testAddress,
		PubKey:  encodedPubKey,
		Url:     "", // Consumer only
	})
	require.NoError(t, err)

	// Verify participant was created
	savedParticipant, found := k.GetParticipant(ctx, testAddress)
	require.True(t, found)
	ctx2 := sdk.UnwrapSDKContext(ctx)
	require.Equal(t, types.Participant{
		Index:             testAddress,
		Address:           testAddress,
		Weight:            -1,
		JoinTime:          ctx2.BlockTime().UnixMilli(),
		JoinHeight:        ctx2.BlockHeight(),
		LastInferenceTime: 0,
		InferenceUrl:      "",
		Status:            types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: types.NewCurrentEpochStats(),
	}, savedParticipant)
}

func TestMsgServer_SubmitNewUnfundedParticipant_AccountAlreadyExists(t *testing.T) {
	_, ms, ctx, mocks := setupKeeperWithMocks(t)

	// Create a test address and public key
	testAddress := testutil.Requester
	privKey := secp256k1.GenPrivKey()
	pubKey := privKey.PubKey()
	pubKeyBytes := pubKey.Bytes()
	encodedPubKey := base64.StdEncoding.EncodeToString(pubKeyBytes)

	// Setup expectations for account keeper - account already exists
	mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx sdk.Context, addr sdk.AccAddress) sdk.AccountI {
			// Return a non-nil value to simulate an existing account
			// The actual value doesn't matter as the function only checks if it's nil
			return &authtypes.BaseAccount{}
		}).AnyTimes()

	// Call the function under test
	_, err := ms.SubmitNewUnfundedParticipant(ctx, &types.MsgSubmitNewUnfundedParticipant{
		Creator: testutil.Creator,
		Address: testAddress,
		PubKey:  encodedPubKey,
		Url:     "url",
	})

	// Verify error is returned
	require.Error(t, err)
	require.Equal(t, types.ErrAccountAlreadyExists, err)
}

func TestMsgServer_SubmitNewUnfundedParticipant_WithInferenceUrl(t *testing.T) {
	k, ms, ctx, mocks := setupKeeperWithMocks(t)

	// Create a test address and public key
	privKey := secp256k1.GenPrivKey()
	pubKey := privKey.PubKey()
	pubKeyBytes := pubKey.Bytes()
	encodedPubKey := base64.StdEncoding.EncodeToString(pubKeyBytes)
	testAddress := sdk.AccAddress(pubKey.Address()).String()

	// Setup expectations for account keeper
	mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	mocks.AccountKeeper.EXPECT().NewAccountWithAddress(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx sdk.Context, addr sdk.AccAddress) sdk.AccountI {
			// Return a BaseAccount that can handle SetPubKey
			return &authtypes.BaseAccount{Address: addr.String()}
		}).AnyTimes()
	mocks.AccountKeeper.EXPECT().SetAccount(gomock.Any(), gomock.Any()).AnyTimes()

	// No funding expected for participants with inference URL

	// Call the function under test
	_, err := ms.SubmitNewUnfundedParticipant(ctx, &types.MsgSubmitNewUnfundedParticipant{
		Creator: testutil.Creator,
		Address: testAddress,
		PubKey:  encodedPubKey,
		Url:     "inference-url",
	})
	require.NoError(t, err)

	// Verify participant was created
	savedParticipant, found := k.GetParticipant(ctx, testAddress)
	require.True(t, found)
	ctx2 := sdk.UnwrapSDKContext(ctx)
	require.Equal(t, types.Participant{
		Index:             testAddress,
		Address:           testAddress,
		Weight:            -1,
		JoinTime:          ctx2.BlockTime().UnixMilli(),
		JoinHeight:        ctx2.BlockHeight(),
		LastInferenceTime: 0,
		InferenceUrl:      "inference-url",
		Status:            types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: types.NewCurrentEpochStats(),
	}, savedParticipant)
}
