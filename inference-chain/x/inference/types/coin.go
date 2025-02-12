package types

import sdk "github.com/cosmos/cosmos-sdk/types"

const (
	BaseCoin   = "nicoin"
	NanoCoin   = "nicoin"
	NativeCoin = "icoin"
	MilliCoin  = "micoin"
	MicroCoin  = "uicoin"
)

// NOTE: In ALL cases, if we represent coins as an int, they should be in BaseCoin units
func GetCoins(coins int64) sdk.Coins {
	return sdk.NewCoins(sdk.NewInt64Coin(BaseCoin, coins))
}
