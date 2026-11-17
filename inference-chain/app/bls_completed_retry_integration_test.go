package app_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math/big"
	"testing"

	"cosmossdk.io/core/appmodule"
	"cosmossdk.io/math"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fp"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/hash_to_curve"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/productscience/inference/app"
	blstypes "github.com/productscience/inference/x/bls/types"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// The real inference hook removes pending refund records. Wrap that installed
// hook with a controlled failure after it runs, so the test exercises the real
// app wiring and cache rollback without adding fault injection to production.
type failingCompletionHook struct {
	blstypes.BlsHooks
	err   error
	calls int
}

const completionHookEvent = "test_bridge_completion_hook"

func (h *failingCompletionHook) AfterThresholdSigningCompleted(ctx context.Context, id []byte, epoch uint64) error {
	h.calls++
	if err := h.BlsHooks.AfterThresholdSigningCompleted(ctx, id, epoch); err != nil {
		return err
	}
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(completionHookEvent))
	return h.err
}

type completedRetryFixture struct {
	app        *app.App
	ctx        sdk.Context
	hook       *failingCompletionHook
	requestID  []byte
	creator    sdk.AccAddress
	refundCoin sdk.Coin
	finalSig   []byte
}

func newCompletedRetryFixture(t *testing.T) completedRetryFixture {
	t.Helper()
	testApp := createTestApp(t)
	ctx := testApp.NewUncachedContext(false, cmtproto.Header{
		Height: testApp.LastBlockHeight(), ChainID: TallyTestChainID,
	})
	hooks, ok := testApp.BlsKeeper.Hooks().(blstypes.MultiBlsHooks)
	require.True(t, ok)
	require.Len(t, hooks, 1, "the dependency must install the real inference hook")
	hook := &failingCompletionHook{BlsHooks: hooks[0], err: errors.New("completion hook unavailable")}
	hooks[0] = hook

	creator := authtypes.NewModuleAddress("completion-retry-user")
	testApp.AccountKeeper.SetAccount(ctx, testApp.AccountKeeper.NewAccountWithAddress(ctx, creator))
	refundCoin := sdk.NewCoin(inferencetypes.BaseCoin, math.NewInt(1_000))
	coins := sdk.NewCoins(refundCoin)
	require.NoError(t, testApp.BankKeeper.MintCoins(ctx, inferencetypes.TopRewardPoolAccName, coins))
	require.NoError(t, testApp.BankKeeper.SendCoinsFromModuleToModule(ctx,
		inferencetypes.TopRewardPoolAccName, inferencetypes.BridgeEscrowAccName, coins))

	const epochID = uint64(992)
	// One slot with a constant secret polynomial is a complete threshold key.
	// Submit an actual signature through AddPartialSignature: this must reach
	// the production aggregation path that enqueues a failed completion hook.
	secret := big.NewInt(7)
	_, _, _, generator := bls12381.Generators()
	var publicKey bls12381.G2Affine
	publicKey.ScalarMultiplication(&generator, secret)
	publicKeyBytes := publicKey.Bytes()
	require.NoError(t, testApp.BlsKeeper.SetEpochBLSData(ctx, blstypes.EpochBLSData{
		EpochId: epochID, DkgPhase: blstypes.DKGPhase_DKG_PHASE_SIGNED,
		ITotalSlots: 1, TSlotsDegree: 0,
		GroupPublicKey: publicKeyBytes[:], SlotPublicKeys: [][]byte{publicKeyBytes[:]},
		Participants: []blstypes.BLSParticipantInfo{{
			Address: creator.String(), PercentageWeight: math.LegacyOneDec(),
			SlotStartIndex: 0, SlotEndIndex: 0,
		}},
	}))
	requestID := bytes.Repeat([]byte{0x94}, 32)
	require.NoError(t, testApp.BlsKeeper.RequestThresholdSignature(ctx, blstypes.SigningData{
		CurrentEpochId: epochID, RequestId: requestID,
		ChainId: bytes.Repeat([]byte{0x95}, 32), Data: [][]byte{bytes.Repeat([]byte{0x96}, 32)},
	}))
	require.NoError(t, testApp.InferenceKeeper.BridgeMintRefundsMap.Set(ctx, hex.EncodeToString(requestID), inferencetypes.MsgRequestBridgeMint{
		Creator: creator.String(), Amount: refundCoin.Amount.String(),
		DestinationAddress: "0xabc", ChainId: "ethereum",
	}))
	request, err := testApp.BlsKeeper.GetSigningStatus(ctx, requestID)
	require.NoError(t, err)
	var u fp.Element
	u.SetBytes(request.MessageHash)
	mapped := bls12381.MapToCurve1(&u)
	hash_to_curve.G1Isogeny(&mapped.X, &mapped.Y)
	var message, signature bls12381.G1Affine
	message.ClearCofactor(&mapped)
	signature.ScalarMultiplication(&message, secret)
	signatureBytes := signature.Bytes()
	ctx = ctx.WithEventManager(sdk.NewEventManager())
	require.NoError(t, testApp.BlsKeeper.AddPartialSignature(ctx, requestID, []uint32{0}, signatureBytes[:], creator.String()))
	request, err = testApp.BlsKeeper.GetSigningStatus(ctx, requestID)
	require.NoError(t, err)
	require.Equal(t, blstypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COMPLETED, request.Status)
	require.NotEmpty(t, request.FinalSignature)
	require.Equal(t, 1, hook.calls)
	require.Equal(t, 1, eventCount(ctx, "inference.bls.EventThresholdSigningCompleted"))
	require.Zero(t, eventCount(ctx, completionHookEvent), "failed hook events must roll back")

	f := completedRetryFixture{testApp, ctx, hook, requestID, creator, refundCoin, request.FinalSignature}
	f.assertPending(t, true)
	f.assertQueued(t, 0)
	return f
}

func (f completedRetryFixture) endBlock(t *testing.T, block int64) sdk.Context {
	t.Helper()
	ctx := f.ctx.WithBlockHeight(f.ctx.BlockHeight() + block).WithEventManager(sdk.NewEventManager())
	module, ok := f.app.ModuleManager.Modules[blstypes.ModuleName].(appmodule.HasEndBlocker)
	require.True(t, ok)
	require.NoError(t, module.EndBlock(ctx))
	return ctx
}

func (f completedRetryFixture) assertPending(t *testing.T, want bool) {
	t.Helper()
	present, err := f.app.InferenceKeeper.BridgeMintRefundsMap.Has(f.ctx, hex.EncodeToString(f.requestID))
	require.NoError(t, err)
	require.Equal(t, want, present)
	require.True(t, f.app.BankKeeper.GetBalance(f.ctx, f.creator, inferencetypes.BaseCoin).IsZero())
	require.Equal(t, f.refundCoin, f.app.InferenceKeeper.GetBridgeEscrowBalance(f.ctx, inferencetypes.BaseCoin))
}

func (f completedRetryFixture) assertQueued(t *testing.T, attempts uint32) {
	t.Helper()
	value := f.ctx.KVStore(f.app.GetKey(blstypes.StoreKey)).Get(blstypes.CompletedPostProcessRetryKey(f.requestID))
	require.NotNil(t, value)
	if attempts == 0 {
		require.Empty(t, value, "fresh/legacy entries start without a counter")
	} else {
		require.Len(t, value, 4)
		require.Equal(t, attempts, binary.BigEndian.Uint32(value))
	}
}

func (f completedRetryFixture) assertCompleted(t *testing.T) {
	t.Helper()
	request, err := f.app.BlsKeeper.GetSigningStatus(f.ctx, f.requestID)
	require.NoError(t, err)
	require.Equal(t, blstypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COMPLETED, request.Status)
	require.Equal(t, f.finalSig, request.FinalSignature)
}

func eventCount(ctx sdk.Context, kind string) int {
	count := 0
	for _, event := range ctx.EventManager().Events() {
		if event.Type == kind {
			count++
		}
	}
	return count
}

func TestBlsCompletedRetryThroughApp_EndBlockRecoversWithoutRefund(t *testing.T) {
	f := newCompletedRetryFixture(t)
	ctx := f.endBlock(t, 1)
	f.assertQueued(t, 1)
	f.assertPending(t, true)
	require.Zero(t, eventCount(ctx, completionHookEvent))

	f.hook.err = nil
	ctx = f.endBlock(t, 2)
	require.False(t, ctx.KVStore(f.app.GetKey(blstypes.StoreKey)).Has(blstypes.CompletedPostProcessRetryKey(f.requestID)))
	f.assertPending(t, false)
	f.assertCompleted(t)
	require.Equal(t, 1, eventCount(ctx, completionHookEvent), "CacheContext must forward success events only once")
	require.Zero(t, eventCount(ctx, "bridge_operation_auto_refunded"))
	require.Equal(t, 3, f.hook.calls)

	ctx = f.endBlock(t, 3)
	require.Equal(t, 3, f.hook.calls, "success must remove the retry entry")
	require.Zero(t, eventCount(ctx, completionHookEvent))
}

func TestBlsCompletedRetryThroughApp_AbandonsOnFiftiethFailureWithoutRefund(t *testing.T) {
	f := newCompletedRetryFixture(t)
	for attempt := uint32(1); attempt < 50; attempt++ {
		ctx := f.endBlock(t, int64(attempt))
		f.assertQueued(t, attempt)
		f.assertPending(t, true)
		require.Zero(t, eventCount(ctx, "completed_post_process_retry_abandoned"))
		require.Zero(t, eventCount(ctx, completionHookEvent))
	}
	ctx := f.endBlock(t, 50)
	require.False(t, ctx.KVStore(f.app.GetKey(blstypes.StoreKey)).Has(blstypes.CompletedPostProcessRetryKey(f.requestID)))
	f.assertPending(t, true)
	f.assertCompleted(t)
	require.Equal(t, 51, f.hook.calls, "one initial call plus fifty retries")
	require.Equal(t, 1, eventCount(ctx, "completed_post_process_retry_abandoned"))
	for _, event := range ctx.EventManager().Events() {
		if event.Type != "completed_post_process_retry_abandoned" {
			continue
		}
		attrs := make(map[string]string)
		for _, attr := range event.Attributes {
			attrs[attr.Key] = attr.Value
		}
		require.Equal(t, hex.EncodeToString(f.requestID), attrs["request_id"])
		require.Equal(t, "992", attrs["current_epoch_id"])
		require.Equal(t, "50", attrs["attempts"])
	}
	closed, err := f.app.InferenceKeeper.ProcessAutoRefundForFailedBridgeOperation(ctx, f.requestID, "retry exhausted")
	require.Error(t, err)
	require.False(t, closed)
	require.Error(t, f.app.BlsKeeper.CancelThresholdSignature(ctx, f.requestID))
	f.assertPending(t, true)
	f.assertCompleted(t)
	require.Zero(t, eventCount(ctx, "bridge_operation_auto_refunded"))

	f.hook.err = nil
	ctx = f.endBlock(t, 51)
	require.Equal(t, 51, f.hook.calls, "an abandoned entry must not run again")
	require.Zero(t, eventCount(ctx, "completed_post_process_retry_abandoned"))
	f.assertPending(t, true)
}
