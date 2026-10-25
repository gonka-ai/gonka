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

// TestSubmitPocValidationsV2_PositiveVoteReclaimsSignerCapFromNegativeRow proves
// that a signer's per-stage validation budget cannot be exhausted by its own
// negative (invalid) votes in a way that drops a later positive vote.
//
// With validation_slots=0 every validator validates every (participant, model)
// pair. A failing target is reported with an immediate -1; a passing target's +1
// is submitted later, after the ML callback. The per-signer stage cap counts every
// stored row regardless of sign, so without reclamation a signer that reports many
// invalid targets first fills its own budget and its later positive votes are
// rejected - excluding otherwise-valid participants from the next epoch.
//
// The cap must still bound how many rows one signer stores per stage, but a
// positive vote at the cap reclaims one of the signer's own non-positive rows so
// the total stays bounded while the consensus-bearing vote is never starved.
func TestSubmitPocValidationsV2_PositiveVoteReclaimsSignerCapFromNegativeRow(t *testing.T) {
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
	registerPoCParticipant(t, k, sdkCtx, testutil.Executor)  // sybil / invalid target (-1)
	registerPoCParticipant(t, k, sdkCtx, testutil.Executor2) // honest target (+1 later)
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
	validatorAcc, err := types.CanonicalAccAddress(testutil.Validator)
	require.NoError(t, err)

	// 1) The signer reports the invalid target first: a -1 fills its cap-1 budget.
	_, err = keeper.NewMsgServerImpl(k).SubmitPocValidationsV2(sdkCtx, &types.MsgSubmitPocValidationsV2{
		Creator:                  testutil.Validator,
		PocStageStartBlockHeight: 100,
		Validations: []*types.PoCValidationEntryV2{
			{ParticipantAddress: testutil.Executor, ModelId: testPoCModelID, ValidatedWeight: -1},
		},
	})
	require.NoError(t, err)
	count, err := k.CountPocValidationsV2ForValidator(sdkCtx, 100, validatorAcc)
	require.NoError(t, err)
	require.Equal(t, 1, count, "the negative row fills the budget")

	// 2) The signer's later positive vote for the honest target must be stored by
	// reclaiming its own negative row - not starved by it.
	_, err = keeper.NewMsgServerImpl(k).SubmitPocValidationsV2(sdkCtx, &types.MsgSubmitPocValidationsV2{
		Creator:                  testutil.Validator,
		PocStageStartBlockHeight: 100,
		Validations: []*types.PoCValidationEntryV2{
			{ParticipantAddress: testutil.Executor2, ModelId: testPoCModelID, ValidatedWeight: 1},
		},
	})
	require.NoError(t, err)

	positiveStored, err := k.HasPocValidationV2(sdkCtx, 100, testutil.Executor2, testPoCModelID, testutil.Validator)
	require.NoError(t, err)
	require.True(t, positiveStored, "honest target's positive vote must survive the signer's own negative rows")
	negativeEvicted, err := k.HasPocValidationV2(sdkCtx, 100, testutil.Executor, testPoCModelID, testutil.Validator)
	require.NoError(t, err)
	require.False(t, negativeEvicted, "the reclaimed negative row is removed")
	count, err = k.CountPocValidationsV2ForValidator(sdkCtx, 100, validatorAcc)
	require.NoError(t, err)
	require.Equal(t, 1, count, "total stored per signer stays bounded by the cap")

	// 3) The cap still bounds growth: with no non-positive row left to reclaim, a
	// further positive vote at the cap is rejected (the safeguard is intact).
	_, err = keeper.NewMsgServerImpl(k).SubmitPocValidationsV2(sdkCtx, &types.MsgSubmitPocValidationsV2{
		Creator:                  testutil.Validator,
		PocStageStartBlockHeight: 100,
		Validations: []*types.PoCValidationEntryV2{
			{ParticipantAddress: testutil.Executor, ModelId: testPoCModelID, ValidatedWeight: 1},
		},
	})
	require.ErrorIs(t, err, types.ErrIllegalState, "cap still bounds growth once no negative row remains")
	count, err = k.CountPocValidationsV2ForValidator(sdkCtx, 100, validatorAcc)
	require.NoError(t, err)
	require.Equal(t, 1, count, "rejected batch stores nothing")
}

// TestSubmitPocValidationsV2_NegativeRowsDoNotStarvePositivesAtScale is the
// scale/ordering repro: with validation_slots=0 a validator validates every
// (participant,model) pair, and dapi submits a failing target's -1 synchronously
// while a passing target's +1 arrives later. So N sybil targets registered in the
// epoch fill a signer's whole stage budget with -1 before any honest +1 lands.
// Without reclamation every honest +1 after the cap is starved; with it each +1
// reclaims one of the signer's own -1 rows, so all honest votes survive and the
// total stored per signer still never exceeds the cap.
func TestSubmitPocValidationsV2_NegativeRowsDoNotStarvePositivesAtScale(t *testing.T) {
	const n = 4 // sybil pairs = cap; honest targets = n
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
	params.EpochValidationParams.MaxValidationsPerSignerPerStage = uint32(n)
	require.NoError(t, k.SetParams(sdkCtx, params))
	k.SetEffectiveEpochIndex(sdkCtx, 0)
	k.SetEpoch(sdkCtx, &types.Epoch{Index: 1, PocStartBlockHeight: 100})
	registerPoCParticipant(t, k, sdkCtx, testutil.Validator)
	k.SetModel(sdkCtx, &types.Model{Id: testPoCModelID})

	addr := func(seed byte) string {
		b := make([]byte, 20)
		for i := range b {
			b[i] = seed + byte(i)
		}
		return sdk.AccAddress(b).String()
	}
	sybils := make([]string, n)
	honest := make([]string, n)
	for i := 0; i < n; i++ {
		sybils[i] = addr(byte(10 + i))
		honest[i] = addr(byte(100 + i))
		for _, target := range []string{sybils[i], honest[i]} {
			registerPoCParticipant(t, k, sdkCtx, target)
			require.NoError(t, k.SetPoCV2StoreCommit(sdkCtx, types.PoCV2StoreCommit{
				ParticipantAddress:       target,
				PocStageStartBlockHeight: 100,
				Count:                    10,
				RootHash:                 make([]byte, 32),
				ModelId:                  testPoCModelID,
			}))
		}
	}
	validatorAcc, err := types.CanonicalAccAddress(testutil.Validator)
	require.NoError(t, err)
	srv := keeper.NewMsgServerImpl(k)

	// 1) The n sybil -1 votes arrive first and fill the signer's whole budget.
	for _, s := range sybils {
		_, err = srv.SubmitPocValidationsV2(sdkCtx, &types.MsgSubmitPocValidationsV2{
			Creator:                  testutil.Validator,
			PocStageStartBlockHeight: 100,
			Validations: []*types.PoCValidationEntryV2{
				{ParticipantAddress: s, ModelId: testPoCModelID, ValidatedWeight: -1},
			},
		})
		require.NoError(t, err)
	}
	count, err := k.CountPocValidationsV2ForValidator(sdkCtx, 100, validatorAcc)
	require.NoError(t, err)
	require.Equal(t, n, count, "the negative rows fill the budget")

	// 2) The later positive votes for n honest targets each reclaim a negative row.
	for _, h := range honest {
		_, err = srv.SubmitPocValidationsV2(sdkCtx, &types.MsgSubmitPocValidationsV2{
			Creator:                  testutil.Validator,
			PocStageStartBlockHeight: 100,
			Validations: []*types.PoCValidationEntryV2{
				{ParticipantAddress: h, ModelId: testPoCModelID, ValidatedWeight: 1},
			},
		})
		require.NoError(t, err)
	}
	for _, h := range honest {
		stored, err := k.HasPocValidationV2(sdkCtx, 100, h, testPoCModelID, testutil.Validator)
		require.NoError(t, err)
		require.True(t, stored, "every honest target's +1 must survive the signer's own -1 rows")
	}
	count, err = k.CountPocValidationsV2ForValidator(sdkCtx, 100, validatorAcc)
	require.NoError(t, err)
	require.Equal(t, n, count, "total stored per signer stays bounded by the cap")
}
