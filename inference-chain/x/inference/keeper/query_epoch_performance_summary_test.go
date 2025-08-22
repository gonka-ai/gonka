package keeper_test

import (
	"strconv"
	"testing"

	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	testutil "github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/nullify"
	"github.com/productscience/inference/x/inference/types"
)

// Prevent strconv unused error
var _ = strconv.IntSize

func TestEpochPerformanceSummaryQuerySingle(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	msgs := createNEpochPerformanceSummary(keeper, ctx, 2)
	tests := []struct {
		desc     string
		request  *types.QueryGetEpochPerformanceSummaryRequest
		response *types.QueryGetEpochPerformanceSummaryResponse
		err      error
	}{
		{
			desc: "First",
			request: &types.QueryGetEpochPerformanceSummaryRequest{
				EpochIndex:    msgs[0].EpochIndex,
				ParticipantId: msgs[0].ParticipantId,
			},
			response: &types.QueryGetEpochPerformanceSummaryResponse{EpochPerformanceSummary: msgs[0]},
		},
		{
			desc: "Second",
			request: &types.QueryGetEpochPerformanceSummaryRequest{
				EpochIndex:    msgs[1].EpochIndex,
				ParticipantId: msgs[1].ParticipantId,
			},
			response: &types.QueryGetEpochPerformanceSummaryResponse{EpochPerformanceSummary: msgs[1]},
		},
		{
			desc: "KeyNotFound",
			request: &types.QueryGetEpochPerformanceSummaryRequest{
				EpochIndex:    100000,
				ParticipantId: testutil.Bech32Addr(100000),
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
			response, err := keeper.EpochPerformanceSummary(ctx, tc.request)
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

func TestEpochPerformanceSummaryQueryPaginated(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	msgs := createNEpochPerformanceSummary(keeper, ctx, 5)

	request := func(next []byte, offset, limit uint64, total bool) *types.QueryAllEpochPerformanceSummaryRequest {
		return &types.QueryAllEpochPerformanceSummaryRequest{
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
			resp, err := keeper.EpochPerformanceSummaryAll(ctx, request(nil, uint64(i), uint64(step), false))
			require.NoError(t, err)
			require.LessOrEqual(t, len(resp.EpochPerformanceSummary), step)
			require.Subset(t,
				nullify.Fill(msgs),
				nullify.Fill(resp.EpochPerformanceSummary),
			)
		}
	})
	t.Run("ByKey", func(t *testing.T) {
		step := 2
		var next []byte
		for i := 0; i < len(msgs); i += step {
			resp, err := keeper.EpochPerformanceSummaryAll(ctx, request(next, 0, uint64(step), false))
			require.NoError(t, err)
			require.LessOrEqual(t, len(resp.EpochPerformanceSummary), step)
			require.Subset(t,
				nullify.Fill(msgs),
				nullify.Fill(resp.EpochPerformanceSummary),
			)
			next = resp.Pagination.NextKey
		}
	})
	t.Run("Total", func(t *testing.T) {
		resp, err := keeper.EpochPerformanceSummaryAll(ctx, request(nil, 0, 0, true))
		require.NoError(t, err)
		require.Equal(t, len(msgs), int(resp.Pagination.Total))
		require.ElementsMatch(t,
			nullify.Fill(msgs),
			nullify.Fill(resp.EpochPerformanceSummary),
		)
	})
	t.Run("InvalidRequest", func(t *testing.T) {
		_, err := keeper.EpochPerformanceSummaryAll(ctx, nil)
		require.ErrorIs(t, err, status.Error(codes.InvalidArgument, "invalid request"))
	})
}
