package inference

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"cosmossdk.io/collections"
	mathsdk "cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

type depthGrowthCollateral struct {
	noopCollateralKeeper
	balance mathsdk.Int
	slashes int
}

func (c *depthGrowthCollateral) GetCollateral(context.Context, sdk.AccAddress) (sdk.Coin, bool) {
	return sdk.NewCoin(types.BaseCoin, c.balance), true
}
func (c *depthGrowthCollateral) Slash(_ context.Context, _ sdk.AccAddress, fraction mathsdk.LegacyDec, _ string, _ mathsdk.Int) (sdk.Coin, error) {
	c.slashes++
	debit := fraction.MulInt(c.balance).TruncateInt()
	c.balance = c.balance.Sub(debit)
	return sdk.NewCoin(types.BaseCoin, debit), nil
}

func TestDepthGrowthChallengeScoringAndFunds(t *testing.T) {
	data, err := os.ReadFile("../../../testutil/testdata/poc_depth_growth.json")
	require.NoError(t, err)
	var fs []struct {
		Count uint32
		Root  string
		Depth uint32
	}
	require.NoError(t, json.Unmarshal(data, &fs))
	for _, rotate := range []bool{false, true} {
		for _, grow := range []bool{false, true} {
			name := "retained_early_control"
			if grow {
				name = "expanded_honest"
			}
			if rotate {
				name += "_rotate"
			}
			t.Run(name, func(t *testing.T) {
				collateral := &depthGrowthCollateral{balance: mathsdk.NewInt(10000)}

				am, k, ctx := prepareChallengeEval(t, 400, 0, []challengeEvalModel{{id: "m1", weight: 4}, {id: "m2", weight: 4}}, collateral)
				params, err := k.GetParams(ctx)
				require.NoError(t, err)
				params.CollateralParams.SlashFractionDowntime = types.DecimalFromFloat(0.1)
				require.NoError(t, k.SetParams(ctx, params))
				for _, model := range []string{"m1", "m2"} {
					k.SetModel(ctx, &types.Model{Id: model})
				}
				ms := keeper.NewMsgServerImpl(k)
				submit := func(i int, height int64) {
					f := fs[i]
					root, err := hex.DecodeString(f.Root)
					require.NoError(t, err)
					entries := []*types.PoCV2CommitEntry{}
					for _, model := range []string{"m1", "m2"} {
						entries = append(entries, &types.PoCV2CommitEntry{ModelId: model, Count: f.Count, RootHash: root, TreeDepth: f.Depth})
					}
					at := ctx.WithBlockHeight(height).WithGasMeter(storetypes.NewGasMeter(1_000_000_000))
					_, err = ms.PoCChallengeStoreCommit(at, &types.MsgPoCChallengeStoreCommit{Creator: testutil.Executor, PocStageStartBlockHeight: 100, Entries: entries})
					require.NoError(t, err)
				}
				submit(1, 110)
				if grow {
					submit(3, 111)
				}

				require.NoError(t, k.SetActiveConfirmationPoCEvent(ctx, types.ConfirmationPoCEvent{EpochIndex: 2, TriggerHeight: 100, GenerationStartHeight: 100, Phase: types.ConfirmationPoCPhase_CONFIRMATION_POC_VALIDATION}))
				event, found, err := k.GetActiveConfirmationPoCEvent(ctx)
				require.NoError(t, err)
				require.True(t, found)
				validator := sdk.MustAccAddressFromBech32(testutil.Validator)
				require.NoError(t, k.Participants.Set(ctx, validator, types.Participant{Index: testutil.Validator, Address: testutil.Validator, Status: types.ParticipantStatus_ACTIVE}))
				commits, err := k.ListChallengeCommits(ctx, testutil.Executor)
				require.NoError(t, err)
				votes := []*types.PoCValidationEntryV2{}
				for _, c := range commits {
					votes = append(votes, &types.PoCValidationEntryV2{ParticipantAddress: testutil.Executor, ModelId: c.ModelId, ValidatedWeight: int64(c.Count)})
				}
				_, err = ms.SubmitPoCChallengeValidations(ctx.WithBlockHeight(event.GetValidationStart(params.EpochParams)), &types.MsgSubmitPoCChallengeValidations{Creator: testutil.Validator, PocStageStartBlockHeight: 100, Validations: votes})
				require.NoError(t, err)
				storedVotes, err := k.ListChallengeValidations(ctx, testutil.Executor)
				require.NoError(t, err)
				require.Len(t, storedVotes, 2)
				decisionHeight := event.GetValidationEnd(params.EpochParams) + 1
				ctx = ctx.WithBlockHeight(decisionHeight)
				require.NoError(t, am.decideCurrentChallengeSegments(ctx, 2, 500, 180, rotate))
				ch, found, err := k.GetPoCChallenge(ctx, testutil.Executor)
				require.NoError(t, err)
				require.True(t, found)
				p, ok := k.GetParticipant(ctx, testutil.Executor)
				require.True(t, ok)
				excluded, err := k.ExcludedParticipantsMap.Has(ctx, collections.Join(uint64(2), sdk.MustAccAddressFromBech32(testutil.Executor)))
				require.NoError(t, err)
				group, found := k.GetEpochGroupData(ctx, 2, "")
				require.True(t, found)
				if grow {
					require.Equal(t, types.ParticipantStatus_ACTIVE, p.Status)
					require.False(t, excluded)
					require.Zero(t, collateral.slashes)
					require.Equal(t, "10000", collateral.balance.String())
					require.Equal(t, int64(8), group.ValidationWeights[0].ConfirmationWeight)
					if rotate {
						require.Equal(t, types.PoCChallengeState_POC_CHALLENGE_STATE_OPEN, ch.State)
						require.Equal(t, decisionHeight, ch.StartHeight)
					} else {
						require.Equal(t, types.PoCChallengeState_POC_CHALLENGE_STATE_PASSED, ch.State)
					}
				} else {
					require.Equal(t, types.PoCChallengeState_POC_CHALLENGE_STATE_CHALLENGE_FAILED, ch.State)
					require.Equal(t, types.ParticipantStatus_INACTIVE, p.Status)
					require.True(t, excluded)
					require.Equal(t, 1, collateral.slashes)
					require.Equal(t, "9000", collateral.balance.String())
				}

				require.NoError(t, am.decideCurrentChallengeSegments(ctx, 2, 500, 180, rotate))
				if grow {
					require.Zero(t, collateral.slashes)
				} else {
					require.Equal(t, 1, collateral.slashes)
				}
			})
		}
	}
}

func TestDepthGrowthRolloutNeedsActualVotes(t *testing.T) {
	key := types.PoCParticipantModelKey{ParticipantAddress: testutil.Executor, ModelID: "m"}
	for _, tc := range []struct {
		name          string
		ready         int
		guardianVotes bool
		want          bool
	}{
		{"all validators ready", 3, false, true},
		{"legacy validators abstain below quorum", 1, false, false},
		{"guardians also lack depth", 1, false, false},
		{"complete guardian can break tie", 1, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addresses := []string{testutil.Validator, testutil.Validator2, testutil.Requester}
			votes := []types.PoCValidationV2{}
			for i := 0; i < tc.ready; i++ {
				votes = append(votes, types.PoCValidationV2{ValidatorParticipantAddress: addresses[i], ValidatedWeight: 4})
			}
			if tc.guardianVotes {
				votes = append(votes, types.PoCValidationV2{ValidatorParticipantAddress: testutil.Creator, ValidatedWeight: 4})
			}
			wc := &PoCWeightCalculator{ModelVotingPowers: map[string]map[string]int64{"m": {addresses[0]: 34, addresses[1]: 33, addresses[2]: 33}}, TotalNetworkWeight: 100, Validations: map[types.PoCParticipantModelKey][]types.PoCValidationV2{key: votes}, GuardianEnabled: true, GuardianAddresses: map[string]bool{testutil.Creator: true}, Logger: noopLogger{}}
			require.Equal(t, tc.want, wc.pocValidated(wc.getParticipantValidations(key), key))
		})
	}
}
