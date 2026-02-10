package inference_test

import (
	"strconv"
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	inference "github.com/productscience/inference/x/inference/module"
	"github.com/productscience/inference/x/inference/types"
	"github.com/productscience/inference/x/inference/utils"
)

// TestComputeNewWeightsV1WithStakingValidators tests V1 weight calculation with staking validators
func TestComputeNewWeightsV1WithStakingValidators(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonkapub")
	sdk.GetConfig().SetBech32PrefixForValidator("gonkavaloper", "gonkavaloperpub")

	validatorAccAddress2, err := utils.OperatorAddressToAccAddress(validatorOperatorAddress2)
	require.NoError(t, err, "Failed to convert operator address to account address")

	// Create validators to be returned by the staking keeper
	// validator2 has 201 tokens so a single valid vote exceeds 2/3 threshold (201 > 301*2/3 = 200.67)
	validators := []stakingtypes.Validator{
		{
			OperatorAddress: validatorOperatorAddress1,
			ConsensusPubkey: &codectypes.Any{},
			Tokens:          math.NewInt(100),
		},
		{
			OperatorAddress: validatorOperatorAddress2,
			ConsensusPubkey: &codectypes.Any{},
			Tokens:          math.NewInt(201),
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

	// Set up V1 batches (using PoCBatch type)
	batch := types.PoCBatch{
		ParticipantAddress:       testutil.Executor2,
		PocStageStartBlockHeight: 100,
		Nonces:                   []int64{1, 2, 3},
		NodeId:                   "node-1",
	}
	k.SetPocBatch(ctx, batch)

	// Set up V1 validations (using PoCValidation type)
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
		Address:      testutil.Executor2,
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

	// Call the V1 function
	result := am.ComputeNewWeightsV1(ctx, upcomingEpoch)

	// Verify the result
	require.Equal(t, 1, len(result))
	require.Equal(t, testutil.Executor2, result[0].Index)
	require.Greater(t, result[0].Weight, int64(0))
}

// TestComputeNewWeightsV1_FirstEpoch tests V1 weight calculation for first epoch
func TestComputeNewWeightsV1_FirstEpoch(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonkapub")
	sdk.GetConfig().SetBech32PrefixForValidator("gonkavaloper", "gonkavaloperpub")

	validatorAccAddress, err := utils.OperatorAddressToAccAddress(validatorOperatorAddress1)
	require.NoError(t, err)

	validators := []stakingtypes.Validator{
		{
			OperatorAddress: validatorOperatorAddress1,
			ConsensusPubkey: &codectypes.Any{},
			Tokens:          math.NewInt(100),
		},
	}

	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	mocks.StubForInitGenesis(ctx)

	mocks.StakingKeeper.EXPECT().
		GetAllValidators(gomock.Any()).
		Return(validators, nil).
		AnyTimes()

	members := make([]*group.GroupMember, len(validators))
	for i, v := range validators {
		address, err := utils.OperatorAddressToAccAddress(v.OperatorAddress)
		require.NoError(t, err)
		members[i] = &group.GroupMember{
			Member: &group.Member{
				Address:  address,
				Weight:   strconv.FormatInt(v.Tokens.Int64(), 10),
				Metadata: "metadata1",
			},
		}
	}
	response := &group.QueryGroupMembersResponse{Members: members}

	mocks.GroupKeeper.EXPECT().
		GroupMembers(gomock.Any(), gomock.Any()).
		Return(response, nil).
		AnyTimes()

	inference.InitGenesis(ctx, k, mocks.StubGenesisState())

	am := inference.NewAppModule(nil, k, nil, nil, nil, nil)

	// Set up V1 batches
	batch := types.PoCBatch{
		ParticipantAddress:       testutil.Executor2,
		PocStageStartBlockHeight: 100,
		Nonces:                   []int64{1, 2, 3},
		NodeId:                   "node-1",
	}
	k.SetPocBatch(ctx, batch)

	// Set up V1 validations
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
		Address:      testutil.Executor2,
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

	upcomingEpoch := types.Epoch{
		Index:               1,
		PocStartBlockHeight: 100,
	}

	result := am.ComputeNewWeightsV1(ctx, upcomingEpoch)

	require.Equal(t, 1, len(result))
}

// TestComputeNewWeightsV1_NotEnoughValidations tests V1 rejection when not enough validations
func TestComputeNewWeightsV1_NotEnoughValidations(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonkapub")
	sdk.GetConfig().SetBech32PrefixForValidator("gonkavaloper", "gonkavaloperpub")

	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)

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
	initMockGroupMembers(&mocks, previousEpochGroupData.ValidationWeights)
	k.SetEpochGroupData(ctx, previousEpochGroupData)

	k.SetEpoch(ctx, &types.Epoch{Index: 1, PocStartBlockHeight: 50})
	k.SetEffectiveEpochIndex(ctx, 1)

	am := inference.NewAppModule(nil, k, nil, nil, nil, nil)

	// Set up V1 batches
	batch := types.PoCBatch{
		ParticipantAddress:       testutil.Executor2,
		PocStageStartBlockHeight: 100,
		Nonces:                   []int64{1, 2, 3},
		NodeId:                   "node-1",
	}
	k.SetPocBatch(ctx, batch)

	// Set up validations with only one validator (not enough weight)
	validation := types.PoCValidation{
		ParticipantAddress:          testutil.Executor2,
		ValidatorParticipantAddress: testutil.Validator, // Only one validator with weight 10 (total 30, needs > 15)
		PocStageStartBlockHeight:    100,
		FraudDetected:               false,
	}
	k.SetPoCValidation(ctx, validation)

	// Set up participant
	participant := types.Participant{
		Index:        testutil.Executor2,
		Address:      testutil.Executor2,
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

	upcomingEpoch := types.Epoch{
		Index:               2,
		PocStartBlockHeight: 100,
	}

	result := am.ComputeNewWeightsV1(ctx, upcomingEpoch)

	// Should be rejected due to not enough valid weight
	require.Equal(t, 0, len(result))
}

// TestComputeNewWeightsV1_FraudDetected tests V1 rejection when fraud is detected
func TestComputeNewWeightsV1_FraudDetected(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonkapub")
	sdk.GetConfig().SetBech32PrefixForValidator("gonkavaloper", "gonkavaloperpub")

	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)

	// Set up previous epoch group data
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
				MemberAddress: testutil.Validator2,
				Weight:        20,
			},
		},
	}
	initMockGroupMembers(&mocks, previousEpochGroupData.ValidationWeights)
	k.SetEpochGroupData(ctx, previousEpochGroupData)

	k.SetEpoch(ctx, &types.Epoch{Index: 1, PocStartBlockHeight: 50})
	k.SetEffectiveEpochIndex(ctx, 1)

	am := inference.NewAppModule(nil, k, nil, nil, nil, nil)

	// Set up V1 batches
	batch := types.PoCBatch{
		ParticipantAddress:       testutil.Executor2,
		PocStageStartBlockHeight: 100,
		Nonces:                   []int64{1, 2, 3},
		NodeId:                   "node-1",
	}
	k.SetPocBatch(ctx, batch)

	// Set up validations with fraud detected by majority
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
		Address:      testutil.Executor2,
		ValidatorKey: "validatorKey1",
		InferenceUrl: "inferenceUrl1",
	}
	k.SetParticipant(ctx, participant)

	upcomingEpoch := types.Epoch{
		Index:               2,
		PocStartBlockHeight: 100,
	}

	result := am.ComputeNewWeightsV1(ctx, upcomingEpoch)

	// Should be rejected due to fraud detection by majority weight
	require.Equal(t, 0, len(result))
}

// TestComputeNewWeightsV1_AllowlistExclusion tests V1 allowlist filtering
func TestComputeNewWeightsV1_AllowlistExclusion(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonkapub")
	sdk.GetConfig().SetBech32PrefixForValidator("gonkavaloper", "gonkavaloperpub")

	validatorAccAddress2, err := utils.OperatorAddressToAccAddress(validatorOperatorAddress2)
	require.NoError(t, err)

	validators := []stakingtypes.Validator{
		{
			OperatorAddress: validatorOperatorAddress2,
			ConsensusPubkey: &codectypes.Any{},
			Tokens:          math.NewInt(200),
		},
	}

	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	mocks.StubForInitGenesisWithValidators(ctx, validators)
	inference.InitGenesis(ctx, k, mocks.StubGenesisState())

	members := []*group.GroupMember{
		{
			Member: &group.Member{
				Address:  validatorAccAddress2,
				Weight:   "200",
				Metadata: "metadata",
			},
		},
	}
	mocks.GroupKeeper.EXPECT().
		GroupMembers(gomock.Any(), gomock.Any()).
		Return(&group.QueryGroupMembersResponse{Members: members}, nil).
		AnyTimes()

	am := inference.NewAppModule(nil, k, nil, nil, nil, nil)

	participantA := testutil.Executor
	participantB := testutil.Executor2

	// Set up V1 batches for both participants
	k.SetPocBatch(ctx, types.PoCBatch{
		ParticipantAddress:       participantA,
		PocStageStartBlockHeight: 100,
		Nonces:                   []int64{1, 2, 3},
		NodeId:                   "node-a",
	})
	k.SetPocBatch(ctx, types.PoCBatch{
		ParticipantAddress:       participantB,
		PocStageStartBlockHeight: 100,
		Nonces:                   []int64{4, 5, 6},
		NodeId:                   "node-b",
	})

	// Set up V1 validations for both
	k.SetPoCValidation(ctx, types.PoCValidation{
		ParticipantAddress:          participantA,
		ValidatorParticipantAddress: validatorAccAddress2,
		PocStageStartBlockHeight:    100,
		FraudDetected:               false,
	})
	k.SetPoCValidation(ctx, types.PoCValidation{
		ParticipantAddress:          participantB,
		ValidatorParticipantAddress: validatorAccAddress2,
		PocStageStartBlockHeight:    100,
		FraudDetected:               false,
	})

	// Set up participants
	require.NoError(t, k.SetParticipant(ctx, types.Participant{
		Index:        participantA,
		Address:      participantA,
		ValidatorKey: "validatorKeyA",
		InferenceUrl: "http://a.example.com/",
	}))
	require.NoError(t, k.SetParticipant(ctx, types.Participant{
		Index:        participantB,
		Address:      participantB,
		ValidatorKey: "validatorKeyB",
		InferenceUrl: "http://b.example.com/",
	}))

	// Set up seeds for both
	k.SetRandomSeed(ctx, types.RandomSeed{Participant: participantA, EpochIndex: 1, Signature: "sigA"})
	k.SetRandomSeed(ctx, types.RandomSeed{Participant: participantB, EpochIndex: 1, Signature: "sigB"})

	// Enable allowlist and add only participantA
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.ParticipantAccessParams.UseParticipantAllowlist = true
	require.NoError(t, k.SetParams(ctx, params))

	addrA, err := sdk.AccAddressFromBech32(participantA)
	require.NoError(t, err)
	require.NoError(t, k.ParticipantAllowListSet.Set(ctx, addrA))

	upcomingEpoch := types.Epoch{
		Index:               1,
		PocStartBlockHeight: 100,
	}

	result := am.ComputeNewWeightsV1(ctx, upcomingEpoch)

	// Only participantA should be in the result
	require.Len(t, result, 1)
	require.Equal(t, participantA, result[0].Index)
}

// TestWeightCalculatorV1_UniqueNonces tests V1 weight calculation from unique nonces
func TestWeightCalculatorV1_UniqueNonces(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonkapub")

	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Setup params with weight scale factor
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams = &types.PocParams{
		WeightScaleFactor: types.DecimalFromFloat(1.0),
	}
	require.NoError(t, k.SetParams(ctx, params))

	am := inference.NewAppModule(nil, k, nil, nil, nil, nil)

	// Create V1 batches with duplicate nonces across batches
	batches := map[string][]types.PoCBatch{
		testutil.Executor: {
			{
				ParticipantAddress:       testutil.Executor,
				PocStageStartBlockHeight: 100,
				Nonces:                   []int64{1, 2, 3}, // 3 unique
				NodeId:                   "node-1",
			},
			{
				ParticipantAddress:       testutil.Executor,
				PocStageStartBlockHeight: 100,
				Nonces:                   []int64{2, 3, 4, 5}, // 2 new unique (4, 5)
				NodeId:                   "node-1",
			},
		},
	}

	validations := map[string][]types.PoCValidation{
		testutil.Executor: {
			{
				ParticipantAddress:          testutil.Executor,
				ValidatorParticipantAddress: testutil.Validator,
				PocStageStartBlockHeight:    100,
				FraudDetected:               false,
			},
		},
	}

	participants := map[string]types.Participant{
		testutil.Executor: {
			Index:        testutil.Executor,
			Address:      testutil.Executor,
			ValidatorKey: "validatorKey",
		},
	}

	seeds := map[string]types.RandomSeed{
		testutil.Executor: {
			Participant: testutil.Executor,
			EpochIndex:  1,
			Signature:   "sig",
		},
	}

	currentValidatorWeights := map[string]int64{
		testutil.Validator: 100,
	}

	// Create V1 calculator
	weightScaleFactor := params.PocParams.GetWeightScaleFactorDec()
	calculator := inference.NewWeightCalculatorV1(
		currentValidatorWeights,
		batches,
		validations,
		participants,
		seeds,
		100,
		am,
		weightScaleFactor,
	)

	result := calculator.Calculate()

	// Should have 1 participant with weight = 5 (unique nonces: 1, 2, 3, 4, 5)
	require.Len(t, result, 1)
	require.Equal(t, int64(5), result[0].Weight)
}
