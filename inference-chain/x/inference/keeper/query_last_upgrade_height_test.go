package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/nullify"
	"github.com/productscience/inference/x/inference/types"
)

func TestLastUpgradeHeightQuery(t *testing.T) {
	t.Parallel()

	keeper, ctx := keepertest.InferenceKeeper(t)

	require.NoError(t, keeper.SetLastUpgradeHeight(ctx, 1234))

	tests := []struct {
		desc     string
		request  *types.QueryGetLastUpgradeHeightRequest
		response *types.QueryGetLastUpgradeHeightResponse
		err      error
	}{
		{
			desc:    "Found",
			request: &types.QueryGetLastUpgradeHeightRequest{},
			response: &types.QueryGetLastUpgradeHeightResponse{
				LastUpgradeHeight: 1234,
				Found:             true,
			},
		},
		{
			desc: "InvalidRequest",
			err:  status.Error(codes.InvalidArgument, "invalid request"),
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()

			response, err := keeper.LastUpgradeHeight(ctx, tc.request)
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
				return
			}

			require.NoError(t, err)
			require.Equal(t, nullify.Fill(tc.response), nullify.Fill(response))
		})
	}
}

func TestLastUpgradeHeightQueryNotFound(t *testing.T) {
	t.Parallel()

	keeper, ctx := keepertest.InferenceKeeper(t)

	response, err := keeper.LastUpgradeHeight(ctx, &types.QueryGetLastUpgradeHeightRequest{})
	require.NoError(t, err)
	require.False(t, response.Found)
	require.Zero(t, response.LastUpgradeHeight)
}
