package types

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// AccountKeeper defines the expected interface for the Account module.
type AccountKeeper interface {
	GetAccount(context.Context, sdk.AccAddress) sdk.AccountI
	SetAccount(context.Context, sdk.AccountI)
	NewAccountWithAddress(context.Context, sdk.AccAddress) sdk.AccountI
}

// BankKeeper defines the expected interface for the Bank module (for read-only operations).
type BankKeeper interface {
	SpendableCoins(context.Context, sdk.AccAddress) sdk.Coins
	GetAllBalances(context.Context, sdk.AccAddress) sdk.Coins
}

// BookkeepingBankKeeper defines the expected interface for logged bank operations.
type BookkeepingBankKeeper interface {
	SendCoins(context.Context, sdk.AccAddress, sdk.AccAddress, sdk.Coins, string) error
	SendCoinsFromAccountToModule(context.Context, sdk.AccAddress, string, sdk.Coins, string) error
	SendCoinsFromModuleToAccount(context.Context, string, sdk.AccAddress, sdk.Coins, string) error
}

// ParamSubspace defines the expected Subspace interface for parameters.
type ParamSubspace interface {
	Get(context.Context, []byte, interface{})
	Set(context.Context, []byte, interface{})
}
