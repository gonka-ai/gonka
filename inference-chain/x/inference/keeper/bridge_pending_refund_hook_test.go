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
	"github.com/productscience/inference/x/inference/keeper"
	inference "github.com/productscience/inference/x/inference/module"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// requestSingleAttemptSignature opens a threshold signing request that fails
// terminally at its first deadline, and returns the BLS keeper and the context
// at that deadline.
func requestSingleAttemptSignature(t *testing.T, k keeper.Keeper, ctx sdk.Context, requestID []byte, epochID uint64) (blskeeper.Keeper, sdk.Context) {
	t.Helper()
	blsK, ok := k.BlsKeeper.(blskeeper.Keeper)
	require.True(t, ok)

	params, err := blsK.GetParams(ctx)
	require.NoError(t, err)
	params.MaxSigningAttempts = 1
	require.NoError(t, blsK.SetParams(ctx, params))
	require.NoError(t, blsK.SetEpochBLSData(ctx, blstypes.EpochBLSData{
		EpochId:        epochID,
		DkgPhase:       blstypes.DKGPhase_DKG_PHASE_SIGNED,
		GroupPublicKey: []byte{1},
	}))
	require.NoError(t, blsK.RequestThresholdSignature(ctx, blstypes.SigningData{
		CurrentEpochId: epochID,
		ChainId:        bytes.Repeat([]byte{0x71}, 32),
		RequestId:      requestID,
		Data:           [][]byte{bytes.Repeat([]byte{0x72}, 32)},
	}))

	request, err := blsK.GetSigningStatus(ctx, requestID)
	require.NoError(t, err)
	return blsK, ctx.WithBlockHeight(request.DeadlineBlockHeight)
}

// expireSigningRequest opens a single-attempt signing request and runs the
// deadline sweep so the request ends EXPIRED. No hooks are installed.
func expireSigningRequest(t *testing.T, k keeper.Keeper, ctx sdk.Context, requestID []byte, epochID uint64) sdk.Context {
	t.Helper()
	blsK, expiryCtx := requestSingleAttemptSignature(t, k, ctx, requestID, epochID)
	require.NoError(t, blsK.ProcessThresholdSigningDeadlines(expiryCtx))
	request, err := blsK.GetSigningStatus(expiryCtx, requestID)
	require.NoError(t, err)
	require.Equal(t, blstypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_EXPIRED, request.Status)
	return expiryCtx
}

func hasEvent(ctx sdk.Context, eventType string) bool {
	for _, event := range ctx.EventManager().Events() {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

// The path the bug report expects end to end: a mint is pending while its
// signing request is still open, the deadline sweep expires the request, and
// the installed inference hook refunds the escrow and closes the request.
func TestBlsFailureHookAutoRefundsPendingMint(t *testing.T) {
	k, _, ctx, mocks := setupKeeperWithMocks(t)
	requestID := bytes.Repeat([]byte{0x46}, 32)
	requestKey := hex.EncodeToString(requestID)

	blsK, expiryCtx := requestSingleAttemptSignature(t, k, ctx, requestID, 901)
	require.NoError(t, blsK.SetHooks(blstypes.NewMultiBlsHooks(blstypes.BlsHooksWrapper{BlsHooks: inference.NewBlsHooks(k)})))

	require.NoError(t, k.BridgeMintRefundsMap.Set(ctx, requestKey, types.MsgRequestBridgeMint{
		Creator:            testutil.Creator,
		Amount:             "1000",
		DestinationAddress: "0xabc",
		ChainId:            "ethereum",
	}))

	creatorAddr, err := sdk.AccAddressFromBech32(testutil.Creator)
	require.NoError(t, err)
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.BridgeEscrowAccName, creatorAddr,
			sdk.NewCoins(sdk.NewCoin(types.BaseCoin, math.NewInt(1000))), "bridge_release").
		Return(nil).
		Times(1)

	require.NoError(t, blsK.ProcessThresholdSigningDeadlines(expiryCtx))

	request, err := blsK.GetSigningStatus(expiryCtx, requestID)
	require.NoError(t, err)
	require.Equal(t, blstypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_CANCELLED, request.Status)

	_, err = k.BridgeMintRefundsMap.Get(expiryCtx, requestKey)
	require.ErrorIs(t, err, collections.ErrNotFound)

	require.True(t, hasEvent(expiryCtx, "bridge_operation_auto_refunded"))
	require.True(t, hasEvent(expiryCtx, "inference.bls.EventThresholdSigningFailed"))
}

// A refund releases the escrow backing a bridge operation, so it must only run
// for a signing request that ended in failure. Every other state is refused
// before any bank call, and the pending entry stays for the manual cancel path.
func TestProcessAutoRefundForFailedBridgeOperation_RefusesUnlessSigningFailed(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T, k keeper.Keeper, ctx sdk.Context, requestID []byte) sdk.Context
		status  string
	}{
		{
			name:    "no signing request",
			prepare: func(_ *testing.T, _ keeper.Keeper, ctx sdk.Context, _ []byte) sdk.Context { return ctx },
			status:  "failed to load signing request",
		},
		{
			name: "signing still collecting signatures",
			prepare: func(t *testing.T, k keeper.Keeper, ctx sdk.Context, requestID []byte) sdk.Context {
				requestSingleAttemptSignature(t, k, ctx, requestID, 902)
				return ctx
			},
			status: blstypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES.String(),
		},
		{
			name: "signing cancelled",
			prepare: func(t *testing.T, k keeper.Keeper, ctx sdk.Context, requestID []byte) sdk.Context {
				expiryCtx := expireSigningRequest(t, k, ctx, requestID, 903)
				blsK, ok := k.BlsKeeper.(blskeeper.Keeper)
				require.True(t, ok)
				require.NoError(t, blsK.CancelThresholdSignature(expiryCtx, requestID))
				return expiryCtx
			},
			status: blstypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_CANCELLED.String(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// No bank expectations: gomock fails the test on any refund call.
			k, _, ctx, _ := setupKeeperWithMocks(t)
			requestID := bytes.Repeat([]byte{0x47}, 32)
			requestKey := hex.EncodeToString(requestID)

			ctx = tt.prepare(t, k, ctx, requestID)
			require.NoError(t, k.BridgeMintRefundsMap.Set(ctx, requestKey, types.MsgRequestBridgeMint{
				Creator:            testutil.Creator,
				Amount:             "1000",
				DestinationAddress: "0xabc",
				ChainId:            "ethereum",
			}))

			closeRetry, err := k.ProcessAutoRefundForFailedBridgeOperation(ctx, requestID, "deadline expired")
			require.ErrorContains(t, err, tt.status)
			require.False(t, closeRetry)

			_, err = k.BridgeMintRefundsMap.Get(ctx, requestKey)
			require.NoError(t, err, "pending entry must survive a refused refund")
			require.False(t, hasEvent(ctx, "bridge_operation_auto_refunded"))
		})
	}
}
