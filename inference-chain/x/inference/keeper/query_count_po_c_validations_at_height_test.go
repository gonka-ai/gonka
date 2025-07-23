package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func TestCountPoCValidationsAtHeight(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)

	// Set up test data
	blockHeight1 := int64(100)
	blockHeight2 := int64(200)
	blockHeight3 := int64(300) // No validations at this height

	// Create PoCValidation objects at different block heights
	validation1 := types.PoCValidation{
		PocStageStartBlockHeight:    blockHeight1,
		ParticipantAddress:          "participant1",
		ValidatorParticipantAddress: "validator1",
		ValidatedAtBlockHeight:      blockHeight1,
		Nonces:                      []int64{1, 2},
		Dist:                        []float64{1.0, 2.0},
		ReceivedDist:                []float64{1.1, 2.1},
		RTarget:                     0.5,
		FraudThreshold:              0.1,
		NInvalid:                    0,
		ProbabilityHonest:           0.95,
		FraudDetected:               false,
	}

	validation2 := types.PoCValidation{
		PocStageStartBlockHeight:    blockHeight1,
		ParticipantAddress:          "participant2",
		ValidatorParticipantAddress: "validator1",
		ValidatedAtBlockHeight:      blockHeight1,
		Nonces:                      []int64{3, 4},
		Dist:                        []float64{3.0, 4.0},
		ReceivedDist:                []float64{3.1, 4.1},
		RTarget:                     0.5,
		FraudThreshold:              0.1,
		NInvalid:                    0,
		ProbabilityHonest:           0.95,
		FraudDetected:               false,
	}

	validation3 := types.PoCValidation{
		PocStageStartBlockHeight:    blockHeight2,
		ParticipantAddress:          "participant1",
		ValidatorParticipantAddress: "validator2",
		ValidatedAtBlockHeight:      blockHeight2,
		Nonces:                      []int64{5, 6},
		Dist:                        []float64{5.0, 6.0},
		ReceivedDist:                []float64{5.1, 6.1},
		RTarget:                     0.5,
		FraudThreshold:              0.1,
		NInvalid:                    0,
		ProbabilityHonest:           0.95,
		FraudDetected:               false,
	}

	// Store PoCValidation objects
	keeper.SetPoCValidation(ctx, validation1)
	keeper.SetPoCValidation(ctx, validation2)
	keeper.SetPoCValidation(ctx, validation3)

	tests := []struct {
		desc     string
		request  *types.QueryCountPoCvalidationsAtHeightRequest
		response *types.QueryCountPoCvalidationsAtHeightResponse
		err      error
	}{
		{
			desc: "BlockHeight1",
			request: &types.QueryCountPoCvalidationsAtHeightRequest{
				BlockHeight: blockHeight1,
			},
			response: &types.QueryCountPoCvalidationsAtHeightResponse{
				Count: 2,
			},
		},
		{
			desc: "BlockHeight2",
			request: &types.QueryCountPoCvalidationsAtHeightRequest{
				BlockHeight: blockHeight2,
			},
			response: &types.QueryCountPoCvalidationsAtHeightResponse{
				Count: 1,
			},
		},
		{
			desc: "BlockHeight3",
			request: &types.QueryCountPoCvalidationsAtHeightRequest{
				BlockHeight: blockHeight3,
			},
			response: &types.QueryCountPoCvalidationsAtHeightResponse{
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
			response, err := keeper.CountPoCvalidationsAtHeight(ctx, tc.request)
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.response, response)
			}
		})
	}
}
