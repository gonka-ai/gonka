package keeper_test

import (
	"context"
	"testing"

	"cosmossdk.io/math"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	blskeeper "github.com/productscience/inference/x/bls/keeper"
	blstypes "github.com/productscience/inference/x/bls/types"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestRequestBridgeMint_SigningEpoch(t *testing.T) {
	for _, currentPhase := range []blstypes.DKGPhase{
		blstypes.DKGPhase_DKG_PHASE_SIGNED,
		blstypes.DKGPhase_DKG_PHASE_FAILED,
		blstypes.DKGPhase_DKG_PHASE_COMPLETED,
	} {
		t.Run(currentPhase.String(), func(t *testing.T) {

			k, ms, ctx, mocks := setupKeeperWithMocks(t)
			ctx = ctx.WithBlockHeight(110)
			setBridgeSigningFallbackEpochs(t, k, ctx)
			blsK := k.BlsKeeper.(blskeeper.Keeper)
			params, err := blsK.GetParams(ctx)
			require.NoError(t, err)
			params.CompletedFallbackBlocks = 10
			require.NoError(t, blsK.SetParams(ctx, params))
			require.NoError(t, blsK.SetEpochBLSData(ctx, blstypes.EpochBLSData{
				EpochId: 10, DkgPhase: currentPhase, GroupPublicKey: []byte{1}, DisputingPhaseDeadlineBlock: 100,
			}))
			expectedEpoch := uint64(10)
			if currentPhase == blstypes.DKGPhase_DKG_PHASE_FAILED {
				expectedEpoch = 9
			}
			bridgeAddress := "0x2222222222222222222222222222222222222222"
			k.SetBridgeContractAddress(ctx, types.BridgeContractAddress{ChainId: "ethereum", Address: bridgeAddress})

			signer := sdk.MustAccAddressFromBech32(testutil.Creator)
			mocks.AccountKeeper.EXPECT().HasAccount(ctx, signer).Return(true)
			mocks.BankViewKeeper.EXPECT().SpendableCoin(ctx, signer, types.BaseCoin).Return(sdk.NewCoin(types.BaseCoin, math.NewInt(1000)))
			mocks.BankKeeper.EXPECT().SendCoinsFromAccountToModule(ctx, signer, types.BridgeEscrowAccName, gomock.Any(), "bridge_escrow").Return(nil)

			resp, err := ms.RequestBridgeMint(ctx, &types.MsgRequestBridgeMint{
				Creator:                  testutil.Creator,
				Amount:                   "100",
				DestinationAddress:       "0x3333333333333333333333333333333333333333",
				ChainId:                  "ethereum",
				DestinationBridgeAddress: bridgeAddress,
			})
			require.NoError(t, err)
			require.Equal(t, expectedEpoch, resp.EpochIndex)
			requests, err := blsK.ListActiveSigningRequests(ctx, expectedEpoch)
			require.NoError(t, err)
			require.Len(t, requests, 1)
			require.Equal(t, expectedEpoch, requests[0].CurrentEpochId)
		})
	}
}

func TestRequestBridgeWithdrawal_SigningEpoch(t *testing.T) {
	for _, currentPhase := range []blstypes.DKGPhase{
		blstypes.DKGPhase_DKG_PHASE_SIGNED,
		blstypes.DKGPhase_DKG_PHASE_FAILED,
		blstypes.DKGPhase_DKG_PHASE_COMPLETED,
	} {
		t.Run(currentPhase.String(), func(t *testing.T) {

			k, _, ctx, _ := setupKeeperWithMocks(t)
			ctx = ctx.WithBlockHeight(110)
			setBridgeSigningFallbackEpochs(t, k, ctx)
			blsK := k.BlsKeeper.(blskeeper.Keeper)
			params, err := blsK.GetParams(ctx)
			require.NoError(t, err)
			params.CompletedFallbackBlocks = 10
			require.NoError(t, blsK.SetParams(ctx, params))
			require.NoError(t, blsK.SetEpochBLSData(ctx, blstypes.EpochBLSData{
				EpochId: 10, DkgPhase: currentPhase, GroupPublicKey: []byte{1}, DisputingPhaseDeadlineBlock: 100,
			}))
			expectedEpoch := uint64(10)
			if currentPhase == blstypes.DKGPhase_DKG_PHASE_FAILED {
				expectedEpoch = 9
			}
			bridgeAddress := "0x2222222222222222222222222222222222222222"
			k.SetBridgeContractAddress(ctx, types.BridgeContractAddress{ChainId: "ethereum", Address: bridgeAddress})

			contractAddress := sdk.AccAddress([]byte("contract_addr______")).String()
			require.NoError(t, k.SetWrappedTokenContract(ctx, types.BridgeWrappedTokenContract{
				ChainId:                "ethereum",
				ContractAddress:        "0x4444444444444444444444444444444444444444",
				WrappedContractAddress: contractAddress,
			}))
			ms := keeper.NewMsgServerWithWasmKeeper(k, mockWasmKeeper{
				GetContractInfoFn: func(context.Context, sdk.AccAddress) *wasmtypes.ContractInfo {
					return &wasmtypes.ContractInfo{CodeID: 1}
				},
			})

			resp, err := ms.RequestBridgeWithdrawal(ctx, &types.MsgRequestBridgeWithdrawal{
				Creator:                  contractAddress,
				Amount:                   "100",
				DestinationAddress:       "0x3333333333333333333333333333333333333333",
				DestinationBridgeAddress: bridgeAddress,
			})
			require.NoError(t, err)
			require.Equal(t, expectedEpoch, resp.EpochIndex)
			requests, err := blsK.ListActiveSigningRequests(ctx, expectedEpoch)
			require.NoError(t, err)
			require.Len(t, requests, 1)
			require.Equal(t, expectedEpoch, requests[0].CurrentEpochId)
		})
	}
}

func TestRequestBridgeMint_RollsBackWhenFallbackExpired(t *testing.T) {
	k, ms, ctx, mocks := setupKeeperWithMocks(t)
	setBridgeSigningFallbackEpochs(t, k, ctx)
	blsK, ok := k.BlsKeeper.(blskeeper.Keeper)
	require.True(t, ok)
	require.NoError(t, blsK.SetEpochBLSData(ctx, blstypes.EpochBLSData{
		EpochId:  9,
		DkgPhase: blstypes.DKGPhase_DKG_PHASE_FAILED,
	}))
	bridgeAddress := "0x2222222222222222222222222222222222222222"
	k.SetBridgeContractAddress(ctx, types.BridgeContractAddress{ChainId: "ethereum", Address: bridgeAddress})

	signer := sdk.MustAccAddressFromBech32(testutil.Creator)
	mocks.AccountKeeper.EXPECT().HasAccount(ctx, signer).Return(true)
	mocks.BankViewKeeper.EXPECT().SpendableCoin(ctx, signer, types.BaseCoin).Return(sdk.NewCoin(types.BaseCoin, math.NewInt(1000)))
	mocks.BankKeeper.EXPECT().SendCoinsFromAccountToModule(ctx, signer, types.BridgeEscrowAccName, gomock.Any(), "bridge_escrow").Return(nil)
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(ctx, types.BridgeEscrowAccName, signer, gomock.Any(), "bridge_release").Return(nil)

	_, err := ms.RequestBridgeMint(ctx, &types.MsgRequestBridgeMint{
		Creator:                  testutil.Creator,
		Amount:                   "100",
		DestinationAddress:       "0x3333333333333333333333333333333333333333",
		ChainId:                  "ethereum",
		DestinationBridgeAddress: bridgeAddress,
	})
	require.ErrorContains(t, err, "no eligible BLS signing epoch")
}
