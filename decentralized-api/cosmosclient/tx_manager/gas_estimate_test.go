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

	blstypes "github.com/productscience/inference/x/bls/types"
	collateraltypes "github.com/productscience/inference/x/collateral/types"
	inferencepkg "github.com/productscience/inference/x/inference"
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

func TestEstimateStoreCommitGas_UsesDeltaNotTotal(t *testing.T) {
	msg := &inferencetypes.MsgPoCV2StoreCommit{
		Entries: []*inferencetypes.PoCV2CommitEntry{{ModelId: "m1", Count: 200}},
	}
	first := estimateMsgGasHinted(msg, GasHints{})
	require.Equal(t, gasPoCV2Base+200*gasPoCV2PerCount, first)

	second := estimateMsgGasHinted(msg, GasHints{StoreCommit: StoreCommitGas{Prev: map[string]uint32{"m1": 100}}})
	require.Equal(t, uint64(100)*gasPoCV2PerCount, second, "unhinted second commit uses delta 100, not total 200")
}

func TestEstimateStoreCommitGas_FeeTreeLoadedIncludesIntrinsicFloor(t *testing.T) {
	msg := &inferencetypes.MsgPoCV2StoreCommit{
		Entries: []*inferencetypes.PoCV2CommitEntry{{ModelId: "m1", Count: 200}},
	}
	loaded := GasHints{
		FeeTreeLoaded: true,
		StoreCommit: StoreCommitGas{
			HasRate:    true,
			PaddedRate: 100,
			HasBase:    true,
			PaddedBase: 500_000,
		},
	}
	require.Equal(t, gasStoreCommitIntrinsic+500_000+200*100, estimateMsgGasHinted(msg, loaded),
		"first commit is intrinsic + period base + total×rate")

	incremental := loaded
	incremental.StoreCommit.Prev = map[string]uint32{"m1": 199}
	require.Equal(t, gasStoreCommitIntrinsic+100, estimateMsgGasHinted(msg, incremental),
		"delta=1 must keep the intrinsic floor; surcharge is only 1×rate")

	zero := GasHints{
		FeeTreeLoaded: true,
		StoreCommit: StoreCommitGas{
			HasRate:    true,
			PaddedRate: 0,
			HasBase:    true,
			PaddedBase: 0,
		},
	}
	require.Equal(t, gasStoreCommitIntrinsic, estimateMsgGasHinted(msg, zero),
		"zero surcharge must not collapse the intrinsic floor")
}

func TestFeeTreeLoad_NilClearsPricing(t *testing.T) {
	fp := inferencetypes.DefaultFeeParams()
	fp.EnabledFeeGroups = []string{inferencetypes.FeeGroupEpoch}
	fp.Groups[0].MinGasPrice = 10

	c := newFeeTreeCache()
	c.Load(fp)
	require.Equal(t, int64(10), c.PriceForMsgs([]sdk.Msg{&inferencetypes.MsgPoCV2StoreCommit{}}))
	require.True(t, c.hints().StoreCommit.HasRate)

	c.Load(nil)
	h := c.hints()
	require.True(t, h.FeeTreeLoaded)
	require.False(t, h.StoreCommit.HasRate)
	require.False(t, h.StoreCommit.HasBase)
	require.Equal(t, int64(0), c.PriceForMsgs([]sdk.Msg{&inferencetypes.MsgPoCV2StoreCommit{}}))
}

func TestFeeTreeLoad_ZeroGasIsOptOutNotDefault(t *testing.T) {
	fp := inferencetypes.DefaultFeeParams()
	fp.Groups[0].Msgs[0].Base.Gas = 0
	fp.Groups[0].Msgs[0].GetStoredDelta().GasPerUnit = 0

	c := newFeeTreeCache()
	c.Load(fp)
	h := c.hints()
	require.True(t, h.StoreCommit.HasRate)
	require.True(t, h.StoreCommit.HasBase)
	require.True(t, h.FeeTreeLoaded)
	require.Equal(t, uint64(0), h.StoreCommit.PaddedRate)
	require.Equal(t, uint64(0), h.StoreCommit.PaddedBase)

	msg := &inferencetypes.MsgPoCV2StoreCommit{
		Entries: []*inferencetypes.PoCV2CommitEntry{{ModelId: "m1", Count: 200}},
	}
	require.Equal(t, gasStoreCommitIntrinsic, estimateMsgGasHinted(msg, h),
		"explicit zero rules must not fall back to 600k/150 defaults, but must keep the intrinsic floor")
}

func TestFeeTreeLoad_DefaultStoreCommitHeadroom(t *testing.T) {
	c := newFeeTreeCache()
	c.Load(inferencetypes.DefaultFeeParams())
	h := c.hints()
	require.Equal(t, uint64(150), h.StoreCommit.PaddedRate)
	require.Equal(t, uint64(600_000), h.StoreCommit.PaddedBase)
	require.Equal(t, uint64(100), h.StoreCommit.ChainRate)
	require.Equal(t, uint64(500_000), h.StoreCommit.ChainBase)
	rate, base, loaded := c.RawStoreCommitLeaf()
	require.True(t, loaded)
	require.Equal(t, uint64(100), rate)
	require.Equal(t, uint64(500_000), base)
}

func TestFeeTreeLoad_MeasuredIntrinsicSurvivesLoad(t *testing.T) {
	c := newFeeTreeCache()
	c.Load(inferencetypes.DefaultFeeParams())
	c.SetStoreCommitIntrinsic(105_000, 2)
	require.True(t, c.hints().StoreCommit.HasMeasured)
	require.Equal(t, uint64(105_000), c.hints().StoreCommit.MeasuredIntrinsic)
	require.Equal(t, uint(2), c.hints().StoreCommit.MeasuredEntries)

	c.Load(inferencetypes.DefaultFeeParams())
	h := c.hints()
	require.True(t, h.StoreCommit.HasMeasured)
	require.Equal(t, uint64(105_000), h.StoreCommit.MeasuredIntrinsic)
	require.Equal(t, uint(2), h.StoreCommit.MeasuredEntries)
	require.Equal(t, uint64(100), h.StoreCommit.ChainRate)

	c.ClearStoreCommitIntrinsic()
	require.False(t, c.hints().StoreCommit.HasMeasured)
	require.Equal(t, uint64(0), c.hints().StoreCommit.MeasuredIntrinsic)
	require.Equal(t, uint(0), c.hints().StoreCommit.MeasuredEntries)
}

func TestFeeTreeLoad_AbsentStoreCommitRuleIsZeroMagnifier(t *testing.T) {
	fp := inferencetypes.DefaultFeeParams()
	fp.EnabledFeeGroups = []string{inferencetypes.FeeGroupEpoch}
	epoch := fp.GroupByName(inferencetypes.FeeGroupEpoch)
	require.NotNil(t, epoch)
	filtered := epoch.Msgs[:0]
	hdURL := sdk.MsgTypeURL(&inferencetypes.MsgSubmitHardwareDiff{})
	for _, rule := range epoch.Msgs {
		if rule != nil && rule.TypeUrl == hdURL {
			filtered = append(filtered, rule)
		}
	}
	epoch.Msgs = filtered

	c := newFeeTreeCache()
	c.Load(fp)
	h := c.hints()
	require.True(t, h.FeeTreeLoaded)
	require.False(t, h.StoreCommit.HasRate)
	require.False(t, h.StoreCommit.HasBase)

	msg := &inferencetypes.MsgPoCV2StoreCommit{
		Entries: []*inferencetypes.PoCV2CommitEntry{{ModelId: "m1", Count: 200}},
	}
	require.Equal(t, gasStoreCommitIntrinsic, estimateMsgGasHinted(msg, h),
		"loaded tree with no StoreCommit leaf must not fall back to 600k/150, but must keep the intrinsic floor")
}

func TestEstimateHardwareDiffGas_InventoryDeltaNotBlobSize(t *testing.T) {
	prev := []*inferencetypes.HardwareNode{{
		LocalId: "n1",
		Status:  inferencetypes.HardwareNodeStatus_INFERENCE,
		Host:    "127.0.0.1",
		Port:    "8080",
	}}
	same := &inferencetypes.HardwareNode{
		LocalId: "n1",
		Status:  inferencetypes.HardwareNodeStatus_INFERENCE,
		Host:    "127.0.0.1",
		Port:    "8080",
	}
	added := &inferencetypes.HardwareNode{
		LocalId: "n2",
		Status:  inferencetypes.HardwareNodeStatus_INFERENCE,
		Host:    "127.0.0.1",
		Port:    "8081",
	}
	hints := GasHints{
		HardwareDiff: HardwareDiffGas{
			HasGasPerByte: true,
			GasPerByte:    100,
			UnitSize:      1000,
			Prev:          prev,
		},
	}

	rewrite := &inferencetypes.MsgSubmitHardwareDiff{
		Creator:       "creator",
		NewOrModified: []*inferencetypes.HardwareNode{same},
	}
	require.Equal(t, gasSubmitHardwareDiff, estimateMsgGasHinted(rewrite, hints),
		"same-size rewrite must not charge extra gas")

	grow := &inferencetypes.MsgSubmitHardwareDiff{
		Creator:       "creator",
		NewOrModified: []*inferencetypes.HardwareNode{added},
	}
	qty := hardwareDiffByteDelta(grow, prev)
	require.Greater(t, qty, uint64(0))
	blob := uint64(added.Size())
	require.NotEqual(t, blob, qty, "delta must not be N × size(new_or_modified)")
	extra := qty * 100 / 1000
	require.Equal(t, gasSubmitHardwareDiff+extra+extra/2, estimateMsgGasHinted(grow, hints))
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
		&inferencetypes.MsgClaimRewards{},
		&inferencetypes.MsgClaimRewards{},
		&inferencetypes.MsgSubmitSeed{},
	}
	want := txOverheadGas + 2*gasClaimRewards + gasSubmitSeed
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

func TestSaturatingMulAdd(t *testing.T) {
	require.Equal(t, uint64(0), saturatingMul(0, 99))
	require.Equal(t, uint64(12), saturatingMul(3, 4))
	require.Equal(t, ^uint64(0), saturatingMul(^uint64(0), 2))
	require.Equal(t, ^uint64(0), saturatingAdd(^uint64(0), 1))
	require.Equal(t, uint64(5), saturatingAdd(2, 3))
}

func TestEstimateStoreCommitGas_OverflowSaturatesThenBatchCaps(t *testing.T) {
	msg := &inferencetypes.MsgPoCV2StoreCommit{
		Entries: []*inferencetypes.PoCV2CommitEntry{{ModelId: "m", Count: ^uint32(0)}},
	}
	hints := GasHints{
		FeeTreeLoaded: true,
		StoreCommit: StoreCommitGas{
			HasRate:    true,
			PaddedRate: ^uint64(0),
			HasBase:    true,
			PaddedBase: 0,
		},
	}
	require.Equal(t, ^uint64(0), estimateStoreCommitGas(msg, hints))
	require.Equal(t, uint64(BatchGasLimit), estimateBatchGas([]sdk.Msg{msg}, 0, hints))
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

func TestIsHardwareDiffOnly(t *testing.T) {
	require.True(t, isHardwareDiffOnly([]sdk.Msg{&inferencetypes.MsgSubmitHardwareDiff{}}))
	require.False(t, isHardwareDiffOnly(nil))
	require.False(t, isHardwareDiffOnly([]sdk.Msg{
		&inferencetypes.MsgSubmitHardwareDiff{},
		&inferencetypes.MsgSubmitHardwareDiff{},
	}))
	require.False(t, isHardwareDiffOnly([]sdk.Msg{&inferencetypes.MsgPoCV2StoreCommit{}}))
}

func TestGasWantedFromSimulate(t *testing.T) {
	require.Equal(t, uint64(500_000), gasWantedFromSimulate(500_000, 0, 0), "zero sim keeps static")
	require.Equal(t, uint64(150_000), gasWantedFromSimulate(500_000, 100_000, 0), "unset multiplier is 1.5×, not raised to static")
	require.Equal(t, uint64(150_000), gasWantedFromSimulate(500_000, 100_000, 1.5), "explicit 1.5×")
	require.Equal(t, uint64(120_000), gasWantedFromSimulate(500_000, 100_000, 1.2), "host can set 1.2×")
	require.Equal(t, uint64(150_000), gasWantedFromSimulate(500_000, 100_000, 0.9), "≤1 falls back to 1.5×")
	require.Equal(t, uint64(150_000), gasWantedFromSimulate(500_000, 100_000, 15), "typo 15 falls back to 1.5×")
	require.Equal(t, uint64(1_500_000), gasWantedFromSimulate(500_000, 1_000_000, 0), "sim×1.5 default")
	require.Equal(t, uint64(1_200_000), gasWantedFromSimulate(500_000, 1_000_000, 1.2), "sim×1.2 override")
	require.Equal(t, uint64(BatchGasLimit), gasWantedFromSimulate(500_000, BatchGasLimit, 0), "cap at BatchGasLimit")
}

func TestApplySimulateHeadroom(t *testing.T) {
	require.Equal(t, uint64(0), applySimulateHeadroom(0, 1.5))
	require.Equal(t, uint64(150), applySimulateHeadroom(100, 0), "zero config is default 1.5")
	require.Equal(t, uint64(150), applySimulateHeadroom(100, 1.5))
	require.Equal(t, uint64(120), applySimulateHeadroom(100, 1.2))
	require.Equal(t, uint64(200), applySimulateHeadroom(100, 2.0))
	require.Equal(t, uint64(150), applySimulateHeadroom(100, 1.0), "1.0 would under-size; use 1.5")
}

func TestStoreCommitIntrinsicFromSim(t *testing.T) {
	got, ok := StoreCommitIntrinsicFromSim(605_100, 100, 500_000, 1)
	require.True(t, ok)
	require.Equal(t, uint64(105_000), got)

	got, ok = StoreCommitIntrinsicFromSim(605_200, 100, 500_000, 2)
	require.True(t, ok)
	require.Equal(t, uint64(105_000), got)

	_, ok = StoreCommitIntrinsicFromSim(0, 100, 500_000, 1)
	require.False(t, ok)
	_, ok = StoreCommitIntrinsicFromSim(500_000, 100, 500_000, 1)
	require.False(t, ok, "used == extra must not produce zero intrinsic")
	_, ok = StoreCommitIntrinsicFromSim(605_100, 100, 500_000, 0)
	require.False(t, ok)
}

func TestEstimateStoreCommitGas_MeasuredIntrinsicUsesRawExtraAndHeadroom(t *testing.T) {
	msg := &inferencetypes.MsgPoCV2StoreCommit{
		Entries: []*inferencetypes.PoCV2CommitEntry{{ModelId: "m1", Count: 200}},
	}
	hints := GasHints{
		FeeTreeLoaded: true,
		StoreCommit: StoreCommitGas{
			HasMeasured:       true,
			MeasuredIntrinsic: 105_000,
			MeasuredEntries:   1,
			ChainRate:         100,
			ChainBase:         500_000,
			HasRate:           true,
			PaddedRate:        150,
			HasBase:           true,
			PaddedBase:        600_000,
		},
	}
	// first of stage: extra = 500k + 200*100; wanted = 1.5 × (105k + extra)
	first := uint64(105_000 + 500_000 + 200*100)
	require.Equal(t, first+first/2, estimateMsgGasHinted(msg, hints))

	incremental := hints
	incremental.StoreCommit.Prev = map[string]uint32{"m1": 199}
	inc := uint64(105_000 + 100)
	require.Equal(t, inc+inc/2, estimateMsgGasHinted(msg, incremental),
		"incremental uses raw rate, not padded 150")

	tight := hints
	tight.TxGasMultiplier = 1.2
	require.Equal(t, first+first/5, estimateMsgGasHinted(msg, tight),
		"host 1.2× override applies to measured StoreCommit")
}

func TestEstimateBatchGas_MeasuredIntrinsicSkipsOverhead(t *testing.T) {
	msg := &inferencetypes.MsgPoCV2StoreCommit{
		Entries: []*inferencetypes.PoCV2CommitEntry{{ModelId: "m1", Count: 1}},
	}
	hints := GasHints{
		StoreCommit: StoreCommitGas{
			HasMeasured:       true,
			MeasuredIntrinsic: 105_000,
			MeasuredEntries:   1,
			ChainRate:         100,
			ChainBase:         500_000,
		},
	}
	perMsg := estimateMsgGasHinted(msg, hints)
	require.Equal(t, perMsg, estimateBatchGas([]sdk.Msg{msg}, 0, hints),
		"measured StoreCommit batch must not add txOverheadGas again")
	require.Equal(t, txOverheadGas+perMsg+gasSubmitSeed, estimateBatchGas([]sdk.Msg{msg, &inferencetypes.MsgSubmitSeed{}}, 0, hints),
		"mixed batch still pays overhead once")
}

func TestEstimateStoreCommitGas_PeriodBaseIsStageWide(t *testing.T) {
	msgB := &inferencetypes.MsgPoCV2StoreCommit{
		Entries: []*inferencetypes.PoCV2CommitEntry{{ModelId: "m2", Count: 10}},
	}
	// Model A is already on chain this stage; this payload is only B.
	// Chain skips period base (existingByModel nonempty). DAPI must too.
	got := estimateMsgGasHinted(msgB, GasHints{
		FeeTreeLoaded: true,
		StoreCommit: StoreCommitGas{
			HasRate:    true,
			PaddedRate: 100,
			HasBase:    true,
			PaddedBase: 500_000,
			Prev:       map[string]uint32{"m1": 50},
		},
	})
	require.Equal(t, gasStoreCommitIntrinsic+10*100, got,
		"new model in a stage that already has a commit must not add period base")
}

func TestEstimateStoreCommitGas_MeasuredFallsBackWhenMoreEntriesThanDummy(t *testing.T) {
	msg := &inferencetypes.MsgPoCV2StoreCommit{
		Entries: []*inferencetypes.PoCV2CommitEntry{
			{ModelId: "m1", Count: 10},
			{ModelId: "m2", Count: 10},
		},
	}
	hints := GasHints{
		FeeTreeLoaded: true,
		StoreCommit: StoreCommitGas{
			HasMeasured:       true,
			MeasuredIntrinsic: 105_000,
			MeasuredEntries:   1,
			ChainRate:         100,
			ChainBase:         500_000,
			HasRate:           true,
			PaddedRate:        150,
			HasBase:           true,
			PaddedBase:        600_000,
		},
	}
	// Static fallback: intrinsic floor + padded extra (first of stage).
	perMsg := gasStoreCommitIntrinsic + 600_000 + 20*150
	require.Equal(t, perMsg, estimateMsgGasHinted(msg, hints))
	require.Equal(t, txOverheadGas+perMsg, estimateBatchGas([]sdk.Msg{msg}, 0, hints),
		"entry-count fallback must keep txOverheadGas; measured batch skip does not apply")
}

func TestIsStoreCommitOnly(t *testing.T) {
	require.True(t, isStoreCommitOnly([]sdk.Msg{&inferencetypes.MsgPoCV2StoreCommit{}}))
	require.False(t, isStoreCommitOnly(nil))
	require.False(t, isStoreCommitOnly([]sdk.Msg{&inferencetypes.MsgSubmitHardwareDiff{}}))
}
