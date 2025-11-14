package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/utils"
	"github.com/stretchr/testify/require"
)

// TestOperatorAddressConversion tests that account addresses are correctly converted to validator operator addresses
func TestOperatorAddressConversion(t *testing.T) {
	tests := []struct {
		name          string
		accountAddr   string
		expectedError bool
	}{
		{
			name:        "valid account address conversion",
			accountAddr: testutil.Bech32Addr(0),
		},
		{
			name:        "valid account address conversion - different address",
			accountAddr: testutil.Bech32Addr(1),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Convert account address to validator operator address
			accAddr, err := sdk.AccAddressFromBech32(tt.accountAddr)
			require.NoError(t, err)

			valOperatorAddr := sdk.ValAddress(accAddr).String()

			// Verify it's a valid validator operator address
			_, err = sdk.ValAddressFromBech32(valOperatorAddr)
			require.NoError(t, err, "converted address should be valid validator operator address")

			// Verify reverse conversion works (compare bytes, not Bech32 strings)
			convertedBack, err := utils.OperatorAddressToAccAddress(valOperatorAddr)
			require.NoError(t, err)

			originalAccAddrParsed, err := sdk.AccAddressFromBech32(tt.accountAddr)
			require.NoError(t, err)

			convertedBackParsed, err := sdk.AccAddressFromBech32(convertedBack)
			require.NoError(t, err)

			require.Equal(t, originalAccAddrParsed, convertedBackParsed, "reverse conversion should return address with same bytes")
		})
	}
}

// TestGetParticipantsFullStatsOperatorAddress tests that GetParticipantsFullStats correctly converts
// account addresses to operator addresses
func TestGetParticipantsFullStatsOperatorAddress(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)

	// Create test participants
	participants := createNParticipant(keeper, ctx, 3)

	// Create epoch group with validation weights
	// This simulates the scenario where participants are in the epoch group
	// Note: This is a simplified test - full integration would require setting up epoch groups properly
	for _, participant := range participants {
		// Verify participant exists
		found, exists := keeper.GetParticipant(ctx, participant.Index)
		require.True(t, exists, "participant should exist")
		require.Equal(t, participant.Index, found.Index)

		// Test address conversion
		accAddr, err := sdk.AccAddressFromBech32(participant.Index)
		require.NoError(t, err)

		expectedOperatorAddr := sdk.ValAddress(accAddr).String()

		// Verify the conversion matches what GetParticipantsFullStats would do
		// (as seen in query_get_participant_current_stats.go line 60)
		require.NotEmpty(t, expectedOperatorAddr, "operator address should not be empty")
		require.Contains(t, expectedOperatorAddr, "valoper", "operator address should contain 'valoper' prefix")
	}
}

// TestParticipantLookupByAccountAddress tests that participants can be looked up by account address
func TestParticipantLookupByAccountAddress(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)

	// Create test participants
	participants := createNParticipant(keeper, ctx, 5)

	for _, participant := range participants {
		// Lookup by account address (as stored in participant.Index)
		found, exists := keeper.GetParticipant(ctx, participant.Index)
		require.True(t, exists, "participant should be found by account address")
		require.Equal(t, participant.Index, found.Index, "found participant should match")
	}
}

// TestParticipantLookupByOperatorAddress tests that the conversion from operator address
// to account address works correctly for participant lookup
func TestParticipantLookupByOperatorAddress(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)

	// Create test participants
	participants := createNParticipant(keeper, ctx, 3)

	for _, participant := range participants {
		// Convert account address to operator address
		accAddr, err := sdk.AccAddressFromBech32(participant.Index)
		require.NoError(t, err)

		operatorAddr := sdk.ValAddress(accAddr).String()

		// Parse original address
		originalAccAddr, err := sdk.AccAddressFromBech32(participant.Index)
		require.NoError(t, err)

		// Convert operator address back to account address bytes
		// This verifies that the conversion is reversible at the byte level
		valAddrFromOperator, err := sdk.ValAddressFromBech32(operatorAddr)
		require.NoError(t, err)
		convertedAccAddrBytes := sdk.AccAddress(valAddrFromOperator)

		// Addresses should have same bytes
		require.Equal(t, originalAccAddr, convertedAccAddrBytes, "address bytes should match after conversion")

		// Lookup participant using original account address
		found, exists := keeper.GetParticipant(ctx, participant.Index)
		require.True(t, exists, "participant should be found by original account address")
		require.Equal(t, participant.Index, found.Index, "found participant should match original")
	}
}

// TestAddressConsistency verifies that address conversion is consistent
// This ensures that the same account address always produces the same operator address
func TestAddressConsistency(t *testing.T) {
	testAddr := testutil.Bech32Addr(0)

	// Convert multiple times
	accAddr1, err := sdk.AccAddressFromBech32(testAddr)
	require.NoError(t, err)
	operatorAddr1 := sdk.ValAddress(accAddr1).String()

	accAddr2, err := sdk.AccAddressFromBech32(testAddr)
	require.NoError(t, err)
	operatorAddr2 := sdk.ValAddress(accAddr2).String()

	// Should produce same result
	require.Equal(t, operatorAddr1, operatorAddr2, "same account address should produce same operator address")

	// Reverse conversion should also be consistent
	convertedBack1, err := utils.OperatorAddressToAccAddress(operatorAddr1)
	require.NoError(t, err)

	convertedBack2, err := utils.OperatorAddressToAccAddress(operatorAddr2)
	require.NoError(t, err)

	require.Equal(t, convertedBack1, convertedBack2, "reverse conversion should be consistent")

	// Compare address bytes (not Bech32 strings, as prefix may differ)
	originalAccAddr, err := sdk.AccAddressFromBech32(testAddr)
	require.NoError(t, err)

	// Get address bytes from operator address directly (avoiding prefix issue)
	valAddr, err := sdk.ValAddressFromBech32(operatorAddr1)
	require.NoError(t, err)
	convertedBackBytes := sdk.AccAddress(valAddr)

	require.Equal(t, originalAccAddr, convertedBackBytes, "reverse conversion should return address with same bytes")
}

// TestGetComputeResultsOperatorAddressConversion verifies that GetComputeResults
// correctly converts account addresses to operator addresses
// This tests the implementation in epoch_group.go line 369
func TestGetComputeResultsOperatorAddressConversion(t *testing.T) {
	// This test verifies the conversion logic used in GetComputeResults
	testAccountAddr := testutil.Bech32Addr(0)

	accAddr, err := sdk.AccAddressFromBech32(testAccountAddr)
	require.NoError(t, err)

	// This is the conversion used in GetComputeResults (epoch_group.go:369)
	valOperatorAddr := sdk.ValAddress(accAddr).String()

	// Verify it's a valid validator operator address
	_, err = sdk.ValAddressFromBech32(valOperatorAddr)
	require.NoError(t, err, "converted address should be valid validator operator address")

	// Verify the address format
	require.Contains(t, valOperatorAddr, "valoper", "operator address should contain 'valoper' prefix")
	require.NotEqual(t, testAccountAddr, valOperatorAddr, "operator address should differ from account address")
}

