package keeper

import (
	"fmt"

	"cosmossdk.io/core/store"
	"cosmossdk.io/log"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/inference/types"
)

type (
	Keeper struct {
		cdc          codec.BinaryCodec
		storeService store.KVStoreService
		logger       log.Logger
		BankKeeper   types.BankEscrowKeeper
		bankView     types.BankKeeper
		validatorSet types.ValidatorSet
		group        types.GroupMessageKeeper
		Staking      types.StakingKeeper
		// the address capable of executing a MsgUpdateParams message. Typically, this
		// should be the x/gov module account.
		authority     string
		AccountKeeper types.AccountKeeper
	}
)

func NewKeeper(
	cdc codec.BinaryCodec,
	storeService store.KVStoreService,
	logger log.Logger,
	authority string,
	bank types.BankEscrowKeeper,
	bankView types.BankKeeper,
	group types.GroupMessageKeeper,
	validatorSet types.ValidatorSet,
	staking types.StakingKeeper,
	accountKeeper types.AccountKeeper,

) Keeper {
	if _, err := sdk.AccAddressFromBech32(authority); err != nil {
		panic(fmt.Sprintf("invalid authority address: %s", authority))
	}

	return Keeper{
		cdc:           cdc,
		storeService:  storeService,
		authority:     authority,
		logger:        logger,
		BankKeeper:    bank,
		bankView:      bankView,
		group:         group,
		validatorSet:  validatorSet,
		Staking:       staking,
		AccountKeeper: accountKeeper,
	}
}

// GetAuthority returns the module's authority.
func (k Keeper) GetAuthority() string {
	return k.authority
}

// Logger returns a module-specific logger.
func (k Keeper) Logger() log.Logger {
	return k.logger.With("module", fmt.Sprintf("x/%s", types.ModuleName))
}

func (k Keeper) LogTransaction(to string, from string, amount int64, memo string) {
	k.Logger().Info("TransactionAudit", "to", to, "from", from, "amount", amount, "memo", memo)
}

func (k Keeper) LogBalance(address string, change int64, result int64, memo string) {
	k.Logger().Info("BalanceAudit", "address", address, "change", change, "result", result, "memo", memo)
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

func (k Keeper) LogDebug(msg string, subSystem types.SubSystem, keyvals ...interface{}) {
	k.Logger().Debug(msg, append(keyvals, "subsystem", subSystem.String())...)
}
