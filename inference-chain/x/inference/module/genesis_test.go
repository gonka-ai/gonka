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
		// this line is used by starport scaffolding # genesis/test/state
	}

	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)

	mocks.StubForInitGenesis(ctx)

	inference.InitGenesis(ctx, k, genesisState)
	got := inference.ExportGenesis(ctx, k)
	require.NotNil(t, got)

	nullify.Fill(&genesisState)
	nullify.Fill(got)

	// this line is used by starport scaffolding # genesis/test/assert
}
