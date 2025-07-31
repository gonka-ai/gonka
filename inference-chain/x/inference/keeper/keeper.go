package keeper

import (
	"fmt"

	"cosmossdk.io/core/store"
	"cosmossdk.io/log"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

type (
	Keeper struct {
		cdc          codec.BinaryCodec
		storeService store.KVStoreService
		logger       log.Logger
		BankKeeper   types.BookkeepingBankKeeper
		BankView     types.BankKeeper
		validatorSet types.ValidatorSet
		group        types.GroupMessageKeeper
		Staking      types.StakingKeeper
		// the address capable of executing a MsgUpdateParams message. Typically, this
		// should be the x/gov module account.
		authority     string
		AccountKeeper types.AccountKeeper
		getWasmKeeper func() wasmkeeper.Keeper `optional:"true"`

		collateralKeeper    types.CollateralKeeper
		streamvestingKeeper types.StreamVestingKeeper
	}
)

func NewKeeper(
	cdc codec.BinaryCodec,
	storeService store.KVStoreService,
	logger log.Logger,
	authority string,
	bank types.BookkeepingBankKeeper,
	bankView types.BankKeeper,
	group types.GroupMessageKeeper,
	validatorSet types.ValidatorSet,
	staking types.StakingKeeper,
	accountKeeper types.AccountKeeper,
	collateralKeeper types.CollateralKeeper,
	streamvestingKeeper types.StreamVestingKeeper,
	getWasmKeeper func() wasmkeeper.Keeper,
) Keeper {
	if _, err := sdk.AccAddressFromBech32(authority); err != nil {
		panic(fmt.Sprintf("invalid authority address: %s", authority))
	}

	return Keeper{
		cdc:                 cdc,
		storeService:        storeService,
		authority:           authority,
		logger:              logger,
		BankKeeper:          bank,
		BankView:            bankView,
		group:               group,
		validatorSet:        validatorSet,
		Staking:             staking,
		AccountKeeper:       accountKeeper,
		collateralKeeper:    collateralKeeper,
		streamvestingKeeper: streamvestingKeeper,
		getWasmKeeper:       getWasmKeeper,
	}
}

// GetAuthority returns the module's authority.
func (k Keeper) GetAuthority() string {
	return k.authority
}

// GetWasmKeeper returns the WASM keeper
func (k Keeper) GetWasmKeeper() wasmkeeper.Keeper {
	return k.getWasmKeeper()
}

// GetCollateralKeeper returns the collateral keeper.
func (k Keeper) GetCollateralKeeper() types.CollateralKeeper {
	return k.collateralKeeper
}

// GetStreamVestingKeeper returns the streamvesting keeper.
func (k Keeper) GetStreamVestingKeeper() types.StreamVestingKeeper {
	return k.streamvestingKeeper
}

// Logger returns a module-specific logger.
func (k Keeper) Logger() log.Logger {
	return k.logger.With("module", fmt.Sprintf("x/%s", types.ModuleName))
}

func (k Keeper) LogInfo(msg string, subSystem types.SubSystem, keyvals ...interface{}) {
	k.Logger().Info(msg, append(keyvals, "subsystem", subSystem.String())...)
}

func (k Keeper) LogError(msg string, subSystem types.SubSystem, keyvals ...interface{}) {
	k.Logger().Error(msg, append(keyvals, "subsystem", subSystem.String())...)
}

func (k Keeper) LogWarn(msg string, subSystem types.SubSystem, keyvals ...interface{}) {
	k.Logger().Warn(msg, append(keyvals, "subsystem", subSystem.String())...)
}

func (k Keeper) LogDebug(msg string, subSystem types.SubSystem, keyVals ...interface{}) {
	k.Logger().Debug(msg, append(keyVals, "subsystem", subSystem.String())...)
}

// Codec returns the binary codec used by the keeper.
func (k Keeper) Codec() codec.BinaryCodec {
	return k.cdc
}

type EntryType int

const (
	Debit EntryType = iota
	Credit
)

func (e EntryType) String() string {
	switch e {
	case Debit:
		return "debit"
	case Credit:
		return "credit"
	default:
		return "unknown"
	}
}
