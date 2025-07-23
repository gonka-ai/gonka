package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func TestCountPoCBatchesAtHeight(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)

	// Set up test data
	blockHeight1 := int64(100)
	blockHeight2 := int64(200)
	blockHeight3 := int64(300) // No batches at this height

	// Create PoCBatch objects at different block heights
	batch1 := types.PoCBatch{
		PocStageStartBlockHeight: blockHeight1,
		ParticipantAddress:       "participant1",
		BatchId:                  "batch1",
		ReceivedAtBlockHeight:    blockHeight1,
		Nonces:                   []int64{1, 2},
		Dist:                     []float64{1.0, 2.0},
	}

	batch2 := types.PoCBatch{
		PocStageStartBlockHeight: blockHeight1,
		ParticipantAddress:       "participant2",
		BatchId:                  "batch2",
		ReceivedAtBlockHeight:    blockHeight1,
		Nonces:                   []int64{3, 4},
		Dist:                     []float64{3.0, 4.0},
	}

	batch3 := types.PoCBatch{
		PocStageStartBlockHeight: blockHeight2,
		ParticipantAddress:       "participant1",
		BatchId:                  "batch3",
		ReceivedAtBlockHeight:    blockHeight2,
		Nonces:                   []int64{5, 6},
		Dist:                     []float64{5.0, 6.0},
	}

	// Store PoCBatch objects
	keeper.SetPocBatch(ctx, batch1)
	keeper.SetPocBatch(ctx, batch2)
	keeper.SetPocBatch(ctx, batch3)

	tests := []struct {
		desc     string
		request  *types.QueryCountPoCbatchesAtHeightRequest
		response *types.QueryCountPoCbatchesAtHeightResponse
		err      error
	}{
		{
			desc: "BlockHeight1",
			request: &types.QueryCountPoCbatchesAtHeightRequest{
				BlockHeight: blockHeight1,
			},
			response: &types.QueryCountPoCbatchesAtHeightResponse{
				Count: 2,
			},
		},
		{
			desc: "BlockHeight2",
			request: &types.QueryCountPoCbatchesAtHeightRequest{
				BlockHeight: blockHeight2,
			},
			response: &types.QueryCountPoCbatchesAtHeightResponse{
				Count: 1,
			},
		},
		{
			desc: "BlockHeight3",
			request: &types.QueryCountPoCbatchesAtHeightRequest{
				BlockHeight: blockHeight3,
			},
			response: &types.QueryCountPoCbatchesAtHeightResponse{
				Count: 0,
			},
		},
		{
			desc: "InvalidRequest",
			err:  status.Error(codes.InvalidArgument, "invalid request"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			response, err := keeper.CountPoCbatchesAtHeight(ctx, tc.request)
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.response, response)
			}
		})
	}
}
