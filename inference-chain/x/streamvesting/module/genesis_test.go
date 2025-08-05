package streamvesting_test

import (
	"testing"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/nullify"
	streamvesting "github.com/productscience/inference/x/streamvesting/module"
	"github.com/productscience/inference/x/streamvesting/types"
	"github.com/stretchr/testify/require"
)

func TestGenesis(t *testing.T) {
	genesisState := types.GenesisState{
		Params: types.DefaultParams(),

		// this line is used by starport scaffolding # genesis/test/state
	}

	k, ctx := keepertest.StreamvestingKeeper(t)
	streamvesting.InitGenesis(ctx, k, genesisState)
	got := streamvesting.ExportGenesis(ctx, k)
	require.NotNil(t, got)

	nullify.Fill(&genesisState)
	nullify.Fill(got)

	// this line is used by starport scaffolding # genesis/test/assert
}
