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

func TestEpochGroupDataQuerySingle(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	msgs := createNEpochGroupData(keeper, ctx, 2)
	tests := []struct {
		desc     string
		request  *types.QueryGetEpochGroupDataRequest
		response *types.QueryGetEpochGroupDataResponse
		err      error
	}{
		{
			desc: "First",
			request: &types.QueryGetEpochGroupDataRequest{
				EpochIndex: msgs[0].EpochIndex,
				ModelId:    msgs[0].ModelId,
			},
			response: &types.QueryGetEpochGroupDataResponse{EpochGroupData: msgs[0]},
		},
		{
			desc: "Second",
			request: &types.QueryGetEpochGroupDataRequest{
				EpochIndex: msgs[1].EpochIndex,
				ModelId:    msgs[1].ModelId,
			},
			response: &types.QueryGetEpochGroupDataResponse{EpochGroupData: msgs[1]},
		},
		{
			desc: "KeyNotFound",
			request: &types.QueryGetEpochGroupDataRequest{
				EpochIndex: 100000,
				ModelId:    "",
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
			response, err := keeper.EpochGroupData(ctx, tc.request)
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

func TestEpochGroupDataQueryPaginated(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	msgs := createNEpochGroupData(keeper, ctx, 5)

	request := func(next []byte, offset, limit uint64, total bool) *types.QueryAllEpochGroupDataRequest {
		return &types.QueryAllEpochGroupDataRequest{
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
			resp, err := keeper.EpochGroupDataAll(ctx, request(nil, uint64(i), uint64(step), false))
			require.NoError(t, err)
			require.LessOrEqual(t, len(resp.EpochGroupData), step)
			require.Subset(t,
				nullify.Fill(msgs),
				nullify.Fill(resp.EpochGroupData),
			)
		}
	})
	t.Run("ByKey", func(t *testing.T) {
		step := 2
		var next []byte
		for i := 0; i < len(msgs); i += step {
			resp, err := keeper.EpochGroupDataAll(ctx, request(next, 0, uint64(step), false))
			require.NoError(t, err)
			require.LessOrEqual(t, len(resp.EpochGroupData), step)
			require.Subset(t,
				nullify.Fill(msgs),
				nullify.Fill(resp.EpochGroupData),
			)
			next = resp.Pagination.NextKey
		}
	})
	t.Run("Total", func(t *testing.T) {
		resp, err := keeper.EpochGroupDataAll(ctx, request(nil, 0, 0, true))
		require.NoError(t, err)
		require.Equal(t, len(msgs), int(resp.Pagination.Total))
		require.ElementsMatch(t,
			nullify.Fill(msgs),
			nullify.Fill(resp.EpochGroupData),
		)
	})
	t.Run("InvalidRequest", func(t *testing.T) {
		_, err := keeper.EpochGroupDataAll(ctx, nil)
		require.ErrorIs(t, err, status.Error(codes.InvalidArgument, "invalid request"))
	})
}
