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
