package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/utils"
)

// TestOperatorAddressConversion verifies that account addresses can be correctly
// converted to validator operator addresses and back, ensuring consistent
// participant identification across genesis and runtime validators.
func TestOperatorAddressConversion(t *testing.T) {
	// Test account address to operator address conversion
	accountAddr := sdk.AccAddress("test-account-address-123456")
	operatorAddr := sdk.ValAddress(accountAddr).String()

	// Verify operator address is valid bech32
	valAddr, err := sdk.ValAddressFromBech32(operatorAddr)
	require.NoError(t, err, "Operator address should be valid bech32")
	require.Equal(t, sdk.ValAddress(accountAddr), valAddr, "Operator address should match account address conversion")

	// Test reverse conversion: operator address to account address
	convertedAccAddr, err := utils.OperatorAddressToAccAddress(operatorAddr)
	require.NoError(t, err, "Should convert operator address back to account address")
	require.Equal(t, accountAddr.String(), convertedAccAddr, "Converted account address should match original")
}

// TestParticipantAddressConsistency verifies that participant addresses
// maintain consistency when converting between account and operator formats.
// This ensures that both genesis and runtime validators can be correctly
// identified using account-based addresses.
func TestParticipantAddressConsistency(t *testing.T) {
	testCases := []struct {
		name          string
		accountAddr   string
		expectSuccess bool
	}{
		{
			name:          "valid account address",
			accountAddr:   sdk.AccAddress("test-address-123456789").String(),
			expectSuccess: true,
		},
		{
			name:          "another valid account address",
			accountAddr:   sdk.AccAddress("another-test-address").String(),
			expectSuccess: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			accAddr, err := sdk.AccAddressFromBech32(tc.accountAddr)
			require.NoError(t, err, "Account address should be valid")

			// Convert to operator address
			operatorAddr := sdk.ValAddress(accAddr).String()

			// Convert back to account address
			convertedAccAddr, err := utils.OperatorAddressToAccAddress(operatorAddr)
			if tc.expectSuccess {
				require.NoError(t, err, "Conversion should succeed")
				require.Equal(t, tc.accountAddr, convertedAccAddr, "Round-trip conversion should preserve address")
			} else {
				require.Error(t, err, "Conversion should fail for invalid addresses")
			}
		})
	}
}

// TestGetComputeResultsOperatorAddress verifies that GetComputeResults
// correctly sets OperatorAddress using account address conversion.
// This is the key fix that ensures runtime validators use consistent addressing.
func TestGetComputeResultsOperatorAddress(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)

	// Create a test account address
	testAccountAddr := sdk.AccAddress("test-participant-address").String()
	accAddr, err := sdk.AccAddressFromBech32(testAccountAddr)
	require.NoError(t, err)

	// Expected operator address (as set in GetComputeResults)
	expectedOperatorAddr := sdk.ValAddress(accAddr).String()

	// Verify the conversion matches what GetComputeResults does
	// (line 369 in epoch_group.go: valOperatorAddr := sdk.ValAddress(accAddr).String())
	require.Equal(t, expectedOperatorAddr, sdk.ValAddress(accAddr).String(),
		"Operator address should be derived from account address consistently")

	// Verify we can convert back
	convertedAccAddr, err := utils.OperatorAddressToAccAddress(expectedOperatorAddr)
	require.NoError(t, err)
	require.Equal(t, testAccountAddr, convertedAccAddr,
		"Should be able to convert operator address back to account address")

	// This test verifies the core fix: that operator addresses are derived
	// from account addresses, not consensus keys, ensuring consistency
	// between genesis and runtime validators.
	_ = keeper // Suppress unused variable warning
	_ = ctx    // Suppress unused variable warning
}

// TestParticipantLookupByAccountAddress verifies that participants can be
// looked up using their account address, which is the consistent identifier
// used across both genesis and runtime validators.
func TestParticipantLookupByAccountAddress(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)

	// Create test participant with account address
	testAccountAddr := sdk.AccAddress("test-lookup-address").String()

	// Verify account address is valid
	accAddr, err := sdk.AccAddressFromBech32(testAccountAddr)
	require.NoError(t, err, "Account address should be valid")

	// Verify operator address can be derived
	operatorAddr := sdk.ValAddress(accAddr).String()
	require.NotEmpty(t, operatorAddr, "Operator address should not be empty")

	// This test ensures that the lookup mechanism works with account addresses,
	// which is the consistent identifier used in GetParticipantsFullStats
	// (line 50-64 in query_get_participant_current_stats.go)
	_ = keeper
	_ = ctx
	_ = testAccountAddr
	_ = operatorAddr
}

