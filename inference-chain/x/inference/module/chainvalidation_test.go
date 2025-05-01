package inference_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	inference "github.com/productscience/inference/x/inference/module"
	"github.com/productscience/inference/x/inference/types"
)

func TestComputeNewWeights(t *testing.T) {
	// Test cases
	tests := []struct {
		name                 string
		epochGroupId         uint64
		setupState           func(t *testing.T, k *keeper.Keeper, ctx context.Context)
		expectedParticipants int
	}{
		{
			name:         "First epoch - no active participants",
			epochGroupId: 1,
			setupState: func(t *testing.T, k *keeper.Keeper, ctx context.Context) {
				// Set up batches
				batch := types.PoCBatch{
					ParticipantAddress:       "participant1",
					PocStageStartBlockHeight: 100,
					Nonces:                   []int64{1, 2, 3},
				}
				k.SetPocBatch(ctx, batch)

				// Set up validations
				validation := types.PoCValidation{
					ParticipantAddress:          "participant1",
					ValidatorParticipantAddress: "validator1",
					PocStageStartBlockHeight:    100,
					FraudDetected:               false,
				}
				k.SetPoCValidation(ctx, validation)

				// Set up participant
				participant := types.Participant{
					Index:        "participant1",
					ValidatorKey: "validatorKey1",
					InferenceUrl: "inferenceUrl1",
					Models:       []string{"model1"},
				}
				k.SetParticipant(ctx, participant)

				// Set up random seed
				seed := types.RandomSeed{
					Participant: "participant1",
					BlockHeight: 100,
					Signature:   "signature1",
				}
				k.SetRandomSeed(ctx, seed)
			},
			expectedParticipants: 1,
		},
		{
			name:         "Subsequent epoch with active participants",
			epochGroupId: 2,
			setupState: func(t *testing.T, k *keeper.Keeper, ctx context.Context) {
				// Set up previous epoch group data
				previousEpochGroupData := types.EpochGroupData{
					EpochGroupId:        1,
					PocStartBlockHeight: 50,
					ValidationWeights: []*types.ValidationWeight{
						{
							MemberAddress: "validator1",
							Weight:        10,
						},
					},
				}
				k.SetEpochGroupData(ctx, previousEpochGroupData)

				// Set previous epoch group ID
				k.SetPreviousEpochGroupId(ctx, 50)

				// Set up batches
				batch := types.PoCBatch{
					ParticipantAddress:       "participant1",
					PocStageStartBlockHeight: 100,
					Nonces:                   []int64{1, 2, 3},
				}
				k.SetPocBatch(ctx, batch)

				// Set up validations
				validation := types.PoCValidation{
					ParticipantAddress:          "participant1",
					ValidatorParticipantAddress: "validator1",
					PocStageStartBlockHeight:    100,
					FraudDetected:               false,
				}
				k.SetPoCValidation(ctx, validation)

				// Set up participant
				participant := types.Participant{
					Index:        "participant1",
					ValidatorKey: "validatorKey1",
					InferenceUrl: "inferenceUrl1",
					Models:       []string{"model1"},
				}
				k.SetParticipant(ctx, participant)

				// Set up random seed
				seed := types.RandomSeed{
					Participant: "participant1",
					BlockHeight: 100,
					Signature:   "signature1",
				}
				k.SetRandomSeed(ctx, seed)
			},
			expectedParticipants: 1,
		},
		{
			name:         "Participant didn't receive enough validations (total voted weight < required) - should default to accepting",
			epochGroupId: 2,
			setupState: func(t *testing.T, k *keeper.Keeper, ctx context.Context) {
				// Set up previous epoch group data with high weight validators
				previousEpochGroupData := types.EpochGroupData{
					EpochGroupId:        1,
					PocStartBlockHeight: 50,
					ValidationWeights: []*types.ValidationWeight{
						{
							MemberAddress: "validator1",
							Weight:        10,
						},
						{
							MemberAddress: "validator2",
							Weight:        20,
						},
					},
				}
				k.SetEpochGroupData(ctx, previousEpochGroupData)

				// Set previous epoch group ID
				k.SetPreviousEpochGroupId(ctx, 50)

				// Set up batches
				batch := types.PoCBatch{
					ParticipantAddress:       "participant1",
					PocStageStartBlockHeight: 100,
					Nonces:                   []int64{1, 2, 3},
				}
				k.SetPocBatch(ctx, batch)

				// Set up validations with only one validator (not enough weight)
				validation := types.PoCValidation{
					ParticipantAddress:          "participant1",
					ValidatorParticipantAddress: "validator1",
					PocStageStartBlockHeight:    100,
					FraudDetected:               false,
				}
				k.SetPoCValidation(ctx, validation)

				// Set up participant
				participant := types.Participant{
					Index:        "participant1",
					ValidatorKey: "validatorKey1",
					InferenceUrl: "inferenceUrl1",
					Models:       []string{"model1"},
				}
				k.SetParticipant(ctx, participant)

				// Set up random seed
				seed := types.RandomSeed{
					Participant: "participant1",
					BlockHeight: 100,
					Signature:   "signature1",
				}
				k.SetRandomSeed(ctx, seed)
			},
			expectedParticipants: 1, // Should be accepted despite not enough validations
		},
		{
			name:         "Participant didn't receive enough valid validations (valid weight < required) - should be rejected",
			epochGroupId: 2,
			setupState: func(t *testing.T, k *keeper.Keeper, ctx context.Context) {
				// Set up previous epoch group data with high weight validators
				previousEpochGroupData := types.EpochGroupData{
					EpochGroupId:        1,
					PocStartBlockHeight: 50,
					ValidationWeights: []*types.ValidationWeight{
						{
							MemberAddress: "validator1",
							Weight:        5,
						},
						{
							MemberAddress: "validator2",
							Weight:        20,
						},
					},
				}
				k.SetEpochGroupData(ctx, previousEpochGroupData)

				// Set previous epoch group ID
				k.SetPreviousEpochGroupId(ctx, 50)

				// Set up batches
				batch := types.PoCBatch{
					ParticipantAddress:       "participant1",
					PocStageStartBlockHeight: 100,
					Nonces:                   []int64{1, 2, 3},
				}
				k.SetPocBatch(ctx, batch)

				// Set up validations with enough total weight but not enough valid weight
				validation1 := types.PoCValidation{
					ParticipantAddress:          "participant1",
					ValidatorParticipantAddress: "validator1",
					PocStageStartBlockHeight:    100,
					FraudDetected:               false, // Valid but low weight
				}
				k.SetPoCValidation(ctx, validation1)

				validation2 := types.PoCValidation{
					ParticipantAddress:          "participant1",
					ValidatorParticipantAddress: "validator2",
					PocStageStartBlockHeight:    100,
					FraudDetected:               true, // Invalid with high weight
				}
				k.SetPoCValidation(ctx, validation2)

				// Set up participant
				participant := types.Participant{
					Index:        "participant1",
					ValidatorKey: "validatorKey1",
					InferenceUrl: "inferenceUrl1",
					Models:       []string{"model1"},
				}
				k.SetParticipant(ctx, participant)
			},
			expectedParticipants: 0, // Should be rejected due to not enough valid validations
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup
			k, ctx := keepertest.InferenceKeeper(t)

			// Create AppModule with the keeper
			am := inference.NewAppModule(nil, k, nil, nil, nil)

			// Setup state
			tt.setupState(t, &k, ctx)

			// Create EpochGroupData
			upcomingGroupData := &types.EpochGroupData{
				EpochGroupId:        tt.epochGroupId,
				PocStartBlockHeight: 100,
			}

			// Call the function
			result := am.ComputeNewWeights(ctx, upcomingGroupData)

			// Verify the result
			require.Equal(t, tt.expectedParticipants, len(result))
		})
	}
}
