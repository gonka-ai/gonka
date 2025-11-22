package keeper_test

import (
	"testing"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/bls/types"
)

func TestAssignSlots(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	tests := []struct {
		name          string
		participants  []types.ParticipantWithWeightAndKey
		totalSlots    uint32
		expectedSlots []struct {
			address string
			start   uint32
			end     uint32
			count   uint32
		}
		expectError bool
	}{
		{
			name: "Equal weights - 3 participants, 100 slots",
			participants: []types.ParticipantWithWeightAndKey{
				{
					Address:            "cosmos1alice",
					PercentageWeight:   math.LegacyNewDec(33),
					Secp256k1PublicKey: []byte("alice_key"),
				},
				{
					Address:            "cosmos1bob",
					PercentageWeight:   math.LegacyNewDec(33),
					Secp256k1PublicKey: []byte("bob_key"),
				},
				{
					Address:            "cosmos1charlie",
					PercentageWeight:   math.LegacyNewDec(34), // Last gets remainder
					Secp256k1PublicKey: []byte("charlie_key"),
				},
			},
			totalSlots: 100,
			expectedSlots: []struct {
				address string
				start   uint32
				end     uint32
				count   uint32
			}{
				{"cosmos1alice", 0, 32, 33},    // 33/100 * 100 = 33 slots
				{"cosmos1bob", 33, 65, 33},     // 33/100 * 100 = 33 slots
				{"cosmos1charlie", 66, 99, 34}, // Remaining 34 slots
			},
		},
		{
			name: "Unequal weights - realistic scenario",
			participants: []types.ParticipantWithWeightAndKey{
				{
					Address:            "cosmos1validator1",
					PercentageWeight:   math.LegacyNewDec(50),
					Secp256k1PublicKey: []byte("val1_key"),
				},
				{
					Address:            "cosmos1validator2",
					PercentageWeight:   math.LegacyNewDec(30),
					Secp256k1PublicKey: []byte("val2_key"),
				},
				{
					Address:            "cosmos1validator3",
					PercentageWeight:   math.LegacyNewDec(20),
					Secp256k1PublicKey: []byte("val3_key"),
				},
			},
			totalSlots: 100,
			expectedSlots: []struct {
				address string
				start   uint32
				end     uint32
				count   uint32
			}{
				{"cosmos1validator1", 0, 49, 50},  // 50/100 * 100 = 50 slots
				{"cosmos1validator2", 50, 79, 30}, // 30/100 * 100 = 30 slots
				{"cosmos1validator3", 80, 99, 20}, // 20/100 * 100 = 20 slots
			},
		},
		{
			name: "Small slot count with rounding",
			participants: []types.ParticipantWithWeightAndKey{
				{
					Address:            "cosmos1alice",
					PercentageWeight:   math.LegacyNewDec(33),
					Secp256k1PublicKey: []byte("alice_key"),
				},
				{
					Address:            "cosmos1bob",
					PercentageWeight:   math.LegacyNewDec(33),
					Secp256k1PublicKey: []byte("bob_key"),
				},
				{
					Address:            "cosmos1charlie",
					PercentageWeight:   math.LegacyNewDec(34),
					Secp256k1PublicKey: []byte("charlie_key"),
				},
			},
			totalSlots: 10,
			expectedSlots: []struct {
				address string
				start   uint32
				end     uint32
				count   uint32
			}{
				{"cosmos1alice", 0, 2, 3},   // 33/100 * 10 = 3.3 → 3 slots
				{"cosmos1bob", 3, 5, 3},     // 33/100 * 10 = 3.3 → 3 slots
				{"cosmos1charlie", 6, 9, 4}, // Remaining 4 slots
			},
		},
		{
			name: "Single participant",
			participants: []types.ParticipantWithWeightAndKey{
				{
					Address:            "cosmos1solo",
					PercentageWeight:   math.LegacyNewDec(100),
					Secp256k1PublicKey: []byte("solo_key"),
				},
			},
			totalSlots: 50,
			expectedSlots: []struct {
				address string
				start   uint32
				end     uint32
				count   uint32
			}{
				{"cosmos1solo", 0, 49, 50}, // Gets all slots
			},
		},
		{
			name:         "Empty participants",
			participants: []types.ParticipantWithWeightAndKey{},
			totalSlots:   100,
			expectError:  true,
		},
		{
			name: "Zero total weight",
			participants: []types.ParticipantWithWeightAndKey{
				{
					Address:            "cosmos1zero",
					PercentageWeight:   math.LegacyZeroDec(),
					Secp256k1PublicKey: []byte("zero_key"),
				},
			},
			totalSlots:  100,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := k.AssignSlots(ctx, tt.participants, tt.totalSlots)

			if tt.expectError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.Len(t, result, len(tt.expectedSlots))

			// Verify slot assignments
			totalAssignedSlots := uint32(0)
			for i, expected := range tt.expectedSlots {
				participant := result[i]

				require.Equal(t, expected.address, participant.Address)
				require.Equal(t, expected.start, participant.SlotStartIndex)
				require.Equal(t, expected.end, participant.SlotEndIndex)

				actualCount := participant.SlotEndIndex - participant.SlotStartIndex + 1
				require.Equal(t, expected.count, actualCount)

				totalAssignedSlots += actualCount

				// Verify secp256k1 key is preserved
				require.NotEmpty(t, participant.Secp256K1PublicKey)

				// Verify percentage weight is preserved
				require.True(t, participant.PercentageWeight.Equal(tt.participants[i].PercentageWeight))
			}

			// Verify all slots are assigned
			require.Equal(t, tt.totalSlots, totalAssignedSlots)

			// Verify no overlapping slots
			for i := 0; i < len(result)-1; i++ {
				require.Equal(t, result[i].SlotEndIndex+1, result[i+1].SlotStartIndex,
					"Slot ranges should be contiguous without gaps or overlaps")
			}

			// Verify first slot starts at 0
			if len(result) > 0 {
				require.Equal(t, uint32(0), result[0].SlotStartIndex)
			}

			// Verify last slot ends at totalSlots-1
			if len(result) > 0 {
				require.Equal(t, tt.totalSlots-1, result[len(result)-1].SlotEndIndex)
			}
		})
	}
}

func TestAssignSlotsWithDecimalWeights(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	// Test with decimal weights that don't sum to a round number
	participants := []types.ParticipantWithWeightAndKey{
		{
			Address:            "cosmos1alice",
			PercentageWeight:   math.LegacyMustNewDecFromStr("33.333"),
			Secp256k1PublicKey: []byte("alice_key"),
		},
		{
			Address:            "cosmos1bob",
			PercentageWeight:   math.LegacyMustNewDecFromStr("33.333"),
			Secp256k1PublicKey: []byte("bob_key"),
		},
		{
			Address:            "cosmos1charlie",
			PercentageWeight:   math.LegacyMustNewDecFromStr("33.334"),
			Secp256k1PublicKey: []byte("charlie_key"),
		},
	}

	result, err := k.AssignSlots(ctx, participants, 1000)
	require.NoError(t, err)
	require.Len(t, result, 3)

	// Verify all slots are assigned
	totalSlots := uint32(0)
	for _, p := range result {
		totalSlots += p.SlotEndIndex - p.SlotStartIndex + 1
	}
	require.Equal(t, uint32(1000), totalSlots)

	// Verify contiguous assignment
	require.Equal(t, uint32(0), result[0].SlotStartIndex)
	require.Equal(t, result[0].SlotEndIndex+1, result[1].SlotStartIndex)
	require.Equal(t, result[1].SlotEndIndex+1, result[2].SlotStartIndex)
	require.Equal(t, uint32(999), result[2].SlotEndIndex)
}

func TestAssignSlotsEnsuresMinimumSlotForNonZeroWeight(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	participants := []types.ParticipantWithWeightAndKey{
		{
			Address:            "cosmos1guardian",
			PercentageWeight:   math.LegacyMustNewDecFromStr("98.5"),
			Secp256k1PublicKey: []byte("guardian_key"),
		},
		{
			Address:            "cosmos1small1",
			PercentageWeight:   math.LegacyMustNewDecFromStr("0.5"),
			Secp256k1PublicKey: []byte("small1_key"),
		},
		{
			Address:            "cosmos1small2",
			PercentageWeight:   math.LegacyMustNewDecFromStr("0.5"),
			Secp256k1PublicKey: []byte("small2_key"),
		},
		{
			Address:            "cosmos1small3",
			PercentageWeight:   math.LegacyMustNewDecFromStr("0.5"),
			Secp256k1PublicKey: []byte("small3_key"),
		},
	}

	result, err := k.AssignSlots(ctx, participants, 100)
	require.NoError(t, err)
	require.Len(t, result, 4)

	slotsByAddress := make(map[string]uint32)
	for _, participant := range result {
		slotsByAddress[participant.Address] = participant.SlotEndIndex - participant.SlotStartIndex + 1
	}

	require.Equal(t, uint32(97), slotsByAddress["cosmos1guardian"])
	require.Equal(t, uint32(1), slotsByAddress["cosmos1small1"])
	require.Equal(t, uint32(1), slotsByAddress["cosmos1small2"])
	require.Equal(t, uint32(1), slotsByAddress["cosmos1small3"])
}

func TestAssignSlotsWithMoreParticipantsThanSlots(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	// Create 10 participants with varying weights
	participants := []types.ParticipantWithWeightAndKey{
		{
			Address:            "cosmos1addr01",
			PercentageWeight:   math.LegacyNewDec(25), // Top 1
			Secp256k1PublicKey: []byte("key01"),
		},
		{
			Address:            "cosmos1addr02",
			PercentageWeight:   math.LegacyNewDec(20), // Top 2
			Secp256k1PublicKey: []byte("key02"),
		},
		{
			Address:            "cosmos1addr03",
			PercentageWeight:   math.LegacyNewDec(15), // Top 3
			Secp256k1PublicKey: []byte("key03"),
		},
		{
			Address:            "cosmos1addr04",
			PercentageWeight:   math.LegacyNewDec(10), // Top 4 (tie)
			Secp256k1PublicKey: []byte("key04"),
		},
		{
			Address:            "cosmos1addr05",
			PercentageWeight:   math.LegacyNewDec(10), // Top 5 (tie, selected by address)
			Secp256k1PublicKey: []byte("key05"),
		},
		{
			Address:            "cosmos1addr06",
			PercentageWeight:   math.LegacyNewDec(10), // Excluded (tie, but address is higher)
			Secp256k1PublicKey: []byte("key06"),
		},
		{
			Address:            "cosmos1addr07",
			PercentageWeight:   math.LegacyNewDec(5), // Excluded
			Secp256k1PublicKey: []byte("key07"),
		},
		{
			Address:            "cosmos1addr08",
			PercentageWeight:   math.LegacyNewDec(3), // Excluded
			Secp256k1PublicKey: []byte("key08"),
		},
		{
			Address:            "cosmos1addr09",
			PercentageWeight:   math.LegacyNewDec(1), // Excluded
			Secp256k1PublicKey: []byte("key09"),
		},
		{
			Address:            "cosmos1addr10",
			PercentageWeight:   math.LegacyNewDec(1), // Excluded
			Secp256k1PublicKey: []byte("key10"),
		},
	}

	// Set totalSlots to 5, so only top 5 by weight should be selected
	result, err := k.AssignSlots(ctx, participants, 5)
	require.NoError(t, err)

	// Should only have 5 participants selected
	require.Len(t, result, 5)

	// Verify the top 5 by weight were selected
	expectedAddresses := []string{
		"cosmos1addr01", // 25%
		"cosmos1addr02", // 20%
		"cosmos1addr03", // 15%
		"cosmos1addr04", // 10%
		"cosmos1addr05", // 10%
	}

	selectedAddresses := make([]string, len(result))
	for i, participant := range result {
		selectedAddresses[i] = participant.Address
	}

	// Verify correct participants were selected (order may vary based on slot assignment)
	for _, expectedAddr := range expectedAddresses {
		found := false
		for _, actualAddr := range selectedAddresses {
			if actualAddr == expectedAddr {
				found = true
				break
			}
		}
		require.True(t, found, "Expected participant %s to be selected", expectedAddr)
	}

	// Verify addr06 was NOT selected (even though it has same weight as addr04 and addr05,
	// it should be excluded because address is lexicographically higher)
	for _, participant := range result {
		require.NotEqual(t, "cosmos1addr06", participant.Address, "cosmos1addr06 should not be selected")
	}

	// Verify all 5 slots are assigned
	totalSlots := uint32(0)
	for _, participant := range result {
		totalSlots += participant.SlotEndIndex - participant.SlotStartIndex + 1
	}
	require.Equal(t, uint32(5), totalSlots)

	// Verify contiguous slot assignment
	require.Equal(t, uint32(0), result[0].SlotStartIndex)
	for i := 0; i < len(result)-1; i++ {
		require.Equal(t, result[i].SlotEndIndex+1, result[i+1].SlotStartIndex,
			"Slot ranges should be contiguous")
	}
	require.Equal(t, uint32(4), result[len(result)-1].SlotEndIndex)
}

func TestAssignSlotsWithMoreParticipantsThanSlotsDeterminism(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	// Create participants with identical weights to test deterministic tiebreaking
	participants := []types.ParticipantWithWeightAndKey{
		{
			Address:            "cosmos1zzzz",
			PercentageWeight:   math.LegacyNewDec(10),
			Secp256k1PublicKey: []byte("key_zzzz"),
		},
		{
			Address:            "cosmos1aaaa",
			PercentageWeight:   math.LegacyNewDec(10),
			Secp256k1PublicKey: []byte("key_aaaa"),
		},
		{
			Address:            "cosmos1mmmm",
			PercentageWeight:   math.LegacyNewDec(10),
			Secp256k1PublicKey: []byte("key_mmmm"),
		},
	}

	// Only 2 slots available
	result, err := k.AssignSlots(ctx, participants, 2)
	require.NoError(t, err)
	require.Len(t, result, 2)

	// With identical weights, should select by lexicographic address order
	// cosmos1aaaa and cosmos1mmmm should be selected (not cosmos1zzzz)
	addresses := []string{result[0].Address, result[1].Address}
	require.Contains(t, addresses, "cosmos1aaaa", "cosmos1aaaa should be selected (lowest address)")
	require.Contains(t, addresses, "cosmos1mmmm", "cosmos1mmmm should be selected (second lowest address)")
	require.NotContains(t, addresses, "cosmos1zzzz", "cosmos1zzzz should be excluded (highest address)")

	// Run the same test again to ensure determinism
	result2, err := k.AssignSlots(ctx, participants, 2)
	require.NoError(t, err)
	require.Len(t, result2, 2)

	// Results should be identical
	for i := range result {
		require.Equal(t, result[i].Address, result2[i].Address)
		require.Equal(t, result[i].SlotStartIndex, result2[i].SlotStartIndex)
		require.Equal(t, result[i].SlotEndIndex, result2[i].SlotEndIndex)
	}
}
