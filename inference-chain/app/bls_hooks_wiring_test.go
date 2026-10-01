package app_test

import (
	"bytes"
	"encoding/hex"
	"testing"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	blstypes "github.com/productscience/inference/x/bls/types"
	inferencemodule "github.com/productscience/inference/x/inference/module"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestBlsHooksInstalledByAppWiring(t *testing.T) {
	testApp := createTestApp(t)

	hooks, ok := testApp.BlsKeeper.Hooks().(blstypes.MultiBlsHooks)
	require.True(t, ok)
	require.Len(t, hooks, 1)

	wrapper, ok := hooks[0].(blstypes.BlsHooksWrapper)
	require.True(t, ok)
	require.IsType(t, inferencemodule.BlsHooks{}, wrapper.BlsHooks)
}

func TestBlsFailureRunsInstalledHookAndRefundsPendingMint(t *testing.T) {
	testApp := createTestApp(t)
	ctx := testApp.NewUncachedContext(false, cmtproto.Header{
		Height:  testApp.LastBlockHeight(),
		ChainID: TallyTestChainID,
	})

	creatorPriv := secp256k1.GenPrivKey()
	creatorAddr := sdk.AccAddress(creatorPriv.PubKey().Address())
	creatorAccount := testApp.AccountKeeper.NewAccountWithAddress(ctx, creatorAddr)
	testApp.AccountKeeper.SetAccount(ctx, creatorAccount)

	refundCoins := sdk.NewCoins(sdk.NewCoin(inferencetypes.BaseCoin, math.NewInt(1_000)))
	require.NoError(t, testApp.BankKeeper.MintCoins(ctx, inferencetypes.TopRewardPoolAccName, refundCoins))
	require.NoError(t, testApp.BankKeeper.SendCoinsFromModuleToModule(
		ctx,
		inferencetypes.TopRewardPoolAccName,
		inferencetypes.BridgeEscrowAccName,
		refundCoins,
	))

	const epochID = uint64(991)
	params, err := testApp.BlsKeeper.GetParams(ctx)
	require.NoError(t, err)
	params.MaxSigningAttempts = 1
	require.NoError(t, testApp.BlsKeeper.SetParams(ctx, params))
	require.NoError(t, testApp.BlsKeeper.SetEpochBLSData(ctx, blstypes.EpochBLSData{
		EpochId:        epochID,
		DkgPhase:       blstypes.DKGPhase_DKG_PHASE_SIGNED,
		GroupPublicKey: []byte{1},
	}))

	requestID := bytes.Repeat([]byte{0x91}, 32)
	require.NoError(t, testApp.BlsKeeper.RequestThresholdSignature(ctx, blstypes.SigningData{
		CurrentEpochId: epochID,
		ChainId:        bytes.Repeat([]byte{0x92}, 32),
		RequestId:      requestID,
		Data:           [][]byte{bytes.Repeat([]byte{0x93}, 32)},
	}))

	requestKey := hex.EncodeToString(requestID)
	require.NoError(t, testApp.InferenceKeeper.BridgeMintRefundsMap.Set(ctx, requestKey, inferencetypes.MsgRequestBridgeMint{
		Creator:            creatorAddr.String(),
		Amount:             refundCoins[0].Amount.String(),
		DestinationAddress: "0xabc",
		ChainId:            "ethereum",
	}))

	request, err := testApp.BlsKeeper.GetSigningStatus(ctx, requestID)
	require.NoError(t, err)
	expiryCtx := ctx.WithBlockHeight(request.DeadlineBlockHeight)
	require.NoError(t, testApp.BlsKeeper.ProcessThresholdSigningDeadlines(expiryCtx))

	cancelledRequest, err := testApp.BlsKeeper.GetSigningStatus(expiryCtx, requestID)
	require.NoError(t, err)
	require.Equal(t, blstypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_CANCELLED, cancelledRequest.Status)

	_, err = testApp.InferenceKeeper.BridgeMintRefundsMap.Get(expiryCtx, requestKey)
	require.ErrorIs(t, err, collections.ErrNotFound)
	require.Equal(t, refundCoins[0], testApp.BankKeeper.GetBalance(expiryCtx, creatorAddr, inferencetypes.BaseCoin))
	require.True(t, testApp.InferenceKeeper.GetBridgeEscrowBalance(expiryCtx, inferencetypes.BaseCoin).IsZero())

	foundRefundEvent := false
	for _, event := range expiryCtx.EventManager().Events() {
		if event.Type == "bridge_operation_auto_refunded" {
			foundRefundEvent = true
			break
		}
	}
	require.True(t, foundRefundEvent)
}
