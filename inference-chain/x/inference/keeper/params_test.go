package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func TestGetParams(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params := types.DefaultParams()

	require.NoError(t, k.SetParams(ctx, params))
	require.EqualValues(t, params, k.GetParams(ctx))
}

func TestTokenomicsParamsGovernance(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	wctx := sdk.UnwrapSDKContext(ctx)

	// Test setting initial parameters
	params := types.DefaultParams()
	require.NoError(t, k.SetParams(ctx, params))

	// Test updating vesting parameters through governance
	testCases := []struct {
		name                    string
		workVestingPeriod       uint64
		rewardVestingPeriod     uint64
		topMinerVestingPeriod   uint64
		expectedWorkVesting     uint64
		expectedRewardVesting   uint64
		expectedTopMinerVesting uint64
	}{
		{
			name:                    "default vesting periods (no vesting)",
			workVestingPeriod:       0,
			rewardVestingPeriod:     0,
			topMinerVestingPeriod:   0,
			expectedWorkVesting:     0,
			expectedRewardVesting:   0,
			expectedTopMinerVesting: 0,
		},
		{
			name:                    "enable vesting for all reward types",
			workVestingPeriod:       180,
			rewardVestingPeriod:     180,
			topMinerVestingPeriod:   180,
			expectedWorkVesting:     180,
			expectedRewardVesting:   180,
			expectedTopMinerVesting: 180,
		},
		{
			name:                    "different vesting periods for different reward types",
			workVestingPeriod:       90,
			rewardVestingPeriod:     180,
			topMinerVestingPeriod:   360,
			expectedWorkVesting:     90,
			expectedRewardVesting:   180,
			expectedTopMinerVesting: 360,
		},
		{
			name:                    "test vesting periods (fast for E2E tests)",
			workVestingPeriod:       2,
			rewardVestingPeriod:     2,
			topMinerVestingPeriod:   2,
			expectedWorkVesting:     2,
			expectedRewardVesting:   2,
			expectedTopMinerVesting: 2,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Update parameters
			updatedParams := params
			updatedParams.TokenomicsParams.WorkVestingPeriod = tc.workVestingPeriod
			updatedParams.TokenomicsParams.RewardVestingPeriod = tc.rewardVestingPeriod
			updatedParams.TokenomicsParams.TopMinerVestingPeriod = tc.topMinerVestingPeriod

			// Set the updated parameters
			require.NoError(t, k.SetParams(ctx, updatedParams))

			// Retrieve and verify the parameters
			retrievedParams := k.GetParams(wctx)
			require.Equal(t, tc.expectedWorkVesting, retrievedParams.TokenomicsParams.WorkVestingPeriod)
			require.Equal(t, tc.expectedRewardVesting, retrievedParams.TokenomicsParams.RewardVestingPeriod)
			require.Equal(t, tc.expectedTopMinerVesting, retrievedParams.TokenomicsParams.TopMinerVestingPeriod)
		})
	}
}

func TestVestingParameterValidation(t *testing.T) {
	testCases := []struct {
		name           string
		vestingPeriod  interface{}
		expectedError  bool
		expectedErrMsg string
	}{
		{
			name:          "valid vesting period - zero (no vesting)",
			vestingPeriod: uint64(0),
			expectedError: false,
		},
		{
			name:          "valid vesting period - positive value",
			vestingPeriod: uint64(180),
			expectedError: false,
		},
		{
			name:          "valid vesting period - large value",
			vestingPeriod: uint64(1000000),
			expectedError: false,
		},
		{
			name:           "invalid parameter type - string",
			vestingPeriod:  "180",
			expectedError:  true,
			expectedErrMsg: "invalid parameter type",
		},
		{
			name:           "invalid parameter type - int",
			vestingPeriod:  180,
			expectedError:  true,
			expectedErrMsg: "invalid parameter type",
		},
		{
			name:           "invalid parameter type - nil interface{}",
			vestingPeriod:  nil,
			expectedError:  true,
			expectedErrMsg: "vesting period cannot be nil",
		},
		{
			name:           "invalid parameter type - nil pointer",
			vestingPeriod:  (*uint64)(nil),
			expectedError:  true,
			expectedErrMsg: "vesting period cannot be nil",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Test the validation function directly
			err := types.ValidateVestingPeriod(tc.vestingPeriod)

			if tc.expectedError {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.expectedErrMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestTokenomicsParamsParamSetPairs(t *testing.T) {
	params := *types.DefaultTokenomicsParams()

	// Test that ParamSetPairs returns the correct number of pairs
	pairs := params.ParamSetPairs()
	require.Len(t, pairs, 3, "TokenomicsParams should have 3 parameter pairs for vesting")

	// Verify the parameter keys are correctly set
	expectedKeys := [][]byte{
		types.KeyWorkVestingPeriod,
		types.KeyRewardVestingPeriod,
		types.KeyTopMinerVestingPeriod,
	}

	for i, pair := range pairs {
		require.Equal(t, expectedKeys[i], pair.Key, "Parameter key mismatch for pair %d", i)
	}
}

func TestTokenomicsParamsValidate(t *testing.T) {
	testCases := []struct {
		name                  string
		workVestingPeriod     uint64
		rewardVestingPeriod   uint64
		topMinerVestingPeriod uint64
		expectedError         bool
		expectedErrMsg        string
	}{
		{
			name:                  "valid vesting parameters",
			workVestingPeriod:     180,
			rewardVestingPeriod:   180,
			topMinerVestingPeriod: 180,
			expectedError:         false,
		},
		{
			name:                  "valid vesting parameters - zero values",
			workVestingPeriod:     0,
			rewardVestingPeriod:   0,
			topMinerVestingPeriod: 0,
			expectedError:         false,
		},
		{
			name:                  "valid vesting parameters - mixed values",
			workVestingPeriod:     90,
			rewardVestingPeriod:   180,
			topMinerVestingPeriod: 360,
			expectedError:         false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			params := *types.DefaultTokenomicsParams()
			params.WorkVestingPeriod = tc.workVestingPeriod
			params.RewardVestingPeriod = tc.rewardVestingPeriod
			params.TopMinerVestingPeriod = tc.topMinerVestingPeriod

			err := params.Validate()

			if tc.expectedError {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.expectedErrMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestParamsValidateCallsTokenomicsValidation(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)

	// Create params with valid structure but we'll test the validation chain
	params := types.DefaultParams()

	// Set valid vesting parameters
	params.TokenomicsParams.WorkVestingPeriod = 180
	params.TokenomicsParams.RewardVestingPeriod = 180
	params.TokenomicsParams.TopMinerVestingPeriod = 180

	// This should pass validation
	err := params.Validate()
	require.NoError(t, err)

	// Verify we can set these params successfully
	require.NoError(t, k.SetParams(ctx, params))

	// Retrieve and verify the parameters
	retrievedParams := k.GetParams(ctx)
	require.Equal(t, uint64(180), retrievedParams.TokenomicsParams.WorkVestingPeriod)
	require.Equal(t, uint64(180), retrievedParams.TokenomicsParams.RewardVestingPeriod)
	require.Equal(t, uint64(180), retrievedParams.TokenomicsParams.TopMinerVestingPeriod)
}

func TestParamsValidateNilChecks(t *testing.T) {
	testCases := []struct {
		name           string
		setupParams    func() types.Params
		expectedErrMsg string
	}{
		{
			name: "nil ValidationParams",
			setupParams: func() types.Params {
				params := types.DefaultParams()
				params.ValidationParams = nil
				return params
			},
			expectedErrMsg: "validation params cannot be nil",
		},
		{
			name: "nil TokenomicsParams",
			setupParams: func() types.Params {
				params := types.DefaultParams()
				params.TokenomicsParams = nil
				return params
			},
			expectedErrMsg: "tokenomics params cannot be nil",
		},
		{
			name: "nil CollateralParams",
			setupParams: func() types.Params {
				params := types.DefaultParams()
				params.CollateralParams = nil
				return params
			},
			expectedErrMsg: "collateral params cannot be nil",
		},
		{
			name: "nil BitcoinRewardParams",
			setupParams: func() types.Params {
				params := types.DefaultParams()
				params.BitcoinRewardParams = nil
				return params
			},
			expectedErrMsg: "bitcoin reward params cannot be nil",
		},
		{
			name: "all params valid",
			setupParams: func() types.Params {
				return types.DefaultParams()
			},
			expectedErrMsg: "", // No error expected
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			params := tc.setupParams()
			err := params.Validate()

			if tc.expectedErrMsg == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.expectedErrMsg)
			}
		})
	}
}

func TestValidationParamsNilFieldChecks(t *testing.T) {
	testCases := []struct {
		name           string
		setupParams    func() *types.ValidationParams
		expectedErrMsg string
	}{
		{
			name: "nil FalsePositiveRate",
			setupParams: func() *types.ValidationParams {
				params := types.DefaultValidationParams()
				params.FalsePositiveRate = nil
				return params
			},
			expectedErrMsg: "false positive rate cannot be nil",
		},
		{
			name: "nil PassValue",
			setupParams: func() *types.ValidationParams {
				params := types.DefaultValidationParams()
				params.PassValue = nil
				return params
			},
			expectedErrMsg: "pass value cannot be nil",
		},
		{
			name: "nil MinValidationAverage",
			setupParams: func() *types.ValidationParams {
				params := types.DefaultValidationParams()
				params.MinValidationAverage = nil
				return params
			},
			expectedErrMsg: "min validation average cannot be nil",
		},
		{
			name: "nil BadParticipantInvalidationRate",
			setupParams: func() *types.ValidationParams {
				params := types.DefaultValidationParams()
				params.BadParticipantInvalidationRate = nil
				return params
			},
			expectedErrMsg: "bad participant invalidation rate cannot be nil",
		},
		{
			name: "nil InvalidationHThreshold",
			setupParams: func() *types.ValidationParams {
				params := types.DefaultValidationParams()
				params.InvalidationHThreshold = nil
				return params
			},
			expectedErrMsg: "invalidation h threshold cannot be nil",
		},
		{
			name: "nil DowntimeGoodPercentage",
			setupParams: func() *types.ValidationParams {
				params := types.DefaultValidationParams()
				params.DowntimeGoodPercentage = nil
				return params
			},
			expectedErrMsg: "downtime good percentage cannot be nil",
		},
		{
			name: "nil DowntimeBadPercentage",
			setupParams: func() *types.ValidationParams {
				params := types.DefaultValidationParams()
				params.DowntimeBadPercentage = nil
				return params
			},
			expectedErrMsg: "downtime bad percentage cannot be nil",
		},
		{
			name: "nil DowntimeHThreshold",
			setupParams: func() *types.ValidationParams {
				params := types.DefaultValidationParams()
				params.DowntimeHThreshold = nil
				return params
			},
			expectedErrMsg: "downtime h threshold cannot be nil",
		},
		{
			name: "nil QuickFailureThreshold",
			setupParams: func() *types.ValidationParams {
				params := types.DefaultValidationParams()
				params.QuickFailureThreshold = nil
				return params
			},
			expectedErrMsg: "quick failure threshold cannot be nil",
		},
		{
			name: "nil InvalidReputationPreserve",
			setupParams: func() *types.ValidationParams {
				params := types.DefaultValidationParams()
				params.InvalidReputationPreserve = nil
				return params
			},
			expectedErrMsg: "invalid reputation preserve cannot be nil",
		},
		{
			name: "nil DowntimeReputationPreserve",
			setupParams: func() *types.ValidationParams {
				params := types.DefaultValidationParams()
				params.DowntimeReputationPreserve = nil
				return params
			},
			expectedErrMsg: "downtime reputation preserve cannot be nil",
		},
		{
			name: "valid ValidationParams",
			setupParams: func() *types.ValidationParams {
				return types.DefaultValidationParams()
			},
			expectedErrMsg: "", // No error expected
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			params := tc.setupParams()
			err := params.Validate()

			if tc.expectedErrMsg == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.expectedErrMsg)
			}
		})
	}
}

func TestTokenomicsParamsNilFieldChecks(t *testing.T) {
	testCases := []struct {
		name           string
		setupParams    func() *types.TokenomicsParams
		expectedErrMsg string
	}{
		{
			name: "nil SubsidyReductionAmount",
			setupParams: func() *types.TokenomicsParams {
				params := types.DefaultTokenomicsParams()
				params.SubsidyReductionAmount = nil
				return params
			},
			expectedErrMsg: "subsidy reduction amount cannot be nil",
		},
		{
			name: "nil CurrentSubsidyPercentage",
			setupParams: func() *types.TokenomicsParams {
				params := types.DefaultTokenomicsParams()
				params.CurrentSubsidyPercentage = nil
				return params
			},
			expectedErrMsg: "current subsidy percentage cannot be nil",
		},
		{
			name: "nil TopRewardAllowedFailure",
			setupParams: func() *types.TokenomicsParams {
				params := types.DefaultTokenomicsParams()
				params.TopRewardAllowedFailure = nil
				return params
			},
			expectedErrMsg: "top reward allowed failure cannot be nil",
		},
		{
			name: "valid TokenomicsParams",
			setupParams: func() *types.TokenomicsParams {
				return types.DefaultTokenomicsParams()
			},
			expectedErrMsg: "", // No error expected
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			params := tc.setupParams()
			err := params.Validate()

			if tc.expectedErrMsg == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.expectedErrMsg)
			}
		})
	}
}

func TestBitcoinRewardParamsGovernance(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	wctx := sdk.UnwrapSDKContext(ctx)

	// Test setting initial parameters
	params := types.DefaultParams()
	require.NoError(t, k.SetParams(ctx, params))

	// Test updating Bitcoin reward parameters through governance
	testCases := []struct {
		name                       string
		initialEpochReward         uint64
		decayRate                  float64
		genesisEpoch               uint64
		utilizationBonusFactor     float64
		fullCoverageBonusFactor    float64
		partialCoverageBonusFactor float64
	}{
		{
			name:                       "default Bitcoin reward parameters",
			initialEpochReward:         285000000000000,
			decayRate:                  -0.000475,
			genesisEpoch:               0,
			utilizationBonusFactor:     0.5,
			fullCoverageBonusFactor:    1.2,
			partialCoverageBonusFactor: 0.1,
		},
		{
			name:                       "modified Bitcoin reward parameters",
			initialEpochReward:         500000,
			decayRate:                  -0.0001,
			genesisEpoch:               100,
			utilizationBonusFactor:     0.3,
			fullCoverageBonusFactor:    1.5,
			partialCoverageBonusFactor: 0.2,
		},
		{
			name:                       "minimal Bitcoin reward parameters",
			initialEpochReward:         1000,
			decayRate:                  -0.00001,
			genesisEpoch:               0,
			utilizationBonusFactor:     0.0,
			fullCoverageBonusFactor:    1.0,
			partialCoverageBonusFactor: 0.0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Update parameters
			updatedParams := params
			updatedParams.BitcoinRewardParams.InitialEpochReward = tc.initialEpochReward
			updatedParams.BitcoinRewardParams.DecayRate = types.DecimalFromFloat(tc.decayRate)
			updatedParams.BitcoinRewardParams.GenesisEpoch = tc.genesisEpoch
			updatedParams.BitcoinRewardParams.UtilizationBonusFactor = types.DecimalFromFloat(tc.utilizationBonusFactor)
			updatedParams.BitcoinRewardParams.FullCoverageBonusFactor = types.DecimalFromFloat(tc.fullCoverageBonusFactor)
			updatedParams.BitcoinRewardParams.PartialCoverageBonusFactor = types.DecimalFromFloat(tc.partialCoverageBonusFactor)

			// Set the updated parameters
			require.NoError(t, k.SetParams(ctx, updatedParams))

			// Retrieve and verify the parameters
			retrievedParams := k.GetParams(wctx)
			require.Equal(t, tc.initialEpochReward, retrievedParams.BitcoinRewardParams.InitialEpochReward)
			require.Equal(t, tc.decayRate, retrievedParams.BitcoinRewardParams.DecayRate.ToFloat())
			require.Equal(t, tc.genesisEpoch, retrievedParams.BitcoinRewardParams.GenesisEpoch)
			require.Equal(t, tc.utilizationBonusFactor, retrievedParams.BitcoinRewardParams.UtilizationBonusFactor.ToFloat())
			require.Equal(t, tc.fullCoverageBonusFactor, retrievedParams.BitcoinRewardParams.FullCoverageBonusFactor.ToFloat())
			require.Equal(t, tc.partialCoverageBonusFactor, retrievedParams.BitcoinRewardParams.PartialCoverageBonusFactor.ToFloat())
		})
	}
}

func TestBitcoinRewardParamsParamSetPairs(t *testing.T) {
	params := *types.DefaultBitcoinRewardParams()

	// Test that ParamSetPairs returns the correct number of pairs
	pairs := params.ParamSetPairs()
	require.Len(t, pairs, 7, "BitcoinRewardParams should have 7 parameter pairs")

	// Verify the parameter keys are correctly set
	expectedKeys := [][]byte{
		types.KeyUseBitcoinRewards,
		types.KeyInitialEpochReward,
		types.KeyDecayRate,
		types.KeyGenesisEpoch,
		types.KeyUtilizationBonusFactor,
		types.KeyFullCoverageBonusFactor,
		types.KeyPartialCoverageBonusFactor,
	}

	for i, pair := range pairs {
		require.Equal(t, expectedKeys[i], pair.Key, "Parameter key mismatch for pair %d", i)
	}
}

func TestBitcoinRewardParamsValidate(t *testing.T) {
	testCases := []struct {
		name                       string
		useBitcoinRewards          bool
		initialEpochReward         uint64
		decayRate                  float64
		genesisEpoch               uint64
		utilizationBonusFactor     float64
		fullCoverageBonusFactor    float64
		partialCoverageBonusFactor float64
		expectedError              bool
		expectedErrMsg             string
	}{
		{
			name:                       "valid Bitcoin reward parameters",
			useBitcoinRewards:          true,
			initialEpochReward:         285000000000000,
			decayRate:                  -0.000475,
			genesisEpoch:               0,
			utilizationBonusFactor:     0.5,
			fullCoverageBonusFactor:    1.2,
			partialCoverageBonusFactor: 0.1,
			expectedError:              false,
		},
		{
			name:                       "valid parameters with zero bonus factors",
			useBitcoinRewards:          true,
			initialEpochReward:         100000,
			decayRate:                  -0.0001,
			genesisEpoch:               100,
			utilizationBonusFactor:     0.0,
			fullCoverageBonusFactor:    1.0,
			partialCoverageBonusFactor: 0.0,
			expectedError:              false,
		},
		{
			name:                       "valid parameters with Bitcoin rewards disabled",
			useBitcoinRewards:          false,
			initialEpochReward:         285000000000000,
			decayRate:                  -0.000475,
			genesisEpoch:               0,
			utilizationBonusFactor:     0.5,
			fullCoverageBonusFactor:    1.2,
			partialCoverageBonusFactor: 0.1,
			expectedError:              false,
		},
		{
			name:                       "invalid decay rate (positive)",
			useBitcoinRewards:          true,
			initialEpochReward:         285000000000000,
			decayRate:                  0.000475, // Positive - should be negative
			genesisEpoch:               0,
			utilizationBonusFactor:     0.5,
			fullCoverageBonusFactor:    1.2,
			partialCoverageBonusFactor: 0.1,
			expectedError:              true,
			expectedErrMsg:             "decay rate must be negative",
		},
		{
			name:                       "invalid decay rate (too extreme)",
			useBitcoinRewards:          true,
			initialEpochReward:         285000000000000,
			decayRate:                  -0.02, // Too extreme (less than -0.01)
			genesisEpoch:               0,
			utilizationBonusFactor:     0.5,
			fullCoverageBonusFactor:    1.2,
			partialCoverageBonusFactor: 0.1,
			expectedError:              true,
			expectedErrMsg:             "decay rate too extreme",
		},
		{
			name:                       "invalid utilization bonus factor (negative)",
			useBitcoinRewards:          true,
			initialEpochReward:         285000000000000,
			decayRate:                  -0.000475,
			genesisEpoch:               0,
			utilizationBonusFactor:     -0.1, // Negative - should be non-negative
			fullCoverageBonusFactor:    1.2,
			partialCoverageBonusFactor: 0.1,
			expectedError:              true,
			expectedErrMsg:             "bonus factor cannot be negative",
		},
		{
			name:                       "invalid full coverage bonus factor (negative)",
			useBitcoinRewards:          true,
			initialEpochReward:         285000000000000,
			decayRate:                  -0.000475,
			genesisEpoch:               0,
			utilizationBonusFactor:     0.5,
			fullCoverageBonusFactor:    -1.2, // Negative - should be non-negative
			partialCoverageBonusFactor: 0.1,
			expectedError:              true,
			expectedErrMsg:             "bonus factor cannot be negative",
		},
		{
			name:                       "invalid partial coverage bonus factor (negative)",
			useBitcoinRewards:          true,
			initialEpochReward:         285000000000000,
			decayRate:                  -0.000475,
			genesisEpoch:               0,
			utilizationBonusFactor:     0.5,
			fullCoverageBonusFactor:    1.2,
			partialCoverageBonusFactor: -0.1, // Negative - should be non-negative
			expectedError:              true,
			expectedErrMsg:             "bonus factor cannot be negative",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			params := *types.DefaultBitcoinRewardParams()
			params.UseBitcoinRewards = tc.useBitcoinRewards
			params.InitialEpochReward = tc.initialEpochReward
			params.DecayRate = types.DecimalFromFloat(tc.decayRate)
			params.GenesisEpoch = tc.genesisEpoch
			params.UtilizationBonusFactor = types.DecimalFromFloat(tc.utilizationBonusFactor)
			params.FullCoverageBonusFactor = types.DecimalFromFloat(tc.fullCoverageBonusFactor)
			params.PartialCoverageBonusFactor = types.DecimalFromFloat(tc.partialCoverageBonusFactor)

			err := params.Validate()

			if tc.expectedError {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.expectedErrMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestBitcoinRewardParamsNilFieldChecks(t *testing.T) {
	testCases := []struct {
		name           string
		setupParams    func() *types.BitcoinRewardParams
		expectedErrMsg string
	}{
		{
			name: "nil DecayRate",
			setupParams: func() *types.BitcoinRewardParams {
				params := types.DefaultBitcoinRewardParams()
				params.DecayRate = nil
				return params
			},
			expectedErrMsg: "decay rate cannot be nil",
		},
		{
			name: "nil UtilizationBonusFactor",
			setupParams: func() *types.BitcoinRewardParams {
				params := types.DefaultBitcoinRewardParams()
				params.UtilizationBonusFactor = nil
				return params
			},
			expectedErrMsg: "utilization bonus factor cannot be nil",
		},
		{
			name: "nil FullCoverageBonusFactor",
			setupParams: func() *types.BitcoinRewardParams {
				params := types.DefaultBitcoinRewardParams()
				params.FullCoverageBonusFactor = nil
				return params
			},
			expectedErrMsg: "full coverage bonus factor cannot be nil",
		},
		{
			name: "nil PartialCoverageBonusFactor",
			setupParams: func() *types.BitcoinRewardParams {
				params := types.DefaultBitcoinRewardParams()
				params.PartialCoverageBonusFactor = nil
				return params
			},
			expectedErrMsg: "partial coverage bonus factor cannot be nil",
		},
		{
			name: "valid BitcoinRewardParams",
			setupParams: func() *types.BitcoinRewardParams {
				return types.DefaultBitcoinRewardParams()
			},
			expectedErrMsg: "", // No error expected
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			params := tc.setupParams()
			err := params.Validate()

			if tc.expectedErrMsg == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.expectedErrMsg)
			}
		})
	}
}
