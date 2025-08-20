package keeper_test

import (
	"testing"

	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/nullify"
	"github.com/productscience/inference/testutil/sample"
	"github.com/productscience/inference/x/inference/types"
)

func TestSettleAmountQuerySingle(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	msgs := createNSettleAmount(keeper, ctx, 2)
	tests := []struct {
		desc     string
		request  *types.QueryGetSettleAmountRequest
		response *types.QueryGetSettleAmountResponse
		err      error
	}{
		{
			desc: "First",
			request: &types.QueryGetSettleAmountRequest{
				Participant: msgs[0].Participant,
			},
			response: &types.QueryGetSettleAmountResponse{SettleAmount: msgs[0]},
		},
		{
			desc: "Second",
			request: &types.QueryGetSettleAmountRequest{
				Participant: msgs[1].Participant,
			},
			response: &types.QueryGetSettleAmountResponse{SettleAmount: msgs[1]},
		},
		{
			desc: "KeyNotFound",
			request: &types.QueryGetSettleAmountRequest{
				Participant: sample.AccAddress(),
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
			response, err := keeper.SettleAmount(ctx, tc.request)
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

func TestSettleAmountQueryPaginated(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	msgs := createNSettleAmount(keeper, ctx, 5)

	request := func(next []byte, offset, limit uint64, total bool) *types.QueryAllSettleAmountRequest {
		return &types.QueryAllSettleAmountRequest{
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
			resp, err := keeper.SettleAmountAll(ctx, request(nil, uint64(i), uint64(step), false))
			require.NoError(t, err)
			require.LessOrEqual(t, len(resp.SettleAmount), step)
			require.Subset(t,
				nullify.Fill(msgs),
				nullify.Fill(resp.SettleAmount),
			)
		}
	})
	t.Run("ByKey", func(t *testing.T) {
		step := 2
		var next []byte
		for i := 0; i < len(msgs); i += step {
			resp, err := keeper.SettleAmountAll(ctx, request(next, 0, uint64(step), false))
			require.NoError(t, err)
			require.LessOrEqual(t, len(resp.SettleAmount), step)
			require.Subset(t,
				nullify.Fill(msgs),
				nullify.Fill(resp.SettleAmount),
			)
			next = resp.Pagination.NextKey
		}
	})
	t.Run("Total", func(t *testing.T) {
		resp, err := keeper.SettleAmountAll(ctx, request(nil, 0, 0, true))
		require.NoError(t, err)
		require.Equal(t, len(msgs), int(resp.Pagination.Total))
		require.ElementsMatch(t,
			nullify.Fill(msgs),
			nullify.Fill(resp.SettleAmount),
		)
	})
	t.Run("InvalidRequest", func(t *testing.T) {
		_, err := keeper.SettleAmountAll(ctx, nil)
		require.ErrorIs(t, err, status.Error(codes.InvalidArgument, "invalid request"))
	})
}
