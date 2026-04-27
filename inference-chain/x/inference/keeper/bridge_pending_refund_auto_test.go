package keeper_test

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strings"
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
)

func TestProcessAutoRefundForFailedBridgeOperation_Mint(t *testing.T) {
	k, _, ctx, mocks := setupKeeperWithMocks(t)
	requestID := bytes.Repeat([]byte{0x44}, 32)
	requestKey := hex.EncodeToString(requestID)

	blsK, ok := k.BlsKeeper.(blskeeper.Keeper)
	require.True(t, ok)
	blsParams, err := blsK.GetParams(ctx)
	require.NoError(t, err)
	blsParams.MaxSigningAttempts = 1
	require.NoError(t, blsK.SetParams(ctx, blsParams))
	require.NoError(t, blsK.SetEpochBLSData(ctx, blstypes.EpochBLSData{
		EpochId:        881,
		DkgPhase:       blstypes.DKGPhase_DKG_PHASE_SIGNED,
		GroupPublicKey: []byte{1},
	}))
	require.NoError(t, blsK.RequestThresholdSignature(ctx, blstypes.SigningData{
		CurrentEpochId: 881,
		ChainId:        bytes.Repeat([]byte{0x71}, 32),
		RequestId:      requestID,
		Data:           [][]byte{bytes.Repeat([]byte{0x72}, 32)},
	}))
	request, err := blsK.GetSigningStatus(ctx, requestID)
	require.NoError(t, err)
	expiryCtx := ctx.WithBlockHeight(request.DeadlineBlockHeight)
	require.NoError(t, blsK.ProcessThresholdSigningDeadlines(expiryCtx))
	expiredRequest, err := blsK.GetSigningStatus(expiryCtx, requestID)
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
	refundCoins := sdk.NewCoins(sdk.NewCoin(types.BaseCoin, math.NewInt(1000)))
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.BridgeEscrowAccName, creatorAddr, refundCoins, "bridge_release").
		Return(nil).
		Times(1)

	closeRetry, err := k.ProcessAutoRefundForFailedBridgeOperation(expiryCtx, requestID, "deadline expired")
	require.NoError(t, err)
	require.True(t, closeRetry)

	_, err = k.BridgeMintRefundsMap.Get(expiryCtx, requestKey)
	require.ErrorIs(t, err, collections.ErrNotFound)

	updatedRequest, err := blsK.GetSigningStatus(expiryCtx, requestID)
	require.NoError(t, err)
	require.Equal(t, blstypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_EXPIRED, updatedRequest.Status)

	found := false
	for _, event := range expiryCtx.EventManager().Events() {
		if event.Type == "bridge_operation_auto_refunded" {
			found = true
			break
		}
	}
	require.True(t, found)
}

func TestProcessAutoRefundForFailedBridgeOperation_Withdrawal(t *testing.T) {
	k, _, ctx, _ := setupKeeperWithMocks(t)
	requestID := bytes.Repeat([]byte{0x55}, 32)
	requestKey := hex.EncodeToString(requestID)

	blsK, ok := k.BlsKeeper.(blskeeper.Keeper)
	require.True(t, ok)
	blsParams, err := blsK.GetParams(ctx)
	require.NoError(t, err)
	blsParams.MaxSigningAttempts = 1
	require.NoError(t, blsK.SetParams(ctx, blsParams))
	require.NoError(t, blsK.SetEpochBLSData(ctx, blstypes.EpochBLSData{
		EpochId:        882,
		DkgPhase:       blstypes.DKGPhase_DKG_PHASE_SIGNED,
		GroupPublicKey: []byte{1},
	}))
	require.NoError(t, blsK.RequestThresholdSignature(ctx, blstypes.SigningData{
		CurrentEpochId: 882,
		ChainId:        bytes.Repeat([]byte{0x81}, 32),
		RequestId:      requestID,
		Data:           [][]byte{bytes.Repeat([]byte{0x82}, 32)},
	}))
	request, err := blsK.GetSigningStatus(ctx, requestID)
	require.NoError(t, err)
	expiryCtx := ctx.WithBlockHeight(request.DeadlineBlockHeight)
	require.NoError(t, blsK.ProcessThresholdSigningDeadlines(expiryCtx))
	expiredRequest, err := blsK.GetSigningStatus(expiryCtx, requestID)
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

	var mintCalls int
	k.SetMintTokensFnForTesting(func(_ sdk.Context, contractAddr, recipient, amount string) error {
		mintCalls++
		require.Equal(t, testutil.Creator, contractAddr)
		require.Equal(t, testutil.Requester, recipient)
		require.Equal(t, "1000", amount)
		return nil
	})

	closeRetry, err := k.ProcessAutoRefundForFailedBridgeOperation(expiryCtx, requestID, "signature aggregation failed")
	require.NoError(t, err)
	require.True(t, closeRetry)
	require.Equal(t, 1, mintCalls)

	_, err = k.BridgeWithdrawalRefundsMap.Get(expiryCtx, requestKey)
	require.ErrorIs(t, err, collections.ErrNotFound)

	updatedRequest, err := blsK.GetSigningStatus(expiryCtx, requestID)
	require.NoError(t, err)
	require.Equal(t, blstypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_EXPIRED, updatedRequest.Status)

	found := false
	for _, event := range expiryCtx.EventManager().Events() {
		if event.Type == "bridge_operation_auto_refunded" {
			found = true
			break
		}
	}
	require.True(t, found)
}

func TestProcessAutoRefundForFailedBridgeOperation_NoPendingContext(t *testing.T) {
	k, _, ctx, _ := setupKeeperWithMocks(t)
	requestID := bytes.Repeat([]byte{0x66}, 32)

	closeRetry, err := k.ProcessAutoRefundForFailedBridgeOperation(ctx, requestID, "deadline expired")
	require.NoError(t, err)
	require.False(t, closeRetry)
}

func TestProcessAutoRefundForFailedBridgeOperation_MintRefundFailure(t *testing.T) {
	k, _, ctx, mocks := setupKeeperWithMocks(t)
	requestID := bytes.Repeat([]byte{0x67}, 32)
	requestKey := hex.EncodeToString(requestID)

	require.NoError(t, k.BridgeMintRefundsMap.Set(ctx, requestKey, types.MsgRequestBridgeMint{
		Creator:            testutil.Creator,
		Amount:             "1000",
		DestinationAddress: "0xabc",
		ChainId:            "ethereum",
	}))

	creatorAddr, err := sdk.AccAddressFromBech32(testutil.Creator)
	require.NoError(t, err)
	refundCoins := sdk.NewCoins(sdk.NewCoin(types.BaseCoin, math.NewInt(1000)))
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.BridgeEscrowAccName, creatorAddr, refundCoins, "bridge_release").
		Return(errors.New("insufficient funds")).
		Times(1)

	closeRetry, err := k.ProcessAutoRefundForFailedBridgeOperation(ctx, requestID, "deadline expired")
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to auto-refund pending bridge mint request")
	require.False(t, closeRetry)

	stillPending, getErr := k.BridgeMintRefundsMap.Get(ctx, requestKey)
	require.NoError(t, getErr)
	require.Equal(t, testutil.Creator, stillPending.Creator)

	foundAutoRefundEvent := false
	for _, event := range ctx.EventManager().Events() {
		if event.Type == "bridge_operation_auto_refunded" {
			foundAutoRefundEvent = true
			break
		}
	}
	require.False(t, foundAutoRefundEvent)
}

func TestProcessAutoRefundForFailedBridgeOperation_WithdrawalContractNotRegistered(t *testing.T) {
	k, _, ctx, _ := setupKeeperWithMocks(t)
	requestID := bytes.Repeat([]byte{0x68}, 32)
	requestKey := hex.EncodeToString(requestID)

	require.NoError(t, k.BridgeWithdrawalRefundsMap.Set(ctx, requestKey, types.MsgRequestBridgeWithdrawal{
		Creator:            testutil.Creator,
		UserAddress:        testutil.Requester,
		Amount:             "1000",
		DestinationAddress: "0xabc",
	}))
	require.NoError(t, k.BridgeWithdrawalTokenRefsMap.Set(ctx, requestKey, types.BridgeTokenReference{
		ChainId:         "ethereum",
		ContractAddress: "0xabc",
	}))

	closeRetry, err := k.ProcessAutoRefundForFailedBridgeOperation(ctx, requestID, "deadline expired")
	require.Error(t, err)
	require.Contains(t, err.Error(), "active wrapped token contract not found")
	require.False(t, closeRetry)

	stillPending, getErr := k.BridgeWithdrawalRefundsMap.Get(ctx, requestKey)
	require.NoError(t, getErr)
	require.Equal(t, testutil.Creator, stillPending.Creator)
}
