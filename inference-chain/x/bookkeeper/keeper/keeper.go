package keeper

import (
	"context"
	"cosmossdk.io/core/store"
	"cosmossdk.io/log"
	"fmt"
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
	}
)

func NewKeeper(
	cdc codec.BinaryCodec,
	storeService store.KVStoreService,
	logger log.Logger,
	authority string,

	bankKeeper types.BankKeeper,
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

func (k Keeper) SendCoinsFromModuleToAccount(ctx context.Context, senderModule string, recipientAddr sdk.AccAddress, amt sdk.Coins, memo string) error {
	err := k.bankKeeper.SendCoinsFromModuleToAccount(ctx, senderModule, recipientAddr, amt)
	if err != nil {
		return err
	}
	for _, coin := range amt {
		k.logTransaction(ctx, recipientAddr.String(), senderModule, coin, memo)
	}
	return nil
}

func (k Keeper) SendCoinsFromModuleToModule(ctx context.Context, senderModule, recipientModule string, amt sdk.Coins, memo string) error {
	err := k.bankKeeper.SendCoinsFromModuleToModule(ctx, senderModule, recipientModule, amt)
	if err != nil {
		return err
	}
	for _, coin := range amt {
		k.logTransaction(ctx, recipientModule, senderModule, coin, memo)
	}
	return nil
}
func (k Keeper) SendCoinsFromAccountToModule(ctx context.Context, senderAddr sdk.AccAddress, recipientModule string, amt sdk.Coins, memo string) error {
	err := k.bankKeeper.SendCoinsFromAccountToModule(ctx, senderAddr, recipientModule, amt)
	if err != nil {
		return err
	}
	for _, coin := range amt {
		k.logTransaction(ctx, recipientModule, senderAddr.String(), coin, memo)
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
		k.logTransaction(ctx, moduleName, "supply", coin, memo)
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
		k.logTransaction(ctx, "supply", moduleName, coin, memo)
	}
	return nil
}

func (k Keeper) LogSubAccountTransaction(ctx context.Context, recipient string, sender string, subAccount string, amt sdk.Coin, memo string) {
	k.logTransaction(ctx, recipient+"_"+subAccount, sender+"_"+subAccount, amt, memo)
}

func (k Keeper) logTransaction(ctx context.Context, to string, from string, coin sdk.Coin, memo string) {
	params := k.GetParams(ctx)

	amount := coin.Amount.Int64()
	if params.DoubleEntry {
		k.Logger().Info("TransactionAudit", "type", "debit", "account", to, "counteraccount", from, "amount", amount, "denom", coin.Denom, "memo", memo, "signedAmount", amount)
		k.Logger().Info("TransactionAudit", "type", "credit", "account", from, "counteraccount", to, "amount", amount, "denom", coin.Denom, "memo", memo, "signedAmount", -amount)
	}
	if params.SimpleEntry {
		amountString := fmt.Sprintf("%d", amount)
		k.Logger().Info(fmt.Sprintf("TransactionEntry to=%-64s from=%-64s amount=%20s denom=%10s memo=%s", to, from, amountString, coin.Denom, memo))
	}
}
