package types_test

import (
	"testing"

	"github.com/productscience/inference/x/bls/types"
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
				Params: types.DefaultParams(),
				// this line is used by starport scaffolding # types/genesis/validField
			},
			valid: true,
		},
		{
			desc: "duplicated epoch id in bls data list",
			genState: &types.GenesisState{
				Params: types.DefaultParams(),
				BlsDataList: []types.EpochBLSData{
					{EpochId: 1},
					{EpochId: 1},
				},
			},
			valid: false,
		},
		{
			desc: "duplicated request id in signing requests",
			genState: &types.GenesisState{
				Params: types.DefaultParams(),
				SigningRequests: []types.ThresholdSigningRequest{
					{RequestId: []byte("request1")},
					{RequestId: []byte("request1")},
				},
			},
			valid: false,
		},
		{
			desc: "duplicated new epoch id in group validation states",
			genState: &types.GenesisState{
				Params: types.DefaultParams(),
				GroupValidationStates: []types.GroupKeyValidationState{
					{NewEpochId: 2},
					{NewEpochId: 2},
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
