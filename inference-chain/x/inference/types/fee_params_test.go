package types_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/stretchr/testify/require"

	collateraltypes "github.com/productscience/inference/x/collateral/types"
	"github.com/productscience/inference/x/inference/types"
)

func TestFeeParamsValidate(t *testing.T) {
	fp := types.DefaultFeeParams()
	require.NoError(t, fp.Validate())

	fp.EnabledFeeGroups = []string{"epoc"}
	require.Error(t, fp.Validate())

	fp = types.DefaultFeeParams()
	fp.Groups[0].Base.PeriodType = "weekly"
	require.Error(t, fp.Validate())

	fp = types.DefaultFeeParams()
	dup := *fp.Groups[0].Msgs[0]
	fp.Groups[0].Msgs = append(fp.Groups[0].Msgs, &dup)
	require.Error(t, fp.Validate())

	fp = types.DefaultFeeParams()
	fp.Groups[0].Msgs[0].GetStoredDelta().Items = "not_a_field"
	require.Error(t, fp.Validate())

	fp = types.DefaultFeeParams()
	fp.Groups = nil
	fp.EnabledFeeGroups = []string{types.FeeGroupEpoch}
	require.Error(t, fp.Validate(), "enabled group must have a groups[] row")

	fp = types.DefaultFeeParams()
	fp.Groups[0].Msgs[0].TypeUrl = sdk.MsgTypeURL(&types.MsgSubmitNewParticipant{})
	require.Error(t, fp.Validate(), "rule type must belong to the compiled group")

	fp = types.DefaultFeeParams()
	fp.Groups[0].Msgs[0].Base.PeriodType = types.PeriodTypeEpoch
	require.Error(t, fp.Validate(), "nonzero StoreCommit base must be poc/1")

	fp = types.DefaultFeeParams()
	fp.Groups[0].Msgs[0].Base.PeriodLength = 2
	require.Error(t, fp.Validate(), "nonzero StoreCommit base must be period_length=1")

	fp = types.DefaultFeeParams()
	fp.Groups[0].Msgs[0].Func = &types.MsgGasRule_RepeatedLen{
		RepeatedLen: &types.RepeatedLenParams{GasPerUnit: 1, Field: "entries"},
	}
	require.Error(t, fp.Validate(), "StoreCommit cannot use repeated_len")

	fp = types.DefaultFeeParams()
	fp.Groups[0].Msgs[1].Func = &types.MsgGasRule_StoredDelta{
		StoredDelta: &types.StoredDeltaParams{
			GasPerUnit: 1, Items: "new_or_modified", ValueField: "id",
		},
	}
	require.Error(t, fp.Validate(), "HardwareDiff cannot use stored_delta")

	fp = types.DefaultFeeParams()
	fp.Groups[0].Msgs[0].GetStoredDelta().ValueField = "model_id"
	require.Error(t, fp.Validate(), "stored_delta value_field must be unsigned int")

	fp = types.DefaultFeeParams()
	fp.Groups[0].Msgs = append(fp.Groups[0].Msgs, &types.MsgGasRule{
		TypeUrl: sdk.MsgTypeURL(&types.MsgSubmitPocValidationsV2{}),
		Func: &types.MsgGasRule_RepeatedLen{
			RepeatedLen: &types.RepeatedLenParams{GasPerUnit: 10, Field: "creator"},
		},
	})
	require.Error(t, fp.Validate(), "repeated_len on a scalar field must be rejected")

	fp = types.DefaultFeeParams()
	fp.Groups[0].Msgs[0].Func = &types.MsgGasRule_StoredBytes{
		StoredBytes: &types.StoredBytesParams{GasPerUnit: 1},
	}
	require.Error(t, fp.Validate(), "StoreCommit cannot use stored_bytes")

	fp = types.DefaultFeeParams()
	fp.Groups[0].Msgs = append(fp.Groups[0].Msgs, &types.MsgGasRule{
		TypeUrl: sdk.MsgTypeURL(&types.MsgSubmitPocValidationsV2{}),
		Func: &types.MsgGasRule_RepeatedLen{
			RepeatedLen: &types.RepeatedLenParams{GasPerUnit: 10, Field: "validations"},
		},
	})
	require.NoError(t, fp.Validate(), "repeated_len on validations is allowed")

	fp = types.DefaultFeeParams()
	fp.Groups[0].Msgs[0].Base.Gas = 10_000_001
	require.Error(t, fp.Validate(), "period base gas must be capped")

	fp = types.DefaultFeeParams()
	fp.Groups[0].Msgs[0].GetStoredDelta().GasPerUnit = 10_001
	require.Error(t, fp.Validate(), "stored_delta gas_per_unit must be capped")

	fp = types.DefaultFeeParams()
	fp.Groups[0].Msgs[1].GetStoredBytes().GasPerUnit = 10_001
	require.Error(t, fp.Validate(), "stored_bytes gas_per_unit must be capped")

	fp = types.DefaultFeeParams()
	fp.Groups[0].Msgs[1].GetStoredBytes().Unit = "gb"
	require.Error(t, fp.Validate(), "stored_bytes.unit must be b|kb|mb")
}

func TestRepeatedFieldLen(t *testing.T) {
	msg := &types.MsgSubmitPocValidationsV2{
		Validations: []*types.PoCValidationEntryV2{{}, {}},
	}
	n, ok := types.RepeatedFieldLen(msg, "validations")
	require.True(t, ok)
	require.Equal(t, uint64(2), n)

	_, ok = types.RepeatedFieldLen(msg, "creator")
	require.False(t, ok, "scalar field is not repeated")

	_, ok = types.RepeatedFieldLen(msg, "missing")
	require.False(t, ok)
}

func TestFeeGroupOf(t *testing.T) {
	require.Equal(t, types.FeeGroupEpoch, types.FeeGroupOf(&types.MsgPoCV2StoreCommit{}))
	require.Equal(t, types.FeeGroupEpoch, types.FeeGroupOf(&types.MsgSubmitHardwareDiff{}))
	require.Equal(t, types.FeeGroupEpoch, types.FeeGroupOf(&collateraltypes.MsgDepositCollateral{}))
	require.Equal(t, types.FeeGroupOnboarding, types.FeeGroupOf(&types.MsgSubmitNewParticipant{}))
	require.Equal(t, types.FeeGroupCosmos, types.FeeGroupOf(&banktypes.MsgSend{}))
	require.Equal(t, "", types.FeeGroupOf(&types.MsgUpdateParams{}))
	require.Equal(t, "", types.FeeGroupOf(&types.MsgSubmitPocBatch{}))
}

func TestIsNetworkDuty(t *testing.T) {
	require.True(t, types.IsNetworkDuty(&types.MsgSubmitPocBatch{}))
	require.True(t, types.IsNetworkDuty(&types.MsgSubmitSeed{}))
	require.True(t, types.IsNetworkDuty(&types.MsgSettleDevshardEscrow{}))
	require.False(t, types.IsNetworkDuty(&types.MsgPoCV2StoreCommit{}))
	require.False(t, types.IsNetworkDuty(&types.MsgSubmitHardwareDiff{}))
	require.False(t, types.IsNetworkDuty(&banktypes.MsgSend{}))
}

func TestDefaultFeeParamsTree(t *testing.T) {
	fp := types.DefaultFeeParams()
	require.Empty(t, fp.EnabledFeeGroups)
	g, rule := fp.RuleForTypeURL(sdk.MsgTypeURL(&types.MsgPoCV2StoreCommit{}))
	require.NotNil(t, g)
	require.Equal(t, types.FeeGroupEpoch, g.Name)
	require.NotNil(t, rule.GetStoredDelta())
	require.Equal(t, uint64(100), rule.GetStoredDelta().GasPerUnit)
	require.Equal(t, uint64(500_000), rule.Base.Gas)
	_, hdRule := fp.RuleForTypeURL(sdk.MsgTypeURL(&types.MsgSubmitHardwareDiff{}))
	require.NotNil(t, hdRule.GetStoredBytes())
	require.Equal(t, uint64(100), hdRule.GetStoredBytes().GasPerUnit)
	require.Equal(t, types.StoredBytesUnitKB, hdRule.GetStoredBytes().Unit)
}

func TestStoredBytesUnitSize(t *testing.T) {
	sz, ok := types.StoredBytesUnitSize("")
	require.True(t, ok)
	require.Equal(t, uint64(1), sz)
	sz, ok = types.StoredBytesUnitSize("b")
	require.True(t, ok)
	require.Equal(t, uint64(1), sz)
	sz, ok = types.StoredBytesUnitSize("kb")
	require.True(t, ok)
	require.Equal(t, uint64(1000), sz)
	sz, ok = types.StoredBytesUnitSize("mb")
	require.True(t, ok)
	require.Equal(t, uint64(1_000_000), sz)
	_, ok = types.StoredBytesUnitSize("gb")
	require.False(t, ok)
}

func TestStoredBytesParamsRoundtripUnit(t *testing.T) {
	orig := &types.StoredBytesParams{GasPerUnit: 100, Unit: types.StoredBytesUnitKB}
	bz, err := orig.Marshal()
	require.NoError(t, err)
	var got types.StoredBytesParams
	require.NoError(t, got.Unmarshal(bz))
	require.True(t, orig.Equal(&got))
}
