package inference

import (
	"math"
	"testing"

	mathsdk "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func TestNonSlotValidationUsesFullModelElectorate(t *testing.T) {
	cases := []struct {
		name      string
		live      int64
		approving int64
		opposing  int64
		abstains  bool
		threshold uint32
		want      bool
	}{
		{name: "before removal", live: 600, approving: 250, opposing: 350},
		{name: "removed delegator cannot lower threshold", live: 450, approving: 250, opposing: 350},
		{name: "honest majority after removal", live: 450, approving: 350, opposing: 250, want: true},
		{name: "abstaining voting power still counts", live: 450, approving: 250, opposing: 350, abstains: true},
		{name: "larger live total is retained", live: 800, approving: 350, opposing: 250},
		{name: "exactly half is insufficient", live: 450, approving: 300, opposing: 300},
		{name: "configured supermajority rejects", live: 450, approving: 400, opposing: 200, threshold: 6667},
		{name: "configured supermajority accepts", live: 450, approving: 401, opposing: 199, threshold: 6667, want: true},
		{name: "removed direct member", live: 250, approving: 250, want: true},
		{name: "direct removal retains other root weight", live: 500, approving: 250, opposing: 100},
		{name: "int64 boundary", live: 450, approving: math.MaxInt64/2 + 1, opposing: math.MaxInt64 / 2, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := types.PoCParticipantModelKey{ParticipantAddress: "host", ModelID: "model-a"}
			wc := &PoCWeightCalculator{
				ModelVotingPowers:  map[string]map[string]int64{key.ModelID: {"a": tc.approving, "c": tc.opposing}},
				TotalNetworkWeight: tc.live,
				PocParams:          &types.PocParams{ValidationVoteThresholdBps: tc.threshold},
				Logger:             noopLogger{},
			}
			votes := []types.PoCValidationV2{{ValidatorParticipantAddress: "a", ValidatedWeight: 1}}
			if !tc.abstains {
				votes = append(votes, types.PoCValidationV2{ValidatorParticipantAddress: "c"})
			}
			require.Equal(t, tc.want, wc.pocValidated(votes, key))
		})
	}
}

func TestNonSlotValidationSeparatesModelElectorates(t *testing.T) {
	wc := &PoCWeightCalculator{
		ModelVotingPowers: map[string]map[string]int64{
			"large": {"a": 250, "c": 350},
			"small": {"a": 300, "c": 100},
		},
		TotalNetworkWeight: 450,
		Logger:             noopLogger{},
	}
	votes := []types.PoCValidationV2{{ValidatorParticipantAddress: "a", ValidatedWeight: 1}, {ValidatorParticipantAddress: "c"}}
	// Repeat in both orders to exercise reuse without sharing a model's base.
	for _, model := range []string{"large", "small", "small", "large"} {
		require.Equal(t, model == "small", wc.pocValidated(votes, types.PoCParticipantModelKey{ModelID: model}))
	}
}

type nonSlotVoteLogger struct {
	noopLogger
	errors     []string
	guardTotal int64
}

func (l *nonSlotVoteLogger) LogError(message string, _ types.SubSystem, _ ...interface{}) {
	l.errors = append(l.errors, message)
}

func (l *nonSlotVoteLogger) LogWarn(_ string, _ types.SubSystem, fields ...interface{}) {
	for i := 0; i+1 < len(fields); i += 2 {
		if fields[i] == "totalWeight" {
			l.guardTotal, _ = fields[i+1].(int64)
		}
	}
}

func TestNonSlotGuardianReceivesNormalizedElectorate(t *testing.T) {
	key := types.PoCParticipantModelKey{ModelID: "model-a"}
	logger := &nonSlotVoteLogger{}
	wc := &PoCWeightCalculator{
		ModelVotingPowers:  map[string]map[string]int64{key.ModelID: {"a": 250, "c": 350}},
		TotalNetworkWeight: 450,
		Logger:             logger,
	}
	// Neither side has a majority of the 600 model VP. The old live-only
	// denominator accepted a's vote instead of entering guardian protection.
	require.False(t, wc.pocValidated([]types.PoCValidationV2{{ValidatorParticipantAddress: "a", ValidatedWeight: 1}}, key))
	require.Equal(t, int64(600), logger.guardTotal)
}

func TestNonSlotAmbiguousMajoritiesUseGuardianProtection(t *testing.T) {
	cases := []struct {
		name    string
		enabled bool
		votes   []types.PoCValidationV2
		want    bool
	}{
		{name: "no guardians"},
		{name: "guardians silent", enabled: true},
		{name: "guardian rejects", enabled: true, votes: []types.PoCValidationV2{{ValidatorParticipantAddress: "guardian"}}},
		{name: "guardian accepts", enabled: true, votes: []types.PoCValidationV2{{ValidatorParticipantAddress: "guardian", ValidatedWeight: 1}}, want: true},
		{name: "guardians split", enabled: true, votes: []types.PoCValidationV2{
			{ValidatorParticipantAddress: "guardian", ValidatedWeight: 1}, {ValidatorParticipantAddress: "guardian-2"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := types.PoCParticipantModelKey{ModelID: "model-a"}
			// Stored votes are unique by voter. Synthetic contradictory entries
			// exercise the defensive branch, which a valid >=50% vote cannot reach.
			votes := []types.PoCValidationV2{{ValidatorParticipantAddress: "a", ValidatedWeight: 1}, {ValidatorParticipantAddress: "a"}}
			logger := &nonSlotVoteLogger{}
			wc := &PoCWeightCalculator{
				ModelVotingPowers:  map[string]map[string]int64{key.ModelID: {"a": 100}},
				TotalNetworkWeight: 50,
				Validations:        map[types.PoCParticipantModelKey][]types.PoCValidationV2{key: append(votes, tc.votes...)},
				GuardianEnabled:    tc.enabled,
				GuardianAddresses:  map[string]bool{"guardian": true, "guardian-2": true},
				Logger:             logger,
			}
			require.Equal(t, tc.want, wc.pocValidated(wc.getParticipantValidations(key), key))
			require.Len(t, logger.errors, 1, "an ambiguous majority must be reported")
		})
	}
}

func TestNonSlotInvalidElectorateRejects(t *testing.T) {
	for _, tc := range []struct {
		name   string
		powers map[string]int64
	}{
		{name: "negative VP", powers: map[string]int64{"a": 100, "c": -1}},
		{name: "sum overflows int64", powers: map[string]int64{"a": math.MaxInt64/2 + 1, "c": math.MaxInt64/2 + 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := types.PoCParticipantModelKey{ModelID: "model-a"}
			logger := &nonSlotVoteLogger{}
			wc := &PoCWeightCalculator{
				ModelVotingPowers:  map[string]map[string]int64{key.ModelID: tc.powers},
				TotalNetworkWeight: 100,
				Validations: map[types.PoCParticipantModelKey][]types.PoCValidationV2{key: {
					{ValidatorParticipantAddress: "a", ValidatedWeight: 1},
					{ValidatorParticipantAddress: "guardian", ValidatedWeight: 1},
				}},
				GuardianEnabled:   true,
				GuardianAddresses: map[string]bool{"guardian": true},
				Logger:            logger,
			}
			require.False(t, wc.pocValidated(wc.getParticipantValidations(key), key))
			require.NotEmpty(t, logger.errors)
		})
	}
}

type delegatedVoteFixture struct {
	keeper   keeper.Keeper
	ctx      sdk.Context
	module   AppModule
	vp       map[string]int64
	upcoming types.Epoch
}

func newDelegatedVoteFixture(t *testing.T, capped bool) delegatedVoteFixture {
	t.Helper()
	k, ctx := newMinimalInferenceKeeper(t)
	const model = "model-a"
	weights := map[string]int64{testutil.Validator: 100, testutil.Validator2: 150, testutil.Executor2: 350}
	direct := []string{testutil.Validator, testutil.Executor2}
	if capped {
		weights = map[string]int64{testutil.Validator: 100, testutil.Validator2: 300, testutil.Executor2: 100, testutil.Creator: 100}
		direct = append(direct, testutil.Creator)
	}
	delegation := &DelegationWeightCalculator{
		Groups:      map[string]*GroupData{model: {Members: direct}},
		Delegations: map[string]map[string]string{model: {testutil.Validator2: testutil.Validator}},
	}
	vp := delegation.ComputeGroupVotingPowers(model, map[string]ParticipationMode{testutil.Validator2: ModeDelegate}, weights)
	if capped {
		capPerModelVotingPowers(vp, mathsdk.LegacyNewDecWithPrec(30, 2), model, noopLogger{})
		require.Equal(t, map[string]int64{testutil.Validator: 180, testutil.Executor2: 100, testutil.Creator: 100}, vp)
	}

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams.ValidationSlots = 0
	params.PocParams.ValidationVoteThresholdBps = 5000
	params.PocParams.PocNormalizationEnabled = false
	require.NoError(t, k.SetParams(ctx, params))
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 5))
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: 5, PocStartBlockHeight: 500}))
	upcoming := types.Epoch{Index: 6, PocStartBlockHeight: 600}
	require.NoError(t, k.SetEpoch(ctx, &upcoming))

	var rootWeights, modelWeights []*types.ValidationWeight
	var active []*types.ActiveParticipant
	for _, address := range sortedKeys(weights) {
		// Distinct real/trust weights pin the post-CapWeight snapshot path.
		rootWeights = append(rootWeights, &types.ValidationWeight{MemberAddress: address, Weight: weights[address] * 10})
		active = append(active, &types.ActiveParticipant{Index: address, Weight: weights[address] * 10, CapWeight: weights[address]})
		require.NoError(t, k.SetParticipant(ctx, types.Participant{
			Index: address, Address: address, ValidatorKey: "validator-key-" + address, Status: types.ParticipantStatus_ACTIVE,
		}))
		if power, ok := vp[address]; ok {
			modelWeights = append(modelWeights, &types.ValidationWeight{MemberAddress: address, Weight: weights[address] * 10, VotingPower: power})
		}
	}
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{EpochId: 5, Participants: active, CapWeightApplied: true}))
	k.SetEpochGroupData(ctx, types.EpochGroupData{EpochIndex: 5, EpochGroupId: 77, SubGroupModels: []string{model}, ValidationWeights: rootWeights})
	k.SetEpochGroupData(ctx, types.EpochGroupData{EpochIndex: 5, EpochGroupId: 78, ModelId: model, ValidationWeights: modelWeights})
	for _, epoch := range []uint64{5, 6} {
		require.NoError(t, k.SetRandomSeed(ctx, types.RandomSeed{Participant: testutil.Validator, EpochIndex: epoch, Signature: "seed"}))
	}
	require.NoError(t, k.SetPoCV2StoreCommit(ctx, types.PoCV2StoreCommit{ParticipantAddress: testutil.Validator, PocStageStartBlockHeight: 600, Count: 40, ModelId: model}))
	require.NoError(t, k.SetMLNodeWeightDistribution(ctx, types.MLNodeWeightDistribution{
		ParticipantAddress: testutil.Validator, PocStageStartBlockHeight: 600, ModelId: model,
		Weights: []*types.MLNodeWeight{{NodeId: "fresh-node", Weight: 40}},
	}))
	return delegatedVoteFixture{keeper: k, ctx: ctx, module: NewAppModule(nil, k, nil, nil, nil, nil), vp: vp, upcoming: upcoming}
}

func TestDelegatedVotingPowerAcrossSnapshotPaths(t *testing.T) {
	cases := []struct {
		name       string
		remove     string
		valid      []string
		live       int64
		modelTotal int64
		capped     bool
		want       bool
	}{
		{name: "before removal", valid: []string{testutil.Validator}, live: 600, modelTotal: 600},
		{name: "removed delegator", remove: testutil.Validator2, valid: []string{testutil.Validator}, live: 450, modelTotal: 600},
		{name: "honest majority after removal", remove: testutil.Validator2, valid: []string{testutil.Executor2}, live: 450, modelTotal: 600, want: true},
		{name: "removed direct voter", remove: testutil.Executor2, valid: []string{testutil.Validator}, live: 250, modelTotal: 250, want: true},
		{name: "capped electorate before removal", valid: []string{testutil.Validator}, live: 600, modelTotal: 380, capped: true},
		{name: "capped electorate after removal", remove: testutil.Validator2, valid: []string{testutil.Validator}, live: 300, modelTotal: 380, capped: true},
		{name: "capped electorate honest majority", remove: testutil.Validator2, valid: []string{testutil.Executor2, testutil.Creator}, live: 300, modelTotal: 380, capped: true, want: true},
	}
	for _, path := range []string{"regular", "confirmation"} {
		t.Run(path, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					f := newDelegatedVoteFixture(t, tc.capped)
					if tc.remove != "" {
						group, err := f.keeper.GetCurrentEpochGroup(f.ctx)
						require.NoError(t, err)
						require.NoError(t, group.RemoveMember(f.ctx, &types.Participant{Address: tc.remove}))
					}
					for _, voter := range sortedKeys(f.vp) {
						if voter == tc.remove {
							continue
						}
						var validatedWeight int64
						for _, address := range tc.valid {
							if voter == address {
								validatedWeight = 40
							}
						}
						require.NoError(t, f.keeper.SetPocValidationV2(f.ctx, types.PoCValidationV2{
							ParticipantAddress: testutil.Validator, ValidatorParticipantAddress: voter,
							PocStageStartBlockHeight: 600, ModelId: "model-a", ValidatedWeight: validatedWeight,
						}))
					}
					if path == "regular" {
						f.module.captureValidationSnapshot(f.ctx, 600, 600, "regular PoC")
					} else {
						f.module.captureConfirmationValidationSnapshot(f.ctx, 600, 600)
					}
					snapshot, found, err := f.keeper.GetPoCValidationSnapshot(f.ctx, 600)
					require.NoError(t, err)
					require.True(t, found)
					require.Equal(t, tc.live, snapshot.TotalNetworkWeight)
					require.Len(t, snapshot.ModelVotingPowers, 1)
					powers := types.VotingPowerSliceToMap(snapshot.ModelVotingPowers[0].VotingPowers)
					total, ok := sumInt64Safe(powers)
					require.True(t, ok)
					require.Equal(t, tc.modelTotal, total, "stored target VP retains delegation after removal")

					var result []*types.ActiveParticipant
					if path == "regular" {
						result = f.module.computeNewWeights(f.ctx, f.upcoming).participants
					} else {
						result = f.module.updateConfirmationWeightsV2(f.ctx, &types.ConfirmationPoCEvent{EpochIndex: 5, TriggerHeight: 600}, snapshot)
					}
					if tc.want {
						require.Len(t, result, 1)
						require.Equal(t, testutil.Validator, result[0].Index)
						require.Equal(t, int64(40), result[0].Weight)
					} else {
						require.Empty(t, result)
					}
				})
			}
		})
	}
}
