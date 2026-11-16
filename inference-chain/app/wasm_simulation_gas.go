package app

import (
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	wasmvm "github.com/CosmWasm/wasmvm/v2"
	wasmvmtypes "github.com/CosmWasm/wasmvm/v2/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// SimulationContractGasLimit is the SDK gas all contract calls of one simulated tx may use together.
// With block.max_gas=-1 wasmd simulates with an infinite meter and the VM gets MaxUint64 gas.
const SimulationContractGasLimit uint64 = 10_000_000

type simulationGasBudgetKey struct{}

type simulationGasBudget struct{ remaining uint64 }

// spend charges a call: the gas the VM reports covers only the VM itself, so the gas
// its host calls (storage, iteration) added to the tx meter is charged as well.
func (b *simulationGasBudget) spend(vmGas, hostGas uint64) {
	if b == nil {
		return
	}
	gas := vmGas + hostGas
	if gas < vmGas {
		gas = b.remaining
	}
	if gas > b.remaining {
		gas = b.remaining
	}
	b.remaining -= gas
}

func gasConsumed(m wasmvm.GasMeter) uint64 {
	if m == nil {
		return 0
	}
	return m.GasConsumed()
}

// hostGas is what the tx meter gained during a call, in VM gas units.
func hostGas(m wasmvm.GasMeter, before uint64) uint64 {
	after := gasConsumed(m)
	if after < before {
		return 0
	}
	return after - before
}

// SimulationContractGasDecorator gives a simulated tx its contract gas budget.
type SimulationContractGasDecorator struct{}

func (SimulationContractGasDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	if simulate {
		budget := &simulationGasBudget{remaining: wasmtypes.DefaultGasMultiplier * SimulationContractGasLimit}
		ctx = ctx.WithValue(simulationGasBudgetKey{}, budget)
	}
	return next(ctx, tx, simulate)
}

// simulationGasCappedEngine charges every contract call of a simulated tx to its budget.
type simulationGasCappedEngine struct {
	wasmtypes.WasmEngine
}

func newSimulationGasCappedEngine(engine wasmtypes.WasmEngine) wasmtypes.WasmEngine {
	return simulationGasCappedEngine{WasmEngine: engine}
}

func (e simulationGasCappedEngine) limit(querier wasmvm.Querier, gasLimit uint64) (*simulationGasBudget, uint64) {
	var ctx sdk.Context
	switch h := querier.(type) {
	case wasmkeeper.QueryHandler:
		ctx = h.Ctx
	case *wasmkeeper.QueryHandler:
		ctx = h.Ctx
	default:
		return nil, gasLimit
	}
	if ctx.ExecMode() != sdk.ExecModeSimulate {
		return nil, gasLimit
	}
	budget, ok := ctx.Value(simulationGasBudgetKey{}).(*simulationGasBudget)
	if !ok {
		budget = &simulationGasBudget{remaining: wasmtypes.DefaultGasMultiplier * SimulationContractGasLimit}
	}
	return budget, min(gasLimit, budget.remaining)
}

func (e simulationGasCappedEngine) Instantiate(checksum wasmvm.Checksum, env wasmvmtypes.Env, info wasmvmtypes.MessageInfo, initMsg []byte, store wasmvm.KVStore, goapi wasmvm.GoAPI, querier wasmvm.Querier, gasMeter wasmvm.GasMeter, gasLimit uint64, deserCost wasmvmtypes.UFraction) (*wasmvmtypes.ContractResult, uint64, error) {
	budget, gasLimit := e.limit(querier, gasLimit)
	before := gasConsumed(gasMeter)
	res, gasUsed, err := e.WasmEngine.Instantiate(checksum, env, info, initMsg, store, goapi, querier, gasMeter, gasLimit, deserCost)
	budget.spend(gasUsed, hostGas(gasMeter, before))
	return res, gasUsed, err
}

func (e simulationGasCappedEngine) Execute(code wasmvm.Checksum, env wasmvmtypes.Env, info wasmvmtypes.MessageInfo, executeMsg []byte, store wasmvm.KVStore, goapi wasmvm.GoAPI, querier wasmvm.Querier, gasMeter wasmvm.GasMeter, gasLimit uint64, deserCost wasmvmtypes.UFraction) (*wasmvmtypes.ContractResult, uint64, error) {
	budget, gasLimit := e.limit(querier, gasLimit)
	before := gasConsumed(gasMeter)
	res, gasUsed, err := e.WasmEngine.Execute(code, env, info, executeMsg, store, goapi, querier, gasMeter, gasLimit, deserCost)
	budget.spend(gasUsed, hostGas(gasMeter, before))
	return res, gasUsed, err
}

func (e simulationGasCappedEngine) Query(code wasmvm.Checksum, env wasmvmtypes.Env, queryMsg []byte, store wasmvm.KVStore, goapi wasmvm.GoAPI, querier wasmvm.Querier, gasMeter wasmvm.GasMeter, gasLimit uint64, deserCost wasmvmtypes.UFraction) (*wasmvmtypes.QueryResult, uint64, error) {
	budget, gasLimit := e.limit(querier, gasLimit)
	before := gasConsumed(gasMeter)
	res, gasUsed, err := e.WasmEngine.Query(code, env, queryMsg, store, goapi, querier, gasMeter, gasLimit, deserCost)
	budget.spend(gasUsed, hostGas(gasMeter, before))
	return res, gasUsed, err
}

func (e simulationGasCappedEngine) Migrate(checksum wasmvm.Checksum, env wasmvmtypes.Env, migrateMsg []byte, store wasmvm.KVStore, goapi wasmvm.GoAPI, querier wasmvm.Querier, gasMeter wasmvm.GasMeter, gasLimit uint64, deserCost wasmvmtypes.UFraction) (*wasmvmtypes.ContractResult, uint64, error) {
	budget, gasLimit := e.limit(querier, gasLimit)
	before := gasConsumed(gasMeter)
	res, gasUsed, err := e.WasmEngine.Migrate(checksum, env, migrateMsg, store, goapi, querier, gasMeter, gasLimit, deserCost)
	budget.spend(gasUsed, hostGas(gasMeter, before))
	return res, gasUsed, err
}

func (e simulationGasCappedEngine) MigrateWithInfo(checksum wasmvm.Checksum, env wasmvmtypes.Env, migrateMsg []byte, migrateInfo wasmvmtypes.MigrateInfo, store wasmvm.KVStore, goapi wasmvm.GoAPI, querier wasmvm.Querier, gasMeter wasmvm.GasMeter, gasLimit uint64, deserCost wasmvmtypes.UFraction) (*wasmvmtypes.ContractResult, uint64, error) {
	budget, gasLimit := e.limit(querier, gasLimit)
	before := gasConsumed(gasMeter)
	res, gasUsed, err := e.WasmEngine.MigrateWithInfo(checksum, env, migrateMsg, migrateInfo, store, goapi, querier, gasMeter, gasLimit, deserCost)
	budget.spend(gasUsed, hostGas(gasMeter, before))
	return res, gasUsed, err
}

func (e simulationGasCappedEngine) Sudo(checksum wasmvm.Checksum, env wasmvmtypes.Env, sudoMsg []byte, store wasmvm.KVStore, goapi wasmvm.GoAPI, querier wasmvm.Querier, gasMeter wasmvm.GasMeter, gasLimit uint64, deserCost wasmvmtypes.UFraction) (*wasmvmtypes.ContractResult, uint64, error) {
	budget, gasLimit := e.limit(querier, gasLimit)
	before := gasConsumed(gasMeter)
	res, gasUsed, err := e.WasmEngine.Sudo(checksum, env, sudoMsg, store, goapi, querier, gasMeter, gasLimit, deserCost)
	budget.spend(gasUsed, hostGas(gasMeter, before))
	return res, gasUsed, err
}

func (e simulationGasCappedEngine) Reply(checksum wasmvm.Checksum, env wasmvmtypes.Env, reply wasmvmtypes.Reply, store wasmvm.KVStore, goapi wasmvm.GoAPI, querier wasmvm.Querier, gasMeter wasmvm.GasMeter, gasLimit uint64, deserCost wasmvmtypes.UFraction) (*wasmvmtypes.ContractResult, uint64, error) {
	budget, gasLimit := e.limit(querier, gasLimit)
	before := gasConsumed(gasMeter)
	res, gasUsed, err := e.WasmEngine.Reply(checksum, env, reply, store, goapi, querier, gasMeter, gasLimit, deserCost)
	budget.spend(gasUsed, hostGas(gasMeter, before))
	return res, gasUsed, err
}

func (e simulationGasCappedEngine) IBCChannelOpen(checksum wasmvm.Checksum, env wasmvmtypes.Env, msg wasmvmtypes.IBCChannelOpenMsg, store wasmvm.KVStore, goapi wasmvm.GoAPI, querier wasmvm.Querier, gasMeter wasmvm.GasMeter, gasLimit uint64, deserCost wasmvmtypes.UFraction) (*wasmvmtypes.IBCChannelOpenResult, uint64, error) {
	budget, gasLimit := e.limit(querier, gasLimit)
	before := gasConsumed(gasMeter)
	res, gasUsed, err := e.WasmEngine.IBCChannelOpen(checksum, env, msg, store, goapi, querier, gasMeter, gasLimit, deserCost)
	budget.spend(gasUsed, hostGas(gasMeter, before))
	return res, gasUsed, err
}

func (e simulationGasCappedEngine) IBCChannelConnect(checksum wasmvm.Checksum, env wasmvmtypes.Env, msg wasmvmtypes.IBCChannelConnectMsg, store wasmvm.KVStore, goapi wasmvm.GoAPI, querier wasmvm.Querier, gasMeter wasmvm.GasMeter, gasLimit uint64, deserCost wasmvmtypes.UFraction) (*wasmvmtypes.IBCBasicResult, uint64, error) {
	budget, gasLimit := e.limit(querier, gasLimit)
	before := gasConsumed(gasMeter)
	res, gasUsed, err := e.WasmEngine.IBCChannelConnect(checksum, env, msg, store, goapi, querier, gasMeter, gasLimit, deserCost)
	budget.spend(gasUsed, hostGas(gasMeter, before))
	return res, gasUsed, err
}

func (e simulationGasCappedEngine) IBCChannelClose(checksum wasmvm.Checksum, env wasmvmtypes.Env, msg wasmvmtypes.IBCChannelCloseMsg, store wasmvm.KVStore, goapi wasmvm.GoAPI, querier wasmvm.Querier, gasMeter wasmvm.GasMeter, gasLimit uint64, deserCost wasmvmtypes.UFraction) (*wasmvmtypes.IBCBasicResult, uint64, error) {
	budget, gasLimit := e.limit(querier, gasLimit)
	before := gasConsumed(gasMeter)
	res, gasUsed, err := e.WasmEngine.IBCChannelClose(checksum, env, msg, store, goapi, querier, gasMeter, gasLimit, deserCost)
	budget.spend(gasUsed, hostGas(gasMeter, before))
	return res, gasUsed, err
}

func (e simulationGasCappedEngine) IBCPacketReceive(checksum wasmvm.Checksum, env wasmvmtypes.Env, msg wasmvmtypes.IBCPacketReceiveMsg, store wasmvm.KVStore, goapi wasmvm.GoAPI, querier wasmvm.Querier, gasMeter wasmvm.GasMeter, gasLimit uint64, deserCost wasmvmtypes.UFraction) (*wasmvmtypes.IBCReceiveResult, uint64, error) {
	budget, gasLimit := e.limit(querier, gasLimit)
	before := gasConsumed(gasMeter)
	res, gasUsed, err := e.WasmEngine.IBCPacketReceive(checksum, env, msg, store, goapi, querier, gasMeter, gasLimit, deserCost)
	budget.spend(gasUsed, hostGas(gasMeter, before))
	return res, gasUsed, err
}

func (e simulationGasCappedEngine) IBCPacketAck(checksum wasmvm.Checksum, env wasmvmtypes.Env, msg wasmvmtypes.IBCPacketAckMsg, store wasmvm.KVStore, goapi wasmvm.GoAPI, querier wasmvm.Querier, gasMeter wasmvm.GasMeter, gasLimit uint64, deserCost wasmvmtypes.UFraction) (*wasmvmtypes.IBCBasicResult, uint64, error) {
	budget, gasLimit := e.limit(querier, gasLimit)
	before := gasConsumed(gasMeter)
	res, gasUsed, err := e.WasmEngine.IBCPacketAck(checksum, env, msg, store, goapi, querier, gasMeter, gasLimit, deserCost)
	budget.spend(gasUsed, hostGas(gasMeter, before))
	return res, gasUsed, err
}

func (e simulationGasCappedEngine) IBCPacketTimeout(checksum wasmvm.Checksum, env wasmvmtypes.Env, msg wasmvmtypes.IBCPacketTimeoutMsg, store wasmvm.KVStore, goapi wasmvm.GoAPI, querier wasmvm.Querier, gasMeter wasmvm.GasMeter, gasLimit uint64, deserCost wasmvmtypes.UFraction) (*wasmvmtypes.IBCBasicResult, uint64, error) {
	budget, gasLimit := e.limit(querier, gasLimit)
	before := gasConsumed(gasMeter)
	res, gasUsed, err := e.WasmEngine.IBCPacketTimeout(checksum, env, msg, store, goapi, querier, gasMeter, gasLimit, deserCost)
	budget.spend(gasUsed, hostGas(gasMeter, before))
	return res, gasUsed, err
}

func (e simulationGasCappedEngine) IBCSourceCallback(checksum wasmvm.Checksum, env wasmvmtypes.Env, msg wasmvmtypes.IBCSourceCallbackMsg, store wasmvm.KVStore, goapi wasmvm.GoAPI, querier wasmvm.Querier, gasMeter wasmvm.GasMeter, gasLimit uint64, deserCost wasmvmtypes.UFraction) (*wasmvmtypes.IBCBasicResult, uint64, error) {
	budget, gasLimit := e.limit(querier, gasLimit)
	before := gasConsumed(gasMeter)
	res, gasUsed, err := e.WasmEngine.IBCSourceCallback(checksum, env, msg, store, goapi, querier, gasMeter, gasLimit, deserCost)
	budget.spend(gasUsed, hostGas(gasMeter, before))
	return res, gasUsed, err
}

func (e simulationGasCappedEngine) IBCDestinationCallback(checksum wasmvm.Checksum, env wasmvmtypes.Env, msg wasmvmtypes.IBCDestinationCallbackMsg, store wasmvm.KVStore, goapi wasmvm.GoAPI, querier wasmvm.Querier, gasMeter wasmvm.GasMeter, gasLimit uint64, deserCost wasmvmtypes.UFraction) (*wasmvmtypes.IBCBasicResult, uint64, error) {
	budget, gasLimit := e.limit(querier, gasLimit)
	before := gasConsumed(gasMeter)
	res, gasUsed, err := e.WasmEngine.IBCDestinationCallback(checksum, env, msg, store, goapi, querier, gasMeter, gasLimit, deserCost)
	budget.spend(gasUsed, hostGas(gasMeter, before))
	return res, gasUsed, err
}
