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

func TestTopMinerQuerySingle(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	msgs := createNTopMiner(keeper, ctx, 2)
	tests := []struct {
		desc     string
		request  *types.QueryGetTopMinerRequest
		response *types.QueryGetTopMinerResponse
		err      error
	}{
		{
			desc: "First",
			request: &types.QueryGetTopMinerRequest{
				Address: msgs[0].Address,
			},
			response: &types.QueryGetTopMinerResponse{TopMiner: msgs[0]},
		},
		{
			desc: "Second",
			request: &types.QueryGetTopMinerRequest{
				Address: msgs[1].Address,
			},
			response: &types.QueryGetTopMinerResponse{TopMiner: msgs[1]},
		},
		{
			desc: "KeyNotFound",
			request: &types.QueryGetTopMinerRequest{
				Address: strconv.Itoa(100000),
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
			response, err := keeper.TopMiner(ctx, tc.request)
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

func TestTopMinerQueryPaginated(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	msgs := createNTopMiner(keeper, ctx, 5)

	request := func(next []byte, offset, limit uint64, total bool) *types.QueryAllTopMinerRequest {
		return &types.QueryAllTopMinerRequest{
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
			resp, err := keeper.TopMinerAll(ctx, request(nil, uint64(i), uint64(step), false))
			require.NoError(t, err)
			require.LessOrEqual(t, len(resp.TopMiner), step)
			require.Subset(t,
				nullify.Fill(msgs),
				nullify.Fill(resp.TopMiner),
			)
		}
	})
	t.Run("ByKey", func(t *testing.T) {
		step := 2
		var next []byte
		for i := 0; i < len(msgs); i += step {
			resp, err := keeper.TopMinerAll(ctx, request(next, 0, uint64(step), false))
			require.NoError(t, err)
			require.LessOrEqual(t, len(resp.TopMiner), step)
			require.Subset(t,
				nullify.Fill(msgs),
				nullify.Fill(resp.TopMiner),
			)
			next = resp.Pagination.NextKey
		}
	})
	t.Run("Total", func(t *testing.T) {
		resp, err := keeper.TopMinerAll(ctx, request(nil, 0, 0, true))
		require.NoError(t, err)
		require.Equal(t, len(msgs), int(resp.Pagination.Total))
		require.ElementsMatch(t,
			nullify.Fill(msgs),
			nullify.Fill(resp.TopMiner),
		)
	})
	t.Run("InvalidRequest", func(t *testing.T) {
		_, err := keeper.TopMinerAll(ctx, nil)
		require.ErrorIs(t, err, status.Error(codes.InvalidArgument, "invalid request"))
	})
}
