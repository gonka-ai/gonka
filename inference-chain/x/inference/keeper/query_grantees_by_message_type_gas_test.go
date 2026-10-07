package keeper_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"cosmossdk.io/core/address"
	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/baseapp"
	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdktestutil "github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	authzkeeper "github.com/cosmos/cosmos-sdk/x/authz/keeper"
	authzmodule "github.com/cosmos/cosmos-sdk/x/authz/module"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// stubAuthzAccountKeeper is the minimal account keeper authzkeeper.NewKeeper needs.
type stubAuthzAccountKeeper struct{}

func (stubAuthzAccountKeeper) AddressCodec() address.Codec {
	return addresscodec.NewBech32Codec("gonka")
}
func (stubAuthzAccountKeeper) GetAccount(_ context.Context, a sdk.AccAddress) sdk.AccountI {
	return authtypes.NewBaseAccountWithAddress(a)
}
func (stubAuthzAccountKeeper) NewAccountWithAddress(_ context.Context, a sdk.AccAddress) sdk.AccountI {
	return authtypes.NewBaseAccountWithAddress(a)
}
func (stubAuthzAccountKeeper) SetAccount(context.Context, sdk.AccountI) {}

// TestGranteesByMessageType_FirstPageGasBounded drives the query through a real
// authz keeper (IAVL on an in-memory DB) so store gas reflects the actual
// pagination scan. A PageRequest with Limit 0 makes the SDK set CountTotal,
// which iterates every grant of the granter on the first page; store gas must
// stay flat once the grant set is past the scan cap.
func TestGranteesByMessageType_FirstPageGasBounded(t *testing.T) {
	enc := moduletestutil.MakeTestEncodingConfig(authzmodule.AppModuleBasic{})
	key := storetypes.NewKVStoreKey(authzkeeper.StoreKey)
	tctx := sdktestutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("t_authz"))
	actx := tctx.Ctx.WithBlockTime(time.Unix(1_800_000_000, 0))
	ak := authzkeeper.NewKeeper(runtime.NewKVStoreService(key), enc.Codec, baseapp.NewMsgServiceRouter(), stubAuthzAccountKeeper{})
	granter := sdk.AccAddress([]byte("granter-000000000000"))

	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	ctx = ctx.WithBlockTime(actx.BlockTime())
	var meter storetypes.GasMeter
	mocks.AuthzKeeper.EXPECT().GranterGrants(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ any, req *authztypes.QueryGranterGrantsRequest) (*authztypes.QueryGranterGrantsResponse, error) {
			return ak.GranterGrants(actx.WithGasMeter(meter), req)
		}).AnyTimes()
	mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), gomock.Any()).Return(authtypes.NewBaseAccountWithAddress(granter)).AnyTimes()

	gas := map[int]storetypes.Gas{}
	done := 0
	for _, n := range []int{12000, 60000} {
		for ; done < n; done++ {
			grantee := sdk.AccAddress([]byte(fmt.Sprintf("grantee-%012d", done)))
			require.NoError(t, ak.SaveGrant(actx, grantee, granter, authztypes.NewGenericAuthorization("/cosmos.bank.v1beta1.MsgSend"), nil))
		}
		tctx.CMS.Commit()
		meter = storetypes.NewInfiniteGasMeter()
		_, err := k.GranteesByMessageType(ctx, &types.QueryGranteesByMessageTypeRequest{
			GranterAddress: granter.String(), MessageTypeUrl: "/inference.bls.MsgSubmitDealerPart"})
		require.NoError(t, err)
		gas[n] = meter.GasConsumed()
		t.Logf("grants=%d store_gas=%d", n, gas[n])
	}
	require.Less(t, float64(gas[60000]), 1.2*float64(gas[12000]))
}
