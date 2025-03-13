package keeper_test

import (
	"strconv"
	"testing"

	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/nullify"
	"github.com/productscience/inference/x/inference/types"
)

// Prevent strconv unused error
var _ = strconv.IntSize

func TestPartialUpgradeQuerySingle(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	msgs := createNPartialUpgrade(keeper, ctx, 2)
	tests := []struct {
		desc     string
		request  *types.QueryGetPartialUpgradeRequest
		response *types.QueryGetPartialUpgradeResponse
		err      error
	}{
		{
			desc: "First",
			request: &types.QueryGetPartialUpgradeRequest{
				Height: msgs[0].Height,
			},
			response: &types.QueryGetPartialUpgradeResponse{PartialUpgrade: msgs[0]},
		},
		{
			desc: "Second",
			request: &types.QueryGetPartialUpgradeRequest{
				Height: msgs[1].Height,
			},
			response: &types.QueryGetPartialUpgradeResponse{PartialUpgrade: msgs[1]},
		},
		{
			desc: "KeyNotFound",
			request: &types.QueryGetPartialUpgradeRequest{
				Height: 100000,
			},
			err: status.Error(codes.NotFound, "not found"),
		},
		{
			desc: "InvalidRequest",
			err:  status.Error(codes.InvalidArgument, "invalid request"),
		},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			response, err := keeper.PartialUpgrade(ctx, tc.request)
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

func TestPartialUpgradeQueryPaginated(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	msgs := createNPartialUpgrade(keeper, ctx, 5)

	request := func(next []byte, offset, limit uint64, total bool) *types.QueryAllPartialUpgradeRequest {
		return &types.QueryAllPartialUpgradeRequest{
			Pagination: &query.PageRequest{
				Key:        next,
				Offset:     offset,
				Limit:      limit,
				CountTotal: total,
			},
		}
	}
	t.Run("ByOffset", func(t *testing.T) {
		step := 2
		for i := 0; i < len(msgs); i += step {
			resp, err := keeper.PartialUpgradeAll(ctx, request(nil, uint64(i), uint64(step), false))
			require.NoError(t, err)
			require.LessOrEqual(t, len(resp.PartialUpgrade), step)
			require.Subset(t,
				nullify.Fill(msgs),
				nullify.Fill(resp.PartialUpgrade),
			)
		}
	})
	t.Run("ByKey", func(t *testing.T) {
		step := 2
		var next []byte
		for i := 0; i < len(msgs); i += step {
			resp, err := keeper.PartialUpgradeAll(ctx, request(next, 0, uint64(step), false))
			require.NoError(t, err)
			require.LessOrEqual(t, len(resp.PartialUpgrade), step)
			require.Subset(t,
				nullify.Fill(msgs),
				nullify.Fill(resp.PartialUpgrade),
			)
			next = resp.Pagination.NextKey
		}
	})
	t.Run("Total", func(t *testing.T) {
		resp, err := keeper.PartialUpgradeAll(ctx, request(nil, 0, 0, true))
		require.NoError(t, err)
		require.Equal(t, len(msgs), int(resp.Pagination.Total))
		require.ElementsMatch(t,
			nullify.Fill(msgs),
			nullify.Fill(resp.PartialUpgrade),
		)
	})
	t.Run("InvalidRequest", func(t *testing.T) {
		_, err := keeper.PartialUpgradeAll(ctx, nil)
		require.ErrorIs(t, err, status.Error(codes.InvalidArgument, "invalid request"))
	})
}
