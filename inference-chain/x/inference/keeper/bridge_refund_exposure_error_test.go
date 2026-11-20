package keeper_test

import (
	"encoding/hex"
	"errors"
	"fmt"
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	blstypes "github.com/productscience/inference/x/bls/types"
	inferencekeeper "github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestBridgeRefundRejectedWhenExposureCannotBeRead(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(fmt.Sprintf("automatic_%t", automatic), func(t *testing.T) {
			k, _, ctx, mocks := setupKeeperWithMocks(t)
			requestID := "unreadable-exposure"
			requestHash := hashBridgeRequestIDForCancelTest(requestID)
			requestKey := hex.EncodeToString(requestHash)
			pending := types.MsgRequestBridgeMint{Creator: testutil.Creator, Amount: "100", ChainId: "ethereum"}
			require.NoError(t, k.BridgeMintRefundsMap.Set(ctx, requestKey, pending))
			blsMock := keepertest.NewMockBlsKeeper(gomock.NewController(t))
			readError := errors.New("exposure store unavailable")
			blsMock.EXPECT().HasExposedPartialSignatures(gomock.Any(), requestHash).Return(false, readError)
			k.BlsKeeper = blsMock
			mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)

			var err error
			if automatic {
				var closeRetry bool
				closeRetry, err = k.ProcessAutoRefundForFailedBridgeOperation(ctx, requestHash, "deadline expired")
				require.False(t, closeRetry)
			} else {
				blsMock.EXPECT().GetSigningStatus(gomock.Any(), requestHash).Return(&blstypes.ThresholdSigningRequest{
					Status: blstypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_EXPIRED,
				}, nil)
				owner, addressErr := sdk.AccAddressFromBech32(testutil.Creator)
				require.NoError(t, addressErr)
				mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), owner).Return(true)
				_, err = inferencekeeper.NewMsgServerImpl(k).CancelBridgeOperation(ctx, &types.MsgCancelBridgeOperation{
					Creator: testutil.Creator, RequestId: requestID,
				})
			}
			require.ErrorIs(t, err, readError)
			stillPending, getErr := k.BridgeMintRefundsMap.Get(ctx, requestKey)
			require.NoError(t, getErr)
			require.Equal(t, pending, stillPending)
		})
	}
}

func TestBridgeWithdrawalRefundRejectedWhenPartialsExposed(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(fmt.Sprintf("automatic_%t", automatic), func(t *testing.T) {
			k, _, ctx, mocks := setupKeeperWithMocks(t)
			requestID := "exposed-withdrawal"
			requestHash := hashBridgeRequestIDForCancelTest(requestID)
			requestKey := hex.EncodeToString(requestHash)
			pending := types.MsgRequestBridgeWithdrawal{Creator: testutil.Creator, UserAddress: testutil.Requester, Amount: "100"}
			reference := types.BridgeTokenReference{ChainId: "ethereum", ContractAddress: "0xabc"}
			require.NoError(t, k.BridgeWithdrawalRefundsMap.Set(ctx, requestKey, pending))
			require.NoError(t, k.BridgeWithdrawalTokenRefsMap.Set(ctx, requestKey, reference))
			require.NoError(t, k.WrappedTokenContractsMap.Set(ctx, collections.Join(reference.ChainId, reference.ContractAddress), types.BridgeWrappedTokenContract{
				ChainId: reference.ChainId, ContractAddress: reference.ContractAddress, WrappedContractAddress: testutil.Creator,
			}))
			blsMock := keepertest.NewMockBlsKeeper(gomock.NewController(t))
			blsMock.EXPECT().HasExposedPartialSignatures(gomock.Any(), requestHash).Return(true, nil)
			k.BlsKeeper = blsMock
			mintCalls := 0
			k.SetMintTokensFnForTesting(func(_ sdk.Context, _, _, _ string) error {
				mintCalls++
				return nil
			})

			var err error
			if automatic {
				var closeRetry bool
				closeRetry, err = k.ProcessAutoRefundForFailedBridgeOperation(ctx, requestHash, "deadline expired")
				require.False(t, closeRetry)
			} else {
				blsMock.EXPECT().GetSigningStatus(gomock.Any(), requestHash).Return(&blstypes.ThresholdSigningRequest{
					Status: blstypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_EXPIRED,
				}, nil)
				owner, addressErr := sdk.AccAddressFromBech32(testutil.Requester)
				require.NoError(t, addressErr)
				mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), owner).Return(true)
				_, err = inferencekeeper.NewMsgServerImpl(k).CancelBridgeOperation(ctx, &types.MsgCancelBridgeOperation{
					Creator: testutil.Requester, RequestId: requestID,
				})
			}
			require.ErrorContains(t, err, "partial signatures have been exposed")
			require.Zero(t, mintCalls)
			stillPending, getErr := k.BridgeWithdrawalRefundsMap.Get(ctx, requestKey)
			require.NoError(t, getErr)
			require.Equal(t, pending, stillPending)
			stillReference, getErr := k.BridgeWithdrawalTokenRefsMap.Get(ctx, requestKey)
			require.NoError(t, getErr)
			require.Equal(t, reference, stillReference)
		})
	}
}
