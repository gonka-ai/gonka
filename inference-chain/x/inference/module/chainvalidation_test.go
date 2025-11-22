package inference_test

import (
	"strconv"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/utils"

	"cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/x/group"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/sample"
	"github.com/productscience/inference/x/inference/keeper"
	inference "github.com/productscience/inference/x/inference/module"
	"github.com/productscience/inference/x/inference/types"
)

var validatorOperatorAddress1 = "gonkavaloper1gcrlrhvw8kd7zr6pl92rxnc6j20chatkcx6w4t"
var validatorOperatorAddress2 = "gonkavaloper1xk89s4ymj9y20aym3xa0mz4jhdx40hewckhw0u"

func TestComputeNewWeightsWithStakingValidators(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonkapub")
	sdk.GetConfig().SetBech32PrefixForValidator("gonkavaloper", "gonkavaloperpub")

	validatorAccAddress1, err := utils.OperatorAddressToAccAddress(validatorOperatorAddress1)
	require.NoError(t, err, "Failed to convert operator address to account address")
	println(validatorAccAddress1)

	validatorAccAddress2, err := utils.OperatorAddressToAccAddress(validatorOperatorAddress2)
	require.NoError(t, err, "Failed to convert operator address to account address")
	println(validatorAccAddress2)

	// Create validators to be returned by the staking keeper
	validators := []stakingtypes.Validator{
		{
			OperatorAddress: validatorOperatorAddress1,
			ConsensusPubkey: &codectypes.Any{},
			Tokens:          math.NewInt(100),
		},
		{
			OperatorAddress: validatorOperatorAddress2,
			ConsensusPubkey: &codectypes.Any{},
			Tokens:          math.NewInt(200),
		},
	}

	// Setup with mocks
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	mocks.StubForInitGenesisWithValidators(ctx, validators)
	inference.InitGenesis(ctx, k, mocks.StubGenesisState())

	members := make([]*group.GroupMember, len(validators))
	for i, v := range validators {
		address, err := utils.OperatorAddressToAccAddress(v.OperatorAddress)
		require.NoError(t, err, "Failed to convert operator address to account address")
		members[i] = &group.GroupMember{
			Member: &group.Member{
				Address:  address,
				Weight:   strconv.FormatInt(v.Tokens.Int64(), 10),
				Metadata: "metadata1",
			},
		}
	}
	response := &group.QueryGroupMembersResponse{
		Members: members,
	}

	// Set up the mock expectation
	mocks.GroupKeeper.EXPECT().
		GroupMembers(gomock.Any(), gomock.Any()).
		Return(response, nil).
		AnyTimes()

	// Create AppModule with the keeper
	am := inference.NewAppModule(nil, k, nil, nil, nil, nil)

	// Set up batches
	batch := types.PoCBatch{
		ParticipantAddress:       testutil.Executor2,
		PocStageStartBlockHeight: 100,
		Nonces:                   []int64{1, 2, 3},
	}
	k.SetPocBatch(ctx, batch)

	// Set up validations
	validation := types.PoCValidation{
		ParticipantAddress:          testutil.Executor2,
		ValidatorParticipantAddress: validatorAccAddress2, // Set validation only for participant with large weight
		PocStageStartBlockHeight:    100,
		FraudDetected:               false,
	}
	k.SetPoCValidation(ctx, validation)

	// Set up participant
	participant := types.Participant{
		Index:        testutil.Executor2,
		ValidatorKey: "validatorKey1",
		InferenceUrl: "http://www.yahoo.com/",
	}
	err = k.SetParticipant(ctx, participant)
	require.NoError(t, err)

	// Set up random seed
	seed := types.RandomSeed{
		Participant: testutil.Executor2,
		EpochIndex:  1,
		Signature:   "signature1",
	}
	k.SetRandomSeed(ctx, seed)

	// Create EpochGroupData with epochIndex <= 1
	upcomingEpoch := types.Epoch{
		Index:               1,
		PocStartBlockHeight: 100,
	}

	// Call the function
	result := am.ComputeNewWeights(ctx, upcomingEpoch)

	// Verify the result
	require.Equal(t, 1, len(result))
}

func TestCollateralGracePeriod(t *testing.T) {
	// Setup with mocks
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Set parameters: Grace period ends at epoch 10
	inferenceParams := types.DefaultParams()
	inferenceParams.CollateralParams.GracePeriodEndEpoch = 10
	k.SetParams(ctx, inferenceParams)

	// Set current epoch to 5 (within grace period).
	// AdjustWeightsByCollateral uses GetLatestEpoch, which is based on the "Effective" epoch.
	// We must set the effective epoch first, then the upcoming epoch.
	k.SetEffectiveEpochIndex(ctx, 4)
	currentEpoch := types.Epoch{Index: 5}
	k.SetEpoch(ctx, &currentEpoch)

	// Create a participant with potential weight but zero collateral
	// The "Weight" field here represents the "PotentialWeight" before adjustment.
	participants := []*types.ActiveParticipant{
		{
			Index:  testutil.Executor2,
			Weight: 1000,
		},
	}

	// Execute the core logic that adjusts weights
	err := k.AdjustWeightsByCollateral(ctx, participants)
	require.NoError(t, err)

	// Verify the result
	finalParticipant := participants[0]
	// During grace period, final weight should remain the same as potential weight
	require.Equal(t, int64(1000), finalParticipant.Weight,
		"During grace period, Weight should be unchanged")
}

func TestNoCollateralPostGracePeriod(t *testing.T) {
	// Setup with mocks
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)

	// Set parameters: Grace period ends at epoch 2
	inferenceParams := types.DefaultParams()
	inferenceParams.CollateralParams.GracePeriodEndEpoch = 2
	inferenceParams.CollateralParams.BaseWeightRatio = types.DecimalFromFloat(0.2)
	k.SetParams(ctx, inferenceParams)

	// Set current epoch to 5 (after grace period)
	k.SetEffectiveEpochIndex(ctx, 4)
	currentEpoch := types.Epoch{Index: 5}
	k.SetEpoch(ctx, &currentEpoch)

	// Create a participant with potential weight but zero collateral
	participantAddress := sample.AccAddress()
	participants := []*types.ActiveParticipant{
		{
			Index:  participantAddress,
			Weight: 1000, // This is the "PotentialWeight"
		},
	}

	// Mock the collateral keeper to return zero collateral for this participant
	addr, err := sdk.AccAddressFromBech32(participantAddress)
	require.NoError(t, err)
	mocks.CollateralKeeper.EXPECT().
		GetCollateral(gomock.Any(), addr).
		Return(sdk.Coin{}, false). // No collateral found
		Times(1)

	// Execute the core logic that adjusts weights
	err = k.AdjustWeightsByCollateral(ctx, participants)
	require.NoError(t, err)

	// Verify the result
	finalParticipant := participants[0]
	// After grace period with no collateral, weight should be base weight (1000 * 0.2 = 200)
	require.Equal(t, int64(200), finalParticipant.Weight,
		"With no collateral post-grace period, Weight should be reduced to the base weight")
}

func TestPostGracePeriod_FullCollateral(t *testing.T) {
	// Setup with mocks
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)

	// Set parameters
	inferenceParams := types.DefaultParams()
	inferenceParams.CollateralParams.GracePeriodEndEpoch = 2
	inferenceParams.CollateralParams.BaseWeightRatio = types.DecimalFromFloat(0.2)
	inferenceParams.CollateralParams.CollateralPerWeightUnit = types.DecimalFromFloat(1.0)
	k.SetParams(ctx, inferenceParams)

	// Set current epoch to 5 (after grace period)
	k.SetEffectiveEpochIndex(ctx, 4)
	currentEpoch := types.Epoch{Index: 5}
	k.SetEpoch(ctx, &currentEpoch)

	// Create a participant with potential weight
	participantAddress := sample.AccAddress()
	participants := []*types.ActiveParticipant{
		{
			Index:  participantAddress,
			Weight: 1000, // This is the "PotentialWeight"
		},
	}

	// Mock the collateral keeper to return enough collateral to cover the remaining 80%
	// Collateral-Eligible Weight = 1000 * (1 - 0.2) = 800
	// Required Collateral = 800 * 1.0 = 800
	addr, err := sdk.AccAddressFromBech32(participantAddress)
	require.NoError(t, err)
	requiredCollateral := sdk.NewCoin(types.BaseCoin, math.NewInt(800))
	mocks.CollateralKeeper.EXPECT().
		GetCollateral(gomock.Any(), addr).
		Return(requiredCollateral, true). // Full collateral found
		Times(1)

	// Execute the core logic that adjusts weights
	err = k.AdjustWeightsByCollateral(ctx, participants)
	require.NoError(t, err)

	// Verify the result
	finalParticipant := participants[0]
	// With full collateral, weight should equal potential weight
	require.Equal(t, int64(1000), finalParticipant.Weight,
		"With full collateral post-grace period, Weight should equal PotentialWeight")
}

func TestPostGracePeriod_PartialCollateral(t *testing.T) {
	// Setup with mocks
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)

	// Set parameters
	inferenceParams := types.DefaultParams()
	inferenceParams.CollateralParams.GracePeriodEndEpoch = 2
	inferenceParams.CollateralParams.BaseWeightRatio = types.DecimalFromFloat(0.2)
	inferenceParams.CollateralParams.CollateralPerWeightUnit = types.DecimalFromFloat(1.0)
	k.SetParams(ctx, inferenceParams)

	// Set current epoch to 5 (after grace period)
	k.SetEffectiveEpochIndex(ctx, 4)
	currentEpoch := types.Epoch{Index: 5}
	k.SetEpoch(ctx, &currentEpoch)

	// Create a participant with potential weight
	participantAddress := sample.AccAddress()
	participants := []*types.ActiveParticipant{
		{
			Index:  participantAddress,
			Weight: 1000, // This is the "PotentialWeight"
		},
	}

	// Mock the collateral keeper to return enough collateral to cover half of the remaining 80%
	// Collateral-Eligible Weight = 1000 * (1 - 0.2) = 800
	// Required Collateral for 50% activation = 800 * 0.5 = 400
	addr, err := sdk.AccAddressFromBech32(participantAddress)
	require.NoError(t, err)
	partialCollateral := sdk.NewCoin(types.BaseCoin, math.NewInt(400))
	mocks.CollateralKeeper.EXPECT().
		GetCollateral(gomock.Any(), addr).
		Return(partialCollateral, true). // Partial collateral found
		Times(1)

	// Execute the core logic that adjusts weights
	err = k.AdjustWeightsByCollateral(ctx, participants)
	require.NoError(t, err)

	// Verify the result
	finalParticipant := participants[0]
	// Base Weight = 1000 * 0.2 = 200
	// Activated Weight = 400 (from collateral) / 1.0 (ratio) = 400
	// Total Weight = 200 + 400 = 600
	require.Equal(t, int64(600), finalParticipant.Weight,
		"With partial collateral post-grace period, Weight should be BaseWeight + ActivatedWeight")
}

func TestComputeNewWeights(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonkapub")
	sdk.GetConfig().SetBech32PrefixForValidator("gonkavaloper", "gonkavaloperpub")

	validatorOperatorAddress := validatorOperatorAddress1
	validatorAccAddress, err := utils.OperatorAddressToAccAddress(validatorOperatorAddress)
	require.NoError(t, err, "Failed to convert operator address to account address")
	println(validatorAccAddress)

	// Test cases
	tests := []struct {
		name                 string
		epochIndex           uint64
		setupState           func(t *testing.T, k *keeper.Keeper, ctx sdk.Context, mocks *keepertest.InferenceMocks)
		expectedParticipants int
	}{
		{
			name:       "First epoch - no active participants",
			epochIndex: 1,
			setupState: func(t *testing.T, k *keeper.Keeper, ctx sdk.Context, mocks *keepertest.InferenceMocks) {
				validators := []stakingtypes.Validator{
					{
						OperatorAddress: validatorOperatorAddress,
						ConsensusPubkey: &codectypes.Any{},
						Tokens:          math.NewInt(100),
					},
				}

				mocks.StubForInitGenesis(ctx)

				// Set up the mock expectation for GetAllValidators
				mocks.StakingKeeper.EXPECT().
					GetAllValidators(gomock.Any()).
					Return(validators, nil).
					AnyTimes()

				members := make([]*group.GroupMember, len(validators))
				for i, v := range validators {
					address, err := utils.OperatorAddressToAccAddress(v.OperatorAddress)
					require.NoError(t, err, "Failed to convert operator address to account address")
					members[i] = &group.GroupMember{
						Member: &group.Member{
							Address:  address,
							Weight:   strconv.FormatInt(v.Tokens.Int64(), 10),
							Metadata: "metadata1",
						},
					}
				}
				response := &group.QueryGroupMembersResponse{
					Members: members,
				}

				mocks.GroupKeeper.EXPECT().
					GroupMembers(gomock.Any(), gomock.Any()).
					Return(response, nil).
					AnyTimes()

				inference.InitGenesis(ctx, *k, mocks.StubGenesisState())

				// Set up batches
				batch := types.PoCBatch{
					ParticipantAddress:       testutil.Executor2,
					PocStageStartBlockHeight: 100,
					Nonces:                   []int64{1, 2, 3},
				}
				k.SetPocBatch(ctx, batch)

				// Set up validations
				validation := types.PoCValidation{
					ParticipantAddress:          testutil.Executor2,
					ValidatorParticipantAddress: validatorAccAddress,
					PocStageStartBlockHeight:    100,
					FraudDetected:               false,
				}
				k.SetPoCValidation(ctx, validation)

				// Set up participant
				participant := types.Participant{
					Index:        testutil.Executor2,
					ValidatorKey: "validatorKey1",
					InferenceUrl: "inferenceUrl1",
				}
				k.SetParticipant(ctx, participant)

				// Set up random seed
				seed := types.RandomSeed{
					Participant: testutil.Executor2,
					EpochIndex:  1,
					Signature:   "signature1",
				}
				k.SetRandomSeed(ctx, seed)
			},
			expectedParticipants: 1,
		},
		//{
		//	name:       "Subsequent epoch with active participants",
		//	epochIndex: 2,
		//	setupState: func(t *testing.T, k *keeper.Keeper, ctx sdk.Context, mocks *keepertest.InferenceMocks) {
		//		// Set up previous epoch group data
		//		previousEpochGroupData := types.EpochGroupData{
		//			EpochGroupId:        1,
		//			PocStartBlockHeight: 50,
		//			EpochIndex:          1,
		//			ValidationWeights: []*types.ValidationWeight{
		//				{
		//					MemberAddress: "validator1",
		//					Weight:        10,
		//				},
		//			},
		//		}
		//		initMockGroupMembers(mocks, previousEpochGroupData.ValidationWeights)
		//		k.SetEpochGroupData(ctx, previousEpochGroupData)
		//
		//		k.SetEpoch(ctx, &types.Epoch{Index: 1, PocStartBlockHeight: 50})
		//		k.SetEffectiveEpochIndex(ctx, 1)
		//
		//		// Set up batches
		//		batch := types.PoCBatch{
		//			ParticipantAddress:       testutil.Executor,
		//			PocStageStartBlockHeight: 100,
		//			Nonces:                   []int64{1, 2, 3},
		//		}
		//		k.SetPocBatch(ctx, batch)
		//
		//		// Set up validations
		//		validation := types.PoCValidation{
		//			ParticipantAddress:          testutil.Executor,
		//			ValidatorParticipantAddress: "validator1",
		//			PocStageStartBlockHeight:    100,
		//			FraudDetected:               false,
		//		}
		//		k.SetPoCValidation(ctx, validation)
		//
		//		// Set up participant
		//		participant := types.Participant{
		//			Index:        testutil.Executor,
		//			ValidatorKey: "validatorKey1",
		//			InferenceUrl: "inferenceUrl1",
		//		}
		//		k.SetParticipant(ctx, participant)
		//
		//		// Set up random seed
		//		seed := types.RandomSeed{
		//			Participant: testutil.Executor,
		//			EpochIndex:  1,
		//			Signature:   "signature1",
		//		}
		//		k.SetRandomSeed(ctx, seed)
		//	},
		//	expectedParticipants: 1,
		//},
		{
			name:       "Participant didn't receive enough validations (total voted weight < required) - should default to accepting",
			epochIndex: 2,
			setupState: func(t *testing.T, k *keeper.Keeper, ctx sdk.Context, mocks *keepertest.InferenceMocks) {
				// Set up previous epoch group data with high weight validators
				previousEpochGroupData := types.EpochGroupData{
					EpochGroupId:        1,
					EpochIndex:          1,
					PocStartBlockHeight: 50,
					ValidationWeights: []*types.ValidationWeight{
						{
							MemberAddress: testutil.Validator,
							Weight:        10,
						},
						{
							MemberAddress: testutil.Validator2,
							Weight:        20,
						},
					},
				}
				initMockGroupMembers(mocks, previousEpochGroupData.ValidationWeights)
				k.SetEpochGroupData(ctx, previousEpochGroupData)

				k.SetEpoch(ctx, &types.Epoch{Index: 1, PocStartBlockHeight: 50})
				k.SetEffectiveEpochIndex(ctx, 1)

				// Set up batches
				batch := types.PoCBatch{
					ParticipantAddress:       testutil.Executor2,
					PocStageStartBlockHeight: 100,
					Nonces:                   []int64{1, 2, 3},
				}
				k.SetPocBatch(ctx, batch)

				// Set up validations with only one validator (not enough weight)
				validation := types.PoCValidation{
					ParticipantAddress:          testutil.Executor2,
					ValidatorParticipantAddress: testutil.Validator,
					PocStageStartBlockHeight:    100,
					FraudDetected:               false,
				}
				k.SetPoCValidation(ctx, validation)

				// Set up participant
				participant := types.Participant{
					Index:        testutil.Executor2,
					ValidatorKey: "validatorKey1",
					InferenceUrl: "inferenceUrl1",
				}
				k.SetParticipant(ctx, participant)

				// Set up random seed
				seed := types.RandomSeed{
					Participant: testutil.Executor2,
					EpochIndex:  1,
					Signature:   "signature1",
				}
				k.SetRandomSeed(ctx, seed)
			},
			expectedParticipants: 0,
		},
		{
			name:       "Participant didn't receive enough valid validations (valid weight < required) - should be rejected",
			epochIndex: 2,
			setupState: func(t *testing.T, k *keeper.Keeper, ctx sdk.Context, mocks *keepertest.InferenceMocks) {
				// Set up previous epoch group data with high weight validators
				previousEpochGroupData := types.EpochGroupData{
					EpochGroupId:        1,
					EpochIndex:          1,
					PocStartBlockHeight: 50,
					ValidationWeights: []*types.ValidationWeight{
						{
							MemberAddress: testutil.Validator,
							Weight:        5,
						},
						{
							// Intentionally using a different address to simulate low weight
							MemberAddress: testutil.Validator2,
							Weight:        20,
						},
					},
				}
				initMockGroupMembers(mocks, previousEpochGroupData.ValidationWeights)
				k.SetEpochGroupData(ctx, previousEpochGroupData)

				k.SetEpoch(ctx, &types.Epoch{Index: 1, PocStartBlockHeight: 50})
				k.SetEffectiveEpochIndex(ctx, 1)

				// Set up batches
				batch := types.PoCBatch{
					ParticipantAddress:       testutil.Executor2,
					PocStageStartBlockHeight: 100,
					Nonces:                   []int64{1, 2, 3},
				}
				k.SetPocBatch(ctx, batch)

				// Set up validations with enough total weight but not enough valid weight
				validation1 := types.PoCValidation{
					ParticipantAddress:          testutil.Executor2,
					ValidatorParticipantAddress: testutil.Validator,
					PocStageStartBlockHeight:    100,
					FraudDetected:               false, // Valid but low weight
				}
				k.SetPoCValidation(ctx, validation1)

				validation2 := types.PoCValidation{
					ParticipantAddress:          testutil.Executor2,
					ValidatorParticipantAddress: testutil.Validator2,
					PocStageStartBlockHeight:    100,
					FraudDetected:               true, // Invalid with high weight
				}
				k.SetPoCValidation(ctx, validation2)

				// Set up participant
				participant := types.Participant{
					Index:        testutil.Executor2,
					ValidatorKey: "validatorKey1",
					InferenceUrl: "inferenceUrl1",
				}
				k.SetParticipant(ctx, participant)
			},
			expectedParticipants: 0, // Should be rejected due to not enough valid validations
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup with mocks
			k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)

			// Create AppModule with the keeper
			am := inference.NewAppModule(nil, k, nil, nil, nil, nil)

			// Setup state
			tt.setupState(t, &k, ctx, &mocks)

			// Set up mock for GroupMembers if needed
			if tt.epochIndex != 1 {
				// Create a mock response for GroupMembers
				members := []*group.GroupMember{
					{
						Member: &group.Member{
							Address:  validatorAccAddress,
							Weight:   "10",
							Metadata: "metadata1",
						},
					},
				}
				response := &group.QueryGroupMembersResponse{
					Members: members,
				}

				// Set up the mock expectation
				mocks.GroupKeeper.EXPECT().
					GroupMembers(gomock.Any(), gomock.Any()).
					Return(response, nil).
					AnyTimes()
			}

			// Create EpochGroupData
			upcomingEpoch := types.Epoch{
				Index:               tt.epochIndex,
				PocStartBlockHeight: 100,
			}
			k.SetEpoch(ctx, &upcomingEpoch)
			k.SetEpochGroupData(ctx, types.EpochGroupData{
				EpochGroupId:        2,
				PocStartBlockHeight: 100,
			})

		// Call the function
		result := am.ComputeNewWeights(ctx, upcomingEpoch)

		// Verify the result
		require.Equal(t, tt.expectedParticipants, len(result))
		})
	}
}

func initMockGroupMembers(mocks *keepertest.InferenceMocks, validator []*types.ValidationWeight) {
	// Create a mock response for GroupMembers
	members := make([]*group.GroupMember, len(validator))
	for i, v := range validator {
		members[i] = &group.GroupMember{
			Member: &group.Member{
				Address:  v.MemberAddress,
				Weight:   "10",
				Metadata: "metadata1",
			},
		}
	}
	response := &group.QueryGroupMembersResponse{
		Members: members,
	}

	// Set up the mock expectation
	mocks.GroupKeeper.EXPECT().
		GroupMembers(gomock.Any(), gomock.Any()).
		Return(response, nil).
		AnyTimes()
}
