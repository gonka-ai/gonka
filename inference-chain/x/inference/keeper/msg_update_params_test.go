package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/types"
)

func TestMsgUpdateParams(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	params := types.DefaultParams()
	require.NoError(t, k.SetParams(ctx, params))
	wctx := sdk.UnwrapSDKContext(ctx)

	// default params
	testCases := []struct {
		name      string
		input     *types.MsgUpdateParams
		expErr    bool
		expErrMsg string
	}{
		{
			name: "invalid authority",
			input: &types.MsgUpdateParams{
				Authority: testutil.Creator,
				Params:    params,
			},
			expErr:    true,
			expErrMsg: "expected gov account as only signer for proposal message",
		},
		{
			name: "invalid params - empty params",
			input: &types.MsgUpdateParams{
				Authority: k.GetAuthority(),
				Params:    types.Params{},
			},
			expErr:    true,
			expErrMsg: "cannot be nil",
		},
		{
			name: "all good",
			input: &types.MsgUpdateParams{
				Authority: k.GetAuthority(),
				Params:    params,
			},
			expErr: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ms.UpdateParams(wctx, tc.input)

			if tc.expErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.expErrMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestMsgUpdateParamsRequiresUsablePocRecipe(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	original := types.DefaultParams()
	require.NoError(t, k.SetParams(ctx, original))

	legacy := types.DefaultParams()
	model := legacy.PocParams.Models[0]
	prefill, _ := model.SchemeParams(types.PocScheme_POC_SCHEME_PREFILL)
	model.SeqLen = prefill.SeqLen
	model.StatTest = prefill.StatTest
	model.DynamicCoefficient = prefill.DynamicCoefficient
	model.Schemes = nil
	require.NoError(t, legacy.Validate(), "legacy migration format remains readable")
	_, err := ms.UpdateParams(ctx, &types.MsgUpdateParams{Authority: k.GetAuthority(), Params: legacy})
	require.ErrorContains(t, err, "DECODE scheme block")
	stored, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, original, stored, "rejected update must not change live params")

	for _, scheme := range []types.PocScheme{types.PocScheme_POC_SCHEME_PREFILL, types.PocScheme_POC_SCHEME_DECODE} {
		params := types.DefaultParams()
		params.PocParams.PocScheme = scheme
		_, err := ms.UpdateParams(ctx, &types.MsgUpdateParams{Authority: k.GetAuthority(), Params: params})
		require.NoError(t, err)
		_, err = k.FreezePocStageRecipe(ctx, 100+int64(scheme))
		require.NoError(t, err, "accepted params must support the next regular PoC")
	}
}
