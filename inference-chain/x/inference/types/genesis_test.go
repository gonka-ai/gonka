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
				Params: types.DefaultParams(),
				// this line is used by starport scaffolding # types/genesis/validField
			},
			valid: true,
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

func TestGenesisStateValidateRejectsExtremeDecimalExponents(t *testing.T) {
	t.Run("genesis-only params", func(t *testing.T) {
		state := types.DefaultGenesis()
		state.GenesisOnlyParams.GenesisGuardianMultiplier.Exponent = 19
		require.ErrorIs(t, state.Validate(), types.ErrInvalidDecimalExponent)
	})

	t.Run("model list", func(t *testing.T) {
		state := types.DefaultGenesis()
		state.ModelList = []types.Model{{
			ValidationThreshold: &types.Decimal{Value: 1, Exponent: 19},
		}}
		require.ErrorIs(t, state.Validate(), types.ErrInvalidDecimalExponent)
	})

	t.Run("participant state", func(t *testing.T) {
		state := types.DefaultGenesis()
		state.ParticipantList = []types.Participant{{
			CurrentEpochStats: &types.CurrentEpochStats{
				InvalidLLR: &types.Decimal{Value: 1, Exponent: 19},
			},
		}}
		require.ErrorIs(t, state.Validate(), types.ErrInvalidDecimalExponent)
	})
}
