package app

import (
	"context"

	"testing"

	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	wasmvm "github.com/CosmWasm/wasmvm/v2"
	wasmvmtypes "github.com/CosmWasm/wasmvm/v2/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
)

// gasRecordingEngine records the gas limit it gets and uses all of it.
type gasRecordingEngine struct {
	wasmtypes.WasmEngine
	gasLimits []uint64
}

func (e *gasRecordingEngine) Execute(_ wasmvm.Checksum, _ wasmvmtypes.Env, _ wasmvmtypes.MessageInfo, _ []byte, _ wasmvm.KVStore, _ wasmvm.GoAPI, _ wasmvm.Querier, _ wasmvm.GasMeter, gasLimit uint64, _ wasmvmtypes.UFraction) (*wasmvmtypes.ContractResult, uint64, error) {
	e.gasLimits = append(e.gasLimits, gasLimit)
	return &wasmvmtypes.ContractResult{}, gasLimit, nil
}

func (e *gasRecordingEngine) MigrateWithInfo(_ wasmvm.Checksum, _ wasmvmtypes.Env, _ []byte, _ wasmvmtypes.MigrateInfo, _ wasmvm.KVStore, _ wasmvm.GoAPI, _ wasmvm.Querier, _ wasmvm.GasMeter, gasLimit uint64, _ wasmvmtypes.UFraction) (*wasmvmtypes.ContractResult, uint64, error) {
	e.gasLimits = append(e.gasLimits, gasLimit)
	return &wasmvmtypes.ContractResult{}, gasLimit, nil
}

func anteCtx(t *testing.T, mode sdk.ExecMode) sdk.Context {
	var out sdk.Context
	_, err := SimulationContractGasDecorator{}.AnteHandle(sdk.Context{}.WithContext(context.Background()).WithExecMode(mode), nil, mode == sdk.ExecModeSimulate,
		func(ctx sdk.Context, _ sdk.Tx, _ bool) (sdk.Context, error) { out = ctx; return ctx, nil })
	require.NoError(t, err)
	return out
}

func TestSimulationGasCappedEngine(t *testing.T) {
	budget := wasmtypes.DefaultGasMultiplier * SimulationContractGasLimit
	execute := func(engine wasmtypes.WasmEngine, querier wasmvm.Querier, gasLimit uint64) {
		_, _, err := engine.Execute(nil, wasmvmtypes.Env{}, wasmvmtypes.MessageInfo{}, nil, nil, wasmvm.GoAPI{}, querier, nil, gasLimit, wasmvmtypes.UFraction{})
		require.NoError(t, err)
	}

	t.Run("simulated calls share one budget", func(t *testing.T) {
		rec := &gasRecordingEngine{}
		engine := newSimulationGasCappedEngine(rec)
		ctx := anteCtx(t, sdk.ExecModeSimulate)
		execute(engine, wasmkeeper.QueryHandler{Ctx: ctx}, budget/4)
		execute(engine, wasmkeeper.QueryHandler{Ctx: ctx}, ^uint64(0))
		execute(engine, wasmkeeper.QueryHandler{Ctx: ctx}, ^uint64(0))
		require.Equal(t, []uint64{budget / 4, budget - budget/4, 0}, rec.gasLimits)
	})

	t.Run("migrate passes a pointer querier", func(t *testing.T) {
		rec := &gasRecordingEngine{}
		engine := newSimulationGasCappedEngine(rec)
		querier := wasmkeeper.QueryHandler{Ctx: anteCtx(t, sdk.ExecModeSimulate)}
		_, _, err := engine.MigrateWithInfo(nil, wasmvmtypes.Env{}, nil, wasmvmtypes.MigrateInfo{}, nil, wasmvm.GoAPI{}, &querier, nil, ^uint64(0), wasmvmtypes.UFraction{})
		require.NoError(t, err)
		require.Equal(t, []uint64{budget}, rec.gasLimits)
	})

	for _, mode := range []sdk.ExecMode{sdk.ExecModeCheck, sdk.ExecModeFinalize} {
		rec := &gasRecordingEngine{}
		engine := newSimulationGasCappedEngine(rec)
		execute(engine, wasmkeeper.QueryHandler{Ctx: anteCtx(t, mode)}, ^uint64(0))
		require.Equal(t, []uint64{^uint64(0)}, rec.gasLimits, "mode %d", mode)
	}
}
