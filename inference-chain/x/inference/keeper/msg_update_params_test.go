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

func TestMsgUpdateParamsApprovedVersionsAreAppendOnly(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	current := types.DefaultParams()
	current.DevshardEscrowParams.ApprovedVersions = []*types.DevshardApprovedVersion{
		approvedVersion("v4", "a"),
		approvedVersion("v5", "b"),
	}
	require.NoError(t, k.SetParams(ctx, current))
	wctx := sdk.UnwrapSDKContext(ctx)

	t.Run("remove", func(t *testing.T) {
		proposed := types.DefaultParams()
		proposed.DevshardEscrowParams.ApprovedVersions = []*types.DevshardApprovedVersion{
			approvedVersion("v5", "b"),
		}
		_, err := ms.UpdateParams(wctx, &types.MsgUpdateParams{
			Authority: k.GetAuthority(),
			Params:    proposed,
		})
		require.ErrorContains(t, err, `approved devshard version "v4" cannot be removed`)
	})

	t.Run("update binary and add", func(t *testing.T) {
		proposed := types.DefaultParams()
		proposed.DevshardEscrowParams.ApprovedVersions = []*types.DevshardApprovedVersion{
			approvedVersion("v5", "c"),
			approvedVersion("v4", "d"),
			approvedVersion("v6", "e"),
		}
		_, err := ms.UpdateParams(wctx, &types.MsgUpdateParams{
			Authority: k.GetAuthority(),
			Params:    proposed,
		})
		require.NoError(t, err)
	})
}

func approvedVersion(name, binarySuffix string) *types.DevshardApprovedVersion {
	return &types.DevshardApprovedVersion{
		Name:   name,
		Binary: "https://example.invalid/devshard-" + binarySuffix + ".zip",
		Sha256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
}
