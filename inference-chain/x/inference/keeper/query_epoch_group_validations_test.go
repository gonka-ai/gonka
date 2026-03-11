package keeper_test

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/nullify"
	"github.com/productscience/inference/x/inference/types"
)

// Prevent strconv unused error
var _ = strconv.IntSize

func TestEpochGroupValidationsQuerySingle(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	msgs := createNEpochGroupValidations(keeper, ctx, 2)
	tests := []struct {
		desc     string
		request  *types.QueryGetEpochGroupValidationsRequest
		response *types.QueryGetEpochGroupValidationsResponse
		err      error
	}{
		{
			desc: "First",
			request: &types.QueryGetEpochGroupValidationsRequest{
				Participant: msgs[0].Participant,
				EpochIndex:  msgs[0].EpochIndex,
			},
			response: &types.QueryGetEpochGroupValidationsResponse{EpochGroupValidations: msgs[0]},
		},
		{
			desc: "Second",
			request: &types.QueryGetEpochGroupValidationsRequest{
				Participant: msgs[1].Participant,
				EpochIndex:  msgs[1].EpochIndex,
			},
			response: &types.QueryGetEpochGroupValidationsResponse{EpochGroupValidations: msgs[1]},
		},
		{
			desc: "KeyNotFound",
			request: &types.QueryGetEpochGroupValidationsRequest{
				Participant: strconv.Itoa(100000),
				EpochIndex:  100000,
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
			response, err := keeper.EpochGroupValidations(ctx, tc.request)
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

func TestEpochGroupValidationsQueryAllDisabled(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	createNEpochGroupValidations(keeper, ctx, 5)

	_, err := keeper.EpochGroupValidationsAll(ctx, &types.QueryAllEpochGroupValidationsRequest{})
	require.ErrorIs(t, err, status.Error(codes.Unimplemented, "EpochGroupValidationsAll is disabled; use EpochGroupValidations by participant and epoch"))

	_, err = keeper.EpochGroupValidationsAll(ctx, nil)
	require.ErrorIs(t, err, status.Error(codes.InvalidArgument, "invalid request"))
}
