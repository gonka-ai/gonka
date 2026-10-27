package keeper_test

import (
	"errors"
	"strings"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestSubmitPocValidationsV2_SkipsUnregisteredModelAndGhostTarget(t *testing.T) {
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
	k.SetModel(sdkCtx, &types.Model{Id: testPoCModelID})
	// Registered, with no store commit, so the third row is skipped by the commit check.
	k.SetModel(sdkCtx, &types.Model{Id: testPoCModelID2})
	registerPoCParticipant(t, k, sdkCtx, testutil.Executor)
	require.NoError(t, k.SetPoCV2StoreCommit(sdkCtx, types.PoCV2StoreCommit{
		ParticipantAddress:       testutil.Executor,
		PocStageStartBlockHeight: 100,
		Count:                    10,
		RootHash:                 make([]byte, 32),
		ModelId:                  testPoCModelID,
	}))
	seedPocValidationSnapshot(t, k, sdkCtx, 100, testutil.Validator, testPoCModelID, testPoCModelID2)

	msgServer := keeper.NewMsgServerImpl(k)
	_, err = msgServer.SubmitPocValidationsV2(sdkCtx, &types.MsgSubmitPocValidationsV2{
		Creator:                  testutil.Validator,
		PocStageStartBlockHeight: 100,
		Validations: []*types.PoCValidationEntryV2{
			{ParticipantAddress: testutil.Executor, ModelId: "not-a-governance-model", ValidatedWeight: 1},
			{ParticipantAddress: testutil.Executor2, ModelId: testPoCModelID, ValidatedWeight: 1},
			{ParticipantAddress: testutil.Executor, ModelId: testPoCModelID2, ValidatedWeight: 1},
			{ParticipantAddress: testutil.Executor, ModelId: testPoCModelID, ValidatedWeight: 4},
		},
	})
	require.NoError(t, err)

	stored, err := k.HasPocValidationV2(sdkCtx, 100, testutil.Executor, testPoCModelID, testutil.Validator)
	require.NoError(t, err)
	require.True(t, stored)
	missingModel, err := k.HasPocValidationV2(sdkCtx, 100, testutil.Executor, "not-a-governance-model", testutil.Validator)
	require.NoError(t, err)
	require.False(t, missingModel)
	missingTarget, err := k.HasPocValidationV2(sdkCtx, 100, testutil.Executor2, testPoCModelID, testutil.Validator)
	require.NoError(t, err)
	require.False(t, missingTarget)
	missingCommit, err := k.HasPocValidationV2(sdkCtx, 100, testutil.Executor, testPoCModelID2, testutil.Validator)
	require.NoError(t, err)
	require.False(t, missingCommit)
}

func TestSubmitPocValidationsV2_RejectsOversizedBatch(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	registerPoCParticipant(t, k, sdkCtx, testutil.Validator)
	msgServer := keeper.NewMsgServerImpl(k)
	msg := &types.MsgSubmitPocValidationsV2{
		Creator:     testutil.Validator,
		Validations: make([]*types.PoCValidationEntryV2, types.MaxValidationsPerMessage+1),
	}
	_, err := msgServer.SubmitPocValidationsV2(sdkCtx, msg)
	require.Error(t, err)
}

func TestSubmitNewParticipant_RejectsUppercaseCreator(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	upper := strings.ToUpper(testutil.Executor)
	decoded, err := sdk.AccAddressFromBech32(upper)
	require.NoError(t, err)
	require.Equal(t, testutil.Executor, decoded.String())

	msg := &types.MsgSubmitNewParticipant{Creator: upper, Url: "https://example.com"}
	require.Error(t, msg.ValidateBasic())
	_, err = ms.SubmitNewParticipant(ctx, msg)
	require.Error(t, err)
	_, found := k.GetParticipant(ctx, testutil.Executor)
	require.False(t, found)

	_, err = ms.SubmitNewParticipant(ctx, &types.MsgSubmitNewParticipant{
		Creator: testutil.Executor,
		Url:     "https://example.com",
	})
	require.NoError(t, err)
	saved, found := k.GetParticipant(ctx, testutil.Executor)
	require.True(t, found)
	require.Equal(t, testutil.Executor, saved.Index)
	require.Equal(t, testutil.Executor, saved.Address)
}

func TestMLNodeWeightDistribution_StoresPositiveRowsOnly(t *testing.T) {
	k, sdkCtx, ms := setupWeightDistribution(t)
	msg := &types.MsgMLNodeWeightDistribution{
		Creator:                  testutil.Validator,
		PocStageStartBlockHeight: 100,
		Entries: []*types.MLNodeDistributionEntry{{
			ModelId: testPoCModelID,
			Weights: []*types.MLNodeWeight{
				{NodeId: "zero", Weight: 0},
				{NodeId: "a", Weight: 33},
				{NodeId: "b", Weight: 33},
				{NodeId: "c", Weight: 9934},
			},
		}},
	}
	_, err := ms.MLNodeWeightDistribution(sdkCtx, msg)
	require.NoError(t, err)
	require.Len(t, msg.Entries[0].Weights, 4, "handler must not mutate the decoded transaction")

	stored, err := k.MLNodeWeightDistribution(sdkCtx, &types.QueryMLNodeWeightDistributionRequest{
		PocStageStartBlockHeight: 100,
		ParticipantAddress:       testutil.Validator,
		ModelId:                  testPoCModelID,
	})
	require.NoError(t, err)
	require.True(t, stored.Found)
	require.Equal(t, []*types.MLNodeWeight{
		{NodeId: "a", Weight: 33},
		{NodeId: "b", Weight: 33},
		{NodeId: "c", Weight: 9934},
	}, stored.Weights)
}

func TestMLNodeWeightDistribution_RejectsBadShapeWithoutStoring(t *testing.T) {
	k, sdkCtx, ms := setupWeightDistribution(t)
	cases := [][]*types.MLNodeWeight{
		{{}},
		{nil},
		{{NodeId: "", Weight: 1}},
		{{NodeId: strings.Repeat("n", types.MaxMLNodeWeightNodeIDBytes+1), Weight: 1}},
		{{NodeId: "a", Weight: 1}, {NodeId: "a", Weight: 1}},
		{{NodeId: "only-zero", Weight: 0}},
		{{NodeId: "a", Weight: 1}},
	}
	for _, weights := range cases {
		_, err := ms.MLNodeWeightDistribution(sdkCtx, &types.MsgMLNodeWeightDistribution{
			Creator:                  testutil.Validator,
			PocStageStartBlockHeight: 100,
			Entries:                  []*types.MLNodeDistributionEntry{{ModelId: testPoCModelID, Weights: weights}},
		})
		require.Error(t, err)
	}
	_, err := ms.MLNodeWeightDistribution(sdkCtx, &types.MsgMLNodeWeightDistribution{
		Creator:                  testutil.Validator,
		PocStageStartBlockHeight: 100,
		Entries: []*types.MLNodeDistributionEntry{
			{ModelId: testPoCModelID, Weights: []*types.MLNodeWeight{{NodeId: "a", Weight: 10000}}},
			{ModelId: testPoCModelID, Weights: []*types.MLNodeWeight{{NodeId: "b", Weight: 1}}},
		},
	})
	require.Error(t, err)

	stored, err := k.MLNodeWeightDistribution(sdkCtx, &types.QueryMLNodeWeightDistributionRequest{
		PocStageStartBlockHeight: 100,
		ParticipantAddress:       testutil.Validator,
		ModelId:                  testPoCModelID,
	})
	require.NoError(t, err)
	require.False(t, stored.Found)
}

func TestMLNodeWeightDistribution_HandlerRejectsPositiveRowOverflow(t *testing.T) {
	k, sdkCtx, ms := setupWeightDistribution(t)
	params, err := k.GetParams(sdkCtx)
	require.NoError(t, err)
	if params.EpochValidationParams == nil {
		params.EpochValidationParams = types.DefaultEpochValidationParams()
	}
	params.EpochValidationParams.MaxPositiveDistributionRows = 2
	require.NoError(t, k.SetParams(sdkCtx, params))

	_, err = ms.MLNodeWeightDistribution(sdkCtx, &types.MsgMLNodeWeightDistribution{
		Creator:                  testutil.Validator,
		PocStageStartBlockHeight: 100,
		Entries: []*types.MLNodeDistributionEntry{{
			ModelId: testPoCModelID,
			Weights: []*types.MLNodeWeight{
				{NodeId: "a", Weight: 3334},
				{NodeId: "b", Weight: 3333},
				{NodeId: "c", Weight: 3333},
			},
		}},
	})
	require.Error(t, err)

	stored, err := k.MLNodeWeightDistribution(sdkCtx, &types.QueryMLNodeWeightDistributionRequest{
		PocStageStartBlockHeight: 100,
		ParticipantAddress:       testutil.Validator,
		ModelId:                  testPoCModelID,
	})
	require.NoError(t, err)
	require.False(t, stored.Found)
}

func TestSubmitPocValidationsV2_StageCapStoresOnlyTheFirstVote(t *testing.T) {
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
	if params.EpochValidationParams == nil {
		params.EpochValidationParams = types.DefaultEpochValidationParams()
	}
	params.EpochValidationParams.MaxValidationsPerSignerPerStage = 1
	require.NoError(t, k.SetParams(sdkCtx, params))
	k.SetEffectiveEpochIndex(sdkCtx, 0)
	k.SetEpoch(sdkCtx, &types.Epoch{Index: 1, PocStartBlockHeight: 100})
	registerPoCParticipant(t, k, sdkCtx, testutil.Validator)
	registerPoCParticipant(t, k, sdkCtx, testutil.Executor)
	registerPoCParticipant(t, k, sdkCtx, testutil.Executor2)
	k.SetModel(sdkCtx, &types.Model{Id: testPoCModelID})
	for _, target := range []string{testutil.Executor, testutil.Executor2} {
		require.NoError(t, k.SetPoCV2StoreCommit(sdkCtx, types.PoCV2StoreCommit{
			ParticipantAddress:       target,
			PocStageStartBlockHeight: 100,
			Count:                    10,
			RootHash:                 make([]byte, 32),
			ModelId:                  testPoCModelID,
		}))
	}
	seedPocValidationSnapshot(t, k, sdkCtx, 100, testutil.Validator, testPoCModelID)

	_, err = keeper.NewMsgServerImpl(k).SubmitPocValidationsV2(sdkCtx, &types.MsgSubmitPocValidationsV2{
		Creator:                  testutil.Validator,
		PocStageStartBlockHeight: 100,
		Validations: []*types.PoCValidationEntryV2{
			{ParticipantAddress: testutil.Executor, ModelId: testPoCModelID, ValidatedWeight: 1},
			{ParticipantAddress: testutil.Executor2, ModelId: testPoCModelID, ValidatedWeight: 1},
		},
	})
	require.NoError(t, err)

	first, err := k.HasPocValidationV2(sdkCtx, 100, testutil.Executor, testPoCModelID, testutil.Validator)
	require.NoError(t, err)
	require.True(t, first)
	second, err := k.HasPocValidationV2(sdkCtx, 100, testutil.Executor2, testPoCModelID, testutil.Validator)
	require.NoError(t, err)
	require.False(t, second)

	_, err = keeper.NewMsgServerImpl(k).SubmitPocValidationsV2(sdkCtx, &types.MsgSubmitPocValidationsV2{
		Creator:                  testutil.Validator,
		PocStageStartBlockHeight: 100,
		Validations:              nil,
	})
	require.ErrorIs(t, err, types.ErrIllegalState)

	_, err = keeper.NewMsgServerImpl(k).SubmitPocValidationsV2(sdkCtx, &types.MsgSubmitPocValidationsV2{
		Creator:                  testutil.Validator,
		PocStageStartBlockHeight: 100,
		Validations: []*types.PoCValidationEntryV2{
			{ParticipantAddress: testutil.Executor2, ModelId: testPoCModelID, ValidatedWeight: 1},
		},
	})
	require.ErrorIs(t, err, types.ErrIllegalState)
	stillAbsent, err := k.HasPocValidationV2(sdkCtx, 100, testutil.Executor2, testPoCModelID, testutil.Validator)
	require.NoError(t, err)
	require.False(t, stillAbsent)
}

func TestSubmitPocValidationsV2_SnapshotVoterEligibilityAndGuardian(t *testing.T) {
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
	registerPoCParticipant(t, k, sdkCtx, testutil.Validator2)
	registerPoCParticipant(t, k, sdkCtx, testutil.Executor)
	k.SetModel(sdkCtx, &types.Model{Id: testPoCModelID})
	require.NoError(t, k.SetPoCV2StoreCommit(sdkCtx, types.PoCV2StoreCommit{
		ParticipantAddress:       testutil.Executor,
		PocStageStartBlockHeight: 100,
		Count:                    10,
		RootHash:                 make([]byte, 32),
		ModelId:                  testPoCModelID,
	}))

	msgServer := keeper.NewMsgServerImpl(k)

	// Rows skipped by earlier checks (non-governance model, non-participant target,
	// missing store commit) do not trigger a validation snapshot load.
	_, err = msgServer.SubmitPocValidationsV2(sdkCtx, &types.MsgSubmitPocValidationsV2{
		Creator:                  testutil.Validator,
		PocStageStartBlockHeight: 100,
		Validations: []*types.PoCValidationEntryV2{
			{ParticipantAddress: testutil.Executor, ModelId: "not-a-governance-model", ValidatedWeight: 1},
		},
	})
	require.NoError(t, err)

	// Missing snapshot rejects the message once a row passes the target/model/commit checks.
	_, err = msgServer.SubmitPocValidationsV2(sdkCtx, &types.MsgSubmitPocValidationsV2{
		Creator:                  testutil.Validator,
		PocStageStartBlockHeight: 100,
		Validations: []*types.PoCValidationEntryV2{
			{ParticipantAddress: testutil.Executor, ModelId: testPoCModelID, ValidatedWeight: 1},
		},
	})
	require.ErrorIs(t, err, types.ErrIllegalState)

	// Seed snapshot where testutil.Validator has positive voting power and
	// testutil.Executor has a zero-weight bootstrap entry for testPoCModelID.
	require.NoError(t, k.SetPoCValidationSnapshot(sdkCtx, types.PoCValidationSnapshot{
		PocStageStartHeight: 100,
		SnapshotHeight:      155,
		TotalNetworkWeight:  100,
		ModelVotingPowers: []*types.ModelVotingPowers{{
			ModelId: testPoCModelID,
			VotingPowers: []*types.VotingPowerEntry{
				{Address: testutil.Validator, VotingPower: 100},
				{Address: testutil.Executor, VotingPower: 0},
			},
		}},
	}))

	// Registered participant without voting power (or with 0 voting power) in the snapshot stores 0 rows.
	for _, ineligible := range []string{testutil.Validator2, testutil.Executor} {
		_, err = msgServer.SubmitPocValidationsV2(sdkCtx, &types.MsgSubmitPocValidationsV2{
			Creator:                  ineligible,
			PocStageStartBlockHeight: 100,
			Validations: []*types.PoCValidationEntryV2{
				{ParticipantAddress: testutil.Executor, ModelId: testPoCModelID, ValidatedWeight: 1},
			},
		})
		require.NoError(t, err)
		hasIneligibleVote, err := k.HasPocValidationV2(sdkCtx, 100, testutil.Executor, testPoCModelID, ineligible)
		require.NoError(t, err)
		require.False(t, hasIneligibleVote)
	}

	// Enabling testutil.Validator2 as a genesis guardian allows its tiebreaker vote.
	guardianAcc := sdk.MustAccAddressFromBech32(testutil.Validator2)
	params.GenesisGuardianParams = &types.GenesisGuardianParams{
		GuardianAddresses: []string{sdk.ValAddress(guardianAcc).String()},
	}
	require.NoError(t, k.SetParams(sdkCtx, params))
	require.NoError(t, k.SetGenesisOnlyParams(sdkCtx, &types.GenesisOnlyParams{
		GenesisGuardianEnabled: true,
	}))

	_, err = msgServer.SubmitPocValidationsV2(sdkCtx, &types.MsgSubmitPocValidationsV2{
		Creator:                  testutil.Validator2,
		PocStageStartBlockHeight: 100,
		Validations: []*types.PoCValidationEntryV2{
			{ParticipantAddress: testutil.Executor, ModelId: testPoCModelID, ValidatedWeight: 1},
		},
	})
	require.NoError(t, err)
	hasGuardianVote, err := k.HasPocValidationV2(sdkCtx, 100, testutil.Executor, testPoCModelID, testutil.Validator2)
	require.NoError(t, err)
	require.True(t, hasGuardianVote)
}

func TestSubmitPocValidationsV2_MixedModelStoresEligibleRowOnly(t *testing.T) {
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
	registerPoCParticipant(t, k, sdkCtx, testutil.Executor)
	k.SetModel(sdkCtx, &types.Model{Id: testPoCModelID})
	k.SetModel(sdkCtx, &types.Model{Id: testPoCModelID2})
	for _, modelID := range []string{testPoCModelID, testPoCModelID2} {
		require.NoError(t, k.SetPoCV2StoreCommit(sdkCtx, types.PoCV2StoreCommit{
			ParticipantAddress:       testutil.Executor,
			PocStageStartBlockHeight: 100,
			Count:                    10,
			RootHash:                 make([]byte, 32),
			ModelId:                  modelID,
		}))
	}
	require.NoError(t, k.SetPoCValidationSnapshot(sdkCtx, types.PoCValidationSnapshot{
		PocStageStartHeight: 100,
		SnapshotHeight:      155,
		TotalNetworkWeight:  100,
		ModelVotingPowers: []*types.ModelVotingPowers{{
			ModelId: testPoCModelID,
			VotingPowers: []*types.VotingPowerEntry{
				{Address: testutil.Validator, VotingPower: 100},
			},
		}, {
			ModelId: testPoCModelID2,
			VotingPowers: []*types.VotingPowerEntry{
				{Address: testutil.Executor, VotingPower: 50},
			},
		}},
	}))

	_, err = keeper.NewMsgServerImpl(k).SubmitPocValidationsV2(sdkCtx, &types.MsgSubmitPocValidationsV2{
		Creator:                  testutil.Validator,
		PocStageStartBlockHeight: 100,
		Validations: []*types.PoCValidationEntryV2{
			{ParticipantAddress: testutil.Executor, ModelId: testPoCModelID, ValidatedWeight: 1},
			{ParticipantAddress: testutil.Executor, ModelId: testPoCModelID2, ValidatedWeight: -1},
		},
	})
	require.NoError(t, err)
	storedA, err := k.HasPocValidationV2(sdkCtx, 100, testutil.Executor, testPoCModelID, testutil.Validator)
	require.NoError(t, err)
	require.True(t, storedA)
	storedB, err := k.HasPocValidationV2(sdkCtx, 100, testutil.Executor, testPoCModelID2, testutil.Validator)
	require.NoError(t, err)
	require.False(t, storedB)
}

func TestMLNodeWeightDistribution_RawRowOverflowBeforePermission(t *testing.T) {
	_, sdkCtx, ms := setupWeightDistribution(t)
	msg := &types.MsgMLNodeWeightDistribution{
		Creator: testutil.Executor,
		Entries: []*types.MLNodeDistributionEntry{{
			Weights: make([]*types.MLNodeWeight, types.MaxMLNodeWeightRawRowsPerEntry+1),
		}},
	}
	_, err := ms.MLNodeWeightDistribution(sdkCtx, msg)
	require.Error(t, err)
	require.Error(t, msg.ValidateRawBounds())
	require.False(t, errors.Is(err, types.ErrParticipantNotFound))
}

func TestMLNodeWeightDistribution_RawBoundsBeforePermission(t *testing.T) {
	_, sdkCtx, ms := setupWeightDistribution(t)
	msg := &types.MsgMLNodeWeightDistribution{
		Creator: testutil.Executor,
		Entries: make([]*types.MLNodeDistributionEntry, types.MaxMLNodeWeightEntriesPerMessage+1),
	}
	_, err := ms.MLNodeWeightDistribution(sdkCtx, msg)
	require.Error(t, err)
	require.Error(t, msg.ValidateRawBounds())
	require.False(t, errors.Is(err, types.ErrParticipantNotFound))
}

func setupWeightDistribution(t *testing.T) (keeper.Keeper, sdk.Context, types.MsgServer) {
	t.Helper()
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
	k.SetModel(sdkCtx, &types.Model{Id: testPoCModelID})
	require.NoError(t, k.SetPoCV2StoreCommit(sdkCtx, types.PoCV2StoreCommit{
		ParticipantAddress:       testutil.Validator,
		PocStageStartBlockHeight: 100,
		Count:                    10000,
		RootHash:                 make([]byte, 32),
		ModelId:                  testPoCModelID,
	}))
	return k, sdkCtx, keeper.NewMsgServerImpl(k)
}
