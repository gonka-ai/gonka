package inference

import (
	"testing"

	"cosmossdk.io/core/header"
	"cosmossdk.io/log"
	mathsdk "cosmossdk.io/math"
	"cosmossdk.io/store"
	"cosmossdk.io/store/metrics"
	storetypes "cosmossdk.io/store/types"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	blskeeper "github.com/productscience/inference/x/bls/keeper"
	blstypes "github.com/productscience/inference/x/bls/types"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

type noopLogger struct{}

func (noopLogger) LogInfo(string, types.SubSystem, ...interface{})  {}
func (noopLogger) LogError(string, types.SubSystem, ...interface{}) {}
func (noopLogger) LogWarn(string, types.SubSystem, ...interface{})  {}
func (noopLogger) LogDebug(string, types.SubSystem, ...interface{}) {}

func TestPoCWeightCalculator_PocValidated_RejectsWhenVotingPowersMissing(t *testing.T) {
	wc := &PoCWeightCalculator{
		ModelVotingPowers:  map[string]map[string]int64{},
		TotalNetworkWeight: 100,
		Logger:             noopLogger{},
	}

	ok := wc.pocValidated([]types.PoCValidationV2{
		{
			ValidatorParticipantAddress: testutil.Validator,
			ValidatedWeight:             1,
		},
	}, types.PoCParticipantModelKey{
		ParticipantAddress: testutil.Executor,
		ModelID:            "missing-model",
	})

	require.False(t, ok)
}

func TestPoCWeightCalculator_PocValidated_SlotSamplingTreatsMissingWeightAsAbstention(t *testing.T) {
	key := types.PoCParticipantModelKey{
		ParticipantAddress: testutil.Executor,
		ModelID:            "model-a",
	}
	modelVotingPowers := map[string]int64{
		testutil.Validator: 40,
	}
	entries, totalWeight := calculations.PrepareSortedEntries(modelVotingPowers)
	require.Equal(t, int64(40), totalWeight)
	require.Equal(t, 51, calculations.ComputeSampledSlotCount(totalWeight, 100, 128))

	wc := &PoCWeightCalculator{
		ModelVotingPowers: map[string]map[string]int64{
			"model-a": modelVotingPowers,
		},
		TotalNetworkWeight: 100,
		ValidationSlots:    128,
		AppHash:            "test-hash",
		sortedVotingPowers: map[string]sortedModelVP{
			"model-a": {entries: entries, totalWeight: totalWeight},
		},
		Logger: noopLogger{},
	}

	ok := wc.pocValidated([]types.PoCValidationV2{
		{
			ValidatorParticipantAddress: testutil.Validator,
			ValidatedWeight:             1,
		},
	}, key)

	require.False(t, ok)
}

func TestPoCWeightCalculator_PocValidated_SlotSamplingAcceptsWhenGroupControlsEnoughWeight(t *testing.T) {
	key := types.PoCParticipantModelKey{
		ParticipantAddress: testutil.Executor,
		ModelID:            "model-a",
	}
	modelVotingPowers := map[string]int64{
		testutil.Validator: 80,
	}
	entries, totalWeight := calculations.PrepareSortedEntries(modelVotingPowers)
	require.Equal(t, int64(80), totalWeight)
	require.Equal(t, 102, calculations.ComputeSampledSlotCount(totalWeight, 100, 128))

	wc := &PoCWeightCalculator{
		ModelVotingPowers: map[string]map[string]int64{
			"model-a": modelVotingPowers,
		},
		TotalNetworkWeight: 100,
		ValidationSlots:    128,
		AppHash:            "test-hash",
		sortedVotingPowers: map[string]sortedModelVP{
			"model-a": {entries: entries, totalWeight: totalWeight},
		},
		Logger: noopLogger{},
	}

	ok := wc.pocValidated([]types.PoCValidationV2{
		{
			ValidatorParticipantAddress: testutil.Validator,
			ValidatedWeight:             1,
		},
	}, key)

	require.True(t, ok)
}

func TestPoCWeightCalculator_CalculateParticipantWeight_ProducesRawWeights(t *testing.T) {
	modelAKey := types.PoCParticipantModelKey{
		ParticipantAddress: testutil.Executor,
		ModelID:            "model-a",
	}
	modelBKey := types.PoCParticipantModelKey{
		ParticipantAddress: testutil.Executor,
		ModelID:            "model-b",
	}

	wc := &PoCWeightCalculator{
		StoreCommits: map[types.PoCParticipantModelKey]types.PoCV2StoreCommit{
			modelAKey: {
				ParticipantAddress:       testutil.Executor,
				PocStageStartBlockHeight: 100,
				Count:                    10,
				ModelId:                  "model-a",
			},
			modelBKey: {
				ParticipantAddress:       testutil.Executor,
				PocStageStartBlockHeight: 100,
				Count:                    10,
				ModelId:                  "model-b",
			},
		},
		NodeWeightDistributions: map[types.PoCParticipantModelKey]types.MLNodeWeightDistribution{
			modelAKey: {
				ParticipantAddress:       testutil.Executor,
				PocStageStartBlockHeight: 100,
				ModelId:                  "model-a",
				Weights: []*types.MLNodeWeight{{
					NodeId: "node-a",
					Weight: 10,
				}},
			},
			modelBKey: {
				ParticipantAddress:       testutil.Executor,
				PocStageStartBlockHeight: 100,
				ModelId:                  "model-b",
				Weights: []*types.MLNodeWeight{{
					NodeId: "node-b",
					Weight: 10,
				}},
			},
		},
		PocParams: &types.PocParams{
			Models: []*types.PoCModelConfig{
				{
					ModelId:           "model-a",
					WeightScaleFactor: types.DecimalFromFloat(1.0),
				},
				{
					ModelId:           "model-b",
					WeightScaleFactor: types.DecimalFromFloat(2.0),
				},
			},
		},
		Logger:                  noopLogger{},
		TimeNormalizationFactor: mathsdk.LegacyOneDec(),
	}

	// PocWeight is raw (no coefficient) -- both models produce same raw weight
	modelANodes, modelAWeight := wc.calculateParticipantWeight(modelAKey)
	modelBNodes, modelBWeight := wc.calculateParticipantWeight(modelBKey)

	require.Equal(t, int64(10), modelAWeight, "raw weight should not include coefficient")
	require.Equal(t, int64(10), modelBWeight, "raw weight should not include coefficient")
	require.Len(t, modelANodes, 1)
	require.Len(t, modelBNodes, 1)
	require.Equal(t, int64(10), modelANodes[0].weight)
	require.Equal(t, int64(10), modelBNodes[0].weight)

	// Coefficients are applied at aggregation
	coefficients := ModelCoefficients(wc.PocParams)
	consensusWeight := AggregateConsensusWeight(
		[]ModelWeight{
			{ModelID: "model-a", RawWeight: modelAWeight},
			{ModelID: "model-b", RawWeight: modelBWeight},
		},
		coefficients,
	)
	// 1.0*10 + 2.0*10 = 30
	require.Equal(t, int64(30), consensusWeight)
}

func TestPoCWeightCalculator_Calculate_RejectsWhenVotingPowerIsInsufficient(t *testing.T) {
	key := types.PoCParticipantModelKey{
		ParticipantAddress: testutil.Executor,
		ModelID:            "model-a",
	}

	wc := &PoCWeightCalculator{
		ModelVotingPowers: map[string]map[string]int64{
			"model-a": {
				testutil.Validator: 40,
			},
		},
		TotalNetworkWeight: 100,
		StoreCommits: map[types.PoCParticipantModelKey]types.PoCV2StoreCommit{
			key: {
				ParticipantAddress:       testutil.Executor,
				PocStageStartBlockHeight: 100,
				Count:                    10,
				ModelId:                  "model-a",
			},
		},
		NodeWeightDistributions: map[types.PoCParticipantModelKey]types.MLNodeWeightDistribution{
			key: {
				ParticipantAddress:       testutil.Executor,
				PocStageStartBlockHeight: 100,
				ModelId:                  "model-a",
				Weights: []*types.MLNodeWeight{{
					NodeId: "node-a",
					Weight: 10,
				}},
			},
		},
		Validations: map[types.PoCParticipantModelKey][]types.PoCValidationV2{
			key: {
				{
					ValidatorParticipantAddress: testutil.Validator,
					ValidatedWeight:             10,
				},
			},
		},
		PocParams: &types.PocParams{
			Models: []*types.PoCModelConfig{
				{
					ModelId:           "model-a",
					WeightScaleFactor: types.DecimalFromFloat(1.0),
				},
			},
		},
		Participants: map[string]types.Participant{
			testutil.Executor: {
				Index:        testutil.Executor,
				Address:      testutil.Executor,
				ValidatorKey: "validator-key",
				InferenceUrl: "http://executor.example.com",
			},
		},
		Seeds: map[string]types.RandomSeed{
			testutil.Executor: {
				Participant: testutil.Executor,
				EpochIndex:  1,
				Signature:   "seed-sig",
			},
		},
		Logger:                  noopLogger{},
		TimeNormalizationFactor: mathsdk.LegacyOneDec(),
	}

	require.Empty(t, wc.Calculate())
}

func TestUpdateConfirmationWeightsV2_UsesPerModelWeightScaleFactor(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonkapub")

	k, ctx := newMinimalInferenceKeeper(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams = &types.PocParams{
		ValidationSlots:         0,
		PocNormalizationEnabled: false,
		Models: []*types.PoCModelConfig{
			{
				ModelId:           "model-a",
				WeightScaleFactor: types.DecimalFromFloat(1.0),
			},
			{
				ModelId:           "model-b",
				WeightScaleFactor: types.DecimalFromFloat(2.0),
			},
		},
	}
	require.NoError(t, k.SetParams(ctx, params))

	am := NewAppModule(nil, k, nil, nil, nil, nil)

	require.NoError(t, k.SetParticipant(ctx, types.Participant{
		Index:        testutil.Executor,
		Address:      testutil.Executor,
		ValidatorKey: "validator-key",
		InferenceUrl: "http://example.com/",
	}))
	k.SetRandomSeed(ctx, types.RandomSeed{
		Participant: testutil.Executor,
		EpochIndex:  2,
		Signature:   "sig",
	})

	require.NoError(t, k.SetPoCV2StoreCommit(ctx, types.PoCV2StoreCommit{
		ParticipantAddress:       testutil.Executor,
		PocStageStartBlockHeight: 180,
		Count:                    10,
		RootHash:                 make([]byte, 32),
		CommitBlockHeight:        180,
		ModelId:                  "model-a",
	}))
	require.NoError(t, k.SetPoCV2StoreCommit(ctx, types.PoCV2StoreCommit{
		ParticipantAddress:       testutil.Executor,
		PocStageStartBlockHeight: 180,
		Count:                    10,
		RootHash:                 make([]byte, 32),
		CommitBlockHeight:        180,
		ModelId:                  "model-b",
	}))

	require.NoError(t, k.SetMLNodeWeightDistribution(ctx, types.MLNodeWeightDistribution{
		ParticipantAddress:       testutil.Executor,
		PocStageStartBlockHeight: 180,
		ModelId:                  "model-a",
		Weights: []*types.MLNodeWeight{{
			NodeId: "node-a",
			Weight: 10,
		}},
	}))
	require.NoError(t, k.SetMLNodeWeightDistribution(ctx, types.MLNodeWeightDistribution{
		ParticipantAddress:       testutil.Executor,
		PocStageStartBlockHeight: 180,
		ModelId:                  "model-b",
		Weights: []*types.MLNodeWeight{{
			NodeId: "node-b",
			Weight: 10,
		}},
	}))

	require.NoError(t, k.SetPocValidationV2(ctx, types.PoCValidationV2{
		ParticipantAddress:          testutil.Executor,
		ValidatorParticipantAddress: testutil.Validator,
		PocStageStartBlockHeight:    180,
		ValidatedWeight:             10,
		ModelId:                     "model-a",
	}))
	require.NoError(t, k.SetPocValidationV2(ctx, types.PoCValidationV2{
		ParticipantAddress:          testutil.Executor,
		ValidatorParticipantAddress: testutil.Validator,
		PocStageStartBlockHeight:    180,
		ValidatedWeight:             10,
		ModelId:                     "model-b",
	}))

	event := &types.ConfirmationPoCEvent{
		EpochIndex:            2,
		EventSequence:         0,
		TriggerHeight:         180,
		GenerationStartHeight: 190,
		Phase:                 types.ConfirmationPoCPhase_CONFIRMATION_POC_COMPLETED,
	}

	// Set up validation snapshot with per-model voting powers
	require.NoError(t, k.SetPoCValidationSnapshot(ctx, types.PoCValidationSnapshot{
		PocStageStartHeight: 180,
		SnapshotHeight:      190,
		AppHash:             "test-hash",
		ModelVotingPowers: []*types.ModelVotingPowers{
			{
				ModelId:      "model-a",
				VotingPowers: []*types.VotingPowerEntry{{Address: testutil.Validator, VotingPower: 100}},
			},
			{
				ModelId:      "model-b",
				VotingPowers: []*types.VotingPowerEntry{{Address: testutil.Validator, VotingPower: 100}},
			},
		},
		TotalNetworkWeight: 100,
	}))

	result := am.updateConfirmationWeightsV2(ctx, event)

	require.Len(t, result, 1)
	require.Equal(t, testutil.Executor, result[0].Index)
	// Calculator produces raw weights (no coefficient)
	require.Equal(t, int64(20), result[0].Weight, "raw weight = 10 + 10")
	// Verify per-model structure
	require.Equal(t, []string{"model-a", "model-b"}, result[0].Models)
	require.Len(t, result[0].MlNodes, 2)

	// Aggregation with coefficients produces consensus weight
	coefficients := ModelCoefficients(params.PocParams)
	consensusWeight := AggregateConsensusWeight(ExtractModelWeights(result[0]), coefficients)
	require.Equal(t, int64(30), consensusWeight, "1.0*10 + 2.0*10 = 30")
}

// --- ComputeGroupCap tests ---

func TestComputeGroupCap_InitialGroup_Uncapped(t *testing.T) {
	wc := &DelegationWeightCalculator{
		Groups: map[string]*GroupData{
			"model-a": {
				Members:          []string{"alice"},
				MemberPocWeights: map[string]int64{"alice": 100},
				ConsensusKoeff:   mathsdk.LegacyOneDec(),
				IsInitialGroup:   true,
			},
		},
		ConsensusWeights: map[string]int64{"alice": 100},
		Params:           WeightParams{CapFactor: mathsdk.LegacyMustNewDecFromStr("2.0")},
	}
	require.Equal(t, int64(-1), wc.ComputeGroupCap("model-a"))
}

func TestComputeGroupCap_TwoGroups_CapLimitsNonInitial(t *testing.T) {
	wc := &DelegationWeightCalculator{
		Groups: map[string]*GroupData{
			"model-a": {
				Members:        []string{"alice"},
				ConsensusKoeff: mathsdk.LegacyOneDec(),
				IsInitialGroup: true,
			},
			"model-b": {
				Members:        []string{"alice"},
				ConsensusKoeff: mathsdk.LegacyOneDec(),
				IsInitialGroup: false,
			},
		},
		ConsensusWeights:   map[string]int64{"alice": 150},
		TotalNetworkWeight: 150,
		Params:             WeightParams{CapFactor: mathsdk.LegacyMustNewDecFromStr("0.5")},
	}

	// model-a: uncapped (initial)
	require.Equal(t, int64(-1), wc.ComputeGroupCap("model-a"))

	// model-b: cap = 0.5 * 150 = 75
	require.Equal(t, int64(75), wc.ComputeGroupCap("model-b"))
}

func TestComputeGroupCap_CapFactorZero_FullyCapped(t *testing.T) {
	wc := &DelegationWeightCalculator{
		Groups: map[string]*GroupData{
			"model-a": {
				Members:        []string{"alice"},
				ConsensusKoeff: mathsdk.LegacyOneDec(),
				IsInitialGroup: false,
			},
		},
		ConsensusWeights:   map[string]int64{"alice": 100},
		TotalNetworkWeight: 100,
		Params:             WeightParams{CapFactor: mathsdk.LegacyZeroDec()},
	}
	require.Equal(t, int64(0), wc.ComputeGroupCap("model-a"))
}

// --- ComputeConsensusWeights with cap tests ---

func TestComputeConsensusWeights_GroupExceedsCap_ProportionalScaling(t *testing.T) {
	wc := &DelegationWeightCalculator{
		Groups: map[string]*GroupData{
			"model-a": {
				Members:          []string{"alice", "bob"},
				MemberPocWeights: map[string]int64{"alice": 100, "bob": 200},
				ConsensusKoeff:   mathsdk.LegacyOneDec(),
				IsInitialGroup:   true,
			},
			"model-b": {
				Members:          []string{"alice", "bob"},
				MemberPocWeights: map[string]int64{"alice": 1000, "bob": 500},
				ConsensusKoeff:   mathsdk.LegacyOneDec(),
				IsInitialGroup:   false,
			},
		},
		// total N-1 = 400, cap_factor = 0.5 -> model-b cap = 200.
		// raw model-b = 1500, scale = 200/1500 = 2/15.
		// alice scaled = floor(1000 * 2/15) = 133, bob scaled = floor(500 * 2/15) = 66.
		ConsensusWeights:   map[string]int64{"alice": 200, "bob": 200},
		TotalNetworkWeight: 400,
		Params:             WeightParams{CapFactor: mathsdk.LegacyMustNewDecFromStr("0.5")},
	}

	result, _ := wc.ComputeConsensusWeights([]string{"model-a", "model-b"})
	require.Equal(t, int64(100+133), result["alice"])
	require.Equal(t, int64(200+66), result["bob"])
}

// A sole eligible group has nothing meaningful to cap against. Even if the cap
// would otherwise bind, ComputeConsensusWeights must skip it -- otherwise a
// network with a single non-initial eligible group stalls.
func TestComputeConsensusWeights_SoleNonInitialGroup_SkipsCap(t *testing.T) {
	wc := &DelegationWeightCalculator{
		Groups: map[string]*GroupData{
			"qwen2.5": {
				Members:          []string{"a", "b"},
				MemberPocWeights: map[string]int64{"a": 7202, "b": 2547},
				ConsensusKoeff:   mathsdk.LegacyMustNewDecFromStr("4.475"),
				IsInitialGroup:   false,
			},
		},
		ConsensusWeights:   map[string]int64{"a": 8912, "b": 4098},
		TotalNetworkWeight: 13010,
		Params:             WeightParams{CapFactor: mathsdk.LegacyMustNewDecFromStr("0.5")},
	}

	result, _ := wc.ComputeConsensusWeights([]string{"qwen2.5"})
	require.Positive(t, result["a"])
	require.Positive(t, result["b"])
}

func TestComputeConsensusWeights_GroupUnderCap_NoScaling(t *testing.T) {
	wc := &DelegationWeightCalculator{
		Groups: map[string]*GroupData{
			"model-a": {
				Members:          []string{"alice"},
				MemberPocWeights: map[string]int64{"alice": 100},
				ConsensusKoeff:   mathsdk.LegacyOneDec(),
				IsInitialGroup:   true,
			},
			"model-b": {
				Members:          []string{"alice"},
				MemberPocWeights: map[string]int64{"alice": 10},
				ConsensusKoeff:   mathsdk.LegacyOneDec(),
				IsInitialGroup:   false,
			},
		},
		// total N-1 = 110, cap_factor = 0.5 -> cap = 55. raw model-b = 10, well under.
		ConsensusWeights:   map[string]int64{"alice": 110},
		TotalNetworkWeight: 110,
		Params:             WeightParams{CapFactor: mathsdk.LegacyMustNewDecFromStr("0.5")},
	}

	result, _ := wc.ComputeConsensusWeights([]string{"model-a", "model-b"})
	// model-a: 100, model-b: 10, total: 110
	require.Equal(t, int64(110), result["alice"])
}

func TestComputeConsensusWeights_InitialGroupExempt(t *testing.T) {
	wc := &DelegationWeightCalculator{
		Groups: map[string]*GroupData{
			"model-a": {
				Members:          []string{"alice"},
				MemberPocWeights: map[string]int64{"alice": 10000},
				ConsensusKoeff:   mathsdk.LegacyOneDec(),
				IsInitialGroup:   true,
			},
		},
		ConsensusWeights: map[string]int64{"alice": 50},
		Params:           WeightParams{CapFactor: mathsdk.LegacyMustNewDecFromStr("1.0")},
	}

	result, _ := wc.ComputeConsensusWeights([]string{"model-a"})
	// Initial group exempt from cap, raw value passes through
	require.Equal(t, int64(10000), result["alice"])
}

// --- IsGroupEligible tests ---

func TestIsGroupEligible_Passes_VMinEstablishedMembers(t *testing.T) {
	// Both members have current pocWeight > 0 AND N-1 consensus weight > 0
	wc := &DelegationWeightCalculator{
		Groups: map[string]*GroupData{
			"model-a": {
				Members:          []string{"alice", "bob"},
				MemberPocWeights: map[string]int64{"alice": 100, "bob": 50},
				ConsensusKoeff:   mathsdk.LegacyOneDec(),
			},
		},
		ConsensusWeights:   map[string]int64{"alice": 100, "bob": 50},
		TotalNetworkWeight: 150,
		Params:             WeightParams{VMin: 2, WThreshold: mathsdk.LegacyZeroDec(), CapFactor: mathsdk.LegacyZeroDec()},
	}
	require.True(t, wc.IsGroupEligible("model-a"))
}

func TestIsGroupEligible_Fails_TooFewEstablishedMembers(t *testing.T) {
	// bob has pocWeight but no N-1 consensus weight -> not established
	wc := &DelegationWeightCalculator{
		Groups: map[string]*GroupData{
			"model-a": {
				Members:          []string{"alice", "bob"},
				MemberPocWeights: map[string]int64{"alice": 100, "bob": 50},
				ConsensusKoeff:   mathsdk.LegacyOneDec(),
			},
		},
		ConsensusWeights:   map[string]int64{"alice": 100},
		TotalNetworkWeight: 100,
		Params:             WeightParams{VMin: 2, WThreshold: mathsdk.LegacyZeroDec(), CapFactor: mathsdk.LegacyZeroDec()},
	}
	require.False(t, wc.IsGroupEligible("model-a"))
}

func TestIsGroupEligible_Fails_NoPocWeight(t *testing.T) {
	// bob has N-1 weight but no current pocWeight -> not established
	wc := &DelegationWeightCalculator{
		Groups: map[string]*GroupData{
			"model-a": {
				Members:          []string{"alice", "bob"},
				MemberPocWeights: map[string]int64{"alice": 100, "bob": 0},
				ConsensusKoeff:   mathsdk.LegacyOneDec(),
			},
		},
		ConsensusWeights:   map[string]int64{"alice": 100, "bob": 50},
		TotalNetworkWeight: 150,
		Params:             WeightParams{VMin: 2, WThreshold: mathsdk.LegacyZeroDec(), CapFactor: mathsdk.LegacyZeroDec()},
	}
	require.False(t, wc.IsGroupEligible("model-a"))
}

func TestIsGroupEligible_InitialModel_BypassesVMin_DuringGenesis(t *testing.T) {
	// Initial model, nobody has N-1 weight yet (genesis). VMin not enforced.
	wc := &DelegationWeightCalculator{
		Groups: map[string]*GroupData{
			"model-a": {
				Members:          []string{"alice"},
				MemberPocWeights: map[string]int64{"alice": 100},
				ConsensusKoeff:   mathsdk.LegacyOneDec(),
				IsInitialGroup:   true,
			},
		},
		ConsensusWeights:   map[string]int64{},
		TotalNetworkWeight: 0,
		Params:             WeightParams{VMin: 2, WThreshold: mathsdk.LegacyZeroDec(), CapFactor: mathsdk.LegacyZeroDec()},
	}
	require.True(t, wc.IsGroupEligible("model-a"))
}

func TestIsGroupEligible_InitialModel_EnforcesVMin_AfterChainReachesThreshold(t *testing.T) {
	// Initial model, chain has >= VMin participants with N-1 weight. VMin enforced.
	// Only 1 has current poc -> ineligible.
	wc := &DelegationWeightCalculator{
		Groups: map[string]*GroupData{
			"model-a": {
				Members:          []string{"alice", "bob"},
				MemberPocWeights: map[string]int64{"alice": 100, "bob": 0},
				ConsensusKoeff:   mathsdk.LegacyOneDec(),
				IsInitialGroup:   true,
			},
		},
		ConsensusWeights:   map[string]int64{"alice": 80, "bob": 70},
		TotalNetworkWeight: 150,
		Params:             WeightParams{VMin: 2, WThreshold: mathsdk.LegacyZeroDec(), CapFactor: mathsdk.LegacyZeroDec()},
	}
	require.False(t, wc.IsGroupEligible("model-a"))
}

func TestIsGroupEligible_NonInitialModel_NoBypass(t *testing.T) {
	// Non-initial model, < VMin members have N-1 weight. No bypass.
	wc := &DelegationWeightCalculator{
		Groups: map[string]*GroupData{
			"model-b": {
				Members:          []string{"alice"},
				MemberPocWeights: map[string]int64{"alice": 100},
				ConsensusKoeff:   mathsdk.LegacyOneDec(),
				IsInitialGroup:   false,
			},
		},
		ConsensusWeights:   map[string]int64{},
		TotalNetworkWeight: 0,
		Params:             WeightParams{VMin: 2, WThreshold: mathsdk.LegacyZeroDec(), CapFactor: mathsdk.LegacyZeroDec()},
	}
	require.False(t, wc.IsGroupEligible("model-b"))
}

// --- Voting power edge cases ---

func TestComputeGroupVotingPowers_MultipleDelegatorsToSameTarget(t *testing.T) {
	wc := &DelegationWeightCalculator{
		Groups: map[string]*GroupData{
			"model-a": {
				Members:        []string{"target"},
				ConsensusKoeff: mathsdk.LegacyOneDec(),
			},
		},
		Delegations: map[string]map[string]string{
			"model-a": {
				"d1": "target",
				"d2": "target",
			},
		},
	}
	modes := map[string]ParticipationMode{
		"target": ModeDirect,
		"d1":     ModeDelegate,
		"d2":     ModeDelegate,
	}
	finalWeights := map[string]int64{
		"target": 100,
		"d1":     200,
		"d2":     300,
	}

	vp := wc.ComputeGroupVotingPowers("model-a", modes, finalWeights)
	// target's VP = own 100 + d1's 200 + d2's 300 = 600
	require.Equal(t, int64(600), vp["target"])
	require.Len(t, vp, 1) // only direct members get entries
}

// TestComputeGroupVotingPowers_FreshMemberWithNoModesEntry asserts that a
// direct member who is absent from the modes map (e.g. earned weight only
// this epoch so ResolveGroupParticipation skipped them) still receives
// voting power from their final weight. Membership is read from g.Members,
// not inferred from modes[m] == ModeDirect.
//
// Regression for OX-7 / RC-3: previously this worked only by accident
// because ModeDirect is the zero value of the ParticipationMode enum, so
// the missing map key returned ModeDirect when read. Any future enum
// change that shifted ModeDirect away from zero would silently strip
// fresh members of voting power. This test pins the explicit behavior.
func TestComputeGroupVotingPowers_FreshMemberWithNoModesEntry(t *testing.T) {
	wc := &DelegationWeightCalculator{
		Groups: map[string]*GroupData{
			"model-a": {
				Members:        []string{"fresh"},
				ConsensusKoeff: mathsdk.LegacyOneDec(),
			},
		},
	}
	// modes is intentionally empty: fresh had no prior-epoch consensus
	// weight, so ResolveGroupParticipation produced no entry for it.
	modes := map[string]ParticipationMode{}
	finalWeights := map[string]int64{"fresh": 42}

	vp := wc.ComputeGroupVotingPowers("model-a", modes, finalWeights)
	require.Equal(t, int64(42), vp["fresh"])
	require.Len(t, vp, 1)
}

// TestComputeGroupVotingPowers_DelegationToNonMemberTargetIgnored asserts
// that a delegation pointing at a non-member is dropped rather than
// creating a spurious map entry. This exercises the explicit memberSet
// check for the delegation target in ComputeGroupVotingPowers.
func TestComputeGroupVotingPowers_DelegationToNonMemberTargetIgnored(t *testing.T) {
	wc := &DelegationWeightCalculator{
		Groups: map[string]*GroupData{
			"model-a": {
				Members:        []string{"member"},
				ConsensusKoeff: mathsdk.LegacyOneDec(),
			},
		},
		Delegations: map[string]map[string]string{
			"model-a": {
				"delegator": "non-member",
			},
		},
	}
	modes := map[string]ParticipationMode{
		"member":    ModeDirect,
		"delegator": ModeDelegate,
	}
	finalWeights := map[string]int64{
		"member":    100,
		"delegator": 500,
	}

	vp := wc.ComputeGroupVotingPowers("model-a", modes, finalWeights)
	// member keeps its own 100, delegator's 500 is discarded because the
	// target is not in g.Members.
	require.Equal(t, int64(100), vp["member"])
	require.Len(t, vp, 1)
	_, hasNonMember := vp["non-member"]
	require.False(t, hasNonMember)
}

func TestResolveGroupParticipation_DelegationToZeroWeightTarget_ModeNone(t *testing.T) {
	wc := &DelegationWeightCalculator{
		Groups: map[string]*GroupData{
			"model-a": {
				Members:        []string{"target"},
				ConsensusKoeff: mathsdk.LegacyOneDec(),
			},
		},
		ConsensusWeights: map[string]int64{
			"delegator": 100,
			"target":    0, // zero weight
		},
		Delegations: map[string]map[string]string{
			"model-a": {"delegator": "target"},
		},
	}

	modes := wc.ResolveGroupParticipation("model-a")
	require.Equal(t, ModeNone, modes["delegator"])
}

// Newcomer (absent from N-1 ConsensusWeights, present in UpcomingActiveParticipants)
// gets ModeDelegate so their delegation applies in their first active epoch.
func TestResolveGroupParticipation_NewcomerWithDelegation(t *testing.T) {
	wc := &DelegationWeightCalculator{
		Groups: map[string]*GroupData{
			"model-a": {
				Members:        []string{"target"},
				ConsensusKoeff: mathsdk.LegacyOneDec(),
			},
		},
		ConsensusWeights: map[string]int64{
			"target": 100,
		},
		UpcomingActiveParticipants: map[string]bool{
			"target":   true,
			"newcomer": true,
		},
		Delegations: map[string]map[string]string{
			"model-a": {"newcomer": "target"},
		},
	}

	modes := wc.ResolveGroupParticipation("model-a")
	require.Equal(t, ModeDelegate, modes["newcomer"])
	require.Equal(t, ModeDirect, modes["target"])
}

// Newcomer's delegation lands on the target's per-model voting power.
func TestComputeGroupVotingPowers_NewcomerDelegationFlows(t *testing.T) {
	wc := &DelegationWeightCalculator{
		Groups: map[string]*GroupData{
			"model-a": {
				Members:        []string{"target"},
				ConsensusKoeff: mathsdk.LegacyOneDec(),
			},
		},
		ConsensusWeights:     map[string]int64{"target": 100},
		UpcomingActiveParticipants: map[string]bool{"target": true, "newcomer": true},
		Delegations: map[string]map[string]string{
			"model-a": {"newcomer": "target"},
		},
	}

	modes := wc.ResolveGroupParticipation("model-a")
	finalWeights := map[string]int64{"target": 100, "newcomer": 50}
	vp := wc.ComputeGroupVotingPowers("model-a", modes, finalWeights)
	require.Equal(t, int64(150), vp["target"])
	require.Len(t, vp, 1)
}

// Newcomer who refuses gets ModeRefuse in their first active epoch.
func TestResolveGroupParticipation_NewcomerRefusal(t *testing.T) {
	wc := &DelegationWeightCalculator{
		Groups: map[string]*GroupData{
			"model-a": {
				Members:        []string{"target"},
				ConsensusKoeff: mathsdk.LegacyOneDec(),
			},
		},
		ConsensusWeights:     map[string]int64{"target": 100},
		UpcomingActiveParticipants: map[string]bool{"target": true, "newcomer": true},
		Refusals: map[string]map[string]bool{
			"model-a": {"newcomer": true},
		},
	}

	modes := wc.ResolveGroupParticipation("model-a")
	require.Equal(t, ModeRefuse, modes["newcomer"])
}

func newMinimalInferenceKeeper(t *testing.T) (keeper.Keeper, sdk.Context) {
	k, ctx, _ := newMinimalInferenceKeeperWithStub(t)
	return k, ctx
}

func newMinimalInferenceKeeperWithStub(t *testing.T) (keeper.Keeper, sdk.Context, *stubGroupKeeper) {
	t.Helper()

	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	storeKey := storetypes.NewKVStoreKey(types.StoreKey)
	transientStoreKey := storetypes.NewTransientStoreKey(types.TransientStoreKey)
	blsStoreKey := storetypes.NewKVStoreKey(blstypes.StoreKey)

	db := dbm.NewMemDB()
	stateStore := store.NewCommitMultiStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	stateStore.MountStoreWithDB(storeKey, storetypes.StoreTypeIAVL, db)
	stateStore.MountStoreWithDB(transientStoreKey, storetypes.StoreTypeTransient, db)
	stateStore.MountStoreWithDB(blsStoreKey, storetypes.StoreTypeIAVL, db)
	require.NoError(t, stateStore.LoadLatestVersion())

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)
	authority := authtypes.NewModuleAddress(govtypes.ModuleName)

	blsKeeper := blskeeper.NewKeeper(
		cdc,
		runtime.NewKVStoreService(blsStoreKey),
		log.NewNopLogger(),
		authority.String(),
	)
	groupStub := &stubGroupKeeper{}
	k := keeper.NewKeeper(
		cdc,
		runtime.NewKVStoreService(storeKey),
		runtime.NewTransientStoreService(transientStoreKey),
		log.NewNopLogger(),
		authority.String(),
		nil,
		nil,
		groupStub,
		nil,
		nil,
		nil,
		blsKeeper,
		nil,
		nil,
		nil,
		func() wasmkeeper.Keeper { return wasmkeeper.Keeper{} },
		nil,
	)
	groupStub.keeper = k

	ctx := sdk.NewContext(stateStore, cmtproto.Header{}, false, log.NewNopLogger()).
		WithHeaderInfo(header.Info{
			Hash: []byte{1, 2, 3, 4, 5, 6, 7, 8},
		})

	require.NoError(t, k.SetParams(ctx, types.DefaultParams()))
	require.NoError(t, blsKeeper.SetParams(ctx, blstypes.DefaultParams()))
	require.NoError(t, k.SetTokenomicsData(ctx, types.TokenomicsData{}))
	genesisParams := types.DefaultGenesisOnlyParams()
	require.NoError(t, k.SetGenesisOnlyParams(ctx, &genesisParams))

	return k, ctx, groupStub
}
