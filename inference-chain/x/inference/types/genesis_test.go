package types_test

import (
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestGenesisState_Validate(t *testing.T) {
	tests := []struct {
		desc     string
		genState *types.GenesisState
		valid    bool
	}{
		{
			desc:     "default is valid",
			genState: types.MockedGenesis(),
			valid:    true,
		},
		{
			desc: "valid genesis state",
			genState: &types.GenesisState{
				Params: types.DefaultParams(),

				InferenceList: []types.Inference{
					{
						Index: "0",
					},
					{
						Index: "1",
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
				EpochGroupDataList: []types.EpochGroupData{
					{
						PocStartBlockHeight: 0,
					},
					{
						PocStartBlockHeight: 1,
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
					TotalFees:      76,
					TotalSubsidies: 1,
					TotalRefunded:  73,
					TotalBurned:    23,
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
				// this line is used by starport scaffolding # types/genesis/validField
			},
			valid: true,
		},
		{
			desc: "duplicated inference",
			genState: &types.GenesisState{
				InferenceList: []types.Inference{
					{
						Index: "0",
					},
					{
						Index: "0",
					},
				},
			},
			valid: false,
		},
		{
			desc: "duplicated participant",
			genState: &types.GenesisState{
				ParticipantList: []types.Participant{
					{
						Index: "0",
					},
					{
						Index: "0",
					},
				},
			},
			valid: false,
		},
		{
			desc: "duplicated epochGroupData",
			genState: &types.GenesisState{
				EpochGroupDataList: []types.EpochGroupData{
					{
						PocStartBlockHeight: 0,
					},
					{
						PocStartBlockHeight: 0,
					},
				},
			},
			valid: false,
		},
		{
			desc: "duplicated settleAmount",
			genState: &types.GenesisState{
				SettleAmountList: []types.SettleAmount{
					{
						Participant: "0",
					},
					{
						Participant: "0",
					},
				},
			},
			valid: false,
		},
		{
			desc: "duplicated epochGroupValidations",
			genState: &types.GenesisState{
				EpochGroupValidationsList: []types.EpochGroupValidations{
					{
						Participant:         "0",
						PocStartBlockHeight: 0,
					},
					{
						Participant:         "0",
						PocStartBlockHeight: 0,
					},
				},
			},
			valid: false,
		},
		{
			desc: "duplicated topMiner",
			genState: &types.GenesisState{
				TopMinerList: []types.TopMiner{
					{
						Address: "0",
					},
					{
						Address: "0",
					},
				},
			},
			valid: false,
		},
		{
			desc: "duplicated inferenceTimeout",
			genState: &types.GenesisState{
				InferenceTimeoutList: []types.InferenceTimeout{
					{
						ExpirationHeight: 0,
						InferenceId:      "0",
					},
					{
						ExpirationHeight: 0,
						InferenceId:      "0",
					},
				},
			},
			valid: false,
		},
		{
			desc: "duplicated inferenceValidationDetails",
			genState: &types.GenesisState{
				InferenceValidationDetailsList: []types.InferenceValidationDetails{
					{
						EpochGroupId: 0,
						InferenceId:  "0",
					},
					{
						EpochGroupId: 0,
						InferenceId:  "0",
					},
				},
			},
			valid: false,
		},
		{
			desc: "duplicated epochPerformanceSummary",
			genState: &types.GenesisState{
				EpochPerformanceSummaryList: []types.EpochPerformanceSummary{
					{
						EpochStartHeight: 0,
						ParticipantId:    "0",
					},
					{
						EpochStartHeight: 0,
						ParticipantId:    "0",
					},
				},
			},
			valid: false,
		},
		{
			desc: "duplicated partialUpgrade",
			genState: &types.GenesisState{
				PartialUpgradeList: []types.PartialUpgrade{
					{
						Height: 0,
					},
					{
						Height: 0,
					},
				},
			},
			valid: false,
		},
		// this line is used by starport scaffolding # types/genesis/testcase
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			err := tc.genState.Validate()
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
