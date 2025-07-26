package inference_test

import (
	"testing"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/nullify"
	inference "github.com/productscience/inference/x/inference/module"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestGenesis(t *testing.T) {
	baseGenesis := types.MockedGenesis()
	genesisState := types.GenesisState{
		Params:            types.DefaultParams(),
		GenesisOnlyParams: types.DefaultGenesisOnlyParams(),
		CosmWasmParams:    baseGenesis.CosmWasmParams,

		ModelList: []types.Model{
			{
				ProposedBy:             "genesis",
				Id:                     "model-1",
				UnitsOfComputePerToken: 10,
				HfRepo:                 "repo1",
				HfCommit:               "commit1",
				ModelArgs:              []string{"--arg1"},
				VRam:                   16,
				ThroughputPerNonce:     100,
			},
		},

		InferenceList: []types.Inference{
			{
				Index:       "0",
				InferenceId: "0",
			},
			{
				Index:       "1",
				InferenceId: "1",
			},
		},
		ParticipantList: []types.Participant{
			{
				Index: "0",
			},
			{
				Index: "1",
			},
		},
		SettleAmountList: []types.SettleAmount{
			{
				Participant: "0",
			},
			{
				Participant: "1",
			},
		},
		EpochGroupValidationsList: []types.EpochGroupValidations{
			{
				Participant:         "0",
				PocStartBlockHeight: 0,
			},
			{
				Participant:         "1",
				PocStartBlockHeight: 1,
			},
		},
		TokenomicsData: &types.TokenomicsData{
			TotalFees:      85,
			TotalSubsidies: 11,
			TotalRefunded:  99,
			TotalBurned:    5,
		},
		TopMinerList: []types.TopMiner{
			{
				Address: "0",
			},
			{
				Address: "1",
			},
		},
		InferenceTimeoutList: []types.InferenceTimeout{
			{
				ExpirationHeight: 0,
				InferenceId:      "0",
			},
			{
				ExpirationHeight: 1,
				InferenceId:      "1",
			},
		},
		InferenceValidationDetailsList: []types.InferenceValidationDetails{
			{
				EpochGroupId: 0,
				InferenceId:  "0",
			},
			{
				EpochGroupId: 1,
				InferenceId:  "1",
			},
		},
		EpochPerformanceSummaryList: []types.EpochPerformanceSummary{
			{
				EpochStartHeight: 0,
				ParticipantId:    "0",
			},
			{
				EpochStartHeight: 1,
				ParticipantId:    "1",
			},
		},
		PartialUpgradeList: []types.PartialUpgrade{
			{
				Height: 0,
			},
			{
				Height: 1,
			},
		},
		// this line is used by starport scaffolding # genesis/test/state
	}

	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)

	mocks.StubForInitGenesis(ctx)

	inference.InitGenesis(ctx, k, genesisState)
	got := inference.ExportGenesis(ctx, k)
	require.NotNil(t, got)

	nullify.Fill(&genesisState)
	nullify.Fill(got)

	require.ElementsMatch(t, genesisState.ModelList, got.ModelList)
	require.ElementsMatch(t, genesisState.InferenceList, got.InferenceList)
	require.ElementsMatch(t, genesisState.ParticipantList, got.ParticipantList)
	require.ElementsMatch(t, genesisState.EpochGroupDataList, got.EpochGroupDataList)
	require.ElementsMatch(t, genesisState.SettleAmountList, got.SettleAmountList)
	require.ElementsMatch(t, genesisState.EpochGroupValidationsList, got.EpochGroupValidationsList)
	require.Equal(t, genesisState.TokenomicsData, got.TokenomicsData)
	require.ElementsMatch(t, genesisState.TopMinerList, got.TopMinerList)
	require.ElementsMatch(t, genesisState.InferenceTimeoutList, got.InferenceTimeoutList)
	require.ElementsMatch(t, genesisState.InferenceValidationDetailsList, got.InferenceValidationDetailsList)
	require.ElementsMatch(t, genesisState.EpochPerformanceSummaryList, got.EpochPerformanceSummaryList)
	require.ElementsMatch(t, genesisState.PartialUpgradeList, got.PartialUpgradeList)
	// this line is used by starport scaffolding # genesis/test/assert
}
