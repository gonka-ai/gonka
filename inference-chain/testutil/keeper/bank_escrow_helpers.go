package keeper

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"go.uber.org/mock/gomock"
)

func (escrow *MockBookkeepingBankKeeper) ExpectAny(context sdk.Context) {
	escrow.EXPECT().SendCoinsFromAccountToModule(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
	escrow.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
	escrow.EXPECT().SendCoinsFromModuleToModule(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
	escrow.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()

}

func coinsOf(amount uint64) sdk.Coins {
	return sdk.Coins{
		sdk.NewInt64Coin(
			"ngonka",
			int64(amount)),
	}
}

func (escrow *MockBookkeepingBankKeeper) ExpectPay(context sdk.Context, who string, amount uint64) *gomock.Call {
	whoAddr, err := sdk.AccAddressFromBech32(who)
	if err != nil {
		panic(err)
	}
	return escrow.EXPECT().SendCoinsFromAccountToModule(context, whoAddr, types.ModuleName, coinsOf(amount), gomock.Any())
}
