package app

import (
	"cosmossdk.io/x/feegrant"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/x/auth/ante"
)

// TxFeegrantKeeper is the fee grant keeper the ante chain hands to DeductFeeDecorator.
func (app *App) TxFeegrantKeeper() ante.FeegrantKeeper {
	return wrapFeegrantKeeper(app.FeeGrantKeeper, runtime.NewKVStoreService(app.GetKey(feegrant.StoreKey)), app.appCodec)
}
