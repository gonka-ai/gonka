package keeper

import (
	"context"
	"fmt"
	"strings"

	"cosmossdk.io/core/store"
	"cosmossdk.io/log"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/bookkeeper/types"
)

type (
	Keeper struct {
		cdc          codec.BinaryCodec
		storeService store.KVStoreService
		logger       log.Logger

		// the address capable of executing a MsgUpdateParams message. Typically, this
		// should be the x/gov module account.
		authority string

		bankKeeper types.BankKeeper
		logConfig  LogConfig
	}
)

type LogConfig struct {
	DoubleEntry bool   `json:"double_entry"`
	SimpleEntry bool   `json:"simple_entry"`
	LogLevel    string `json:"log_level"`
}

func NewKeeper(
	cdc codec.BinaryCodec,
	storeService store.KVStoreService,
	logger log.Logger,
	authority string,

	bankKeeper types.BankKeeper,
	logConfig LogConfig,
) Keeper {
	if _, err := sdk.AccAddressFromBech32(authority); err != nil {
		panic(fmt.Sprintf("invalid authority address: %s", authority))
	}

	return Keeper{
		cdc:          cdc,
		storeService: storeService,
		authority:    authority,
		logger:       logger,

		bankKeeper: bankKeeper,
		logConfig:  logConfig,
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

func (k Keeper) SendCoins(ctx context.Context, fromAddr, toAddr sdk.AccAddress, amt sdk.Coins, memo string) error {
	err := k.bankKeeper.SendCoins(ctx, fromAddr, toAddr, amt)
	if err != nil {
		return err
	}
	for _, coin := range amt {
		k.logTransaction(ctx, toAddr.String(), fromAddr.String(), coin, memo, "")
	}
	return nil
}

func (k Keeper) SendCoinsFromModuleToAccount(ctx context.Context, senderModule string, recipientAddr sdk.AccAddress, amt sdk.Coins, memo string) error {
	err := k.bankKeeper.SendCoinsFromModuleToAccount(ctx, senderModule, recipientAddr, amt)
	if err != nil {
		return err
	}
	for _, coin := range amt {
		k.logTransaction(ctx, recipientAddr.String(), senderModule, coin, memo, "")
	}
	return nil
}

func (k Keeper) SendCoinsFromModuleToModule(ctx context.Context, senderModule, recipientModule string, amt sdk.Coins, memo string) error {
	err := k.bankKeeper.SendCoinsFromModuleToModule(ctx, senderModule, recipientModule, amt)
	if err != nil {
		return err
	}
	for _, coin := range amt {
		k.logTransaction(ctx, recipientModule, senderModule, coin, memo, "")
	}
	return nil
}
func (k Keeper) SendCoinsFromAccountToModule(ctx context.Context, senderAddr sdk.AccAddress, recipientModule string, amt sdk.Coins, memo string) error {
	err := k.bankKeeper.SendCoinsFromAccountToModule(ctx, senderAddr, recipientModule, amt)
	if err != nil {
		return err
	}
	for _, coin := range amt {
		k.logTransaction(ctx, recipientModule, senderAddr.String(), coin, memo, "")
	}
	return nil
}

func (k Keeper) MintCoins(ctx context.Context, moduleName string, amt sdk.Coins, memo string) error {
	if amt.IsZero() {
		return nil
	}
	err := k.bankKeeper.MintCoins(ctx, moduleName, amt)
	if err != nil {
		return err
	}
	for _, coin := range amt {
		k.logTransaction(ctx, moduleName, "supply", coin, memo, "")
	}
	return nil
}

func (k Keeper) BurnCoins(ctx context.Context, moduleName string, amt sdk.Coins, memo string) error {
	if amt.IsZero() {
		k.Logger().Info("No coins to burn")
		return nil
	}
	err := k.bankKeeper.BurnCoins(ctx, moduleName, amt)
	if err != nil {
		return err
	}
	for _, coin := range amt {
		k.logTransaction(ctx, "supply", moduleName, coin, memo, "")
	}
	return nil
}

func (k Keeper) LogSubAccountTransaction(ctx context.Context, recipient string, sender string, subAccount string, amt sdk.Coin, memo string) {
	k.logTransaction(ctx, recipient+"_"+subAccount, sender+"_"+subAccount, amt, memo, subAccount)
}

func (k Keeper) logTransaction(ctx context.Context, to string, from string, coin sdk.Coin, memo string, subAccount string) {
	if coin.Amount.IsZero() {
		return
	}
	height := sdk.UnwrapSDKContext(ctx).BlockHeight()
	logFunc := k.getLogFunction(k.logConfig.LogLevel)
	amount := coin.Amount.Int64()
	if k.logConfig.DoubleEntry {
		logFunc("TransactionAudit", "type", "debit", "account", to, "counteraccount", from, "amount", amount, "denom", coin.Denom, "memo", memo, "signedAmount", amount, "height", height)
		logFunc("TransactionAudit", "type", "credit", "account", from, "counteraccount", to, "amount", amount, "denom", coin.Denom, "memo", memo, "signedAmount", -amount, "height", height)
	}
	if k.logConfig.SimpleEntry {
		amountString := fmt.Sprintf("%d", amount)
		heightString := fmt.Sprintf("%d", height)
		if subAccount != "" {
			// Extra space here to ensure alignment in logs
			logFunc(fmt.Sprintf("SubAccountEntry  to=%s from=%s amount=%20s %-10s height=%8s memo=%s subaccount=%s", fixedSize(to, 64), fixedSize(from, 64), amountString, coin.Denom, heightString, memo, subAccount))
		} else {
			logFunc(fmt.Sprintf("TransactionEntry to=%s from=%s amount=%20s %-10s height=%8s memo=%s", fixedSize(to, 64), fixedSize(from, 64), amountString, coin.Denom, heightString, memo))
		}
	}
}

func (k Keeper) getLogFunction(level string) func(msg string, keyvals ...interface{}) {
	switch strings.ToLower(level) {
	case "info":
		return k.Logger().Info
	case "debug":
		return k.Logger().Debug
	case "error":
		return k.Logger().Error
	case "warn":
		return k.Logger().Warn
	default:
		return k.Logger().Info
	}
}

// no easy way to truncate AND pad a string in Sprintf
func fixedSize(to string, size int) string {
	if len(to) > size {
		return to[:size]
	} else {
		return to + strings.Repeat(" ", size-len(to))
	}
}
