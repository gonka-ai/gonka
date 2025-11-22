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
	genesisState := types.GenesisState{
		Params:            types.DefaultParams(),
		GenesisOnlyParams: types.DefaultGenesisOnlyParams(),
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
				ValidationThreshold:    &types.Decimal{Value: 99, Exponent: -2},
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
	// this line is used by starport scaffolding # genesis/test/assert
}
