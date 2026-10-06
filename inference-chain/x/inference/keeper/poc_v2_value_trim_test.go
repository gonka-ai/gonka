package keeper_test

import (
	"fmt"
	"strings"
	"testing"

	"cosmossdk.io/collections"
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

const mainnetPoCModelID = "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8"

func setupPocValidationsV2Test(t *testing.T) (keeper.Keeper, sdk.Context, types.MsgServer) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(160)
	params, err := k.GetParams(sdkCtx)
	require.NoError(t, err)
	params.PocParams = &types.PocParams{PocV2Enabled: true}
	params.EpochParams = &types.EpochParams{
		PocStageDuration:      50,
		PocExchangeDuration:   20,
		PocValidationDelay:    5,
		PocValidationDuration: 100,
	}
	require.NoError(t, k.SetParams(sdkCtx, params))
	k.SetEffectiveEpochIndex(sdkCtx, 0)
	k.SetEpoch(sdkCtx, &types.Epoch{Index: 1, PocStartBlockHeight: 100})
	registerPoCParticipant(t, k, sdkCtx, testutil.Validator)
	return k, sdkCtx, keeper.NewMsgServerImpl(k)
}

func pocTargets(n int) []string {
	out := make([]string, n)
	for i := range out {
		b := make([]byte, 20)
		b[0], b[19] = 0xA5, byte(i+1)
		out[i] = sdk.AccAddress(b).String()
	}
	return out
}

func submitPocValidations(t *testing.T, ctx sdk.Context, ms types.MsgServer, targets []string) storetypes.Gas {
	t.Helper()
	msg := &types.MsgSubmitPocValidationsV2{Creator: testutil.Validator, PocStageStartBlockHeight: 100}
	for i, addr := range targets {
		msg.Validations = append(msg.Validations, &types.PoCValidationEntryV2{
			ParticipantAddress: addr,
			ModelId:            mainnetPoCModelID,
			ValidatedWeight:    int64(1000 + i),
		})
	}
	ctx = ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	_, err := ms.SubmitPocValidationsV2(ctx, msg)
	require.NoError(t, err)
	return ctx.GasMeter().GasConsumed()
}

// The stored value must not repeat the key; readers get the full record back.
func TestSubmitPocValidationsV2_ValueHoldsOnlyWeight(t *testing.T) {
	k, ctx, ms := setupPocValidationsV2Test(t)
	targets := pocTargets(3)
	gas := submitPocValidations(t, ctx, ms, targets)
	t.Logf("SubmitPocValidationsV2 gas, 3 entries: %d", gas)

	byStage, err := k.GetPoCValidationsV2ByStage(ctx, 100)
	require.NoError(t, err)
	require.Len(t, byStage, 3)
	for i, addr := range targets {
		got := byStage[types.PoCParticipantModelKey{ParticipantAddress: addr, ModelID: mainnetPoCModelID}]
		require.Equal(t, []types.PoCValidationV2{{
			ParticipantAddress:          addr,
			ValidatorParticipantAddress: testutil.Validator,
			PocStageStartBlockHeight:    100,
			ModelId:                     mainnetPoCModelID,
			ValidatedWeight:             int64(1000 + i),
		}}, got)

		raw, err := k.PoCValidationsV2.Get(ctx, collectionsKey(addr, testutil.Validator))
		require.NoError(t, err)
		require.Equal(t, types.PoCValidationV2{PocStageStartBlockHeight: 100, ValidatedWeight: int64(1000 + i)}, raw)
	}

	// The trimmed value still counts as present: a repeat vote is skipped.
	submitPocValidations(t, ctx, ms, targets[:1])
	byStage, err = k.GetPoCValidationsV2ByStage(ctx, 100)
	require.NoError(t, err)
	require.Len(t, byStage[types.PoCParticipantModelKey{ParticipantAddress: targets[0], ModelID: mainnetPoCModelID}], 1)
}

// Records written before the change carry every field and read back unchanged.
func TestGetPoCValidationsV2ByStage_ReadsFullLegacyValues(t *testing.T) {
	k, ctx, _ := setupPocValidationsV2Test(t)
	legacy := types.PoCValidationV2{
		ParticipantAddress:          testutil.Executor,
		ValidatorParticipantAddress: testutil.Validator,
		PocStageStartBlockHeight:    100,
		ModelId:                     mainnetPoCModelID,
		ValidatedWeight:             7,
	}
	require.NoError(t, k.PoCValidationsV2.Set(ctx, collectionsKey(testutil.Executor, testutil.Validator), legacy))
	trimmed := legacy
	trimmed.ValidatorParticipantAddress = testutil.Validator2
	require.NoError(t, k.SetPocValidationV2(ctx, trimmed))

	byStage, err := k.GetPoCValidationsV2ByStage(ctx, 100)
	require.NoError(t, err)
	got := byStage[types.PoCParticipantModelKey{ParticipantAddress: testutil.Executor, ModelID: mainnetPoCModelID}]
	require.ElementsMatch(t, []types.PoCValidationV2{legacy, trimmed}, got)
}

// A zero weight at stage zero would encode to an empty value; that record keeps every field.
func TestSetPocValidationV2_ZeroRecordKeepsFields(t *testing.T) {
	k, ctx, _ := setupPocValidationsV2Test(t)
	v := types.PoCValidationV2{
		ParticipantAddress:          testutil.Executor,
		ValidatorParticipantAddress: testutil.Validator,
		ModelId:                     mainnetPoCModelID,
	}
	require.NoError(t, k.SetPocValidationV2(ctx, v))
	exists, err := k.HasPocValidationV2(ctx, 0, testutil.Executor, mainnetPoCModelID, testutil.Validator)
	require.NoError(t, err)
	require.True(t, exists)
	byStage, err := k.GetPoCValidationsV2ByStage(ctx, 0)
	require.NoError(t, err)
	require.Equal(t, []types.PoCValidationV2{v}, byStage[types.PoCParticipantModelKey{ParticipantAddress: testutil.Executor, ModelID: mainnetPoCModelID}])
}

// Upper-case bech32 is accepted; such a record keeps every field and reads back as submitted.
func TestSubmitPocValidationsV2_NonCanonicalAddressKeepsFields(t *testing.T) {
	k, ctx, ms := setupPocValidationsV2Test(t)
	up := strings.ToUpper(testutil.Executor)
	_, err := ms.SubmitPocValidationsV2(ctx, &types.MsgSubmitPocValidationsV2{
		Creator:                  testutil.Validator,
		PocStageStartBlockHeight: 100,
		Validations:              []*types.PoCValidationEntryV2{{ParticipantAddress: up, ModelId: mainnetPoCModelID, ValidatedWeight: 5}},
	})
	require.NoError(t, err)
	byStage, err := k.GetPoCValidationsV2ByStage(ctx, 100)
	require.NoError(t, err)
	require.Equal(t, []types.PoCValidationV2{{
		ParticipantAddress:          up,
		ValidatorParticipantAddress: testutil.Validator,
		PocStageStartBlockHeight:    100,
		ModelId:                     mainnetPoCModelID,
		ValidatedWeight:             5,
	}}, byStage[types.PoCParticipantModelKey{ParticipantAddress: up, ModelID: mainnetPoCModelID}])

	upSigner := types.PoCValidationV2{
		ParticipantAddress:          testutil.Executor2,
		ValidatorParticipantAddress: strings.ToUpper(testutil.Validator),
		PocStageStartBlockHeight:    100,
		ModelId:                     mainnetPoCModelID,
		ValidatedWeight:             6,
	}
	require.NoError(t, k.SetPocValidationV2(ctx, upSigner))
	byStage, err = k.GetPoCValidationsV2ByStage(ctx, 100)
	require.NoError(t, err)
	require.Equal(t, []types.PoCValidationV2{upSigner}, byStage[types.PoCParticipantModelKey{ParticipantAddress: testutil.Executor2, ModelID: mainnetPoCModelID}])
}

// Write gas per entry at mainnet address and model-id lengths.
func TestSubmitPocValidationsV2_GasPerEntry(t *testing.T) {
	_, ctx1, ms1 := setupPocValidationsV2Test(t)
	one := submitPocValidations(t, ctx1, ms1, pocTargets(1))
	_, ctx10, ms10 := setupPocValidationsV2Test(t)
	ten := submitPocValidations(t, ctx10, ms10, pocTargets(10))
	perEntry := (ten - one) / 9
	t.Logf("SubmitPocValidationsV2 gas: 1 entry %d, 10 entries %d, per entry %d", one, ten, perEntry)
	require.Less(t, perEntry, uint64(6500), fmt.Sprintf("per-entry gas %d", perEntry))
}

func collectionsKey(participant, validator string) collections.Triple[int64, sdk.AccAddress, collections.Pair[string, sdk.AccAddress]] {
	return collections.Join3(int64(100), sdk.MustAccAddressFromBech32(participant),
		collections.Join(mainnetPoCModelID, sdk.MustAccAddressFromBech32(validator)))
}
