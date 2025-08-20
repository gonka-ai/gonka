package genesistransfer_test

import (
	"testing"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/nullify"
	genesistransfer "github.com/productscience/inference/x/genesistransfer/module"
	"github.com/productscience/inference/x/genesistransfer/types"
	"github.com/stretchr/testify/require"
)

func TestGenesis(t *testing.T) {
	genesisState := types.GenesisState{
		Params: types.DefaultParams(),

		// this line is used by starport scaffolding # genesis/test/state
	}

	k, ctx := keepertest.GenesistransferKeeper(t)
	genesistransfer.InitGenesis(ctx, k, genesisState)
	got := genesistransfer.ExportGenesis(ctx, k)
	require.NotNil(t, got)

	nullify.Fill(&genesisState)
	nullify.Fill(got)

	// this line is used by starport scaffolding # genesis/test/assert
}
