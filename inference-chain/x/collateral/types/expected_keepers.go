package types

import (
	"context"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// AccountKeeper defines the expected interface for the Account module.
type AccountKeeper interface {
	GetAccount(context.Context, sdk.AccAddress) sdk.AccountI
	// Methods imported from account should be defined here
}

// BankKeeper defines the expected interface for the Bank module.
type BankKeeper interface {
	SpendableCoins(context.Context, sdk.AccAddress) sdk.Coins
	// Methods imported from bank should be defined here
}

// BankEscrowKeeper Methods imported from bank should be defined here
type BookkeepingBankKeeper interface {
	SendCoinsFromModuleToAccount(ctx context.Context, senderModule string, recipientAddr sdk.AccAddress, amt sdk.Coins, memo string) error
	SendCoinsFromModuleToModule(ctx context.Context, senderModule, recipientModule string, amt sdk.Coins, memo string) error
	SendCoinsFromAccountToModule(ctx context.Context, senderAddr sdk.AccAddress, recipientModule string, amt sdk.Coins, memo string) error
	// For logging transactions to tracking accounts, like vesting holds
	LogSubAccountTransaction(ctx context.Context, recipient string, sender string, subAccount string, amt sdk.Coin, memo string)
}

// ParamSubspace defines the expected Subspace interface for parameters.
type ParamSubspace interface {
	Get(context.Context, []byte, interface{})
	Set(context.Context, []byte, interface{})
}

// RequiredCollateralProvider defines the read-only tokenomics data that the collateral
// module consumes from inference when computing slash base for staking-driven slashing.
type RequiredCollateralProvider interface {
	GetRequiredCollateralForSlash(ctx context.Context, participantAddress sdk.AccAddress) math.Int
}
