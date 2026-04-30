package tx_manager

import (
	"testing"

	"cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/auth/tx"
	"github.com/stretchr/testify/require"

	inferencepkg "github.com/productscience/inference/x/inference"
	blstypes "github.com/productscience/inference/x/bls/types"
	collateraltypes "github.com/productscience/inference/x/collateral/types"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

// TestEstimateMsgGas_KnownTypes pins the per-msg-type estimates so that
// any silent change to the lookup table fails CI rather than shipping a
// surprise. Numbers come from gas_estimate.go constants; if you intentionally
// retune a value, update the test in the same commit.
func TestEstimateMsgGas_KnownTypes(t *testing.T) {
	cases := []struct {
		name string
		msg  sdk.Msg
		want uint64
	}{
		// Inference lifecycle (all bypass-exempt, but still sized).
		{"MsgStartInference", &inferencetypes.MsgStartInference{}, gasStartInference},
		{"MsgFinishInference", &inferencetypes.MsgFinishInference{}, gasFinishInference},
		{"MsgValidation", &inferencetypes.MsgValidation{}, gasValidation},
		{"MsgInvalidateInference", &inferencetypes.MsgInvalidateInference{}, gasInvalidateInference},
		{"MsgRevalidateInference", &inferencetypes.MsgRevalidateInference{}, gasRevalidateInference},

		// PoC duty.
		{"MsgSubmitPocBatch", &inferencetypes.MsgSubmitPocBatch{}, gasSubmitPocBatch},
		{"MsgSubmitPocValidationsV2", &inferencetypes.MsgSubmitPocValidationsV2{}, gasSubmitPocValidationsV2},

		// Routine host duties (now bypass-exempt).
		{"MsgSubmitHardwareDiff", &inferencetypes.MsgSubmitHardwareDiff{}, gasSubmitHardwareDiff},
		{"MsgClaimRewards", &inferencetypes.MsgClaimRewards{}, gasClaimRewards},

		// Other host operations.
		{"MsgSubmitSeed", &inferencetypes.MsgSubmitSeed{}, gasSubmitSeed},
		{"MsgSubmitNewParticipant", &inferencetypes.MsgSubmitNewParticipant{}, gasSubmitNewParticipant},
		{"MsgSubmitNewUnfundedParticipant", &inferencetypes.MsgSubmitNewUnfundedParticipant{}, gasSubmitNewUnfundedParticipant},
		{"MsgBridgeExchange", &inferencetypes.MsgBridgeExchange{}, gasBridgeExchange},

		// BLS DKG (bypass-exempt).
		{"MsgSubmitDealerPart", &blstypes.MsgSubmitDealerPart{}, gasSubmitDealerPart},
		{"MsgSubmitVerificationVector", &blstypes.MsgSubmitVerificationVector{}, gasSubmitVerificationVector},
		{"MsgSubmitGroupKeyValidationSignature", &blstypes.MsgSubmitGroupKeyValidationSignature{}, gasSubmitGroupKeyValidationSignature},
		{"MsgRespondDealerComplaints", &blstypes.MsgRespondDealerComplaints{}, gasRespondDealerComplaints},
		{"MsgRequestThresholdSignature", &blstypes.MsgRequestThresholdSignature{}, gasRequestThresholdSignature},
		{"MsgSubmitPartialSignature", &blstypes.MsgSubmitPartialSignature{}, gasSubmitPartialSignature},

		// Collateral.
		{"MsgDepositCollateral", &collateraltypes.MsgDepositCollateral{}, gasDepositCollateral},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := estimateMsgGas(tc.msg)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestEstimateMsgGas_DefaultFallback confirms that an unknown message type
// (one not in the switch) falls through to the conservative default and is
// flagged as not explicit.
func TestEstimateMsgGas_DefaultFallback(t *testing.T) {
	// A message type that intentionally isn't in the per-type switch.
	type unknownMsg struct{ sdk.Msg }
	got, explicit := lookupMsgGas(&unknownMsg{})
	require.Equal(t, gasDefaultEstimate, got)
	require.False(t, explicit)
	// And the public estimateMsgGas wrapper returns the same value.
	require.Equal(t, gasDefaultEstimate, estimateMsgGas(&unknownMsg{}))
}

// TestEstimateMsgGas_PoCV2_LinearInCount asserts the on-chain formula is
// mirrored: gas should grow base + sum(entry.Count) * per_count.
func TestEstimateMsgGas_PoCV2_LinearInCount(t *testing.T) {
	zero := &inferencetypes.MsgPoCV2StoreCommit{}
	require.Equal(t, gasPoCV2Base, estimateMsgGas(zero), "no entries = base only")

	for _, count := range []uint32{1, 100, 10_000, 1_000_000} {
		msg := &inferencetypes.MsgPoCV2StoreCommit{
			Entries: []*inferencetypes.PoCV2CommitEntry{{Count: count}},
		}
		want := gasPoCV2Base + uint64(count)*gasPoCV2PerCount
		require.Equal(t, want, estimateMsgGas(msg),
			"count=%d should yield base + count*per_count", count)
	}

	// Multi-entry: per_count applies to summed Count.
	multi := &inferencetypes.MsgPoCV2StoreCommit{
		Entries: []*inferencetypes.PoCV2CommitEntry{{Count: 100}, {Count: 200}, {Count: 300}},
	}
	want := gasPoCV2Base + uint64(600)*gasPoCV2PerCount
	require.Equal(t, want, estimateMsgGas(multi))
}

// TestEstimateMsgGas_MLNodeDistribution_LinearInNodes asserts the gas grows
// linearly with the total number of node entries summed across all model
// entries.
func TestEstimateMsgGas_MLNodeDistribution_LinearInNodes(t *testing.T) {
	zero := &inferencetypes.MsgMLNodeWeightDistribution{}
	require.Equal(t, gasMLNodeBase, estimateMsgGas(zero), "no entries = base only")

	// 5 nodes spread across 2 model entries.
	msg := &inferencetypes.MsgMLNodeWeightDistribution{
		Entries: []*inferencetypes.MLNodeDistributionEntry{
			{Weights: []*inferencetypes.MLNodeWeight{{}, {}, {}}},
			{Weights: []*inferencetypes.MLNodeWeight{{}, {}}},
		},
	}
	want := gasMLNodeBase + uint64(5)*gasMLNodePerNode
	require.Equal(t, want, estimateMsgGas(msg))
}

// TestEstimateBatchGas_SumsPlusOverhead confirms the batch-level estimator
// adds the tx-level overhead and sums per-msg estimates.
func TestEstimateBatchGas_SumsPlusOverhead(t *testing.T) {
	msgs := []sdk.Msg{
		&inferencetypes.MsgFinishInference{}, // gasFinishInference
		&inferencetypes.MsgFinishInference{}, // gasFinishInference
		&inferencetypes.MsgSubmitSeed{},      // gasSubmitSeed
	}
	want := txOverheadGas + 2*gasFinishInference + gasSubmitSeed
	require.Equal(t, want, estimateBatchGas(msgs, 0))
}

// TestEstimateBatchGas_RetryMultiplier asserts each retry attempt doubles
// gasWanted up to the BatchGasLimit ceiling. This is what lets an OOG-on-
// underestimate eventually succeed instead of looping at the same gas.
func TestEstimateBatchGas_RetryMultiplier(t *testing.T) {
	msgs := []sdk.Msg{&inferencetypes.MsgSubmitSeed{}}
	base := estimateBatchGas(msgs, 0)

	for attempt := 1; attempt <= 5; attempt++ {
		expected := base
		for i := 0; i < attempt; i++ {
			expected = uint64(float64(expected) * gasRetryMultiplier)
		}
		got := estimateBatchGas(msgs, attempt)
		if expected > BatchGasLimit {
			require.Equal(t, uint64(BatchGasLimit), got, "attempt=%d should cap at BatchGasLimit", attempt)
		} else {
			require.Equal(t, expected, got, "attempt=%d should double base estimate", attempt)
		}
	}
}

// TestEstimateBatchGas_CapsAtBatchGasLimit confirms we never request more
// gas than the chain's NetworkDutyFeeBypassDecorator GasCap can accommodate
// (currently 3B; BatchGasLimit is 1B, well within).
func TestEstimateBatchGas_CapsAtBatchGasLimit(t *testing.T) {
	// A large PoCV2 commit can naturally exceed BatchGasLimit at high count.
	huge := &inferencetypes.MsgPoCV2StoreCommit{
		Entries: []*inferencetypes.PoCV2CommitEntry{{Count: ^uint32(0)}}, // max uint32
	}
	got := estimateBatchGas([]sdk.Msg{huge}, 0)
	require.Equal(t, uint64(BatchGasLimit), got, "should cap, not overflow")

	// Also via retry escalation on a moderate batch.
	moderate := []sdk.Msg{&inferencetypes.MsgClaimRewards{}}
	got = estimateBatchGas(moderate, 100) // way more retries than realistic
	require.Equal(t, uint64(BatchGasLimit), got)
}

// TestEstimateBatchGas_EmptyBatch returns just the tx-level overhead — no
// crash on a zero-msg batch.
func TestEstimateBatchGas_EmptyBatch(t *testing.T) {
	require.Equal(t, txOverheadGas, estimateBatchGas(nil, 0))
	require.Equal(t, txOverheadGas, estimateBatchGas([]sdk.Msg{}, 0))
}

// newTestTxBuilder spins up a real TxBuilder backed by the standard cosmos
// proto codec. Real builder is cheaper to set up than to stub correctly,
// since TxBuilder is an interface with several internal-state methods.
func newTestTxBuilder(t *testing.T) client.TxBuilder {
	t.Helper()
	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)
	cfg := tx.NewTxConfig(cdc, tx.DefaultSignModes)
	return cfg.NewTxBuilder()
}

// TestApplyGasAndFee_SetsGasLimitAndZeroFee covers the v0.2.12 mainnet
// configuration: minGasPriceNgonka=0, so fees are always empty regardless
// of gasWanted, and the gas limit reflects the per-batch estimate.
func TestApplyGasAndFee_SetsGasLimitAndZeroFee(t *testing.T) {
	b := newTestTxBuilder(t)

	applyGasAndFee(b, 250_000, 0)
	require.Equal(t, uint64(250_000), b.GetTx().GetGas())
	require.True(t, b.GetTx().GetFee().IsZero(), "fees must be empty when minGasPrice=0")
}

// TestApplyGasAndFee_ComputesFeeFromMinGasPrice verifies that when
// min_gas_price is non-zero (post-v0.2.12), fee = gasWanted × minGasPrice.
func TestApplyGasAndFee_ComputesFeeFromMinGasPrice(t *testing.T) {
	b := newTestTxBuilder(t)

	const gasWanted, minGasPrice = uint64(250_000), int64(10)
	applyGasAndFee(b, gasWanted, minGasPrice)

	require.Equal(t, gasWanted, b.GetTx().GetGas())
	fees := b.GetTx().GetFee()
	require.Len(t, fees, 1)
	require.Equal(t, "ngonka", fees[0].Denom)
	expected := math.NewIntFromUint64(gasWanted).MulRaw(minGasPrice)
	require.True(t, fees[0].Amount.Equal(expected),
		"fee should be gasWanted × minGasPrice, got %s expected %s", fees[0].Amount, expected)
}

// TestApplyGasAndFee_CapsAtBatchGasLimit confirms we never set gasWanted
// above the chain's NetworkDutyFeeBypassDecorator GasCap can accommodate.
// Both 0 and "exceeds limit" should clamp to BatchGasLimit.
func TestApplyGasAndFee_CapsAtBatchGasLimit(t *testing.T) {
	// Zero -> use BatchGasLimit (defensive default).
	b := newTestTxBuilder(t)
	applyGasAndFee(b, 0, 0)
	require.Equal(t, uint64(BatchGasLimit), b.GetTx().GetGas())

	// Above limit -> cap at BatchGasLimit.
	b2 := newTestTxBuilder(t)
	applyGasAndFee(b2, BatchGasLimit*2, 0)
	require.Equal(t, uint64(BatchGasLimit), b2.GetTx().GetGas())
}

// TestBroadcastMessages_EmptyBatch_NoOp pins the early-return guard at
// the top of broadcastMessagesAtAttempt: a zero-message batch returns
// (nil, zero-time, nil) without touching the factory or the wire. Without
// this guard a refactor could route an empty batch into BuildUnsignedTx,
// which produces a confusing chain-side decode error far from the
// cause. The guard fields none of the manager's other state, so a zero-
// value manager is enough to exercise the path.
func TestBroadcastMessages_EmptyBatch_NoOp(t *testing.T) {
	m := &manager{}

	resp, ts, err := m.BroadcastMessages("test-id")
	require.NoError(t, err)
	require.Nil(t, resp)
	require.True(t, ts.IsZero())

	// Same for the internal helper that retry uses, with a non-zero attempt.
	resp, ts, err = m.broadcastMessagesAtAttempt("test-id", 5, nil)
	require.NoError(t, err)
	require.Nil(t, resp)
	require.True(t, ts.IsZero())

	resp, ts, err = m.broadcastMessagesAtAttempt("test-id", 0, []sdk.Msg{})
	require.NoError(t, err)
	require.Nil(t, resp)
	require.True(t, ts.IsZero())
}

// TestEstimateMsgGas_AllInferenceOperationKeyPermsHaveExplicitEstimate
// catches the case where someone adds a new message type to the warm key's
// authz permission list (InferenceOperationKeyPerms) but forgets to add it
// to the gas estimator switch. Without this guard, the new message would
// silently fall through to gasDefaultEstimate, which may not be enough to
// cover its real consumption.
//
// We use lookupMsgGas (which returns explicit=true iff the type has a
// case in the switch) rather than comparing values, because several
// legitimate per-type estimates happen to coincide with gasDefaultEstimate.
//
// If this test fails, add the missing case to lookupMsgGas in
// gas_estimate.go with a number sized from a fresh mainnet sample.
func TestEstimateMsgGas_AllInferenceOperationKeyPermsHaveExplicitEstimate(t *testing.T) {
	for _, msg := range inferencepkg.InferenceOperationKeyPerms {
		t.Run(sdk.MsgTypeURL(msg), func(t *testing.T) {
			_, explicit := lookupMsgGas(msg)
			require.True(t, explicit,
				"%T is in InferenceOperationKeyPerms but has no explicit gas estimate; "+
					"add a case to lookupMsgGas in gas_estimate.go", msg)
		})
	}
}
