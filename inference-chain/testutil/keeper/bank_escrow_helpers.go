package keeper

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/golang/mock/gomock"
	"github.com/productscience/inference/x/inference/types"
)

func (escrow *MockBankEscrowKeeper) ExpectAny(context sdk.Context) {
	escrow.EXPECT().SendCoinsFromAccountToModule(context, gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
	escrow.EXPECT().SendCoinsFromModuleToAccount(context, gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
	escrow.EXPECT().SendCoinsFromModuleToModule(context, gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
}

func coinsOf(amount uint64) sdk.Coins {
	return sdk.Coins{
		sdk.NewInt64Coin(
			"icoin",
			int64(amount)),
	}
}

func (escrow *MockBankEscrowKeeper) ExpectPay(context sdk.Context, who string, amount uint64) *gomock.Call {
	whoAddr, err := sdk.AccAddressFromBech32(who)
	if err != nil {
		panic(err)
	}
	return escrow.EXPECT().SendCoinsFromAccountToModule(context, whoAddr, types.ModuleName, coinsOf(amount))
}
