package app

import (
	"testing"

	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/stretchr/testify/require"

	inferencetypes "github.com/productscience/inference/x/inference/types"
)

// Exercise the bypass and final fee checker together: withholding the bypass
// must actually charge the duty's enabled group, including in DeliverTx.
func TestGonkaFeeChecker_DutyAuthorization(t *testing.T) {
	for _, checkTx := range []bool{false, true} {
		mode := "DeliverTx"
		if checkTx {
			mode = "CheckTx"
		}
		for _, wrapped := range []bool{false, true} {
			shape := "direct"
			if wrapped {
				shape = "MsgExec"
			}
			for _, tc := range []struct {
				name       string
				authorized bool
				mixed      bool
				bankFirst  bool
				disabled   bool
				fee        sdk.Coins
				wantError  bool
			}{
				{name: "inactive claim pays", wantError: true},
				{name: "inactive claim pays exact minimum", fee: sdk.NewCoins(sdk.NewInt64Coin("ngonka", 1_000_000))},
				{name: "inactive claim rejects wrong denomination", fee: sdk.NewCoins(sdk.NewInt64Coin("other", 1_000_000)), wantError: true},
				{name: "active claim keeps waiver", authorized: true},
				{name: "authorized duty does not raise mixed price", authorized: true, mixed: true, fee: sdk.NewCoins(sdk.NewInt64Coin("ngonka", 200_000))},
				{name: "authorized duty after paying message", authorized: true, mixed: true, bankFirst: true, fee: sdk.NewCoins(sdk.NewInt64Coin("ngonka", 200_000))},
				{name: "unauthorized duty raises mixed price", mixed: true, fee: sdk.NewCoins(sdk.NewInt64Coin("ngonka", 200_000)), wantError: true},
				{name: "unauthorized duty after paying message", mixed: true, bankFirst: true, fee: sdk.NewCoins(sdk.NewInt64Coin("ngonka", 200_000)), wantError: true},
				{name: "disabled duty group stays free", disabled: true},
			} {
				t.Run(mode+"/"+shape+"/"+tc.name, func(t *testing.T) {
					actor := dutyActor()
					newKeeper := registeredInactiveDutyKeeper
					if tc.authorized {
						newKeeper = registeredDutyKeeper
					}
					ik, ctx := newKeeper(t, actor)
					ir := ik.Codec().(codec.ProtoCodecMarshaler).InterfaceRegistry()
					authztypes.RegisterInterfaces(ir)
					banktypes.RegisterInterfaces(ir)
					params, err := ik.GetParams(ctx)
					require.NoError(t, err)
					params.FeeParams = inferencetypes.DefaultFeeParams()
					params.FeeParams.EnabledFeeGroups = []string{inferencetypes.FeeGroupEpoch, inferencetypes.FeeGroupCosmos}
					params.FeeParams.GroupByName(inferencetypes.FeeGroupEpoch).MinGasPrice = 10
					params.FeeParams.GroupByName(inferencetypes.FeeGroupCosmos).MinGasPrice = 2
					if tc.disabled {
						params.FeeParams.EnabledFeeGroups = []string{inferencetypes.FeeGroupCosmos}
					}
					require.NoError(t, ik.SetParams(ctx, params))
					ctx = ctx.WithIsCheckTx(checkTx).WithGasMeter(storetypes.NewGasMeter(100_000))

					claim := &inferencetypes.MsgClaimRewards{Creator: actor.String(), EpochIndex: dutyTestEpoch - 1, Seed: 1}
					msgs := []sdk.Msg{claim}
					if tc.mixed {
						bank := &banktypes.MsgSend{FromAddress: actor.String(), ToAddress: actor.String(), Amount: sdk.NewCoins(sdk.NewCoin("ngonka", math.OneInt()))}
						msgs = append(msgs, bank)
						if tc.bankFirst {
							msgs[0], msgs[1] = msgs[1], msgs[0]
						}
					}
					if wrapped {
						// Fee authorization follows the inner actor, not the warm key.
						// The separate signed-tx tests cover the required authz grant.
						exec := authztypes.NewMsgExec(sdk.AccAddress("warm-key-000000000000"), msgs)
						msgs = []sdk.Msg{&exec}
					}
					tx := testFeeTx{msgs: msgs, fee: tc.fee, gas: 100_000}
					decorator := NetworkDutyFeeBypassDecorator{InferenceKeeper: ik, GasCap: 1_000_000}
					_, err = decorator.AnteHandle(ctx, tx, false, func(ctx sdk.Context, tx sdk.Tx, _ bool) (sdk.Context, error) {
						fees, _, feeErr := GonkaFeeChecker(ik, nil)(ctx, tx)
						if !tc.wantError && feeErr == nil {
							require.Equal(t, tc.fee.String(), fees.String())
						}
						return ctx, feeErr
					})
					if tc.wantError {
						require.ErrorIs(t, err, sdkerrors.ErrInsufficientFee)
					} else {
						require.NoError(t, err)
					}
				})
			}
		}
	}
}
