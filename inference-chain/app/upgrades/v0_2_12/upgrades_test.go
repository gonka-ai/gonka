package v0_2_12

import (
	"testing"

	"cosmossdk.io/store/prefix"
	sdk "github.com/cosmos/cosmos-sdk/types"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/sample"
	blstypes "github.com/productscience/inference/x/bls/types"
	inferencekeeper "github.com/productscience/inference/x/inference/keeper"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestUpgradeName(t *testing.T) {
	require.Equal(t, "v0.2.12", UpgradeName)
}

func TestClearTrainingState(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	execAddr := sdk.MustAccAddressFromBech32(sample.AccAddress())
	startAddr := sdk.MustAccAddressFromBech32(sample.AccAddress())

	require.NoError(t, k.TrainingExecAllowListSet.Set(ctx, execAddr))
	require.NoError(t, k.TrainingStartAllowListSet.Set(ctx, startAddr))

	store := inferencekeeper.EmptyPrefixStore(ctx, &k)
	store.Set([]byte(inferencetypes.TrainingTaskKeyPrefix+"1"), []byte("task"))
	store.Set([]byte(inferencetypes.TrainingTaskSequenceKey), []byte{1})
	store.Set([]byte(inferencetypes.QueuedTrainingTaskKeyPrefix+"1"), []byte{1})
	store.Set([]byte(inferencetypes.InProgressTrainingTaskKeyPrefix+"1"), []byte{1})
	store.Set([]byte("TrainingTask/sync/1/store/key/value"), []byte("value"))
	store.Set([]byte("TrainingTask/sync/1/heartbeat/0/participant/node"), []byte("hb"))
	store.Set([]byte("TrainingTask/sync/1/barrier/b1/0/participant/node/value"), []byte("barrier"))

	require.NoError(t, clearTrainingState(ctx, k))

	hasExec, err := k.TrainingExecAllowListSet.Has(ctx, execAddr)
	require.NoError(t, err)
	require.False(t, hasExec)

	hasStart, err := k.TrainingStartAllowListSet.Has(ctx, startAddr)
	require.NoError(t, err)
	require.False(t, hasStart)

	for _, key := range [][]byte{
		[]byte(inferencetypes.TrainingTaskKeyPrefix + "1"),
		[]byte(inferencetypes.TrainingTaskSequenceKey),
		[]byte(inferencetypes.QueuedTrainingTaskKeyPrefix + "1"),
		[]byte(inferencetypes.InProgressTrainingTaskKeyPrefix + "1"),
		[]byte("TrainingTask/sync/1/store/key/value"),
		[]byte("TrainingTask/sync/1/heartbeat/0/participant/node"),
		[]byte("TrainingTask/sync/1/barrier/b1/0/participant/node/value"),
	} {
		require.Nil(t, store.Get(key), "expected key %q to be deleted", string(key))
	}
}

// legacyPrefixes is the ordered list of store prefixes whose key codec changed
// in v0.2.12 and whose old data must be wiped by clearLegacyPoCv2Data.
var legacyPrefixes = [][]byte{
	inferencetypes.LegacyPoCValidationV2Prefix,
	inferencetypes.LegacyPoCV2StoreCommitPrefix,
	inferencetypes.LegacyMLNodeWeightDistributionPrefix,
}

func countPrefixEntries(t *testing.T, store *prefix.Store, pfx []byte) int {
	t.Helper()
	sub := prefix.NewStore(store, pfx)
	iter := sub.Iterator(nil, nil)
	defer iter.Close()
	count := 0
	for ; iter.Valid(); iter.Next() {
		count++
	}
	return count
}

func TestClearLegacyPoCv2Data(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	store := inferencekeeper.EmptyPrefixStore(ctx, &k)

	// Write 3 raw entries under each legacy prefix. Key shape is arbitrary --
	// clearLegacyPoCv2Data iterates raw bytes and does not decode keys.
	for i, pfx := range legacyPrefixes {
		for j := 0; j < 3; j++ {
			key := append(append([]byte{}, pfx...), []byte{byte(i), byte(j)}...)
			store.Set(key, []byte("v"))
		}
		require.Equal(t, 3, countPrefixEntries(t, store, pfx),
			"expected 3 entries under legacy prefix %d before clear", i)
	}

	// Canary: unrelated prefix must be untouched.
	canaryKey := append(append([]byte{}, inferencetypes.PocV2EnabledEpochPrefix...), []byte("canary")...)
	store.Set(canaryKey, []byte("keep"))

	require.NoError(t, clearLegacyPoCv2Data(ctx, k))

	for i, pfx := range legacyPrefixes {
		require.Equal(t, 0, countPrefixEntries(t, store, pfx),
			"expected legacy prefix %d to be empty after clear", i)
	}
	require.Equal(t, []byte("keep"), store.Get(canaryKey), "canary entry should survive clear")
}

func requirePoCModelConfig(t *testing.T, models []*inferencetypes.PoCModelConfig, modelID string) *inferencetypes.PoCModelConfig {
	t.Helper()
	for _, model := range models {
		if model != nil && model.ModelId == modelID {
			return model
		}
	}
	require.Failf(t, "missing PoC model config", "model_id=%s", modelID)
	return nil
}

func requireKimiPoCModelConfig(
	t *testing.T,
	models []*inferencetypes.PoCModelConfig,
	baseWeightScaleFactor *inferencetypes.Decimal,
	penaltyStartEpoch uint64,
) {
	t.Helper()
	kimi := requirePoCModelConfig(t, models, kimiModelID)
	require.Equal(t, int64(1024), kimi.SeqLen)
	require.NotNil(t, kimi.StatTest)
	require.Equal(t, &inferencetypes.Decimal{Value: 4, Exponent: -1}, kimi.StatTest.DistThreshold)
	require.Equal(t, &inferencetypes.Decimal{Value: 1, Exponent: -1}, kimi.StatTest.PMismatch)
	require.Equal(t, &inferencetypes.Decimal{Value: 5, Exponent: -2}, kimi.StatTest.PValueThreshold)
	require.Equal(t, kimiWeightScaleFactor(baseWeightScaleFactor), kimi.WeightScaleFactor)
	require.Equal(t, penaltyStartEpoch, kimi.PenaltyStartEpoch)
}

func requireTestnetPoCModelConfig(
	t *testing.T,
	models []*inferencetypes.PoCModelConfig,
	penaltyStartEpoch uint64,
) {
	t.Helper()
	model := requirePoCModelConfig(t, models, testnetModelID)
	require.Equal(t, int64(256), model.SeqLen)
	require.NotNil(t, model.StatTest)
	require.Equal(t, inferencetypes.DecimalFromFloat(0.4), model.StatTest.DistThreshold)
	require.Equal(t, inferencetypes.DecimalFromFloat(0.1), model.StatTest.PMismatch)
	require.Equal(t, inferencetypes.DecimalFromFloat(0.05), model.StatTest.PValueThreshold)
	require.Equal(t, inferencetypes.DecimalFromFloat(4.475), model.WeightScaleFactor)
	require.Equal(t, penaltyStartEpoch, model.PenaltyStartEpoch)
}

func requireDeprecatedPoCParamsCleared(t *testing.T, poc *inferencetypes.PocParams) {
	t.Helper()
	require.NotNil(t, poc)
	require.Nil(t, poc.WeightScaleFactor)
	require.Nil(t, poc.ModelParams)
	require.Empty(t, poc.ModelId)
	require.Zero(t, poc.SeqLen)
	require.Nil(t, poc.StatTest)
}

func TestMigrateParams(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// InferenceKeeperReturningMocks already sets DefaultParams() which has
	// Models populated. Overwrite to simulate pre-upgrade state.
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams.ModelId = "founding-model"
	params.PocParams.SeqLen = 512
	params.PocParams.WeightScaleFactor = inferencetypes.DecimalFromFloat(0.75)
	params.PocParams.StatTest = inferencetypes.DefaultPoCStatTestParams()
	params.PocParams.Models = nil
	params.DelegationParams = nil
	require.NoError(t, k.SetParams(ctx, params))
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 9))

	require.NoError(t, migrateParams(ctx, k))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Len(t, got.PocParams.Models, 3)
	m := requirePoCModelConfig(t, got.PocParams.Models, "founding-model")
	require.Equal(t, "founding-model", m.ModelId)
	require.Equal(t, int64(512), m.SeqLen)
	require.NotNil(t, m.WeightScaleFactor)
	wsf, err := m.WeightScaleFactor.ToLegacyDec()
	require.NoError(t, err)
	require.Equal(t, "0.750000000000000000", wsf.String())
	require.NotNil(t, m.StatTest)
	require.Equal(t, uint64(0), m.PenaltyStartEpoch)
	requireKimiPoCModelConfig(t, got.PocParams.Models, params.PocParams.WeightScaleFactor, 11)
	requireTestnetPoCModelConfig(t, got.PocParams.Models, 11)

	require.NotNil(t, got.DelegationParams)
	require.Equal(t, "founding-model", got.DelegationParams.InitialModelId)
	defaults := inferencetypes.DefaultDelegationParams()
	require.Equal(t, int64(5), got.DelegationParams.DeployWindow)
	require.Equal(t, defaults.VMin, got.DelegationParams.VMin)
	require.Equal(t, defaults.WThreshold, got.DelegationParams.WThreshold)
	require.Equal(t, inferencetypes.DecimalFromFloat(0.75), got.DelegationParams.CapFactor)
	require.Equal(t, inferencetypes.DecimalFromFloat(0.05), got.DelegationParams.DelegationShare)
	require.Equal(t, inferencetypes.DecimalFromFloat(0.1), got.DelegationParams.RefusalPenalty)
	require.Equal(t, inferencetypes.DecimalFromFloat(0.15), got.DelegationParams.NoParticipationPenalty)
	require.Equal(t, inferencetypes.DecimalFromFloat(0.3), got.DelegationParams.MaxModelVotingPowerPercentage)
	requireDeprecatedPoCParamsCleared(t, got.PocParams)

	// Idempotency: a second run must not duplicate models or alter values.
	require.NoError(t, migrateParams(ctx, k))
	got2, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Len(t, got2.PocParams.Models, 3)
	founder := requirePoCModelConfig(t, got2.PocParams.Models, "founding-model")
	require.Equal(t, int64(512), founder.SeqLen)
	requireKimiPoCModelConfig(t, got2.PocParams.Models, params.PocParams.WeightScaleFactor, 11)
	requireTestnetPoCModelConfig(t, got2.PocParams.Models, 11)
	requireDeprecatedPoCParamsCleared(t, got2.PocParams)
}

func TestUpdateGovernanceModels(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams.Models = []*inferencetypes.PoCModelConfig{
		{ModelId: "founding-model", SeqLen: 512, StatTest: inferencetypes.DefaultPoCStatTestParams(),
			WeightScaleFactor: inferencetypes.DecimalFromFloat(1.0)},
		{ModelId: kimiModelID, SeqLen: 1024, StatTest: inferencetypes.DefaultPoCStatTestParams(),
			WeightScaleFactor: inferencetypes.DecimalFromFloat(1.0)},
		{ModelId: testnetModelID, SeqLen: 256, StatTest: inferencetypes.DefaultPoCStatTestParams(),
			WeightScaleFactor: inferencetypes.DecimalFromFloat(4.475)},
	}
	require.NoError(t, k.SetParams(ctx, params))

	k.SetModel(ctx, &inferencetypes.Model{
		ProposedBy:             "existing",
		Id:                     "founding-model",
		UnitsOfComputePerToken: 1,
		HfRepo:                 "founding-model",
	})
	k.SetModel(ctx, &inferencetypes.Model{
		ProposedBy:             "remove-me",
		Id:                     "unapproved-model",
		UnitsOfComputePerToken: 1,
		HfRepo:                 "unapproved-model",
	})

	require.NoError(t, updateGovernanceModels(ctx, k))

	founder, found := k.GetGovernanceModel(ctx, "founding-model")
	require.True(t, found)
	require.Equal(t, "existing", founder.ProposedBy)

	_, found = k.GetGovernanceModel(ctx, "unapproved-model")
	require.False(t, found)

	kimi, found := k.GetGovernanceModel(ctx, kimiModelID)
	require.True(t, found)
	require.Equal(t, k.GetAuthority(), kimi.ProposedBy)
	require.Equal(t, kimiModelID, kimi.Id)
	require.Equal(t, uint64(10000), kimi.UnitsOfComputePerToken)
	require.Equal(t, kimiModelID, kimi.HfRepo)
	require.Equal(t, "5a49d036ab7472b7d5912ded487150ec1358c11d", kimi.HfCommit)
	require.Equal(t, []string{
		"--max-model-len", "240000",
		"--tool-call-parser", "kimi_k2",
		"--reasoning-parser", "kimi_k2",
	}, kimi.ModelArgs)
	require.Equal(t, uint64(720), kimi.VRam)
	require.Equal(t, uint64(1500), kimi.ThroughputPerNonce)
	require.Equal(t, &inferencetypes.Decimal{Value: 920, Exponent: -3}, kimi.ValidationThreshold)

	testnetModel, found := k.GetGovernanceModel(ctx, testnetModelID)
	require.True(t, found)
	require.Equal(t, k.GetAuthority(), testnetModel.ProposedBy)
	require.Equal(t, uint64(100), testnetModel.UnitsOfComputePerToken)
	require.Equal(t, testnetModelID, testnetModel.HfRepo)
	require.Equal(t, "a09a35458c702b33eeacc393d103063234e8bc28", testnetModel.HfCommit)
	require.Equal(t, []string{"--quantization", "fp8"}, testnetModel.ModelArgs)
	require.Equal(t, uint64(24), testnetModel.VRam)
	require.Equal(t, uint64(10000), testnetModel.ThroughputPerNonce)
	require.Equal(t, &inferencetypes.Decimal{Value: 85, Exponent: -2}, testnetModel.ValidationThreshold)

	require.NoError(t, updateGovernanceModels(ctx, k))
	models, err := k.GetGovernanceModels(ctx)
	require.NoError(t, err)
	require.Len(t, models, 3)
}

func setupBackfillFixture(t *testing.T, k inferencekeeper.Keeper, ctx sdk.Context, modelID string, addr1, addr2 string) {
	t.Helper()

	// Set a primary model in PocParams so backfillVotingPower can discover it.
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams.Models = []*inferencetypes.PoCModelConfig{
		{ModelId: modelID, SeqLen: 256, StatTest: inferencetypes.DefaultPoCStatTestParams(),
			WeightScaleFactor: inferencetypes.DecimalFromFloat(1.0)},
	}
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, k.SetEpoch(ctx, &inferencetypes.Epoch{Index: 1, PocStartBlockHeight: 100}))
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 1))

	ap := inferencetypes.ActiveParticipants{
		EpochId: 1,
		Participants: []*inferencetypes.ActiveParticipant{
			{Index: addr1, Weight: 100},
			{Index: addr2, Weight: 200},
		},
	}
	require.NoError(t, k.SetActiveParticipants(ctx, ap))

	k.SetEpochGroupData(ctx, inferencetypes.EpochGroupData{
		EpochIndex: 1,
		ModelId:    modelID,
		ValidationWeights: []*inferencetypes.ValidationWeight{
			{MemberAddress: addr1, Weight: 100},
			{MemberAddress: addr2, Weight: 200},
		},
	})
}

func TestBackfillVotingPower(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	addr1 := sample.AccAddress()
	addr2 := sample.AccAddress()
	const modelID = "m1"
	setupBackfillFixture(t, k, ctx, modelID, addr1, addr2)

	require.NoError(t, backfillVotingPower(ctx, k))

	ap, found := k.GetActiveParticipants(ctx, 1)
	require.True(t, found)
	require.Len(t, ap.Participants, 2)
	for _, p := range ap.Participants {
		require.Len(t, p.VotingPowers, 1, "participant %s should have one voting power entry", p.Index)
		require.Equal(t, modelID, p.VotingPowers[0].ModelId)
		require.Equal(t, p.Weight, p.VotingPowers[0].VotingPower)
	}

	egd, found := k.GetEpochGroupData(ctx, 1, modelID)
	require.True(t, found)
	require.Len(t, egd.ValidationWeights, 2)
	for _, vw := range egd.ValidationWeights {
		require.Equal(t, vw.Weight, vw.VotingPower,
			"voting_power should equal weight for member %s", vw.MemberAddress)
	}
}

func TestBackfillVotingPower_EdgeCases(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	addrZero := sample.AccAddress()
	addrPreset := sample.AccAddress()
	const modelID = "m1"

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams.Models = []*inferencetypes.PoCModelConfig{{ModelId: modelID}}
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, k.SetEpoch(ctx, &inferencetypes.Epoch{Index: 1, PocStartBlockHeight: 100}))
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 1))

	// Participant with Weight=0 and participant with pre-existing VotingPowers.
	preset := []*inferencetypes.ModelVotingPower{{ModelId: modelID, VotingPower: 999}}
	ap := inferencetypes.ActiveParticipants{
		EpochId: 1,
		Participants: []*inferencetypes.ActiveParticipant{
			{Index: addrZero, Weight: 0},
			{Index: addrPreset, Weight: 50, VotingPowers: preset},
		},
	}
	require.NoError(t, k.SetActiveParticipants(ctx, ap))

	k.SetEpochGroupData(ctx, inferencetypes.EpochGroupData{
		EpochIndex: 1,
		ModelId:    modelID,
		ValidationWeights: []*inferencetypes.ValidationWeight{
			{MemberAddress: addrZero, Weight: 0},                      // weight 0 -> voting_power stays 0
			{MemberAddress: addrPreset, Weight: 50, VotingPower: 777}, // already set -> unchanged
		},
	})

	require.NoError(t, backfillVotingPower(ctx, k))

	got, found := k.GetActiveParticipants(ctx, 1)
	require.True(t, found)
	for _, p := range got.Participants {
		switch p.Index {
		case addrZero:
			// Weight was 0, so backfill still writes [{modelID, 0}] because
			// the code only skips when VotingPowers is non-empty. That's the
			// expected behavior; we pin it here so regressions are visible.
			require.Len(t, p.VotingPowers, 1)
			require.Equal(t, int64(0), p.VotingPowers[0].VotingPower)
		case addrPreset:
			require.Equal(t, preset, p.VotingPowers,
				"pre-existing voting powers must not be overwritten")
		}
	}

	egd, found := k.GetEpochGroupData(ctx, 1, modelID)
	require.True(t, found)
	for _, vw := range egd.ValidationWeights {
		switch vw.MemberAddress {
		case addrZero:
			require.Equal(t, int64(0), vw.VotingPower, "weight-0 entry should stay 0")
		case addrPreset:
			require.Equal(t, int64(777), vw.VotingPower, "pre-existing voting_power should be preserved")
		}
	}
}

func TestBackfillVotingPower_SafeSkips(t *testing.T) {
	// Each sub-case represents a pre-upgrade state where backfill should
	// silently skip rather than fail the upgrade.
	t.Run("no effective epoch", func(t *testing.T) {
		k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
		require.NoError(t, backfillVotingPower(ctx, k))
	})

	t.Run("empty models list", func(t *testing.T) {
		k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
		params, err := k.GetParams(ctx)
		require.NoError(t, err)
		params.PocParams.Models = nil
		require.NoError(t, k.SetParams(ctx, params))
		require.NoError(t, k.SetEffectiveEpochIndex(ctx, 1))
		require.NoError(t, backfillVotingPower(ctx, k))
	})

	t.Run("empty primary model id", func(t *testing.T) {
		k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
		params, err := k.GetParams(ctx)
		require.NoError(t, err)
		params.PocParams.Models = []*inferencetypes.PoCModelConfig{{ModelId: ""}}
		require.NoError(t, k.SetParams(ctx, params))
		require.NoError(t, k.SetEffectiveEpochIndex(ctx, 1))
		require.NoError(t, backfillVotingPower(ctx, k))
	})

	t.Run("no active participants for epoch", func(t *testing.T) {
		k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
		params, err := k.GetParams(ctx)
		require.NoError(t, err)
		params.PocParams.Models = []*inferencetypes.PoCModelConfig{{ModelId: "m1"}}
		require.NoError(t, k.SetParams(ctx, params))
		require.NoError(t, k.SetEffectiveEpochIndex(ctx, 1))
		// No SetActiveParticipants and no SetEpochGroupData.
		require.NoError(t, backfillVotingPower(ctx, k))
	})

	t.Run("no subgroup epoch group data", func(t *testing.T) {
		k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
		params, err := k.GetParams(ctx)
		require.NoError(t, err)
		params.PocParams.Models = []*inferencetypes.PoCModelConfig{{ModelId: "m1"}}
		require.NoError(t, k.SetParams(ctx, params))
		require.NoError(t, k.SetEffectiveEpochIndex(ctx, 1))
		// AP exists but the (epoch=1, model="m1") subgroup does not.
		require.NoError(t, k.SetActiveParticipants(ctx, inferencetypes.ActiveParticipants{
			EpochId: 1,
			Participants: []*inferencetypes.ActiveParticipant{
				{Index: sample.AccAddress(), Weight: 100},
			},
		}))
		require.NoError(t, backfillVotingPower(ctx, k))
	})
}

func TestInitNewPruningState(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 42))
	require.NoError(t, k.PruningState.Set(ctx, inferencetypes.PruningState{
		PocBatchesPrunedEpoch:            10,
		PocValidationsPrunedEpoch:        10,
		InferencePrunedEpoch:             10,
		EpochGroupValidationsPrunedEpoch: 10,
		DevshardPrunedEpoch:              10,
		// New markers left at 0 to simulate pre-upgrade state.
	}))

	require.NoError(t, initNewPruningState(ctx, k))

	state, err := k.PruningState.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(42), state.PocValidationsV2PrunedEpoch)
	require.Equal(t, int64(42), state.PocV2StoreCommitsPrunedEpoch)
	require.Equal(t, int64(42), state.MlnodeWeightDistributionsPrunedEpoch)
	require.Equal(t, int64(42), state.PocValidationSnapshotsPrunedEpoch)
	// Pre-existing markers for other collections must be preserved.
	require.Equal(t, int64(10), state.PocBatchesPrunedEpoch)
	require.Equal(t, int64(10), state.PocValidationsPrunedEpoch)
	require.Equal(t, int64(10), state.InferencePrunedEpoch)
	require.Equal(t, int64(10), state.EpochGroupValidationsPrunedEpoch)
	require.Equal(t, int64(10), state.DevshardPrunedEpoch)
}

func TestInitNewPruningState_DoesNotLowerMarkers(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 42))
	require.NoError(t, k.PruningState.Set(ctx, inferencetypes.PruningState{
		PocValidationsV2PrunedEpoch: 100, // already above current epoch
	}))

	require.NoError(t, initNewPruningState(ctx, k))

	state, err := k.PruningState.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(100), state.PocValidationsV2PrunedEpoch,
		"should not lower an already-advanced marker")
}

func TestMigrationSequence(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	store := inferencekeeper.EmptyPrefixStore(ctx, &k)

	// Legacy-prefix data to clear.
	for i, pfx := range legacyPrefixes {
		key := append(append([]byte{}, pfx...), []byte{byte(i)}...)
		store.Set(key, []byte("v"))
	}

	// Pre-upgrade params: singular fields only, no Models, no DelegationParams.
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams.ModelId = "founding-model"
	params.PocParams.SeqLen = 256
	params.PocParams.WeightScaleFactor = inferencetypes.DecimalFromFloat(1.0)
	params.PocParams.StatTest = inferencetypes.DefaultPoCStatTestParams()
	params.PocParams.Models = nil
	params.DelegationParams = nil
	require.NoError(t, k.SetParams(ctx, params))
	k.SetModel(ctx, &inferencetypes.Model{Id: "founding-model", ProposedBy: "existing"})
	k.SetModel(ctx, &inferencetypes.Model{Id: "unapproved-model", ProposedBy: "remove"})

	// Epoch + ActiveParticipants + subgroup EpochGroupData with unset voting_power.
	require.NoError(t, k.SetEpoch(ctx, &inferencetypes.Epoch{Index: 5, PocStartBlockHeight: 500}))
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 5))

	addr1 := sample.AccAddress()
	addr2 := sample.AccAddress()
	require.NoError(t, k.SetActiveParticipants(ctx, inferencetypes.ActiveParticipants{
		EpochId: 5,
		Participants: []*inferencetypes.ActiveParticipant{
			{Index: addr1, Weight: 100},
			{Index: addr2, Weight: 200},
		},
	}))
	k.SetEpochGroupData(ctx, inferencetypes.EpochGroupData{
		EpochIndex: 5,
		ModelId:    "founding-model",
		ValidationWeights: []*inferencetypes.ValidationWeight{
			{MemberAddress: addr1, Weight: 100},
			{MemberAddress: addr2, Weight: 200},
		},
	})

	// Pruning state starts fresh.
	require.NoError(t, k.PruningState.Set(ctx, inferencetypes.PruningState{}))

	// Run the multi-model migration functions in the same order as CreateUpgradeHandler.
	require.NoError(t, clearLegacyPoCv2Data(ctx, k))
	require.NoError(t, migrateParams(ctx, k))
	require.NoError(t, updateGovernanceModels(ctx, k))
	require.NoError(t, backfillVotingPower(ctx, k))
	require.NoError(t, initNewPruningState(ctx, k))

	// Legacy prefixes empty.
	for i, pfx := range legacyPrefixes {
		require.Equal(t, 0, countPrefixEntries(t, store, pfx),
			"legacy prefix %d should be cleared", i)
	}

	// Params migrated.
	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Len(t, got.PocParams.Models, 3)
	requirePoCModelConfig(t, got.PocParams.Models, "founding-model")
	requireKimiPoCModelConfig(t, got.PocParams.Models, params.PocParams.WeightScaleFactor, 7)
	requireTestnetPoCModelConfig(t, got.PocParams.Models, 7)
	require.NotNil(t, got.DelegationParams)
	require.Equal(t, "founding-model", got.DelegationParams.InitialModelId)
	requireDeprecatedPoCParamsCleared(t, got.PocParams)
	_, found := k.GetGovernanceModel(ctx, "unapproved-model")
	require.False(t, found)
	_, found = k.GetGovernanceModel(ctx, kimiModelID)
	require.True(t, found)
	_, found = k.GetGovernanceModel(ctx, testnetModelID)
	require.True(t, found)

	// Backfill picked up the migrated model id (proves ordering dependency).
	ap, found := k.GetActiveParticipants(ctx, 5)
	require.True(t, found)
	for _, p := range ap.Participants {
		require.Len(t, p.VotingPowers, 1)
		require.Equal(t, "founding-model", p.VotingPowers[0].ModelId)
		require.Equal(t, p.Weight, p.VotingPowers[0].VotingPower)
	}
	egd, found := k.GetEpochGroupData(ctx, 5, "founding-model")
	require.True(t, found)
	for _, vw := range egd.ValidationWeights {
		require.Equal(t, vw.Weight, vw.VotingPower)
	}

	// Pruning markers seeded.
	pstate, err := k.PruningState.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(5), pstate.PocValidationsV2PrunedEpoch)
	require.Equal(t, int64(5), pstate.PocV2StoreCommitsPrunedEpoch)
	require.Equal(t, int64(5), pstate.MlnodeWeightDistributionsPrunedEpoch)
	require.Equal(t, int64(5), pstate.PocValidationSnapshotsPrunedEpoch)
}

func TestAdjustBLSParameters_SetsDefaultsForZeroValues(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.DisputePhaseDurationBlocks = 0
	params.MaxSigningAttempts = 0
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, adjustBLSParameters(ctx, k))

	updated, err := k.GetParams(ctx)
	require.NoError(t, err)
	defaults := blstypes.DefaultParams()
	require.Equal(t, defaults.DisputePhaseDurationBlocks, updated.DisputePhaseDurationBlocks)
	require.Equal(t, defaults.MaxSigningAttempts, updated.MaxSigningAttempts)
}

func TestAdjustBLSParameters_PreservesExplicitValues(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.DisputePhaseDurationBlocks = 17
	params.MaxSigningAttempts = 7
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, adjustBLSParameters(ctx, k))

	updated, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(17), updated.DisputePhaseDurationBlocks)
	require.Equal(t, uint32(7), updated.MaxSigningAttempts)
}
