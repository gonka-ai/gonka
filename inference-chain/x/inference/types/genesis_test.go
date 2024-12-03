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
			genState: types.DefaultGenesis(),
			valid:    true,
		},
		{
			desc: "valid genesis state",
			genState: &types.GenesisState{

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
