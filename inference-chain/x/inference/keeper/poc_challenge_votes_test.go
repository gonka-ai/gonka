package keeper_test

import (
	"fmt"
	"sort"
	"testing"

	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// seedChallengeVoteSnapshot stores a validation snapshot under key with the
// given model -> (address -> voting power) table.
func seedChallengeVoteSnapshot(t *testing.T, k keeper.Keeper, ctx sdk.Context, key int64, powers map[string]map[string]int64) {
	t.Helper()
	modelIDs := make([]string, 0, len(powers))
	for id := range powers {
		modelIDs = append(modelIDs, id)
	}
	sort.Strings(modelIDs)
	models := make([]*types.ModelVotingPowers, 0, len(modelIDs))
	for _, id := range modelIDs {
		models = append(models, &types.ModelVotingPowers{
			ModelId:      id,
			VotingPowers: types.VotingPowerMapToSlice(powers[id]),
		})
	}
	require.NoError(t, k.SetPoCValidationSnapshot(ctx, types.PoCValidationSnapshot{
		PocStageStartHeight: key,
		TotalNetworkWeight:  1000,
		ModelVotingPowers:   models,
	}))
}

func registerChallengeVoters(t *testing.T, k keeper.Keeper, ctx sdk.Context, addrs ...string) {
	t.Helper()
	for _, addr := range addrs {
		require.NoError(t, k.Participants.Set(ctx, sdk.MustAccAddressFromBech32(addr), types.Participant{
			Index:   addr,
			Address: addr,
		}))
	}
}

func challengeVoteEntry(modelID string) *types.PoCValidationEntryV2 {
	return &types.PoCValidationEntryV2{
		ParticipantAddress: testutil.Executor,
		ModelId:            modelID,
		ValidatedWeight:    4,
	}
}

func challengeVoteMsg(creator string, entries ...*types.PoCValidationEntryV2) *types.MsgSubmitPoCChallengeValidations {
	return &types.MsgSubmitPoCChallengeValidations{
		Creator:                  creator,
		PocStageStartBlockHeight: 50,
		Validations:              entries,
	}
}

type challengeVoteFixture struct {
	k       keeper.Keeper
	ctx     sdk.Context
	voteCtx sdk.Context
	ms      types.MsgServer
}

// setupRegularChallengeVote opens a challenge on Executor at height 50 and
// positions voteCtx inside the regular PoC validation exchange window of the
// upcoming epoch 3, whose snapshot key is 2000.
func setupRegularChallengeVote(t *testing.T) challengeVoteFixture {
	t.Helper()
	k, ctx, _ := setupChallengeCreate(t, 100)
	seedOpenChallenge(t, k, ctx, 50)
	registerChallengeVoters(t, k, ctx, testutil.Validator, testutil.Validator2, testutil.Executor2, testutil.Creator, testutil.Requester)
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: 3, PocStartBlockHeight: 2000}))
	upcoming := types.NewEpochContext(types.Epoch{Index: 3, PocStartBlockHeight: 2000}, *challengeEpochParams())
	return challengeVoteFixture{
		k:       k,
		ctx:     ctx,
		voteCtx: ctx.WithBlockHeight(upcoming.StartOfPoCValidation() + 1),
		ms:      keeper.NewMsgServerImpl(k),
	}
}

func (f challengeVoteFixture) storedVotes(t *testing.T) []types.PoCValidationV2 {
	t.Helper()
	vals, err := f.k.ListChallengeValidations(f.ctx, testutil.Executor)
	require.NoError(t, err)
	return vals
}

func TestSubmitPoCChallengeValidations_RejectsVoterWithoutPower(t *testing.T) {
	f := setupRegularChallengeVote(t)
	seedChallengeVoteSnapshot(t, f.k, f.ctx, 2000, map[string]map[string]int64{
		challengeTestModel: {testutil.Validator: 100},
	})

	_, err := f.ms.SubmitPoCChallengeValidations(f.voteCtx, challengeVoteMsg(testutil.Validator2, challengeVoteEntry(challengeTestModel)))
	require.NoError(t, err, "a vote without power is dropped, not rejected")
	require.Empty(t, f.storedVotes(t))

	_, err = f.ms.SubmitPoCChallengeValidations(f.voteCtx, challengeVoteMsg(testutil.Validator, challengeVoteEntry(challengeTestModel)))
	require.NoError(t, err)
	votes := f.storedVotes(t)
	require.Len(t, votes, 1)
	require.Equal(t, testutil.Validator, votes[0].ValidatorParticipantAddress)
}

func TestSubmitPoCChallengeValidations_RejectsModelNotInSnapshot(t *testing.T) {
	f := setupRegularChallengeVote(t)
	f.k.SetModel(f.ctx, &types.Model{Id: "other-model"})
	seedChallengeVoteSnapshot(t, f.k, f.ctx, 2000, map[string]map[string]int64{
		challengeTestModel: {testutil.Validator: 100},
		"other-model":      {testutil.Validator2: 100},
	})

	_, err := f.ms.SubmitPoCChallengeValidations(f.voteCtx, challengeVoteMsg(testutil.Validator,
		challengeVoteEntry("other-model"), // governance model, but no power for this voter
		challengeVoteEntry("zz0"),         // not a model at all
	))
	require.NoError(t, err)
	require.Empty(t, f.storedVotes(t))
}

func TestSubmitPoCChallengeValidations_ZeroPowerIsSkipped(t *testing.T) {
	f := setupRegularChallengeVote(t)
	seedChallengeVoteSnapshot(t, f.k, f.ctx, 2000, map[string]map[string]int64{
		challengeTestModel: {testutil.Validator: 0, testutil.Validator2: 100},
	})

	_, err := f.ms.SubmitPoCChallengeValidations(f.voteCtx, challengeVoteMsg(testutil.Validator, challengeVoteEntry(challengeTestModel)))
	require.NoError(t, err)
	require.Empty(t, f.storedVotes(t))
}

func TestSubmitPoCChallengeValidations_MultiAccountStackingBounded(t *testing.T) {
	f := setupRegularChallengeVote(t)
	const spamModels = 50
	spamEntries := make([]*types.PoCValidationEntryV2, 0, spamModels)
	for i := 0; i < spamModels; i++ {
		id := fmt.Sprintf("spam-model-%d", i)
		f.k.SetModel(f.ctx, &types.Model{Id: id})
		spamEntries = append(spamEntries, challengeVoteEntry(id))
	}
	f.k.SetModel(f.ctx, &types.Model{Id: "model-a"})
	f.k.SetModel(f.ctx, &types.Model{Id: "model-b"})
	seedChallengeVoteSnapshot(t, f.k, f.ctx, 2000, map[string]map[string]int64{
		"model-a": {testutil.Validator: 100, testutil.Validator2: 50},
		"model-b": {testutil.Validator: 100, testutil.Validator2: 50},
	})

	// Three registered accounts without power, each voting on every registered model.
	for _, spammer := range []string{testutil.Executor2, testutil.Creator, testutil.Requester} {
		_, err := f.ms.SubmitPoCChallengeValidations(f.voteCtx, challengeVoteMsg(spammer, spamEntries...))
		require.NoError(t, err)
	}
	require.Empty(t, f.storedVotes(t))

	// Two validators with power on two models each.
	for _, voter := range []string{testutil.Validator, testutil.Validator2} {
		_, err := f.ms.SubmitPoCChallengeValidations(f.voteCtx, challengeVoteMsg(voter,
			challengeVoteEntry("model-a"), challengeVoteEntry("model-b"), challengeVoteEntry("spam-model-1")))
		require.NoError(t, err)
	}
	require.Len(t, f.storedVotes(t), 4)
}

func TestSubmitPoCChallengeValidations_SnapshotMissing(t *testing.T) {
	f := setupRegularChallengeVote(t)

	_, err := f.ms.SubmitPoCChallengeValidations(f.voteCtx, challengeVoteMsg(testutil.Validator, challengeVoteEntry(challengeTestModel)))
	require.ErrorIs(t, err, types.ErrPoCChallengeSnapshotMissing)
	require.Empty(t, f.storedVotes(t))
}

func TestSubmitPoCChallengeValidations_RegularWindowUsesUpcomingEpochSnapshot(t *testing.T) {
	f := setupRegularChallengeVote(t)
	// Power under the confirmation key must not count in the regular window.
	seedChallengeVoteSnapshot(t, f.k, f.ctx, 70, map[string]map[string]int64{
		challengeTestModel: {testutil.Validator: 100},
	})
	seedChallengeVoteSnapshot(t, f.k, f.ctx, 2000, map[string]map[string]int64{
		challengeTestModel: {testutil.Validator2: 100},
	})

	_, err := f.ms.SubmitPoCChallengeValidations(f.voteCtx, challengeVoteMsg(testutil.Validator, challengeVoteEntry(challengeTestModel)))
	require.NoError(t, err)
	require.Empty(t, f.storedVotes(t))

	_, err = f.ms.SubmitPoCChallengeValidations(f.voteCtx, challengeVoteMsg(testutil.Validator2, challengeVoteEntry(challengeTestModel)))
	require.NoError(t, err)
	require.Len(t, f.storedVotes(t), 1)
}

func TestSubmitPoCChallengeValidations_ConfirmationWindowUsesTriggerHeightSnapshot(t *testing.T) {
	f := setupRegularChallengeVote(t)
	require.NoError(t, f.k.SetActiveConfirmationPoCEvent(f.ctx, types.ConfirmationPoCEvent{
		EpochIndex:            2,
		TriggerHeight:         70,
		GenerationStartHeight: 80,
		Phase:                 types.ConfirmationPoCPhase_CONFIRMATION_POC_VALIDATION,
	}))
	params, err := f.k.GetParams(f.ctx)
	require.NoError(t, err)
	event, ok, err := f.k.GetActiveConfirmationPoCEvent(f.ctx)
	require.NoError(t, err)
	require.True(t, ok)
	voteCtx := f.ctx.WithBlockHeight(event.GetValidationStart(params.EpochParams))

	// Power under the regular key must not count in the confirmation window.
	seedChallengeVoteSnapshot(t, f.k, f.ctx, 2000, map[string]map[string]int64{
		challengeTestModel: {testutil.Validator: 100},
	})
	seedChallengeVoteSnapshot(t, f.k, f.ctx, 70, map[string]map[string]int64{
		challengeTestModel: {testutil.Validator2: 100},
	})

	_, err = f.ms.SubmitPoCChallengeValidations(voteCtx, challengeVoteMsg(testutil.Validator, challengeVoteEntry(challengeTestModel)))
	require.NoError(t, err)
	require.Empty(t, f.storedVotes(t))

	_, err = f.ms.SubmitPoCChallengeValidations(voteCtx, challengeVoteMsg(testutil.Validator2, challengeVoteEntry(challengeTestModel)))
	require.NoError(t, err)
	require.Len(t, f.storedVotes(t), 1)
}

func TestSubmitPoCChallengeValidations_EntryCountBounded(t *testing.T) {
	f := setupRegularChallengeVote(t)
	seedChallengeVoteSnapshot(t, f.k, f.ctx, 2000, map[string]map[string]int64{
		challengeTestModel: {testutil.Validator: 100},
	})

	_, err := f.ms.SubmitPoCChallengeValidations(f.voteCtx, challengeVoteMsg(testutil.Validator))
	require.Error(t, err)
	require.Contains(t, err.Error(), "must not be empty")

	entries := make([]*types.PoCValidationEntryV2, 0, types.MaxPoCChallengeValidationsPerMsg+1)
	for i := 0; i <= types.MaxPoCChallengeValidationsPerMsg; i++ {
		entries = append(entries, challengeVoteEntry(fmt.Sprintf("model-%d", i)))
	}
	_, err = f.ms.SubmitPoCChallengeValidations(f.voteCtx, challengeVoteMsg(testutil.Validator, entries...))
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds maximum count")
	require.Empty(t, f.storedVotes(t))

	_, err = f.ms.SubmitPoCChallengeValidations(f.voteCtx, challengeVoteMsg(testutil.Validator, entries[:types.MaxPoCChallengeValidationsPerMsg]...))
	require.NoError(t, err)
}

func TestSubmitPoCChallengeValidations_NonCanonicalCreatorRejected(t *testing.T) {
	f := setupRegularChallengeVote(t)
	seedChallengeVoteSnapshot(t, f.k, f.ctx, 2000, map[string]map[string]int64{
		challengeTestModel: {testutil.Validator: 100},
	})
	msg := challengeVoteMsg(testutil.Validator, challengeVoteEntry(challengeTestModel))
	msg.Creator = toUpperBech32(testutil.Validator)

	_, err := f.ms.SubmitPoCChallengeValidations(f.voteCtx, msg)
	require.ErrorIs(t, err, types.ErrInvalidAddress)
	require.Empty(t, f.storedVotes(t))
}

func toUpperBech32(addr string) string {
	out := make([]byte, len(addr))
	for i := 0; i < len(addr); i++ {
		c := addr[i]
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}

// TestSubmitPoCChallengeValidations_GasForHonestVote records the gas one
// single-entry vote costs against snapshots of increasing size. The DAPI
// submits this message with a fixed 250k gas limit.
func TestSubmitPoCChallengeValidations_GasForHonestVote(t *testing.T) {
	cases := []struct {
		name    string
		models  int
		entries int
	}{
		{name: "live", models: 3, entries: 28},
		{name: "x10", models: 5, entries: 300},
		{name: "stress", models: 8, entries: 2000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := setupRegularChallengeVote(t)
			powers := make(map[string]map[string]int64, tc.models)
			perModel := tc.entries / tc.models
			for m := 0; m < tc.models; m++ {
				id := fmt.Sprintf("model-%d", m)
				if m == 0 {
					id = challengeTestModel
				}
				table := make(map[string]int64, perModel)
				for i := 0; i < perModel; i++ {
					table[sdk.AccAddress([]byte(fmt.Sprintf("voter-%03d-%08d", m, i))).String()] = int64(10 + i)
				}
				table[testutil.Validator] = 100
				powers[id] = table
			}
			seedChallengeVoteSnapshot(t, f.k, f.ctx, 2000, powers)

			voteCtx := f.voteCtx.WithGasMeter(storetypes.NewInfiniteGasMeter())
			_, err := f.ms.SubmitPoCChallengeValidations(voteCtx, challengeVoteMsg(testutil.Validator, challengeVoteEntry(challengeTestModel)))
			require.NoError(t, err)
			require.Len(t, f.storedVotes(t), 1)
			gas := voteCtx.GasMeter().GasConsumed()
			t.Logf("snapshot %s: %d models, %d power entries -> %d gas", tc.name, tc.models, tc.entries, gas)
			if tc.name == "live" {
				require.Less(t, gas, uint64(150_000), "live-size snapshot must leave headroom under the 250k DAPI gas limit")
			}
		})
	}
}
