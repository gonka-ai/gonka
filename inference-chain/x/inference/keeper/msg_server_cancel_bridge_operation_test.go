package keeper_test

import (
	"bytes"
	"encoding/hex"
	"testing"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	blskeeper "github.com/productscience/inference/x/bls/keeper"
	blstypes "github.com/productscience/inference/x/bls/types"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"golang.org/x/crypto/sha3"
)

func TestMsgServer_CancelBridgeOperation_NotFound(t *testing.T) {
	_, ms, ctx, mocks := setupKeeperWithMocks(t)
	creatorAddr, err := sdk.AccAddressFromBech32(testutil.Creator)
	require.NoError(t, err)
	mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), creatorAddr).Return(true).Times(1)

	_, err = ms.CancelBridgeOperation(sdk.WrapSDKContext(ctx), &types.MsgCancelBridgeOperation{
		Creator:   testutil.Creator,
		RequestId: "missing_request",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "pending bridge operation not found")
}

func TestMsgServer_CancelBridgeOperation_CreatorMismatch(t *testing.T) {
	k, ms, ctx, mocks := setupKeeperWithMocks(t)
	requestID := "req_cancel_mismatch"
	requestHash := hashBridgeRequestIDForCancelTest(requestID)
	requestKey := hex.EncodeToString(requestHash)

	require.NoError(t, k.BridgeMintRefundsMap.Set(ctx, requestKey, types.MsgRequestBridgeMint{
		Creator:            testutil.Creator,
		Amount:             "1000",
		DestinationAddress: "0xabc",
		ChainId:            "ethereum",
	}))

	attacker := testutil.Requester
	attackerAddr, err := sdk.AccAddressFromBech32(attacker)
	require.NoError(t, err)
	mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), attackerAddr).Return(true).Times(1)

	_, err = ms.CancelBridgeOperation(sdk.WrapSDKContext(ctx), &types.MsgCancelBridgeOperation{
		Creator:   attacker,
		RequestId: requestID,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "creator mismatch")

	stillPending, getErr := k.BridgeMintRefundsMap.Get(ctx, requestKey)
	require.NoError(t, getErr)
	require.Equal(t, testutil.Creator, stillPending.Creator)
}

func TestMsgServer_CancelBridgeOperation_MintSuccess(t *testing.T) {
	k, ms, ctx, mocks := setupKeeperWithMocks(t)
	requestID := "req_cancel_mint_success"
	requestHash := hashBridgeRequestIDForCancelTest(requestID)
	requestKey := hex.EncodeToString(requestHash)

	blsK, ok := k.BlsKeeper.(blskeeper.Keeper)
	require.True(t, ok)
	blsParams, err := blsK.GetParams(ctx)
	require.NoError(t, err)
	blsParams.MaxSigningAttempts = 1
	require.NoError(t, blsK.SetParams(ctx, blsParams))

	require.NoError(t, blsK.SetEpochBLSData(ctx, blstypes.EpochBLSData{
		EpochId:        777,
		DkgPhase:       blstypes.DKGPhase_DKG_PHASE_SIGNED,
		GroupPublicKey: []byte{1},
	}))

	require.NoError(t, blsK.RequestThresholdSignature(ctx, blstypes.SigningData{
		CurrentEpochId: 777,
		ChainId:        bytes.Repeat([]byte{0x11}, 32),
		RequestId:      requestHash,
		Data:           [][]byte{bytes.Repeat([]byte{0x22}, 32)},
	}))
	request, err := blsK.GetSigningStatus(ctx, requestHash)
	require.NoError(t, err)
	expiryCtx := ctx.WithBlockHeight(request.DeadlineBlockHeight)
	require.NoError(t, blsK.ProcessThresholdSigningDeadlines(expiryCtx))

	expiredRequest, err := blsK.GetSigningStatus(expiryCtx, requestHash)
	require.NoError(t, err)
	require.Equal(t, blstypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_EXPIRED, expiredRequest.Status)

	require.NoError(t, k.BridgeMintRefundsMap.Set(expiryCtx, requestKey, types.MsgRequestBridgeMint{
		Creator:            testutil.Creator,
		Amount:             "1000",
		DestinationAddress: "0xabc",
		ChainId:            "ethereum",
	}))

	creatorAddr, err := sdk.AccAddressFromBech32(testutil.Creator)
	require.NoError(t, err)
	mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), creatorAddr).Return(true).Times(1)
	refundCoins := sdk.NewCoins(sdk.NewCoin(types.BaseCoin, math.NewInt(1000)))
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.BridgeEscrowAccName, creatorAddr, refundCoins, "bridge_release").
		Return(nil).
		Times(1)

	_, err = ms.CancelBridgeOperation(sdk.WrapSDKContext(expiryCtx), &types.MsgCancelBridgeOperation{
		Creator:   testutil.Creator,
		RequestId: requestID,
	})
	require.NoError(t, err)

	_, err = k.BridgeMintRefundsMap.Get(expiryCtx, requestKey)
	require.ErrorIs(t, err, collections.ErrNotFound)

	cancelledRequest, err := blsK.GetSigningStatus(expiryCtx, requestHash)
	require.NoError(t, err)
	require.Equal(t, blstypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_CANCELLED, cancelledRequest.Status)
}

func TestMsgServer_CancelBridgeOperation_WithdrawalUserAllowed(t *testing.T) {
	k, ms, ctx, mocks := setupKeeperWithMocks(t)
	requestID := "req_cancel_withdrawal_user"
	requestHash := hashBridgeRequestIDForCancelTest(requestID)
	requestKey := hex.EncodeToString(requestHash)

	require.NoError(t, k.BridgeWithdrawalRefundsMap.Set(ctx, requestKey, types.MsgRequestBridgeWithdrawal{
		Creator:            testutil.Creator,
		UserAddress:        testutil.Requester,
		Amount:             "1000",
		DestinationAddress: "0xabc",
	}))

	userAddr, err := sdk.AccAddressFromBech32(testutil.Requester)
	require.NoError(t, err)
	mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), userAddr).Return(true).Times(1)

	_, err = ms.CancelBridgeOperation(sdk.WrapSDKContext(ctx), &types.MsgCancelBridgeOperation{
		Creator:   testutil.Requester,
		RequestId: requestID,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to cancel threshold signing request")
	require.NotContains(t, err.Error(), "creator mismatch")

	_, err = k.BridgeWithdrawalRefundsMap.Get(ctx, requestKey)
	require.NoError(t, err)
}

func hashBridgeRequestIDForCancelTest(requestID string) []byte {
	hash := sha3.NewLegacyKeccak256()
	hash.Write([]byte(requestID))
	return hash.Sum(nil)
}
