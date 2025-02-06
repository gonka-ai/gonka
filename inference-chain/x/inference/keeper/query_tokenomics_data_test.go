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

func TestTokenomicsDataQuery(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	item := createTestTokenomicsData(keeper, ctx)
	tests := []struct {
		desc     string
		request  *types.QueryGetTokenomicsDataRequest
		response *types.QueryGetTokenomicsDataResponse
		err      error
	}{
		{
			desc:     "First",
			request:  &types.QueryGetTokenomicsDataRequest{},
			response: &types.QueryGetTokenomicsDataResponse{TokenomicsData: item},
		},
		{
			desc: "InvalidRequest",
			err:  status.Error(codes.InvalidArgument, "invalid request"),
		},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			response, err := keeper.TokenomicsData(ctx, tc.request)
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
			} else {
				require.NoError(t, err)
				require.Equal(t,
					nullify.Fill(tc.response),
					nullify.Fill(response),
				)
			}
		})
	}
}
