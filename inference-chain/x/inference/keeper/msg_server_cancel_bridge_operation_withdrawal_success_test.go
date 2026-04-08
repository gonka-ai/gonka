package keeper_test

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	blskeeper "github.com/productscience/inference/x/bls/keeper"
	blstypes "github.com/productscience/inference/x/bls/types"
	inferencekeeper "github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestMsgServer_CancelBridgeOperation_WithdrawalSuccess(t *testing.T) {
	k, _, ctx, mocks := setupKeeperWithMocks(t)
	requestID := "req_cancel_withdrawal_success"
	requestHash := hashBridgeRequestIDForCancelTest(requestID)
	requestKey := hex.EncodeToString(requestHash)

	blsK, ok := k.BlsKeeper.(blskeeper.Keeper)
	require.True(t, ok)
	blsParams, err := blsK.GetParams(ctx)
	require.NoError(t, err)
	blsParams.MaxSigningAttempts = 1
	require.NoError(t, blsK.SetParams(ctx, blsParams))
	require.NoError(t, blsK.SetEpochBLSData(ctx, blstypes.EpochBLSData{
		EpochId:        778,
		DkgPhase:       blstypes.DKGPhase_DKG_PHASE_SIGNED,
		GroupPublicKey: []byte{1},
	}))

	require.NoError(t, blsK.RequestThresholdSignature(ctx, blstypes.SigningData{
		CurrentEpochId: 778,
		ChainId:        bytes.Repeat([]byte{0x31}, 32),
		RequestId:      requestHash,
		Data:           [][]byte{bytes.Repeat([]byte{0x32}, 32)},
	}))

	request, err := blsK.GetSigningStatus(ctx, requestHash)
	require.NoError(t, err)

	expiryCtx := ctx.WithBlockHeight(request.DeadlineBlockHeight)
	require.NoError(t, blsK.ProcessThresholdSigningDeadlines(expiryCtx))

	expiredRequest, err := blsK.GetSigningStatus(expiryCtx, requestHash)
	require.NoError(t, err)
	require.Equal(t, blstypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_EXPIRED, expiredRequest.Status)

	require.NoError(t, k.BridgeWithdrawalRefundsMap.Set(expiryCtx, requestKey, types.MsgRequestBridgeWithdrawal{
		Creator:            testutil.Creator,
		UserAddress:        testutil.Requester,
		Amount:             "1000",
		DestinationAddress: "0xabc",
	}))
	wrappedRef := types.BridgeTokenReference{
		ChainId:         "ethereum",
		ContractAddress: "0xabc",
	}
	require.NoError(t, k.BridgeWithdrawalTokenRefsMap.Set(expiryCtx, requestKey, wrappedRef))
	require.NoError(t, k.WrappedTokenContractsMap.Set(expiryCtx, collections.Join(wrappedRef.ChainId, strings.ToLower(wrappedRef.ContractAddress)), types.BridgeWrappedTokenContract{
		ChainId:                wrappedRef.ChainId,
		ContractAddress:        wrappedRef.ContractAddress,
		WrappedContractAddress: testutil.Creator,
	}))

	userAddr, err := sdk.AccAddressFromBech32(testutil.Requester)
	require.NoError(t, err)
	mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), userAddr).Return(true).Times(1)

	var mintCalls int
	k.SetMintTokensFnForTesting(func(_ sdk.Context, contractAddr, recipient, amount string) error {
		mintCalls++
		require.Equal(t, testutil.Creator, contractAddr)
		require.Equal(t, testutil.Requester, recipient)
		require.Equal(t, "1000", amount)
		return nil
	})

	ms := inferencekeeper.NewMsgServerImpl(k)
	_, err = ms.CancelBridgeOperation(sdk.WrapSDKContext(expiryCtx), &types.MsgCancelBridgeOperation{
		Creator:   testutil.Requester,
		RequestId: requestID,
	})
	require.NoError(t, err)
	require.Equal(t, 1, mintCalls)

	_, err = k.BridgeWithdrawalRefundsMap.Get(expiryCtx, requestKey)
	require.ErrorIs(t, err, collections.ErrNotFound)

	cancelledRequest, err := blsK.GetSigningStatus(expiryCtx, requestHash)
	require.NoError(t, err)
	require.Equal(t, blstypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_CANCELLED, cancelledRequest.Status)

	foundCancelEvent := false
	for _, event := range expiryCtx.EventManager().Events() {
		if event.Type == "bridge_operation_cancelled" {
			foundCancelEvent = true
			break
		}
	}
	require.True(t, foundCancelEvent)
}

func TestMsgServer_CancelBridgeOperation_WithdrawalCreatorAllowed(t *testing.T) {
	k, _, ctx, mocks := setupKeeperWithMocks(t)
	requestID := "req_cancel_withdrawal_creator"
	requestHash := hashBridgeRequestIDForCancelTest(requestID)
	requestKey := hex.EncodeToString(requestHash)

	blsK, ok := k.BlsKeeper.(blskeeper.Keeper)
	require.True(t, ok)
	blsParams, err := blsK.GetParams(ctx)
	require.NoError(t, err)
	blsParams.MaxSigningAttempts = 1
	require.NoError(t, blsK.SetParams(ctx, blsParams))
	require.NoError(t, blsK.SetEpochBLSData(ctx, blstypes.EpochBLSData{
		EpochId:        779,
		DkgPhase:       blstypes.DKGPhase_DKG_PHASE_SIGNED,
		GroupPublicKey: []byte{1},
	}))

	require.NoError(t, blsK.RequestThresholdSignature(ctx, blstypes.SigningData{
		CurrentEpochId: 779,
		ChainId:        bytes.Repeat([]byte{0x41}, 32),
		RequestId:      requestHash,
		Data:           [][]byte{bytes.Repeat([]byte{0x42}, 32)},
	}))

	request, err := blsK.GetSigningStatus(ctx, requestHash)
	require.NoError(t, err)

	expiryCtx := ctx.WithBlockHeight(request.DeadlineBlockHeight)
	require.NoError(t, blsK.ProcessThresholdSigningDeadlines(expiryCtx))

	expiredRequest, err := blsK.GetSigningStatus(expiryCtx, requestHash)
	require.NoError(t, err)
	require.Equal(t, blstypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_EXPIRED, expiredRequest.Status)

	require.NoError(t, k.BridgeWithdrawalRefundsMap.Set(expiryCtx, requestKey, types.MsgRequestBridgeWithdrawal{
		Creator:            testutil.Creator,
		UserAddress:        testutil.Requester,
		Amount:             "1000",
		DestinationAddress: "0xabc",
	}))
	wrappedRef := types.BridgeTokenReference{
		ChainId:         "ethereum",
		ContractAddress: "0xabc",
	}
	require.NoError(t, k.BridgeWithdrawalTokenRefsMap.Set(expiryCtx, requestKey, wrappedRef))
	require.NoError(t, k.WrappedTokenContractsMap.Set(expiryCtx, collections.Join(wrappedRef.ChainId, strings.ToLower(wrappedRef.ContractAddress)), types.BridgeWrappedTokenContract{
		ChainId:                wrappedRef.ChainId,
		ContractAddress:        wrappedRef.ContractAddress,
		WrappedContractAddress: testutil.Creator,
	}))

	creatorAddr, err := sdk.AccAddressFromBech32(testutil.Creator)
	require.NoError(t, err)
	mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), creatorAddr).Return(true).Times(1)

	var mintCalls int
	k.SetMintTokensFnForTesting(func(_ sdk.Context, contractAddr, recipient, amount string) error {
		mintCalls++
		require.Equal(t, testutil.Creator, contractAddr)
		require.Equal(t, testutil.Requester, recipient)
		require.Equal(t, "1000", amount)
		return nil
	})

	ms := inferencekeeper.NewMsgServerImpl(k)
	_, err = ms.CancelBridgeOperation(sdk.WrapSDKContext(expiryCtx), &types.MsgCancelBridgeOperation{
		Creator:   testutil.Creator,
		RequestId: requestID,
	})
	require.NoError(t, err)
	require.Equal(t, 1, mintCalls)

	_, err = k.BridgeWithdrawalRefundsMap.Get(expiryCtx, requestKey)
	require.ErrorIs(t, err, collections.ErrNotFound)

	cancelledRequest, err := blsK.GetSigningStatus(expiryCtx, requestHash)
	require.NoError(t, err)
	require.Equal(t, blstypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_CANCELLED, cancelledRequest.Status)
}
