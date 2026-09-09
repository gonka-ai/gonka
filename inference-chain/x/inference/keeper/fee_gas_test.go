package keeper_test

import (
	"testing"

	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// extraGasRecorder counts only fee-group ConsumeGas, ignoring KV metering.
type extraGasRecorder struct {
	storetypes.GasMeter
	extra storetypes.Gas
}

func newExtraGasRecorder() *extraGasRecorder {
	return &extraGasRecorder{GasMeter: storetypes.NewGasMeter(1_000_000_000)}
}

func (r *extraGasRecorder) ConsumeGas(amount storetypes.Gas, descriptor string) {
	if descriptor == "fee_group_period_base" || descriptor == "fee_group_extra" {
		r.extra += amount
	}
	r.GasMeter.ConsumeGas(amount, descriptor)
}

func enableEpochRates(fp *types.FeeParams, storeCommitBase, storeCommitPer, hdPerByte uint64) {
	fp.EnabledFeeGroups = []string{types.FeeGroupEpoch}
	epoch := fp.GroupByName(types.FeeGroupEpoch)
	if epoch == nil {
		return
	}
	for _, rule := range epoch.Msgs {
		if d := rule.GetStoredDelta(); d != nil && rule.Base != nil {
			rule.Base.Gas = storeCommitBase
			d.GasPerUnit = storeCommitPer
		}
		if b := rule.GetStoredBytes(); b != nil {
			b.GasPerUnit = hdPerByte
		}
	}
}

func TestChargeExtraGas_StoreCommitDeltaAndBaseOnce(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	fp := types.DefaultFeeParams()
	enableEpochRates(fp, 1_000, 10, 0)
	params.FeeParams = fp
	require.NoError(t, k.SetParams(ctx, params))

	signer := sdk.AccAddress("creatoraddr________")
	msg := &types.MsgPoCV2StoreCommit{
		Creator:                  signer.String(),
		PocStageStartBlockHeight: 100,
		Entries:                  []*types.PoCV2CommitEntry{{ModelId: "m", Count: 8}},
	}

	rec := newExtraGasRecorder()
	ctx = ctx.WithGasMeter(rec)
	require.NoError(t, k.ChargeExtraGas(ctx, signer, msg, 8, true))
	require.Equal(t, storetypes.Gas(1_080), rec.extra)

	rec2 := newExtraGasRecorder()
	ctx2 := ctx.WithGasMeter(rec2)
	require.NoError(t, k.ChargeExtraGas(ctx2, signer, msg, 15, false))
	require.Equal(t, storetypes.Gas(150), rec2.extra, "existing commits skip period base")
}

func TestChargeExtraGas_RunsWhenGroupOff(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	fp := types.DefaultFeeParams()
	enableEpochRates(fp, 1_000, 10, 0)
	fp.EnabledFeeGroups = nil
	params.FeeParams = fp
	require.NoError(t, k.SetParams(ctx, params))

	rec := newExtraGasRecorder()
	ctx = ctx.WithGasMeter(rec)
	signer := sdk.AccAddress("creatoraddr________")
	msg := &types.MsgPoCV2StoreCommit{PocStageStartBlockHeight: 100}
	require.NoError(t, k.ChargeExtraGas(ctx, signer, msg, 8, true))
	require.Equal(t, storetypes.Gas(1_080), rec.extra, "sybil meter is independent of enabled_fee_groups")
}

func TestChargeExtraGas_HardwareDiffRunsWhenGroupOff(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	fp := types.DefaultFeeParams()
	enableEpochRates(fp, 1_000, 10, 10)
	fp.EnabledFeeGroups = nil
	params.FeeParams = fp
	require.NoError(t, k.SetParams(ctx, params))

	rec := newExtraGasRecorder()
	signer := sdk.AccAddress("creatoraddr________")
	hd := &types.MsgSubmitHardwareDiff{Creator: signer.String()}
	require.NoError(t, k.ChargeExtraGas(ctx.WithGasMeter(rec), signer, hd, 7_000, false))
	require.Equal(t, storetypes.Gas(70), rec.extra, "HD extra gas is observed before epoch coins turn on")
}

func TestChargeExtraGas_SkippedWhenNoRule(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.FeeParams = &types.FeeParams{}
	require.NoError(t, k.SetParams(ctx, params))

	rec := newExtraGasRecorder()
	ctx = ctx.WithGasMeter(rec)
	signer := sdk.AccAddress("creatoraddr________")
	msg := &types.MsgPoCV2StoreCommit{PocStageStartBlockHeight: 100}
	require.NoError(t, k.ChargeExtraGas(ctx, signer, msg, 8, true))
	require.Equal(t, storetypes.Gas(0), rec.extra)
}

func TestChargeExtraGas_ExistingCommitsSkipPeriodBaseWithoutMarker(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	fp := types.DefaultFeeParams()
	enableEpochRates(fp, 500_000, 100, 0)
	fp.EnabledFeeGroups = nil
	params.FeeParams = fp
	require.NoError(t, k.SetParams(ctx, params))

	rec := newExtraGasRecorder()
	signer := sdk.AccAddress("creatoraddr________")
	msg := &types.MsgPoCV2StoreCommit{PocStageStartBlockHeight: 100}
	require.NoError(t, k.ChargeExtraGas(ctx.WithGasMeter(rec), signer, msg, 3, false))
	require.Equal(t, storetypes.Gas(300), rec.extra, "mid-PoC upgrade must not charge 500k")
}

func TestChargeExtraGas_HardwareDiffStoredBytes(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	fp := types.DefaultFeeParams()
	enableEpochRates(fp, 500_000, 100, 10)
	params.FeeParams = fp
	require.NoError(t, k.SetParams(ctx, params))

	signer := sdk.AccAddress("creatoraddr________")
	hd := &types.MsgSubmitHardwareDiff{Creator: signer.String()}

	recSame := newExtraGasRecorder()
	require.NoError(t, k.ChargeExtraGas(ctx.WithGasMeter(recSame), signer, hd, 0, false))
	require.Equal(t, storetypes.Gas(0), recSame.extra, "same-size rewrite: qty 0")

	recGrow := newExtraGasRecorder()
	require.NoError(t, k.ChargeExtraGas(ctx.WithGasMeter(recGrow), signer, hd, 7_000, false))
	require.Equal(t, storetypes.Gas(70), recGrow.extra, "grow: floor(Δbytes × N / 1000)")

	recFirst := newExtraGasRecorder()
	require.NoError(t, k.ChargeExtraGas(ctx.WithGasMeter(recFirst), signer, hd, 40_000, false))
	require.Equal(t, storetypes.Gas(400), recFirst.extra, "first-ever: floor(after × N / 1000)")
}

func TestChargeExtraGas_HardwareDiffDefault100Per1000Bytes(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.FeeParams = types.DefaultFeeParams()
	require.NoError(t, k.SetParams(ctx, params))

	signer := sdk.AccAddress("creatoraddr________")
	hd := &types.MsgSubmitHardwareDiff{Creator: signer.String()}

	rec := newExtraGasRecorder()
	require.NoError(t, k.ChargeExtraGas(ctx.WithGasMeter(rec), signer, hd, 1_000, false))
	require.Equal(t, storetypes.Gas(100), rec.extra, "100 gas per 1000 bytes")

	recSmall := newExtraGasRecorder()
	require.NoError(t, k.ChargeExtraGas(ctx.WithGasMeter(recSmall), signer, hd, 9, false))
	require.Equal(t, storetypes.Gas(0), recSmall.extra, "sub-10-byte growth floors to 0 at 100 gas/kb")
}

func TestChargeExtraGas_HardwareDiffUnitBIsPerByte(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	fp := types.DefaultFeeParams()
	hd := fp.Groups[0].Msgs[1].GetStoredBytes()
	hd.GasPerUnit = 10
	hd.Unit = types.StoredBytesUnitB
	params.FeeParams = fp
	require.NoError(t, k.SetParams(ctx, params))

	signer := sdk.AccAddress("creatoraddr________")
	msg := &types.MsgSubmitHardwareDiff{Creator: signer.String()}
	rec := newExtraGasRecorder()
	require.NoError(t, k.ChargeExtraGas(ctx.WithGasMeter(rec), signer, msg, 7, false))
	require.Equal(t, storetypes.Gas(70), rec.extra, "unit=b is N × Δbytes")
}

func TestChargeExtraGas_HardwareDiffDoesNotEatStoreCommitBase(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	fp := types.DefaultFeeParams()
	enableEpochRates(fp, 500_000, 100, 10)
	params.FeeParams = fp
	require.NoError(t, k.SetParams(ctx, params))

	signer := sdk.AccAddress("creatoraddr________")
	commit := &types.MsgPoCV2StoreCommit{
		Creator:                  signer.String(),
		PocStageStartBlockHeight: 100,
		Entries:                  []*types.PoCV2CommitEntry{{ModelId: "m", Count: 1}},
	}
	hd := &types.MsgSubmitHardwareDiff{Creator: signer.String()}

	recCommit := newExtraGasRecorder()
	require.NoError(t, k.ChargeExtraGas(ctx.WithGasMeter(recCommit), signer, commit, 1, true))
	require.Equal(t, storetypes.Gas(500_000+100), recCommit.extra)

	recHD := newExtraGasRecorder()
	require.NoError(t, k.ChargeExtraGas(ctx.WithGasMeter(recHD), signer, hd, 5_000, false))
	require.Equal(t, storetypes.Gas(50), recHD.extra, "HD must not consume StoreCommit period base")
}

func setValidationsRepeatedLen(fp *types.FeeParams, per uint64) {
	epoch := fp.GroupByName(types.FeeGroupEpoch)
	if epoch == nil {
		return
	}
	epoch.Msgs = append(epoch.Msgs, &types.MsgGasRule{
		TypeUrl: sdk.MsgTypeURL(&types.MsgSubmitPocValidationsV2{}),
		Func: &types.MsgGasRule_RepeatedLen{
			RepeatedLen: &types.RepeatedLenParams{GasPerUnit: per, Field: "validations"},
		},
	})
}

func TestChargeExtraGas_RepeatedLenIgnoresHandlerQty(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	fp := types.DefaultFeeParams()
	setValidationsRepeatedLen(fp, 10)
	params.FeeParams = fp
	require.NoError(t, k.SetParams(ctx, params))

	signer := sdk.AccAddress("creatoraddr________")
	msg := &types.MsgSubmitPocValidationsV2{
		Creator: signer.String(),
		Validations: []*types.PoCValidationEntryV2{
			{}, {}, {},
		},
	}

	rec := newExtraGasRecorder()
	require.NoError(t, k.ChargeExtraGas(ctx.WithGasMeter(rec), signer, msg, 999, false))
	require.Equal(t, storetypes.Gas(30), rec.extra, "qty is len(validations), not the handler argument")
}

func TestChargeMessageRuleGas_RepeatedLenWithoutHandlerQty(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	fp := types.DefaultFeeParams()
	setValidationsRepeatedLen(fp, 7)
	params.FeeParams = fp
	require.NoError(t, k.SetParams(ctx, params))

	msg := &types.MsgSubmitPocValidationsV2{
		Validations: []*types.PoCValidationEntryV2{{}, {}},
	}
	rec := newExtraGasRecorder()
	require.NoError(t, k.ChargeMessageRuleGas(ctx.WithGasMeter(rec), msg))
	require.Equal(t, storetypes.Gas(14), rec.extra)
}

func TestChargeMessageRuleGas_StoreCommitStoredDeltaNoops(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	fp := types.DefaultFeeParams()
	enableEpochRates(fp, 500_000, 100, 0)
	params.FeeParams = fp
	require.NoError(t, k.SetParams(ctx, params))

	msg := &types.MsgPoCV2StoreCommit{
		PocStageStartBlockHeight: 100,
		Entries:                  []*types.PoCV2CommitEntry{{ModelId: "m", Count: 8}},
	}
	rec := newExtraGasRecorder()
	require.NoError(t, k.ChargeMessageRuleGas(ctx.WithGasMeter(rec), msg))
	require.Equal(t, storetypes.Gas(0), rec.extra, "stored_delta still needs handler Count delta")
}
