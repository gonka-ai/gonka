package keeper_test

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	blskeeper "github.com/productscience/inference/x/bls/keeper"
	blstypes "github.com/productscience/inference/x/bls/types"
	inferencekeeper "github.com/productscience/inference/x/inference/keeper"
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
	wrappedRef := types.BridgeTokenReference{
		ChainId:         "ethereum",
		ContractAddress: "0xabc",
	}
	require.NoError(t, k.BridgeWithdrawalTokenRefsMap.Set(ctx, requestKey, wrappedRef))
	require.NoError(t, k.WrappedTokenContractsMap.Set(ctx, collections.Join(wrappedRef.ChainId, strings.ToLower(wrappedRef.ContractAddress)), types.BridgeWrappedTokenContract{
		ChainId:                wrappedRef.ChainId,
		ContractAddress:        wrappedRef.ContractAddress,
		WrappedContractAddress: testutil.Creator,
	}))

	userAddr, err := sdk.AccAddressFromBech32(testutil.Requester)
	require.NoError(t, err)
	mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), userAddr).Return(true).Times(1)

	_, err = ms.CancelBridgeOperation(sdk.WrapSDKContext(ctx), &types.MsgCancelBridgeOperation{
		Creator:   testutil.Requester,
		RequestId: requestID,
	})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "creator mismatch")

	_, err = k.BridgeWithdrawalRefundsMap.Get(ctx, requestKey)
	require.NoError(t, err)
}

func TestMsgServer_CancelBridgeOperation_CompletedSigningRejected(t *testing.T) {
	k, _, ctx, mocks := setupKeeperWithMocks(t)
	requestID := "req_cancel_completed"
	requestHash := hashBridgeRequestIDForCancelTest(requestID)
	requestKey := hex.EncodeToString(requestHash)

	require.NoError(t, k.BridgeMintRefundsMap.Set(ctx, requestKey, types.MsgRequestBridgeMint{
		Creator:            testutil.Creator,
		Amount:             "1000",
		DestinationAddress: "0xabc",
		ChainId:            "ethereum",
	}))

	creatorAddr, err := sdk.AccAddressFromBech32(testutil.Creator)
	require.NoError(t, err)
	mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), creatorAddr).Return(true).Times(1)

	blsMock := keepertest.NewMockBlsKeeper(gomock.NewController(t))
	blsMock.EXPECT().
		GetSigningStatus(gomock.Any(), requestHash).
		Return(&blstypes.ThresholdSigningRequest{
			Status: blstypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COMPLETED,
		}, nil).
		Times(1)

	k.BlsKeeper = blsMock
	ms := inferencekeeper.NewMsgServerImpl(k)

	_, err = ms.CancelBridgeOperation(sdk.WrapSDKContext(ctx), &types.MsgCancelBridgeOperation{
		Creator:   testutil.Creator,
		RequestId: requestID,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to cancel threshold signing request")
	require.Contains(t, err.Error(), "threshold signing already completed")

	stillPending, getErr := k.BridgeMintRefundsMap.Get(ctx, requestKey)
	require.NoError(t, getErr)
	require.Equal(t, testutil.Creator, stillPending.Creator)
}

func TestMsgServer_GovernanceCancelBridgeOperation_MintOverrideRecipient(t *testing.T) {
	k, ms, ctx, mocks := setupKeeperWithMocks(t)
	requestID := "req_gov_cancel_mint_override_recipient"
	requestHash := hashBridgeRequestIDForCancelTest(requestID)
	requestKey := hex.EncodeToString(requestHash)

	blsK, ok := k.BlsKeeper.(blskeeper.Keeper)
	require.True(t, ok)
	blsParams, err := blsK.GetParams(ctx)
	require.NoError(t, err)
	blsParams.MaxSigningAttempts = 1
	require.NoError(t, blsK.SetParams(ctx, blsParams))
	require.NoError(t, blsK.SetEpochBLSData(ctx, blstypes.EpochBLSData{
		EpochId:        880,
		DkgPhase:       blstypes.DKGPhase_DKG_PHASE_SIGNED,
		GroupPublicKey: []byte{1},
	}))
	require.NoError(t, blsK.RequestThresholdSignature(ctx, blstypes.SigningData{
		CurrentEpochId: 880,
		ChainId:        bytes.Repeat([]byte{0x61}, 32),
		RequestId:      requestHash,
		Data:           [][]byte{bytes.Repeat([]byte{0x62}, 32)},
	}))
	request, err := blsK.GetSigningStatus(ctx, requestHash)
	require.NoError(t, err)
	expiryCtx := ctx.WithBlockHeight(request.DeadlineBlockHeight)
	require.NoError(t, blsK.ProcessThresholdSigningDeadlines(expiryCtx))

	require.NoError(t, k.BridgeMintRefundsMap.Set(expiryCtx, requestKey, types.MsgRequestBridgeMint{
		Creator:            testutil.Creator,
		Amount:             "1000",
		DestinationAddress: "0xabc",
		ChainId:            "ethereum",
	}))

	overrideRecipient := testutil.Requester
	overrideRecipientAddr, err := sdk.AccAddressFromBech32(overrideRecipient)
	require.NoError(t, err)
	refundCoins := sdk.NewCoins(sdk.NewCoin(types.BaseCoin, math.NewInt(1000)))
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.BridgeEscrowAccName, overrideRecipientAddr, refundCoins, "bridge_release").
		Return(nil).
		Times(1)

	_, err = ms.GovernanceCancelBridgeOperation(sdk.WrapSDKContext(expiryCtx), &types.MsgGovernanceCancelBridgeOperation{
		Authority:         k.GetAuthority(),
		RequestId:         requestID,
		OverrideRecipient: overrideRecipient,
		Reason:            "manual recovery",
	})
	require.NoError(t, err)

	_, err = k.BridgeMintRefundsMap.Get(expiryCtx, requestKey)
	require.ErrorIs(t, err, collections.ErrNotFound)

	cancelledRequest, err := blsK.GetSigningStatus(expiryCtx, requestHash)
	require.NoError(t, err)
	require.Equal(t, blstypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_CANCELLED, cancelledRequest.Status)
}

func TestMsgServer_GovernanceCancelBridgeOperation_WithdrawalOverrideWrappedContractAndRecipient(t *testing.T) {
	k, _, ctx, _ := setupKeeperWithMocks(t)
	requestID := "req_gov_cancel_withdrawal_override"
	requestHash := hashBridgeRequestIDForCancelTest(requestID)
	requestKey := hex.EncodeToString(requestHash)

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
		ChainId:        bytes.Repeat([]byte{0x63}, 32),
		RequestId:      requestHash,
		Data:           [][]byte{bytes.Repeat([]byte{0x64}, 32)},
	}))
	request, err := blsK.GetSigningStatus(ctx, requestHash)
	require.NoError(t, err)
	expiryCtx := ctx.WithBlockHeight(request.DeadlineBlockHeight)
	require.NoError(t, blsK.ProcessThresholdSigningDeadlines(expiryCtx))

	oldWrapped := testutil.Creator
	newWrapped := testutil.Requester
	refundRecipient := testutil.Creator

	require.NoError(t, k.BridgeWithdrawalRefundsMap.Set(expiryCtx, requestKey, types.MsgRequestBridgeWithdrawal{
		Creator:            oldWrapped,
		UserAddress:        testutil.Requester,
		Amount:             "1000",
		DestinationAddress: "0xabc",
	}))

	ref := types.BridgeTokenReference{
		ChainId:         "ethereum",
		ContractAddress: "0xabc",
	}
	require.NoError(t, k.BridgeWithdrawalTokenRefsMap.Set(expiryCtx, requestKey, ref))
	require.NoError(t, k.WrappedContractReverseIndex.Set(expiryCtx, strings.ToLower(oldWrapped), ref))
	require.NoError(t, k.WrappedContractReverseIndex.Set(expiryCtx, strings.ToLower(newWrapped), ref))
	require.NoError(t, k.WrappedTokenContractsMap.Set(expiryCtx, collections.Join(ref.ChainId, strings.ToLower(ref.ContractAddress)), types.BridgeWrappedTokenContract{
		ChainId:                ref.ChainId,
		ContractAddress:        ref.ContractAddress,
		WrappedContractAddress: newWrapped,
	}))

	var mintCalls int
	k.SetMintTokensFnForTesting(func(_ sdk.Context, contractAddr, recipient, amount string) error {
		mintCalls++
		require.Equal(t, newWrapped, contractAddr)
		require.Equal(t, refundRecipient, recipient)
		require.Equal(t, "1000", amount)
		return nil
	})

	ms := inferencekeeper.NewMsgServerImpl(k)
	_, err = ms.GovernanceCancelBridgeOperation(sdk.WrapSDKContext(expiryCtx), &types.MsgGovernanceCancelBridgeOperation{
		Authority:               k.GetAuthority(),
		RequestId:               requestID,
		OverrideRecipient:       refundRecipient,
		OverrideWrappedContract: newWrapped,
		Reason:                  "manual recovery",
	})
	require.NoError(t, err)
	require.Equal(t, 1, mintCalls)

	_, err = k.BridgeWithdrawalRefundsMap.Get(expiryCtx, requestKey)
	require.ErrorIs(t, err, collections.ErrNotFound)

	cancelledRequest, err := blsK.GetSigningStatus(expiryCtx, requestHash)
	require.NoError(t, err)
	require.Equal(t, blstypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_CANCELLED, cancelledRequest.Status)
}

func TestMsgServer_GovernanceCancelBridgeOperation_WithdrawalOverrideWrappedContractMismatchRejected(t *testing.T) {
	k, _, ctx, _ := setupKeeperWithMocks(t)
	requestID := "req_gov_cancel_withdrawal_override_mismatch"
	requestHash := hashBridgeRequestIDForCancelTest(requestID)
	requestKey := hex.EncodeToString(requestHash)

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
		ChainId:        bytes.Repeat([]byte{0x65}, 32),
		RequestId:      requestHash,
		Data:           [][]byte{bytes.Repeat([]byte{0x66}, 32)},
	}))
	request, err := blsK.GetSigningStatus(ctx, requestHash)
	require.NoError(t, err)
	expiryCtx := ctx.WithBlockHeight(request.DeadlineBlockHeight)
	require.NoError(t, blsK.ProcessThresholdSigningDeadlines(expiryCtx))

	require.NoError(t, k.BridgeWithdrawalRefundsMap.Set(expiryCtx, requestKey, types.MsgRequestBridgeWithdrawal{
		Creator:            testutil.Creator,
		UserAddress:        testutil.Requester,
		Amount:             "1000",
		DestinationAddress: "0xabc",
	}))

	refA := types.BridgeTokenReference{
		ChainId:         "ethereum",
		ContractAddress: "0xaaa",
	}
	refB := types.BridgeTokenReference{
		ChainId:         "ethereum",
		ContractAddress: "0xbbb",
	}
	require.NoError(t, k.BridgeWithdrawalTokenRefsMap.Set(expiryCtx, requestKey, refA))
	require.NoError(t, k.WrappedContractReverseIndex.Set(expiryCtx, strings.ToLower(testutil.Creator), refA))
	require.NoError(t, k.WrappedTokenContractsMap.Set(expiryCtx, collections.Join(refA.ChainId, strings.ToLower(refA.ContractAddress)), types.BridgeWrappedTokenContract{
		ChainId:                refA.ChainId,
		ContractAddress:        refA.ContractAddress,
		WrappedContractAddress: testutil.Creator,
	}))
	require.NoError(t, k.WrappedContractReverseIndex.Set(expiryCtx, strings.ToLower(testutil.Requester), refB))
	require.NoError(t, k.WrappedTokenContractsMap.Set(expiryCtx, collections.Join(refB.ChainId, strings.ToLower(refB.ContractAddress)), types.BridgeWrappedTokenContract{
		ChainId:                refB.ChainId,
		ContractAddress:        refB.ContractAddress,
		WrappedContractAddress: testutil.Requester,
	}))

	ms := inferencekeeper.NewMsgServerImpl(k)
	_, err = ms.GovernanceCancelBridgeOperation(sdk.WrapSDKContext(expiryCtx), &types.MsgGovernanceCancelBridgeOperation{
		Authority:               k.GetAuthority(),
		RequestId:               requestID,
		OverrideWrappedContract: testutil.Requester,
		Reason:                  "manual recovery",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not match pending withdrawal token reference")

	stillPending, getErr := k.BridgeWithdrawalRefundsMap.Get(expiryCtx, requestKey)
	require.NoError(t, getErr)
	require.Equal(t, testutil.Creator, stillPending.Creator)
}

func TestMsgServer_GovernanceCancelBridgeOperation_WithdrawalOverrideUsesStoredTokenReference(t *testing.T) {
	k, _, ctx, _ := setupKeeperWithMocks(t)
	requestID := "req_gov_cancel_withdrawal_override_requires_original_reference"
	requestHash := hashBridgeRequestIDForCancelTest(requestID)
	requestKey := hex.EncodeToString(requestHash)

	blsK, ok := k.BlsKeeper.(blskeeper.Keeper)
	require.True(t, ok)
	blsParams, err := blsK.GetParams(ctx)
	require.NoError(t, err)
	blsParams.MaxSigningAttempts = 1
	require.NoError(t, blsK.SetParams(ctx, blsParams))
	require.NoError(t, blsK.SetEpochBLSData(ctx, blstypes.EpochBLSData{
		EpochId:        883,
		DkgPhase:       blstypes.DKGPhase_DKG_PHASE_SIGNED,
		GroupPublicKey: []byte{1},
	}))
	require.NoError(t, blsK.RequestThresholdSignature(ctx, blstypes.SigningData{
		CurrentEpochId: 883,
		ChainId:        bytes.Repeat([]byte{0x67}, 32),
		RequestId:      requestHash,
		Data:           [][]byte{bytes.Repeat([]byte{0x68}, 32)},
	}))
	request, err := blsK.GetSigningStatus(ctx, requestHash)
	require.NoError(t, err)
	expiryCtx := ctx.WithBlockHeight(request.DeadlineBlockHeight)
	require.NoError(t, blsK.ProcessThresholdSigningDeadlines(expiryCtx))

	require.NoError(t, k.BridgeWithdrawalRefundsMap.Set(expiryCtx, requestKey, types.MsgRequestBridgeWithdrawal{
		Creator:            testutil.Creator,
		UserAddress:        testutil.Requester,
		Amount:             "1000",
		DestinationAddress: "0xabc",
	}))
	require.NoError(t, k.BridgeWithdrawalTokenRefsMap.Set(expiryCtx, requestKey, types.BridgeTokenReference{
		ChainId:         "ethereum",
		ContractAddress: "0xabc",
	}))

	overrideReference := types.BridgeTokenReference{
		ChainId:         "ethereum",
		ContractAddress: "0xabc",
	}
	require.NoError(t, k.WrappedContractReverseIndex.Set(expiryCtx, strings.ToLower(testutil.Requester), overrideReference))
	require.NoError(t, k.WrappedTokenContractsMap.Set(expiryCtx, collections.Join(overrideReference.ChainId, strings.ToLower(overrideReference.ContractAddress)), types.BridgeWrappedTokenContract{
		ChainId:                overrideReference.ChainId,
		ContractAddress:        overrideReference.ContractAddress,
		WrappedContractAddress: testutil.Requester,
	}))

	var mintCalls int
	k.SetMintTokensFnForTesting(func(_ sdk.Context, contractAddr, recipient, amount string) error {
		mintCalls++
		require.Equal(t, testutil.Requester, contractAddr)
		require.Equal(t, testutil.Requester, recipient)
		require.Equal(t, "1000", amount)
		return nil
	})

	ms := inferencekeeper.NewMsgServerImpl(k)
	_, err = ms.GovernanceCancelBridgeOperation(sdk.WrapSDKContext(expiryCtx), &types.MsgGovernanceCancelBridgeOperation{
		Authority:               k.GetAuthority(),
		RequestId:               requestID,
		OverrideWrappedContract: testutil.Requester,
		Reason:                  "manual recovery",
	})
	require.NoError(t, err)
	require.Equal(t, 1, mintCalls)

	_, getErr := k.BridgeWithdrawalRefundsMap.Get(expiryCtx, requestKey)
	require.ErrorIs(t, getErr, collections.ErrNotFound)
	_, getErr = k.BridgeWithdrawalTokenRefsMap.Get(expiryCtx, requestKey)
	require.ErrorIs(t, getErr, collections.ErrNotFound)
}

func hashBridgeRequestIDForCancelTest(requestID string) []byte {
	hash := sha3.NewLegacyKeccak256()
	hash.Write([]byte(requestID))
	return hash.Sum(nil)
}
