package keeper_test

import (
	"context"
	"strconv"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// Prevent strconv unused error
var _ = strconv.IntSize

// createNPoCBatch creates n PoCBatch objects at the specified block height
func createNPoCBatch(keeper keeper.Keeper, ctx context.Context, n int, blockHeight int64) {
	for i := 0; i < n; i++ {
		participantAddress := "address" + strconv.Itoa(i)
		batchId := "batch" + strconv.Itoa(i)
		
		pocBatch := types.PoCBatch{
			ParticipantAddress:       participantAddress,
			PocStageStartBlockHeight: blockHeight,
			ReceivedAtBlockHeight:    blockHeight + 1,
			Nonces:                   []int64{int64(i), int64(i + 1)},
			Dist:                     []float64{0.5, 0.5},
			BatchId:                  batchId,
		}
		
		keeper.SetPocBatch(ctx, pocBatch)
	}
}

func TestCountPoCBatchesAtHeightQuery(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	wctx := sdk.WrapSDKContext(ctx)
	
	// Create PoCBatches at different heights
	createNPoCBatch(keeper, wctx, 3, 100)
	createNPoCBatch(keeper, wctx, 5, 200)
	
	tests := []struct {
		desc     string
		request  *types.QueryCountPoCbatchesAtHeightRequest
		response *types.QueryCountPoCbatchesAtHeightResponse
		err      error
	}{
		{
			desc: "Height 100",
			request: &types.QueryCountPoCbatchesAtHeightRequest{
				BlockHeight: 100,
			},
			response: &types.QueryCountPoCbatchesAtHeightResponse{
				Count: 3,
			},
		},
		{
			desc: "Height 200",
			request: &types.QueryCountPoCbatchesAtHeightRequest{
				BlockHeight: 200,
			},
			response: &types.QueryCountPoCbatchesAtHeightResponse{
				Count: 5,
			},
		},
		{
			desc: "Height with no batches",
			request: &types.QueryCountPoCbatchesAtHeightRequest{
				BlockHeight: 300,
			},
			response: &types.QueryCountPoCbatchesAtHeightResponse{
				Count: 0,
			},
		},
		{
			desc: "Invalid request",
			err:  status.Error(codes.InvalidArgument, "invalid request"),
		},
	}
	
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			response, err := keeper.CountPoCbatchesAtHeight(wctx, tc.request)
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.response, response)
			}
		})
	}
}