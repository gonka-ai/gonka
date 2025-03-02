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

func TestInferenceTimeoutQuerySingle(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	msgs := createNInferenceTimeout(keeper, ctx, 2)
	tests := []struct {
		desc     string
		request  *types.QueryGetInferenceTimeoutRequest
		response *types.QueryGetInferenceTimeoutResponse
		err      error
	}{
		{
			desc: "First",
			request: &types.QueryGetInferenceTimeoutRequest{
				ExpirationHeight: msgs[0].ExpirationHeight,
				InferenceId:      msgs[0].InferenceId,
			},
			response: &types.QueryGetInferenceTimeoutResponse{InferenceTimeout: msgs[0]},
		},
		{
			desc: "Second",
			request: &types.QueryGetInferenceTimeoutRequest{
				ExpirationHeight: msgs[1].ExpirationHeight,
				InferenceId:      msgs[1].InferenceId,
			},
			response: &types.QueryGetInferenceTimeoutResponse{InferenceTimeout: msgs[1]},
		},
		{
			desc: "KeyNotFound",
			request: &types.QueryGetInferenceTimeoutRequest{
				ExpirationHeight: 100000,
				InferenceId:      strconv.Itoa(100000),
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
			response, err := keeper.InferenceTimeout(ctx, tc.request)
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

func TestInferenceTimeoutQueryPaginated(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	msgs := createNInferenceTimeout(keeper, ctx, 5)

	request := func(next []byte, offset, limit uint64, total bool) *types.QueryAllInferenceTimeoutRequest {
		return &types.QueryAllInferenceTimeoutRequest{
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
			resp, err := keeper.InferenceTimeoutAll(ctx, request(nil, uint64(i), uint64(step), false))
			require.NoError(t, err)
			require.LessOrEqual(t, len(resp.InferenceTimeout), step)
			require.Subset(t,
				nullify.Fill(msgs),
				nullify.Fill(resp.InferenceTimeout),
			)
		}
	})
	t.Run("ByKey", func(t *testing.T) {
		step := 2
		var next []byte
		for i := 0; i < len(msgs); i += step {
			resp, err := keeper.InferenceTimeoutAll(ctx, request(next, 0, uint64(step), false))
			require.NoError(t, err)
			require.LessOrEqual(t, len(resp.InferenceTimeout), step)
			require.Subset(t,
				nullify.Fill(msgs),
				nullify.Fill(resp.InferenceTimeout),
			)
			next = resp.Pagination.NextKey
		}
	})
	t.Run("Total", func(t *testing.T) {
		resp, err := keeper.InferenceTimeoutAll(ctx, request(nil, 0, 0, true))
		require.NoError(t, err)
		require.Equal(t, len(msgs), int(resp.Pagination.Total))
		require.ElementsMatch(t,
			nullify.Fill(msgs),
			nullify.Fill(resp.InferenceTimeout),
		)
	})
	t.Run("InvalidRequest", func(t *testing.T) {
		_, err := keeper.InferenceTimeoutAll(ctx, nil)
		require.ErrorIs(t, err, status.Error(codes.InvalidArgument, "invalid request"))
	})
}
