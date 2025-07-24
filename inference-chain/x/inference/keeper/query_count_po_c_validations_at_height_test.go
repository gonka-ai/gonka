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

// createNPoCValidation creates n PoCValidation objects at the specified block height
func createNPoCValidation(keeper keeper.Keeper, ctx context.Context, n int, blockHeight int64) {
	for i := 0; i < n; i++ {
		participantAddress := "address" + strconv.Itoa(i)
		validatorAddress := "validator" + strconv.Itoa(i)
		
		pocValidation := types.PoCValidation{
			ParticipantAddress:          participantAddress,
			ValidatorParticipantAddress: validatorAddress,
			PocStageStartBlockHeight:    blockHeight,
			ValidatedAtBlockHeight:      blockHeight + 1,
			Nonces:                      []int64{int64(i), int64(i + 1)},
			Dist:                        []float64{0.5, 0.5},
			ReceivedDist:                []float64{0.5, 0.5},
			RTarget:                     0.1,
			FraudThreshold:              0.2,
			NInvalid:                    0,
			ProbabilityHonest:           0.99,
			FraudDetected:               false,
		}
		
		keeper.SetPoCValidation(ctx, pocValidation)
	}
}

func TestCountPoCValidationsAtHeightQuery(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	wctx := sdk.WrapSDKContext(ctx)
	
	// Create PoCValidations at different heights
	createNPoCValidation(keeper, wctx, 4, 100)
	createNPoCValidation(keeper, wctx, 6, 200)
	
	tests := []struct {
		desc     string
		request  *types.QueryCountPoCValidationsAtHeightRequest
		response *types.QueryCountPoCValidationsAtHeightResponse
		err      error
	}{
		{
			desc: "Height 100",
			request: &types.QueryCountPoCValidationsAtHeightRequest{
				BlockHeight: 100,
			},
			response: &types.QueryCountPoCValidationsAtHeightResponse{
				Count: 4,
			},
		},
		{
			desc: "Height 200",
			request: &types.QueryCountPoCValidationsAtHeightRequest{
				BlockHeight: 200,
			},
			response: &types.QueryCountPoCValidationsAtHeightResponse{
				Count: 6,
			},
		},
		{
			desc: "Height with no validations",
			request: &types.QueryCountPoCValidationsAtHeightRequest{
				BlockHeight: 300,
			},
			response: &types.QueryCountPoCValidationsAtHeightResponse{
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
			response, err := keeper.CountPoCValidationsAtHeight(wctx, tc.request)
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.response, response)
			}
		})
	}
}