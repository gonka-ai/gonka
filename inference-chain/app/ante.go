package app

import (
	"encoding/json"
	"errors"
	"fmt"

	ibcante "github.com/cosmos/ibc-go/v8/modules/core/ante"
	"github.com/cosmos/ibc-go/v8/modules/core/keeper"

	corestoretypes "cosmossdk.io/core/store"
	storetypes "cosmossdk.io/store/types"
	circuitante "cosmossdk.io/x/circuit/ante"
	circuitkeeper "cosmossdk.io/x/circuit/keeper"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/auth/ante"

	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"

	inferencemodulekeeper "github.com/productscience/inference/x/inference/keeper"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

// HandlerOptions extend the SDK's AnteHandler options by requiring the IBC
// channel keeper.
type HandlerOptions struct {
	ante.HandlerOptions

	IBCKeeper             *keeper.Keeper
	NodeConfig            *wasmtypes.NodeConfig
	WasmKeeper            *wasmkeeper.Keeper
	TXCounterStoreService corestoretypes.KVStoreService
	CircuitKeeper         *circuitkeeper.Keeper
	InferenceKeeper       *inferencemodulekeeper.Keeper
}

// Gas is still charged against the tx's gas limit; this only bypasses fee checks.
type LiquidityPoolFeeBypassDecorator struct {
	// Dynamic sources from chain state
	WasmKeeper      *wasmkeeper.Keeper
	InferenceKeeper *inferencemodulekeeper.Keeper
	GasCap          uint64 // maximum allowed gas for bypassed txs
	Priority        int64  // optional priority boost so zero-fee txs aren't starved
}

// minimal struct to decode {"send":{"contract":"..."}} from cw20 base
type cw20SendEnvelope struct {
	Send struct {
		Contract string `json:"contract"`
	} `json:"send"`
}

func isAllWasmExec(tx sdk.Tx) bool {
	for _, m := range tx.GetMsgs() {
		// type assertion is fastest & version-safe
		if _, ok := m.(*wasmtypes.MsgExecuteContract); !ok {
			return false
		}
	}
	return true
}

// matchesAllowedSwap checks if a MsgExecuteContract is either a direct call to a pool
// or a cw20 Send{contract:<pool>} to a pool.
func (d LiquidityPoolFeeBypassDecorator) matchesAllowedSwap(ctx sdk.Context, msg sdk.Msg, poolAddress string, wrappedCodeID uint64) bool {
	exec, ok := msg.(*wasmtypes.MsgExecuteContract)
	if !ok {
		return false
	}

	// Helper to check if a contract address is a wrapped token instance by code id
	isWrappedByCodeID := func(addr string) bool {
		if d.WasmKeeper == nil {
			return false
		}
		acc, err := sdk.AccAddressFromBech32(addr)
		if err != nil {
			return false
		}
		info := d.WasmKeeper.GetContractInfo(ctx, acc)
		if info == nil {
			return false
		}
		return info.CodeID == wrappedCodeID
	}

	// Path A: direct execute to pool
	if exec.Contract == poolAddress {
		return true
	}

	// Path B: cw20::Send to pool (exec is sent to cw20)
	var env cw20SendEnvelope
	if err := json.Unmarshal(exec.Msg, &env); err == nil {
		if env.Send.Contract != "" && env.Send.Contract == poolAddress {
			// Only allow if the caller contract is a wrapped token (by code id)
			if isWrappedByCodeID(exec.Contract) {
				return true
			}
		}
	}
	return false
}

func (d LiquidityPoolFeeBypassDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	msgs := tx.GetMsgs()

	// Fast path: only consider txs that are *entirely* wasm MsgExecuteContract.
	if !isAllWasmExec(tx) {
		return next(ctx, tx, simulate)
	}

	// Check if we have the required chain state for fee bypass
	var (
		poolAddress   string
		wrappedCodeID uint64
		havePool      bool
		haveWrapped   bool
	)
	if d.InferenceKeeper != nil {
		if pool, found := d.InferenceKeeper.GetLiquidityPool(ctx); found {
			poolAddress = pool.Address
			havePool = true
		}
		if codeID, found := d.InferenceKeeper.GetWrappedTokenCodeID(ctx); found {
			wrappedCodeID = codeID
			haveWrapped = true
		}
	}

	// If no pool or wrapped token is registered yet, just pass through without fee bypass
	if !havePool || !haveWrapped {
		return next(ctx, tx, simulate)
	}

	// Check if ALL messages in the transaction qualify for fee bypass
	// We only care about MsgExecuteContract messages - ignore all other message types
	allAllowed := true
	for _, m := range msgs {
		if !d.matchesAllowedSwap(ctx, m, poolAddress, wrappedCodeID) {
			allAllowed = false
			break
		}
	}

	if allAllowed {
		// Enforce gas cap only for bypassed wasm txs
		if feeTx, ok := tx.(sdk.FeeTx); ok {
			if d.GasCap > 0 && feeTx.GetGas() > d.GasCap {
				return ctx, fmt.Errorf("fee-bypass: gas %d exceeds cap %d", feeTx.GetGas(), d.GasCap)
			}
		}
		// Log successful fee bypass
		if d.InferenceKeeper != nil {
			d.InferenceKeeper.LogInfo("AnteHandle: LiquidityPoolFeeBypass - applying fee bypass",
				inferencetypes.System, "poolAddress", poolAddress, "wrappedCodeID", wrappedCodeID)
		}
		// Waive min-gas-prices (fees) but keep metering; optionally raise priority.
		ctx = ctx.WithMinGasPrices(sdk.DecCoins{})
		if d.Priority != 0 {
			ctx = ctx.WithPriority(d.Priority)
		}
		return next(ctx, tx, simulate)
	}
	return next(ctx, tx, simulate)
}

// NewAnteHandler constructor
func NewAnteHandler(options HandlerOptions) (sdk.AnteHandler, error) {
	if options.AccountKeeper == nil {
		return nil, errors.New("account keeper is required for ante builder")
	}
	if options.BankKeeper == nil {
		return nil, errors.New("bank keeper is required for ante builder")
	}
	if options.SignModeHandler == nil {
		return nil, errors.New("sign mode handler is required for ante builder")
	}
	if options.NodeConfig == nil {
		return nil, errors.New("node config is required for ante builder")
	}
	if options.TXCounterStoreService == nil {
		return nil, errors.New("wasm store service is required for ante builder")
	}
	if options.CircuitKeeper == nil {
		return nil, errors.New("circuit keeper is required for ante builder")
	}

	anteDecorators := []sdk.AnteDecorator{
		ante.NewSetUpContextDecorator(), // outermost AnteDecorator. SetUpContext must be called first
		wasmkeeper.NewLimitSimulationGasDecorator(options.NodeConfig.SimulationGasLimit), // after setup context to enforce limits early
		wasmkeeper.NewCountTXDecorator(options.TXCounterStoreService),
		wasmkeeper.NewGasRegisterDecorator(options.WasmKeeper.GetGasRegister()),
		circuitante.NewCircuitBreakerDecorator(options.CircuitKeeper),
		ante.NewExtensionOptionsDecorator(options.ExtensionOptionChecker),
		ante.NewValidateBasicDecorator(),
		ante.NewTxTimeoutHeightDecorator(),
		ante.NewValidateMemoDecorator(options.AccountKeeper),
		ante.NewConsumeGasForTxSizeDecorator(options.AccountKeeper),
		LiquidityPoolFeeBypassDecorator{
			WasmKeeper:      options.WasmKeeper,
			InferenceKeeper: options.InferenceKeeper,
			GasCap:          500000,    // safe cap for swap path; tune after measuring simulate
			Priority:        1_000_000, // optional: ensure zero-fee txs aren't starved
		},
		ante.NewDeductFeeDecorator(options.AccountKeeper, options.BankKeeper, options.FeegrantKeeper, options.TxFeeChecker),
		ante.NewSetPubKeyDecorator(options.AccountKeeper), // SetPubKeyDecorator must be called before all signature verification decorators
		ante.NewValidateSigCountDecorator(options.AccountKeeper),
		ante.NewSigGasConsumeDecorator(options.AccountKeeper, options.SigGasConsumer),
		ante.NewSigVerificationDecorator(options.AccountKeeper, options.SignModeHandler),
		ante.NewIncrementSequenceDecorator(options.AccountKeeper),
		ibcante.NewRedundantRelayDecorator(options.IBCKeeper),
	}

	return sdk.ChainAnteDecorators(anteDecorators...), nil
}

func (app *App) setAnteHandler(txConfig client.TxConfig, nodeConfig wasmtypes.NodeConfig, txCounterStoreKey *storetypes.KVStoreKey) {
	anteHandler, err := NewAnteHandler(
		HandlerOptions{
			HandlerOptions: ante.HandlerOptions{
				AccountKeeper:   app.AccountKeeper,
				BankKeeper:      app.BankKeeper,
				SignModeHandler: txConfig.SignModeHandler(),
				FeegrantKeeper:  app.FeeGrantKeeper,
				SigGasConsumer:  ante.DefaultSigVerificationGasConsumer,
				SigVerifyOptions: []ante.SigVerificationDecoratorOption{
					ante.WithUnorderedTxGasCost(0),
				},
			},
			IBCKeeper:             app.IBCKeeper,
			NodeConfig:            &nodeConfig,
			WasmKeeper:            &app.WasmKeeper,
			InferenceKeeper:       &app.InferenceKeeper,
			TXCounterStoreService: runtime.NewKVStoreService(txCounterStoreKey),
			CircuitKeeper:         &app.CircuitBreakerKeeper,
		},
	)
	if err != nil {
		panic(fmt.Errorf("failed to create AnteHandler: %s", err))
	}

	// Set the AnteHandler for the app
	app.SetAnteHandler(anteHandler)
}
