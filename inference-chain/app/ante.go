package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	ibcante "github.com/cosmos/ibc-go/v8/modules/core/ante"
	"github.com/cosmos/ibc-go/v8/modules/core/keeper"

	corestoretypes "cosmossdk.io/core/store"
	storetypes "cosmossdk.io/store/types"
	circuitante "cosmossdk.io/x/circuit/ante"
	circuitkeeper "cosmossdk.io/x/circuit/keeper"
	"cosmossdk.io/x/feegrant"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/x/auth/ante"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

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
	FeegrantStoreService  corestoretypes.KVStoreService
	CircuitKeeper         *circuitkeeper.Keeper
	InferenceKeeper       *inferencemodulekeeper.Keeper
	Codec                 codec.Codec
	AuthzKeeper           AuthzAuthorizationKeeper
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
		// Set the fee bypass flag so the custom TxFeeChecker also allows zero fees.
		ctx = ctx.WithMinGasPrices(sdk.DecCoins{})
		ctx = ctx.WithValue(networkDutyFeeBypassKey{}, true)
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

	ak := txAuthKeeper{options.AccountKeeper}
	anteDecorators := []sdk.AnteDecorator{
		ante.NewSetUpContextDecorator(), // outermost AnteDecorator. SetUpContext must be called first
		TxParamsCacheDecorator{},        // inference params, auth params and signer accounts read once per tx
		wasmkeeper.NewLimitSimulationGasDecorator(options.NodeConfig.SimulationGasLimit), // after setup context to enforce limits early
		// wasmd CountTX skips KV in Simulate; this wrapper meters it. Remove when wasmd does.
		NewCountTXSimulateGasDecorator(options.TXCounterStoreService),
		wasmkeeper.NewGasRegisterDecorator(options.WasmKeeper.GetGasRegister()),
		circuitante.NewCircuitBreakerDecorator(options.CircuitKeeper),
		ante.NewExtensionOptionsDecorator(options.ExtensionOptionChecker),
		ante.NewValidateBasicDecorator(),
		MaxTxFeeDecorator{},
		ante.NewTxTimeoutHeightDecorator(),
		ante.NewValidateMemoDecorator(ak),
		ante.NewConsumeGasForTxSizeDecorator(ak),
		LiquidityPoolFeeBypassDecorator{
			WasmKeeper:      options.WasmKeeper,
			InferenceKeeper: options.InferenceKeeper,
			GasCap:          500000,    // safe cap for swap path; tune after measuring simulate
			Priority:        1_000_000, // optional: ensure zero-fee txs aren't starved
		},
		NetworkDutyFeeBypassDecorator{
			InferenceKeeper: options.InferenceKeeper,
			// Cap for fee-exempt duty transactions. Sized at 3x the DAPI's
			// BatchGasLimit (1B, see decentralized-api/cosmosclient/tx_manager/
			// tx_manager.go:58) to accommodate the largest legitimate batched
			// PoC V2 / weight-distribution txs with headroom for future growth.
			// Raise if you see legitimate duty transactions rejected with
			// "gas N exceeds cap 3000000000".
			GasCap: 3_000_000_000,
			// Network-duty txs (PoC, validation, BLS, weight distribution) are
			// consensus-critical and must outrank all other zero-fee bypass
			// paths. LiquidityPoolFeeBypass uses Priority=1_000_000; this is
			// 10x to ensure under mempool pressure the duty txs land in blocks
			// before discretionary swap traffic.
			Priority: 10_000_000,
		},
		ante.NewDeductFeeDecorator(ak, options.BankKeeper, wrapFeegrantKeeper(options.FeegrantKeeper, options.FeegrantStoreService, options.Codec), GonkaFeeChecker(options.InferenceKeeper)),
		FeeGroupRepeatedLenDecorator{InferenceKeeper: options.InferenceKeeper},
		// Cheap mempool filters before signature verification (avoid crypto work on
		// obviously invalid PoC txs). CheckTx ante failures discard
		// state (including fee deduction), so fee-first is not an economic throttle.
		NewPocPeriodValidationDecorator(options.InferenceKeeper, options.Codec),
		ante.NewSetPubKeyDecorator(ak),
		ante.NewValidateSigCountDecorator(ak),
		ante.NewSigGasConsumeDecorator(ak, options.SigGasConsumer),
		ante.NewSigVerificationDecorator(ak, options.SignModeHandler),
		// SDK skips unordered nonce KV in Simulate; this meters it. Remove when the SDK does.
		NewUnorderedNonceSimGasDecorator(options.AccountKeeper),
		// Authz grant lookup after signature verification: the outer Grantee has
		// proven they signed the transaction before we read grant storage.
		// CheckTx-only and only for network-duty fee-bypassed txs.
		NewMsgExecAuthorizationDecorator(options.Codec, options.AuthzKeeper),
		// Bridge early-reject after sig verification: group membership / bridge-state
		// reads must not run on unauthenticated txs.
		NewBridgeExchangeEarlyRejectDecorator(options.InferenceKeeper),
		ante.NewIncrementSequenceDecorator(ak),
		ibcante.NewRedundantRelayDecorator(options.IBCKeeper),
	}

	return sdk.ChainAnteDecorators(anteDecorators...), nil
}

// TxParamsCacheDecorator installs the per-tx inference params cache; the
// context reaches the msg handlers, so ante and handlers share one KV read.
type TxParamsCacheDecorator struct{}

func (TxParamsCacheDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	ctx = inferencemodulekeeper.WithTxParamsCache(ctx).WithValue(txAuthCacheKey{}, newTxAuthCache())
	return next(ctx, tx, simulate)
}

// txAuthKeeper serves the SDK ante decorators x/auth params and accounts from one read per tx.
// Within the ante chain accounts are written only through SetAccount below.
type txAuthKeeper struct {
	ante.AccountKeeper
}

type txAuthCacheKey struct{}

type txAuthCache struct {
	params   authtypes.Params
	paramsOk bool
	accounts map[string]sdk.AccountI
}

func newTxAuthCache() *txAuthCache {
	return &txAuthCache{accounts: map[string]sdk.AccountI{}}
}

func txAuthCacheFrom(ctx context.Context) *txAuthCache {
	c, _ := ctx.Value(txAuthCacheKey{}).(*txAuthCache)
	return c
}

func (k txAuthKeeper) GetParams(ctx context.Context) authtypes.Params {
	c := txAuthCacheFrom(ctx)
	if c == nil {
		return k.AccountKeeper.GetParams(ctx)
	}
	if !c.paramsOk {
		c.params, c.paramsOk = k.AccountKeeper.GetParams(ctx), true
	}
	return c.params
}

func (k txAuthKeeper) GetAccount(ctx context.Context, addr sdk.AccAddress) sdk.AccountI {
	c := txAuthCacheFrom(ctx)
	if c == nil {
		return k.AccountKeeper.GetAccount(ctx, addr)
	}
	if acc, ok := c.accounts[string(addr)]; ok {
		return acc
	}
	acc := k.AccountKeeper.GetAccount(ctx, addr)
	if acc != nil {
		c.accounts[string(addr)] = acc
	}
	return acc
}

func (k txAuthKeeper) SetAccount(ctx context.Context, acc sdk.AccountI) {
	k.AccountKeeper.SetAccount(ctx, acc)
	if c := txAuthCacheFrom(ctx); c != nil {
		c.accounts[string(acc.GetAddress())] = acc
	}
}

// txFeegrantKeeper is Keeper.UseGrantedFees with one grant read instead of two (UpdateAllowance re-reads)
// and no write when Accept leaves the grant unchanged (zero-fee duty txs).
type txFeegrantKeeper struct {
	ante.FeegrantKeeper
	store corestoretypes.KVStoreService
	cdc   codec.BinaryCodec
}

func wrapFeegrantKeeper(k ante.FeegrantKeeper, store corestoretypes.KVStoreService, cdc codec.BinaryCodec) ante.FeegrantKeeper {
	if k == nil || store == nil || cdc == nil {
		return k
	}
	return txFeegrantKeeper{FeegrantKeeper: k, store: store, cdc: cdc}
}

func (k txFeegrantKeeper) UseGrantedFees(ctx context.Context, granter, grantee sdk.AccAddress, fee sdk.Coins, msgs []sdk.Msg) error {
	store := k.store.OpenKVStore(ctx)
	key := feegrant.FeeAllowanceKey(granter, grantee)
	bz, err := store.Get(key)
	if err != nil {
		return err
	}
	if len(bz) == 0 {
		return sdkerrors.ErrNotFound.Wrap("fee-grant not found")
	}
	var grant feegrant.Grant
	if err := k.cdc.Unmarshal(bz, &grant); err != nil {
		return err
	}
	allowance, err := grant.GetGrant()
	if err != nil {
		return err
	}
	remove, err := allowance.Accept(ctx, fee, msgs)
	if remove {
		// revocation (grant and expiry-queue entry) stays with the SDK keeper
		return k.FeegrantKeeper.UseGrantedFees(ctx, granter, grantee, fee, msgs)
	}
	if err != nil {
		return err
	}

	updated, err := feegrant.NewGrant(granter, grantee, allowance)
	if err != nil {
		return err
	}
	out, err := k.cdc.Marshal(&updated)
	if err != nil {
		return err
	}
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	// Same events as Keeper.UseGrantedFees: the use event, then UpdateAllowance's.
	sdkCtx.EventManager().EmitEvent(sdk.NewEvent(feegrant.EventTypeUseFeeGrant,
		sdk.NewAttribute(feegrant.AttributeKeyGranter, granter.String()),
		sdk.NewAttribute(feegrant.AttributeKeyGrantee, grantee.String()),
	))
	if !bytes.Equal(out, bz) {
		if err := store.Set(key, out); err != nil {
			return err
		}
	}
	sdkCtx.EventManager().EmitEvent(sdk.NewEvent(feegrant.EventTypeUpdateFeeGrant,
		sdk.NewAttribute(feegrant.AttributeKeyGranter, updated.Granter),
		sdk.NewAttribute(feegrant.AttributeKeyGrantee, updated.Grantee),
	))
	return nil
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
			Codec:                 app.appCodec,
			AuthzKeeper:           &app.AuthzKeeper,
			TXCounterStoreService: runtime.NewKVStoreService(txCounterStoreKey),
			FeegrantStoreService:  runtime.NewKVStoreService(app.GetKey(feegrant.StoreKey)),
			CircuitKeeper:         &app.CircuitBreakerKeeper,
		},
	)
	if err != nil {
		//nolint:forbidigo
		//init code:
		panic(fmt.Errorf("failed to create AnteHandler: %s", err))
	}

	// Set the AnteHandler for the app
	app.SetAnteHandler(anteHandler)
}
