package types

import sdk "github.com/cosmos/cosmos-sdk/types"

const (
	BaseCoin   = "ngonka"
	NanoCoin   = "ngonka"
	NativeCoin = "gonka"
	MilliCoin  = "mgonka"
	MicroCoin  = "ugonka"
)

// NOTE: In ALL cases, if we represent coins as an int, they should be in BaseCoin units
func GetCoins(coins int64) sdk.Coins {
	return sdk.NewCoins(GetCoin(coins))
}
func GetCoin(coin int64) sdk.Coin {
	return sdk.NewInt64Coin(BaseCoin, coin)
}
