package keeper

import (
	"fmt"
	"math/big"
	"testing"

	"cosmossdk.io/log"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// createTestLogger creates a logger for testing
func createTestLogger(t *testing.T) log.Logger {
	return log.NewTestLogger(t)
}

// createTestValidationWeight creates a ValidationWeight with proper MLNode structure for testing
func createTestValidationWeight(memberAddress string, weight int64, reputation int32) *types.ValidationWeight {
	return &types.ValidationWeight{
		MemberAddress:      memberAddress,
		Weight:             weight,
		Reputation:         reputation,
		ConfirmationWeight: weight, // For tests, assume all weight is confirmed
		MlNodes: []*types.MLNodeInfo{
			{
				NodeId:             memberAddress + "-node",
				PocWeight:          weight,
				TimeslotAllocation: []bool{true, false}, // POC_SLOT=false
			},
		},
	}
}

func TestCalculateFixedEpochReward(t *testing.T) {
	// Test parameters matching Bitcoin proposal defaults
	initialReward := uint64(285000000000000)
	decayRate := types.DecimalFromFloat(-0.000475) // Halving every ~1460 epochs (4 years)

	t.Run("Zero epochs returns initial reward", func(t *testing.T) {
		result := CalculateFixedEpochReward(0, initialReward, decayRate)
		require.Equal(t, initialReward, result)
	})

	t.Run("Reward decreases with positive epochs", func(t *testing.T) {
		result100 := CalculateFixedEpochReward(100, initialReward, decayRate)
		result200 := CalculateFixedEpochReward(200, initialReward, decayRate)
		result500 := CalculateFixedEpochReward(500, initialReward, decayRate)

		// Each subsequent epoch should have lower rewards due to negative decay rate
		require.Less(t, result100, initialReward, "100 epochs should have lower reward than initial")
		require.Less(t, result200, result100, "200 epochs should have lower reward than 100 epochs")
		require.Less(t, result500, result200, "500 epochs should have lower reward than 200 epochs")
	})

	t.Run("Approximate halving after 1460 epochs", func(t *testing.T) {
		// After ~1460 epochs, reward should be approximately half of initial
		result1460 := CalculateFixedEpochReward(1460, initialReward, decayRate)
		expectedHalf := initialReward / 2

		// Allow 5% tolerance for exponential calculation precision
		tolerance := expectedHalf / 20 // 5% tolerance
		require.InDelta(t, expectedHalf, result1460, float64(tolerance), "Reward should approximately halve after 1460 epochs")
	})

	t.Run("Edge case: zero initial reward", func(t *testing.T) {
		result := CalculateFixedEpochReward(100, 0, decayRate)
		require.Equal(t, uint64(0), result)
	})

	t.Run("Edge case: nil decay rate", func(t *testing.T) {
		result := CalculateFixedEpochReward(100, initialReward, nil)
		require.Equal(t, initialReward, result, "Nil decay rate should return initial reward")
	})

	t.Run("Edge case: very large epochs", func(t *testing.T) {
		// After many epochs, reward should approach 0
		result := CalculateFixedEpochReward(10000, initialReward, decayRate)
		// After 10,000 epochs: exp(-0.000475 * 10000) ≈ 0.0086
		// Expected: 285,000,000,000,000 * 0.0086 ≈ 2,451,000,000,000
		require.Less(t, result, uint64(3000000000000), "After 10000 epochs, reward should be very small relative to initial")
		require.Greater(t, result, uint64(2000000000000), "But should still have some value due to gradual decay")
	})

	t.Run("Positive decay rate increases reward", func(t *testing.T) {
		positiveDecayRate := types.DecimalFromFloat(0.0001) // Small positive rate
		result := CalculateFixedEpochReward(100, initialReward, positiveDecayRate)
		require.Greater(t, result, initialReward, "Positive decay rate should increase reward")
	})
}

func TestGetParticipantPoCWeight(t *testing.T) {
	// Create test epoch group data with validation weights
	epochGroupData := &types.EpochGroupData{
		ValidationWeights: []*types.ValidationWeight{
			createTestValidationWeight("participant1", 1000, 100),
			createTestValidationWeight("participant2", 2500, 150),
			{
				MemberAddress:      "participant3",
				Weight:             0, // Zero weight participant
				Reputation:         50,
				ConfirmationWeight: 0,
				MlNodes: []*types.MLNodeInfo{
					{
						NodeId:             "participant3-node",
						PocWeight:          0,
						TimeslotAllocation: []bool{true, false},
					},
				},
			},
			{
				MemberAddress: "participant4",
				Weight:        -100, // Negative weight participant
				Reputation:    75,
			},
		},
	}

	t.Run("Valid participant returns correct weight", func(t *testing.T) {
		weight := GetParticipantPoCWeight("participant1", epochGroupData)
		require.Equal(t, uint64(1000), weight)

		weight2 := GetParticipantPoCWeight("participant2", epochGroupData)
		require.Equal(t, uint64(2500), weight2)
	})

	t.Run("Zero weight participant returns zero", func(t *testing.T) {
		weight := GetParticipantPoCWeight("participant3", epochGroupData)
		require.Equal(t, uint64(0), weight)
	})

	t.Run("Negative weight participant returns zero", func(t *testing.T) {
		weight := GetParticipantPoCWeight("participant4", epochGroupData)
		require.Equal(t, uint64(0), weight)
	})

	t.Run("Non-existent participant returns zero", func(t *testing.T) {
		weight := GetParticipantPoCWeight("nonexistent", epochGroupData)
		require.Equal(t, uint64(0), weight)
	})

	t.Run("Empty participant address returns zero", func(t *testing.T) {
		weight := GetParticipantPoCWeight("", epochGroupData)
		require.Equal(t, uint64(0), weight)
	})

	t.Run("Nil epoch group data returns zero", func(t *testing.T) {
		weight := GetParticipantPoCWeight("participant1", nil)
		require.Equal(t, uint64(0), weight)
	})

	t.Run("Empty validation weights returns zero", func(t *testing.T) {
		emptyEpochData := &types.EpochGroupData{
			ValidationWeights: []*types.ValidationWeight{},
		}
		weight := GetParticipantPoCWeight("participant1", emptyEpochData)
		require.Equal(t, uint64(0), weight)
	})
}

func TestCalculateParticipantBitcoinRewards(t *testing.T) {
	// Setup test data
	bitcoinParams := &types.BitcoinRewardParams{
		InitialEpochReward: 285000000000000,
		DecayRate:          types.DecimalFromFloat(-0.000475),
		GenesisEpoch:       1,
	}

	// Create epoch group data with validation weights and MLNodes
	epochGroupData := &types.EpochGroupData{
		EpochIndex: 100, // 99 epochs since genesis (epochsSinceGenesis = 100 - 1)
		ValidationWeights: []*types.ValidationWeight{
			{
				MemberAddress:      "participant1",
				Weight:             1000,
				Reputation:         100,
				ConfirmationWeight: 1000, // All weight confirmed (no split for these tests)
				MlNodes: []*types.MLNodeInfo{
					{
						NodeId:             "node1",
						PocWeight:          1000,
						TimeslotAllocation: []bool{true, false}, // POC_SLOT=false
					},
				},
			},
			{
				MemberAddress:      "participant2",
				Weight:             2000, // 50% weight - tests power capping to 30%
				Reputation:         150,
				ConfirmationWeight: 2000,
				MlNodes: []*types.MLNodeInfo{
					{
						NodeId:             "node2",
						PocWeight:          2000,
						TimeslotAllocation: []bool{true, false},
					},
				},
			},
			{
				MemberAddress:      "participant3",
				Weight:             1000,
				Reputation:         120,
				ConfirmationWeight: 1000,
				MlNodes: []*types.MLNodeInfo{
					{
						NodeId:             "node3",
						PocWeight:          1000,
						TimeslotAllocation: []bool{true, false},
					},
				},
			},
		},
	}

	// Create test participants
	participants := []types.Participant{
		{
			Address:     "participant1",
			CoinBalance: 500, // WorkCoins from user fees
			Status:      types.ParticipantStatus_ACTIVE,
			CurrentEpochStats: &types.CurrentEpochStats{
				InferenceCount: 100,
				MissedRequests: 0,
			},
		},
		{
			Address:     "participant2",
			CoinBalance: 1000, // WorkCoins from user fees
			Status:      types.ParticipantStatus_ACTIVE,
			CurrentEpochStats: &types.CurrentEpochStats{
				InferenceCount: 100,
				MissedRequests: 0,
			},
		},
		{
			Address:     "participant3",
			CoinBalance: 750, // WorkCoins from user fees
			Status:      types.ParticipantStatus_ACTIVE,
			CurrentEpochStats: &types.CurrentEpochStats{
				InferenceCount: 100,
				MissedRequests: 0,
			},
		},
	}

	t.Run("Successful Bitcoin reward distribution", func(t *testing.T) {
		logger := createTestLogger(t)
		results, bitcoinResult, err := CalculateParticipantBitcoinRewards(participants, epochGroupData, bitcoinParams, nil, nil, logger)
		require.NoError(t, err)
		require.Equal(t, 3, len(results))

		// Verify BitcoinResult
		require.Greater(t, bitcoinResult.Amount, int64(0))
		require.Equal(t, uint64(100), bitcoinResult.EpochNumber)
		require.True(t, bitcoinResult.DecayApplied) // Since epoch > genesis

		// Calculate expected rewards with power capping
		// Apply power capping to verify the algorithm works correctly
		uncappedWeights := []*types.ActiveParticipant{
			{Index: "participant1", Weight: 1000},
			{Index: "participant2", Weight: 2000}, // 50% - should be capped
			{Index: "participant3", Weight: 1000},
		}
		cappedWeights, wasCapped := ApplyPowerCappingForWeights(uncappedWeights)
		require.True(t, wasCapped, "Power capping should be applied when participant2 has 50%")

		// Calculate total after capping
		totalPoCWeightAfterCapping := uint64(0)
		for _, p := range cappedWeights {
			totalPoCWeightAfterCapping += uint64(p.Weight)
		}

		expectedEpochReward := CalculateFixedEpochReward(99, 285000000000000, bitcoinParams.DecayRate)
		require.Equal(t, int64(expectedEpochReward), bitcoinResult.Amount)

		// Calculate base rewards using actual capped weights
		expectedP1Base := (uint64(cappedWeights[0].Weight) * expectedEpochReward) / totalPoCWeightAfterCapping
		expectedP2Base := (uint64(cappedWeights[1].Weight) * expectedEpochReward) / totalPoCWeightAfterCapping
		expectedP3Base := (uint64(cappedWeights[2].Weight) * expectedEpochReward) / totalPoCWeightAfterCapping

		// Calculate remainder
		totalBase := expectedP1Base + expectedP2Base + expectedP3Base
		remainder := expectedEpochReward - totalBase

		// Verify participant1: uses capped weight + any remainder
		p1Result := results[0]
		require.NoError(t, p1Result.Error)
		require.Equal(t, "participant1", p1Result.Settle.Participant)
		require.Equal(t, uint64(500), p1Result.Settle.WorkCoins) // Preserved user fees
		require.Equal(t, expectedP1Base+remainder, p1Result.Settle.RewardCoins)

		// Verify participant2: reward based on capped weight (should be less than 50% of total reward)
		p2Result := results[1]
		require.NoError(t, p2Result.Error)
		require.Equal(t, "participant2", p2Result.Settle.Participant)
		require.Equal(t, uint64(1000), p2Result.Settle.WorkCoins) // Preserved user fees
		require.Equal(t, expectedP2Base, p2Result.Settle.RewardCoins)
		// Verify capping worked: participant2 should get less than 50% despite having 50% uncapped weight
		require.Less(t, p2Result.Settle.RewardCoins, expectedEpochReward/2, "Capped participant should get less than 50%")

		// Verify participant3: uses capped weight
		p3Result := results[2]
		require.NoError(t, p3Result.Error)
		require.Equal(t, "participant3", p3Result.Settle.Participant)
		require.Equal(t, uint64(750), p3Result.Settle.WorkCoins) // Preserved user fees
		require.Equal(t, expectedP3Base, p3Result.Settle.RewardCoins)

		// Verify total rewards distributed matches epoch reward exactly
		totalDistributed := p1Result.Settle.RewardCoins + p2Result.Settle.RewardCoins + p3Result.Settle.RewardCoins
		require.Equal(t, expectedEpochReward, totalDistributed, "Complete epoch reward must be distributed")
	})

	t.Run("Invalid participants get no rewards", func(t *testing.T) {
		invalidParticipants := []types.Participant{
			{
				Address:     "participant1",
				CoinBalance: 500,
				Status:      types.ParticipantStatus_INVALID, // Invalid status
				CurrentEpochStats: &types.CurrentEpochStats{
					InferenceCount: 100,
					MissedRequests: 0,
				},
			},
			{
				Address:     "participant2",
				CoinBalance: 1000,
				Status:      types.ParticipantStatus_ACTIVE,
				CurrentEpochStats: &types.CurrentEpochStats{
					InferenceCount: 100,
					MissedRequests: 0,
				},
			},
			{
				Address:     "participant3",
				CoinBalance: 750,
				Status:      types.ParticipantStatus_INACTIVE,
				CurrentEpochStats: &types.CurrentEpochStats{
					InferenceCount: 100,
				},
			},
		}

		logger := createTestLogger(t)
		results, bitcoinResult, err := CalculateParticipantBitcoinRewards(invalidParticipants, epochGroupData, bitcoinParams, nil, nil, logger)
		require.NoError(t, err)
		require.Equal(t, 3, len(results))

		// Verify BitcoinResult still shows fixed epoch reward
		require.Greater(t, bitcoinResult.Amount, int64(0))
		require.Equal(t, uint64(100), bitcoinResult.EpochNumber)

		// Invalid participant gets no rewards
		p1Result := results[0]
		require.NoError(t, p1Result.Error)
		require.Equal(t, uint64(0), p1Result.Settle.WorkCoins)   // Invalid participants don't get WorkCoins
		require.Equal(t, uint64(0), p1Result.Settle.RewardCoins) // Invalid participants don't get RewardCoins

		// Valid participant gets all rewards (since they have all the PoC weight)
		p2Result := results[1]
		require.NoError(t, p2Result.Error)
		require.Equal(t, uint64(1000), p2Result.Settle.WorkCoins)  // Valid participant gets WorkCoins
		require.Greater(t, p2Result.Settle.RewardCoins, uint64(0)) // Valid participant gets all RewardCoins

		// Valid participant gets all rewards (since they have all the PoC weight)
		p3Result := results[2]
		require.NoError(t, p3Result.Error)
		require.Equal(t, uint64(0), p3Result.Settle.WorkCoins)   // Valid participant gets WorkCoins
		require.Equal(t, uint64(0), p3Result.Settle.RewardCoins) // Valid participant gets all RewardCoins

	})

	t.Run("Negative coin balance error", func(t *testing.T) {
		negativeParticipants := []types.Participant{
			{
				Address:     "participant1",
				CoinBalance: -100, // Negative balance
				Status:      types.ParticipantStatus_ACTIVE,
				CurrentEpochStats: &types.CurrentEpochStats{
					InferenceCount: 100,
					MissedRequests: 0,
				},
			},
		}

		logger := createTestLogger(t)
		results, _, err := CalculateParticipantBitcoinRewards(negativeParticipants, epochGroupData, bitcoinParams, nil, nil, logger)
		require.NoError(t, err)
		require.Equal(t, 1, len(results))

		p1Result := results[0]
		require.Error(t, p1Result.Error)
		require.Equal(t, types.ErrNegativeCoinBalance, p1Result.Error)
	})

	t.Run("Zero PoC weight participants get no rewards", func(t *testing.T) {
		// Epoch group with zero weight participant
		zeroWeightEpochData := &types.EpochGroupData{
			EpochIndex: 50,
			ValidationWeights: []*types.ValidationWeight{
				{
					MemberAddress:      "participant1",
					Weight:             0, // Zero weight
					Reputation:         100,
					ConfirmationWeight: 0,
					MlNodes:            []*types.MLNodeInfo{},
				},
				{
					MemberAddress:      "participant2",
					Weight:             1000,
					Reputation:         150,
					ConfirmationWeight: 1000,
					MlNodes: []*types.MLNodeInfo{
						{
							NodeId:             "node2",
							PocWeight:          1000,
							TimeslotAllocation: []bool{true, false},
						},
					},
				},
			},
		}

		zeroWeightParticipants := []types.Participant{
			{
				Address:     "participant1",
				CoinBalance: 500,
				Status:      types.ParticipantStatus_ACTIVE,
				CurrentEpochStats: &types.CurrentEpochStats{
					InferenceCount: 100,
					MissedRequests: 0,
				},
			},
			{
				Address:     "participant2",
				CoinBalance: 1000,
				Status:      types.ParticipantStatus_ACTIVE,
				CurrentEpochStats: &types.CurrentEpochStats{
					InferenceCount: 100,
					MissedRequests: 0,
				},
			},
		}

		logger := createTestLogger(t)
		results, _, err := CalculateParticipantBitcoinRewards(zeroWeightParticipants, zeroWeightEpochData, bitcoinParams, nil, nil, logger)
		require.NoError(t, err)
		require.Equal(t, 2, len(results))

		// Zero weight participant gets WorkCoins but no RewardCoins
		p1Result := results[0]
		require.NoError(t, p1Result.Error)
		require.Equal(t, uint64(500), p1Result.Settle.WorkCoins) // WorkCoins preserved
		require.Equal(t, uint64(0), p1Result.Settle.RewardCoins) // No RewardCoins due to zero weight

		// Non-zero weight participant gets all RewardCoins
		p2Result := results[1]
		require.NoError(t, p2Result.Error)
		require.Equal(t, uint64(1000), p2Result.Settle.WorkCoins)  // WorkCoins preserved
		require.Greater(t, p2Result.Settle.RewardCoins, uint64(0)) // Gets all RewardCoins
	})

	t.Run("Parameter validation", func(t *testing.T) {
		logger := createTestLogger(t)

		// Nil participants
		_, _, err := CalculateParticipantBitcoinRewards(nil, epochGroupData, bitcoinParams, nil, nil, logger)
		require.Error(t, err)
		require.Contains(t, err.Error(), "participants cannot be nil")

		// Nil epoch group data
		_, _, err = CalculateParticipantBitcoinRewards(participants, nil, bitcoinParams, nil, nil, logger)
		require.Error(t, err)
		require.Contains(t, err.Error(), "epoch group data cannot be nil")

		// Nil bitcoin params
		_, _, err = CalculateParticipantBitcoinRewards(participants, epochGroupData, nil, nil, nil, logger)
		require.Error(t, err)
		require.Contains(t, err.Error(), "bitcoin parameters cannot be nil")
	})

	t.Run("Genesis epoch reward distribution", func(t *testing.T) {
		// Test at first reward epoch (no decay since epochsSinceGenesis = 1 - 1 = 0)
		genesisEpochData := &types.EpochGroupData{
			EpochIndex: 1, // First reward epoch (epoch 0 is skipped)
			ValidationWeights: []*types.ValidationWeight{
				{
					MemberAddress:      "participant1",
					Weight:             1000,
					Reputation:         100,
					ConfirmationWeight: 1000,
					MlNodes: []*types.MLNodeInfo{
						{
							NodeId:             "node1",
							PocWeight:          1000,
							TimeslotAllocation: []bool{true, false},
						},
					},
				},
			},
		}

		genesisParticipants := []types.Participant{
			{
				Address:     "participant1",
				CoinBalance: 500,
				Status:      types.ParticipantStatus_ACTIVE,
				CurrentEpochStats: &types.CurrentEpochStats{
					InferenceCount: 100,
					MissedRequests: 0,
				},
			},
		}

		logger := createTestLogger(t)
		results, bitcoinResult, err := CalculateParticipantBitcoinRewards(genesisParticipants, genesisEpochData, bitcoinParams, nil, nil, logger)
		require.NoError(t, err)
		require.Equal(t, 1, len(results))

		// At first reward epoch, reward should be initial amount (no decay since epochsSinceGenesis = 0)
		require.Equal(t, int64(285000000000000), bitcoinResult.Amount)
		require.Equal(t, uint64(1), bitcoinResult.EpochNumber)
		require.False(t, bitcoinResult.DecayApplied) // No decay at first reward epoch

		// Participant gets full reward
		p1Result := results[0]
		require.NoError(t, p1Result.Error)
		require.Equal(t, uint64(500), p1Result.Settle.WorkCoins)               // WorkCoins preserved
		require.Equal(t, uint64(285000000000000), p1Result.Settle.RewardCoins) // Full epoch reward
	})

	t.Run("Complete epoch reward distribution with remainder", func(t *testing.T) {
		// Test scenario where integer division creates remainder
		// Use an epoch reward that doesn't divide evenly by participant weights
		oddRewardParams := &types.BitcoinRewardParams{
			InitialEpochReward: 100,                       // Small reward for easier testing
			DecayRate:          types.DecimalFromFloat(0), // No decay for predictability
			GenesisEpoch:       1,
		}

		// 3 participants with equal weight - 100 doesn't divide evenly by 3
		remainderEpochData := &types.EpochGroupData{
			EpochIndex: 1, // First reward epoch for no decay (epochsSinceGenesis = 1 - 1 = 0)
			ValidationWeights: []*types.ValidationWeight{
				{
					MemberAddress:      "participant1",
					Weight:             1000,
					Reputation:         100,
					ConfirmationWeight: 1000,
					MlNodes: []*types.MLNodeInfo{
						{
							NodeId:             "node1",
							PocWeight:          1000,
							TimeslotAllocation: []bool{true, false},
						},
					},
				},
				{
					MemberAddress:      "participant2",
					Weight:             1000,
					Reputation:         100,
					ConfirmationWeight: 1000,
					MlNodes: []*types.MLNodeInfo{
						{
							NodeId:             "node2",
							PocWeight:          1000,
							TimeslotAllocation: []bool{true, false},
						},
					},
				},
				{
					MemberAddress:      "participant3",
					Weight:             1000,
					Reputation:         100,
					ConfirmationWeight: 1000,
					MlNodes: []*types.MLNodeInfo{
						{
							NodeId:             "node3",
							PocWeight:          1000,
							TimeslotAllocation: []bool{true, false},
						},
					},
				},
			},
		}

		remainderParticipants := []types.Participant{
			{
				Address:     "participant1",
				CoinBalance: 100,
				Status:      types.ParticipantStatus_ACTIVE,
				CurrentEpochStats: &types.CurrentEpochStats{
					InferenceCount: 100,
					MissedRequests: 0,
				},
			},
			{
				Address:     "participant2",
				CoinBalance: 200,
				Status:      types.ParticipantStatus_ACTIVE,
				CurrentEpochStats: &types.CurrentEpochStats{
					InferenceCount: 100,
					MissedRequests: 0,
				},
			},
			{
				Address:     "participant3",
				CoinBalance: 300,
				Status:      types.ParticipantStatus_ACTIVE,
				CurrentEpochStats: &types.CurrentEpochStats{
					InferenceCount: 100,
					MissedRequests: 0,
				},
			},
		}

		logger := createTestLogger(t)
		results, bitcoinResult, err := CalculateParticipantBitcoinRewards(remainderParticipants, remainderEpochData, oddRewardParams, nil, nil, logger)
		require.NoError(t, err)
		require.Equal(t, 3, len(results))

		// Verify BitcoinResult shows correct epoch reward
		require.Equal(t, int64(100), bitcoinResult.Amount)

		// Calculate what each participant should get: 100/3 = 33 remainder 1
		// With simple distribution: first participant gets 33 + 1 = 34, others get 33
		totalDistributed := results[0].Settle.RewardCoins + results[1].Settle.RewardCoins + results[2].Settle.RewardCoins

		// CRITICAL: Total distributed must equal the fixed epoch reward exactly
		require.Equal(t, uint64(100), totalDistributed, "Complete epoch reward must be distributed")

		// Verify individual distributions
		for i, result := range results {
			require.NoError(t, result.Error, "Participant %d should have no error", i+1)

			// Verify WorkCoins are preserved correctly
			expectedWorkCoins := uint64((i + 1) * 100) // 100, 200, 300
			require.Equal(t, expectedWorkCoins, result.Settle.WorkCoins, "WorkCoins must be preserved for participant %d", i+1)
		}

		// Verify remainder distribution: first participant gets base + remainder, others get base
		require.Equal(t, uint64(34), results[0].Settle.RewardCoins, "First participant should get 33 + 1 remainder = 34")
		require.Equal(t, uint64(33), results[1].Settle.RewardCoins, "Second participant should get 33")
		require.Equal(t, uint64(33), results[2].Settle.RewardCoins, "Third participant should get 33")
	})
}

func TestGetBitcoinSettleAmounts(t *testing.T) {
	// Setup test data - same as previous tests to ensure consistency
	bitcoinParams := &types.BitcoinRewardParams{
		InitialEpochReward: 285000000000000,
		DecayRate:          types.DecimalFromFloat(-0.000475),
		GenesisEpoch:       1,
	}

	// Setup settle parameters for supply cap checking
	settleParams := &SettleParameters{
		TotalSubsidyPaid:   1000000,            // Already paid 1M coins
		TotalSubsidySupply: 600000000000000000, // 600M total supply cap (600 * 10^15)
	}

	// Use equal weights to avoid power capping interference (testing supply cap, not power cap)
	epochGroupData := &types.EpochGroupData{
		EpochIndex: 100,
		ValidationWeights: []*types.ValidationWeight{
			createTestValidationWeight("participant1", 500, 100),
			createTestValidationWeight("participant2", 500, 150),
		},
	}

	participants := []types.Participant{
		{
			Address:     "participant1",
			CoinBalance: 500,
			Status:      types.ParticipantStatus_ACTIVE,
			CurrentEpochStats: &types.CurrentEpochStats{
				InferenceCount: 100,
				MissedRequests: 0,
			},
		},
		{
			Address:     "participant2",
			CoinBalance: 1000,
			Status:      types.ParticipantStatus_ACTIVE,
			CurrentEpochStats: &types.CurrentEpochStats{
				InferenceCount: 100,
				MissedRequests: 0,
			},
		},
	}

	t.Run("Main entry point function works correctly", func(t *testing.T) {
		// Call the main entry point function
		logger := createTestLogger(t)
		results, bitcoinResult, err := GetBitcoinSettleAmounts(participants, epochGroupData, bitcoinParams, nil, settleParams, nil, logger)
		require.NoError(t, err)
		require.Equal(t, 2, len(results))

		// Verify it returns same results as the underlying function
		expectedResults, expectedBitcoinResult, expectedErr := CalculateParticipantBitcoinRewards(participants, epochGroupData, bitcoinParams, nil, nil, logger)
		require.Equal(t, expectedErr, err)
		require.Equal(t, expectedBitcoinResult, bitcoinResult)
		require.Equal(t, len(expectedResults), len(results))

		// Verify each result matches
		for i, result := range results {
			expected := expectedResults[i]
			require.Equal(t, expected.Error, result.Error)
			require.Equal(t, expected.Settle.Participant, result.Settle.Participant)
			require.Equal(t, expected.Settle.WorkCoins, result.Settle.WorkCoins)
			require.Equal(t, expected.Settle.RewardCoins, result.Settle.RewardCoins)
		}

		// Verify interface compatibility (returns correct types)
		require.IsType(t, []*SettleResult{}, results)
		require.IsType(t, BitcoinResult{}, bitcoinResult)

		// Verify WorkCoins and RewardCoins are properly calculated
		require.Equal(t, uint64(500), results[0].Settle.WorkCoins)   // Preserved user fees
		require.Equal(t, uint64(1000), results[1].Settle.WorkCoins)  // Preserved user fees
		require.Greater(t, results[0].Settle.RewardCoins, uint64(0)) // Bitcoin rewards
		require.Greater(t, results[1].Settle.RewardCoins, uint64(0)) // Bitcoin rewards
	})

	t.Run("Parameter validation in main entry point", func(t *testing.T) {
		logger := createTestLogger(t)

		// Nil participants
		_, _, err := GetBitcoinSettleAmounts(nil, epochGroupData, bitcoinParams, nil, settleParams, nil, logger)
		require.Error(t, err)
		require.Contains(t, err.Error(), "participants cannot be nil")

		// Nil epoch group data
		_, _, err = GetBitcoinSettleAmounts(participants, nil, bitcoinParams, nil, settleParams, nil, logger)
		require.Error(t, err)
		require.Contains(t, err.Error(), "epochGroupData cannot be nil")

		// Nil bitcoin params
		_, _, err = GetBitcoinSettleAmounts(participants, epochGroupData, nil, nil, settleParams, nil, logger)
		require.Error(t, err)
		require.Contains(t, err.Error(), "bitcoinParams cannot be nil")

		// Nil settle params
		_, _, err = GetBitcoinSettleAmounts(participants, epochGroupData, bitcoinParams, nil, nil, nil, logger)
		require.Error(t, err)
		require.Contains(t, err.Error(), "settleParams cannot be nil")
	})

	t.Run("Supply cap enforcement with remainder distribution", func(t *testing.T) {
		// Test scenario where we're approaching supply cap and need proportional reduction
		supplyCappedParams := &SettleParameters{
			TotalSubsidyPaid:   600000000000000000 - 100000, // Very close to cap (100K remaining)
			TotalSubsidySupply: 600000000000000000,          // 600M total supply cap
		}

		// Call with supply cap constraints
		logger := createTestLogger(t)
		results, bitcoinResult, err := GetBitcoinSettleAmounts(participants, epochGroupData, bitcoinParams, nil, supplyCappedParams, nil, logger)
		require.NoError(t, err)

		// Verify the amount was reduced to fit within cap
		require.Equal(t, int64(100000), bitcoinResult.Amount, "Should mint only remaining supply")

		// Verify total distributed rewards exactly match the available amount
		var totalDistributed uint64 = 0
		for _, result := range results {
			if result.Error == nil && result.Settle != nil {
				totalDistributed += result.Settle.RewardCoins
			}
		}
		require.Equal(t, uint64(100000), totalDistributed, "Total distributed should exactly match available supply")

		// Verify participants still received proportional rewards (reduced but fair)
		require.Greater(t, results[0].Settle.RewardCoins, uint64(0), "Participant 1 should get some rewards")
		require.Greater(t, results[1].Settle.RewardCoins, uint64(0), "Participant 2 should get some rewards")
		require.Equal(t, results[0].Settle.RewardCoins, results[1].Settle.RewardCoins, "Equal weights should get equal rewards (500 each)")
	})

	t.Run("Supply cap already reached - zero rewards", func(t *testing.T) {
		// Test scenario where supply cap is already reached
		capReachedParams := &SettleParameters{
			TotalSubsidyPaid:   600000000000000000, // Already at cap
			TotalSubsidySupply: 600000000000000000, // 600M total supply cap
		}

		// Call with supply cap already reached
		logger := createTestLogger(t)
		results, bitcoinResult, err := GetBitcoinSettleAmounts(participants, epochGroupData, bitcoinParams, nil, capReachedParams, nil, logger)
		require.NoError(t, err)

		// Verify no rewards are minted
		require.Equal(t, int64(0), bitcoinResult.Amount, "Should mint zero when cap reached")

		// Verify all participant rewards are zero
		for _, result := range results {
			if result.Error == nil && result.Settle != nil {
				require.Equal(t, uint64(0), result.Settle.RewardCoins, "All RewardCoins should be zero when cap reached")
				// WorkCoins should still be preserved
				require.Greater(t, result.Settle.WorkCoins, uint64(0), "WorkCoins should still be preserved")
			}
		}
	})
}

// TestPhase2BonusFunctions tests the Phase 2 enhancement stub functions
func TestPhase2BonusFunctions(t *testing.T) {
	// Setup test data
	epochGroupData := &types.EpochGroupData{
		EpochIndex: 100,
		ValidationWeights: []*types.ValidationWeight{
			createTestValidationWeight("participant1", 1000, 100),
			createTestValidationWeight("participant2", 2000, 150),
		},
	}

	participants := []types.Participant{
		{
			Address:     "participant1",
			CoinBalance: 500,
			Status:      types.ParticipantStatus_ACTIVE,
			CurrentEpochStats: &types.CurrentEpochStats{
				InferenceCount: 100,
				MissedRequests: 0,
			},
		},
		{
			Address:     "participant2",
			CoinBalance: 1000,
			Status:      types.ParticipantStatus_ACTIVE,
			CurrentEpochStats: &types.CurrentEpochStats{
				InferenceCount: 100,
				MissedRequests: 0,
			},
		},
	}

	t.Run("CalculateUtilizationBonuses returns 1.0 multipliers", func(t *testing.T) {
		bonuses := CalculateUtilizationBonuses(participants, epochGroupData)
		require.Equal(t, 2, len(bonuses))
		require.Equal(t, 1.0, bonuses["participant1"], "Phase 1 should return 1.0 multiplier")
		require.Equal(t, 1.0, bonuses["participant2"], "Phase 1 should return 1.0 multiplier")
	})

	t.Run("CalculateModelCoverageBonuses returns 1.0 multipliers", func(t *testing.T) {
		bonuses := CalculateModelCoverageBonuses(participants, epochGroupData)
		require.Equal(t, 2, len(bonuses))
		require.Equal(t, 1.0, bonuses["participant1"], "Phase 1 should return 1.0 multiplier")
		require.Equal(t, 1.0, bonuses["participant2"], "Phase 1 should return 1.0 multiplier")
	})

	t.Run("GetMLNodeAssignments returns empty list", func(t *testing.T) {
		assignments := GetMLNodeAssignments("participant1", epochGroupData)
		require.Empty(t, assignments, "Phase 1 should return empty assignment list")

		assignments2 := GetMLNodeAssignments("participant2", epochGroupData)
		require.Empty(t, assignments2, "Phase 1 should return empty assignment list")
	})

	t.Run("Bonus functions handle nil parameters", func(t *testing.T) {
		// Nil epoch group data
		bonuses := CalculateUtilizationBonuses(participants, nil)
		require.Equal(t, 2, len(bonuses))
		require.Equal(t, 1.0, bonuses["participant1"])
		require.Equal(t, 1.0, bonuses["participant2"])

		bonuses2 := CalculateModelCoverageBonuses(participants, nil)
		require.Equal(t, 2, len(bonuses2))
		require.Equal(t, 1.0, bonuses2["participant1"])
		require.Equal(t, 1.0, bonuses2["participant2"])

		// Nil participant for MLNode assignments
		assignments := GetMLNodeAssignments("", nil)
		require.Empty(t, assignments)
	})

	t.Run("Bonus functions handle empty participants", func(t *testing.T) {
		emptyParticipants := []types.Participant{}

		bonuses := CalculateUtilizationBonuses(emptyParticipants, epochGroupData)
		require.Empty(t, bonuses, "Empty participants should return empty bonus map")

		bonuses2 := CalculateModelCoverageBonuses(emptyParticipants, epochGroupData)
		require.Empty(t, bonuses2, "Empty participants should return empty bonus map")
	})
}

// TestBonusIntegrationInGetParticipantPoCWeight tests the integration of bonus functions
func TestBonusIntegrationInGetParticipantPoCWeight(t *testing.T) {
	epochGroupData := &types.EpochGroupData{
		ValidationWeights: []*types.ValidationWeight{
			createTestValidationWeight("participant1", 1000, 100),
			createTestValidationWeight("participant2", 2500, 150),
		},
	}

	t.Run("Phase 1 integration maintains original weights", func(t *testing.T) {
		// In Phase 1, bonus functions return 1.0, so final weight should equal base weight
		weight1 := GetParticipantPoCWeight("participant1", epochGroupData)
		require.Equal(t, uint64(1000), weight1, "Phase 1: finalWeight = baseWeight × 1.0 × 1.0 = baseWeight")

		weight2 := GetParticipantPoCWeight("participant2", epochGroupData)
		require.Equal(t, uint64(2500), weight2, "Phase 1: finalWeight = baseWeight × 1.0 × 1.0 = baseWeight")
	})

	t.Run("Bonus integration handles edge cases", func(t *testing.T) {
		// Zero weight participant
		zeroWeightData := &types.EpochGroupData{
			ValidationWeights: []*types.ValidationWeight{
				createTestValidationWeight("zeroParticipant", 0, 100),
			},
		}

		weight := GetParticipantPoCWeight("zeroParticipant", zeroWeightData)
		require.Equal(t, uint64(0), weight, "Zero base weight should result in zero final weight regardless of bonuses")

		// Negative weight participant
		negativeWeightData := &types.EpochGroupData{
			ValidationWeights: []*types.ValidationWeight{
				{
					MemberAddress: "negativeParticipant",
					Weight:        -500,
					Reputation:    100,
				},
			},
		}

		weightNeg := GetParticipantPoCWeight("negativeParticipant", negativeWeightData)
		require.Equal(t, uint64(0), weightNeg, "Negative base weight should result in zero final weight")
	})

	t.Run("Bonus integration architecture ready for Phase 2", func(t *testing.T) {
		// This test verifies the integration architecture is in place
		// When Phase 2 is implemented, bonus functions will return actual multipliers
		// and this integration will automatically apply them

		weight := GetParticipantPoCWeight("participant1", epochGroupData)
		require.Equal(t, uint64(1000), weight)

		// Verify the integration doesn't break with different epoch data structures
		largeWeightData := &types.EpochGroupData{
			ValidationWeights: []*types.ValidationWeight{
				{
					MemberAddress:      "participant1",
					Weight:             1000000, // Large weight
					Reputation:         100,
					ConfirmationWeight: 1000000,
					MlNodes: []*types.MLNodeInfo{
						{
							NodeId:             "participant1-node",
							PocWeight:          1000000,
							TimeslotAllocation: []bool{true, false},
						},
					},
				},
			},
		}

		largeWeight := GetParticipantPoCWeight("participant1", largeWeightData)
		require.Equal(t, uint64(1000000), largeWeight, "Large weights should be handled correctly")
	})
}

// TestLargeValueEdgeCases tests behavior with maximum and large values
func TestLargeValueEdgeCases(t *testing.T) {
	t.Run("CalculateFixedEpochReward with large values", func(t *testing.T) {
		// Test with large but reasonable initial reward
		largeReward := uint64(1000000000)              // 1 billion
		decayRate := types.DecimalFromFloat(-0.000001) // Very small decay

		// Should handle large values without overflow
		result := CalculateFixedEpochReward(1, largeReward, decayRate)
		require.Less(t, result, largeReward, "Decay should reduce the reward")
		require.Greater(t, result, largeReward/2, "Result should still be close to original with small decay")

		// Test with very large epochs but reasonable initial reward
		result2 := CalculateFixedEpochReward(1000000, 285000000000000, decayRate)
		require.Greater(t, result2, uint64(0), "Should not underflow to zero")
		require.Less(t, result2, uint64(285000000000000), "Should be reduced due to decay")

		// Test mathematical limits - should not panic or overflow
		result3 := CalculateFixedEpochReward(100000, 100000000, types.DecimalFromFloat(-0.0001))
		require.GreaterOrEqual(t, result3, uint64(0), "Should handle extreme cases gracefully")
	})

	t.Run("Large number of participants", func(t *testing.T) {
		// Test with many participants to verify scalability
		numParticipants := 1000
		largeParticipants := make([]types.Participant, numParticipants)
		largeValidationWeights := make([]*types.ValidationWeight, numParticipants)

		for i := 0; i < numParticipants; i++ {
			address := fmt.Sprintf("participant%d", i)
			largeParticipants[i] = types.Participant{
				Address:     address,
				CoinBalance: int64(100 + i), // Different balances
				Status:      types.ParticipantStatus_ACTIVE,
				CurrentEpochStats: &types.CurrentEpochStats{
					InferenceCount: 100,
					MissedRequests: 0,
				},
			}
			largeValidationWeights[i] = createTestValidationWeight(address, int64(1000+i), 100)
		}

		largeEpochData := &types.EpochGroupData{
			EpochIndex:        50,
			ValidationWeights: largeValidationWeights,
		}

		bitcoinParams := &types.BitcoinRewardParams{
			InitialEpochReward: 285000000000000,
			DecayRate:          types.DecimalFromFloat(-0.000475),
			GenesisEpoch:       1,
		}

		// Should handle large number of participants efficiently
		logger := createTestLogger(t)
		results, bitcoinResult, err := CalculateParticipantBitcoinRewards(largeParticipants, largeEpochData, bitcoinParams, nil, nil, logger)
		require.NoError(t, err)
		require.Equal(t, numParticipants, len(results))

		// Verify total distribution equals epoch reward
		totalDistributed := uint64(0)
		for _, result := range results {
			require.NoError(t, result.Error)
			require.Greater(t, result.Settle.WorkCoins, uint64(0), "Each participant should have WorkCoins")
			require.Greater(t, result.Settle.RewardCoins, uint64(0), "Each participant should have RewardCoins")
			totalDistributed += result.Settle.RewardCoins
		}

		require.Equal(t, uint64(bitcoinResult.Amount), totalDistributed, "Complete reward distribution with many participants")
	})

	t.Run("Large PoC weights", func(t *testing.T) {
		// Test with very large PoC weights (equal to avoid power capping)
		largeWeightData := &types.EpochGroupData{
			ValidationWeights: []*types.ValidationWeight{
				{
					MemberAddress:      "participant1",
					Weight:             1000000000000, // 1 trillion
					Reputation:         100,
					ConfirmationWeight: 1000000000000,
					MlNodes: []*types.MLNodeInfo{
						{
							NodeId:             "participant1-node",
							PocWeight:          1000000000000,
							TimeslotAllocation: []bool{true, false},
						},
					},
				},
				{
					MemberAddress:      "participant2",
					Weight:             1000000000000, // 1 trillion (equal weights)
					Reputation:         150,
					ConfirmationWeight: 1000000000000,
					MlNodes: []*types.MLNodeInfo{
						{
							NodeId:             "participant2-node",
							PocWeight:          1000000000000,
							TimeslotAllocation: []bool{true, false},
						},
					},
				},
			},
		}

		weight1 := GetParticipantPoCWeight("participant1", largeWeightData)
		require.Equal(t, uint64(1000000000000), weight1)

		weight2 := GetParticipantPoCWeight("participant2", largeWeightData)
		require.Equal(t, uint64(1000000000000), weight2)

		// Test distribution with large weights
		largeParticipants := []types.Participant{
			{
				Address:     "participant1",
				CoinBalance: 500,
				Status:      types.ParticipantStatus_ACTIVE,
				CurrentEpochStats: &types.CurrentEpochStats{
					InferenceCount: 100,
					MissedRequests: 0,
				},
			},
			{
				Address:     "participant2",
				CoinBalance: 1000,
				Status:      types.ParticipantStatus_ACTIVE,
				CurrentEpochStats: &types.CurrentEpochStats{
					InferenceCount: 100,
					MissedRequests: 0,
				},
			},
		}

		bitcoinParams := &types.BitcoinRewardParams{
			InitialEpochReward: 285000000000000,
			DecayRate:          types.DecimalFromFloat(0), // No decay for predictability
			GenesisEpoch:       1,
		}

		largeWeightData.EpochIndex = 1 // First reward epoch for no decay (epochsSinceGenesis = 1 - 1 = 0)

		logger := createTestLogger(t)
		results, bitcoinResult, err := CalculateParticipantBitcoinRewards(largeParticipants, largeWeightData, bitcoinParams, nil, nil, logger)
		require.NoError(t, err)
		require.Equal(t, 2, len(results))

		// Verify proportional distribution even with large weights
		// participant1: 1T / 2T = 1/2 of rewards
		// participant2: 1T / 2T = 1/2 of rewards (equal weights)
		totalReward := uint64(bitcoinResult.Amount)
		expectedP1 := totalReward / 2
		expectedP2 := totalReward / 2

		// Allow for remainder adjustment on first participant
		require.InDelta(t, expectedP1, results[0].Settle.RewardCoins, 1, "Large weight equal distribution")
		require.InDelta(t, expectedP2, results[1].Settle.RewardCoins, 1, "Large weight equal distribution")

		// Verify complete distribution
		totalDistributed := results[0].Settle.RewardCoins + results[1].Settle.RewardCoins
		require.Equal(t, totalReward, totalDistributed, "Complete distribution with large weights")
	})
}

// TestMathematicalPrecision tests calculation accuracy and precision
func TestMathematicalPrecision(t *testing.T) {
	t.Run("Decay calculation precision", func(t *testing.T) {
		// Test precision of exponential decay calculations
		initialReward := uint64(285000000000000)
		decayRate := types.DecimalFromFloat(-0.000475)

		// Test known values for precision verification
		result1460 := CalculateFixedEpochReward(1460, initialReward, decayRate)
		result2920 := CalculateFixedEpochReward(2920, initialReward, decayRate) // Double the epochs

		// After 2920 epochs, reward should be approximately 1/4 of initial (two halvings)
		expectedQuarter := initialReward / 4
		tolerance := expectedQuarter / 10 // 10% tolerance for exponential precision

		require.InDelta(t, expectedQuarter, result2920, float64(tolerance), "Double halving should result in quarter reward")

		// Verify consistent decay progression
		require.Less(t, result2920, result1460, "More epochs should result in lower rewards")

		// Verify exponential property: if f(x) = initial * e^(rate*x), then f(2x) ≈ [f(x)]^2 / initial
		// This is approximate due to discrete calculations and rounding
		// Use big.Int to prevent overflow with large numbers
		result1460Big := new(big.Int).SetUint64(result1460)
		initialRewardBig := new(big.Int).SetUint64(initialReward)

		// Calculate: (result1460 * result1460) / initialReward using big integers
		expectedApproxBig := new(big.Int).Mul(result1460Big, result1460Big)
		expectedApproxBig = expectedApproxBig.Div(expectedApproxBig, initialRewardBig)

		expectedApprox := expectedApproxBig.Uint64()
		require.InDelta(t, expectedApprox, result2920, float64(expectedApprox)/5, "Exponential decay property should hold approximately with 20% tolerance")
	})

	t.Run("Proportional distribution precision", func(t *testing.T) {
		// Test precision of proportional distribution with prime numbers
		// Use prime numbers to test integer division precision
		primeRewardParams := &types.BitcoinRewardParams{
			InitialEpochReward: 97,                        // Prime number
			DecayRate:          types.DecimalFromFloat(0), // No decay
			GenesisEpoch:       1,
		}

		// Three participants with equal weights (avoids power capping, still tests precision with prime reward)
		primeEpochData := &types.EpochGroupData{
			EpochIndex: 1, // First reward epoch for no decay (epochsSinceGenesis = 1 - 1 = 0)
			ValidationWeights: []*types.ValidationWeight{
				createTestValidationWeight("participant1", 10, 100),
				createTestValidationWeight("participant2", 10, 100),
				createTestValidationWeight("participant3", 10, 100),
			},
		}

		primeParticipants := []types.Participant{
			{Address: "participant1", CoinBalance: 100, Status: types.ParticipantStatus_ACTIVE, CurrentEpochStats: &types.CurrentEpochStats{InferenceCount: 100, MissedRequests: 0}},
			{Address: "participant2", CoinBalance: 200, Status: types.ParticipantStatus_ACTIVE, CurrentEpochStats: &types.CurrentEpochStats{InferenceCount: 100, MissedRequests: 0}},
			{Address: "participant3", CoinBalance: 300, Status: types.ParticipantStatus_ACTIVE, CurrentEpochStats: &types.CurrentEpochStats{InferenceCount: 100, MissedRequests: 0}},
		}

		logger := createTestLogger(t)
		results, bitcoinResult, err := CalculateParticipantBitcoinRewards(primeParticipants, primeEpochData, primeRewardParams, nil, nil, logger)
		require.NoError(t, err)
		require.Equal(t, 3, len(results))

		// Total weight: 10 + 10 + 10 = 30
		// Expected base distribution: 97/30 ≈ 3.233...
		// participant1: 10/30 * 97 = 32.333... → 32
		// participant2: 10/30 * 97 = 32.333... → 32
		// participant3: 10/30 * 97 = 32.333... → 32
		// Base total: 32 + 32 + 32 = 96, remainder: 97 - 96 = 1

		expectedBase := uint64(32)
		expectedRemainder := uint64(1)

		// First participant should get base + remainder (with equal weights, remainder goes to first)
		require.Equal(t, expectedBase+expectedRemainder, results[0].Settle.RewardCoins, "First participant gets base + remainder")
		require.Equal(t, expectedBase, results[1].Settle.RewardCoins, "Second participant gets base only")
		require.Equal(t, expectedBase, results[2].Settle.RewardCoins, "Third participant gets base only")

		// Verify total equals epoch reward exactly
		totalDistributed := results[0].Settle.RewardCoins + results[1].Settle.RewardCoins + results[2].Settle.RewardCoins
		require.Equal(t, uint64(97), totalDistributed, "Exact distribution of prime reward")
		require.Equal(t, int64(97), bitcoinResult.Amount, "BitcoinResult shows correct amount")
	})

	t.Run("Zero remainder distribution", func(t *testing.T) {
		// Test case where reward divides evenly (no remainder)
		evenRewardParams := &types.BitcoinRewardParams{
			InitialEpochReward: 100,                       // Divides evenly by participant weights
			DecayRate:          types.DecimalFromFloat(0), // No decay
			GenesisEpoch:       1,
		}

		evenEpochData := &types.EpochGroupData{
			EpochIndex: 1, // First reward epoch for no decay (epochsSinceGenesis = 1 - 1 = 0)
			ValidationWeights: []*types.ValidationWeight{
				createTestValidationWeight("participant1", 50, 100), // 50/100 = 50%
				createTestValidationWeight("participant2", 50, 100), // 50/100 = 50%
			},
		}

		evenParticipants := []types.Participant{
			{Address: "participant1", CoinBalance: 100, Status: types.ParticipantStatus_ACTIVE, CurrentEpochStats: &types.CurrentEpochStats{InferenceCount: 100, MissedRequests: 0}},
			{Address: "participant2", CoinBalance: 200, Status: types.ParticipantStatus_ACTIVE, CurrentEpochStats: &types.CurrentEpochStats{InferenceCount: 100, MissedRequests: 0}},
		}

		logger := createTestLogger(t)
		results, bitcoinResult, err := CalculateParticipantBitcoinRewards(evenParticipants, evenEpochData, evenRewardParams, nil, nil, logger)
		require.NoError(t, err)
		require.Equal(t, 2, len(results))

		// Should divide evenly: 50% = 50, 50% = 50, total = 100, remainder = 0
		require.Equal(t, uint64(50), results[0].Settle.RewardCoins, "50% of 100 = 50")
		require.Equal(t, uint64(50), results[1].Settle.RewardCoins, "50% of 100 = 50")

		// Verify total distribution
		totalDistributed := results[0].Settle.RewardCoins + results[1].Settle.RewardCoins
		require.Equal(t, uint64(100), totalDistributed, "Even distribution should total exactly")
		require.Equal(t, int64(100), bitcoinResult.Amount, "BitcoinResult shows correct amount")
	})
}

// Test GetPreservedWeight function
func TestGetPreservedWeight(t *testing.T) {
	t.Run("Calculate preserved weight from POC_SLOT=true nodes", func(t *testing.T) {
		epochGroupData := &types.EpochGroupData{
			ValidationWeights: []*types.ValidationWeight{
				{
					MemberAddress: "participant1",
					Weight:        300,
					MlNodes: []*types.MLNodeInfo{
						{
							NodeId:             "node1",
							PocWeight:          100,
							TimeslotAllocation: []bool{true, true}, // POC_SLOT=true (preserved)
						},
						{
							NodeId:             "node2",
							PocWeight:          200,
							TimeslotAllocation: []bool{true, false}, // POC_SLOT=false (not preserved)
						},
					},
				},
			},
		}

		preservedWeight := GetPreservedWeight("participant1", epochGroupData)
		require.Equal(t, int64(100), preservedWeight, "Should sum only POC_SLOT=true nodes")
	})

	t.Run("All nodes preserved", func(t *testing.T) {
		epochGroupData := &types.EpochGroupData{
			ValidationWeights: []*types.ValidationWeight{
				{
					MemberAddress: "participant1",
					Weight:        300,
					MlNodes: []*types.MLNodeInfo{
						{
							NodeId:             "node1",
							PocWeight:          100,
							TimeslotAllocation: []bool{true, true}, // POC_SLOT=true
						},
						{
							NodeId:             "node2",
							PocWeight:          200,
							TimeslotAllocation: []bool{true, true}, // POC_SLOT=true
						},
					},
				},
			},
		}

		preservedWeight := GetPreservedWeight("participant1", epochGroupData)
		require.Equal(t, int64(300), preservedWeight, "Should sum all nodes when all preserved")
	})

	t.Run("No nodes preserved", func(t *testing.T) {
		epochGroupData := &types.EpochGroupData{
			ValidationWeights: []*types.ValidationWeight{
				{
					MemberAddress: "participant1",
					Weight:        300,
					MlNodes: []*types.MLNodeInfo{
						{
							NodeId:             "node1",
							PocWeight:          100,
							TimeslotAllocation: []bool{true, false}, // POC_SLOT=false
						},
						{
							NodeId:             "node2",
							PocWeight:          200,
							TimeslotAllocation: []bool{true, false}, // POC_SLOT=false
						},
					},
				},
			},
		}

		preservedWeight := GetPreservedWeight("participant1", epochGroupData)
		require.Equal(t, int64(0), preservedWeight, "Should be 0 when no nodes preserved")
	})

	t.Run("Participant not found", func(t *testing.T) {
		epochGroupData := &types.EpochGroupData{
			ValidationWeights: []*types.ValidationWeight{
				{
					MemberAddress: "participant1",
					Weight:        300,
				},
			},
		}

		preservedWeight := GetPreservedWeight("nonexistent", epochGroupData)
		require.Equal(t, int64(0), preservedWeight, "Should return 0 for nonexistent participant")
	})
}

// Test confirmation weight capping without power capping
func TestCalculateParticipantBitcoinRewards_ConfirmationCapping(t *testing.T) {
	t.Run("Confirmation capping applies when confirmed < non-preserved", func(t *testing.T) {
		bitcoinParams := &types.BitcoinRewardParams{
			GenesisEpoch:       1,
			InitialEpochReward: 600, // Total reward to distribute
			DecayRate:          types.DecimalFromFloat(0.0),
		}

		epochGroupData := &types.EpochGroupData{
			EpochIndex: 1,
			ValidationWeights: []*types.ValidationWeight{
				{
					MemberAddress:      "participant1",
					Weight:             300, // Original total weight
					ConfirmationWeight: 150, // Confirmed only 150 out of 200 non-preserved
					MlNodes: []*types.MLNodeInfo{
						{
							NodeId:             "node1",
							PocWeight:          100,
							TimeslotAllocation: []bool{true, true}, // POC_SLOT=true (preserved=100)
						},
						{
							NodeId:             "node2",
							PocWeight:          200,
							TimeslotAllocation: []bool{true, false}, // POC_SLOT=false (should use confirmed)
						},
					},
				},
				{
					MemberAddress:      "participant2",
					Weight:             150, // Original total weight
					ConfirmationWeight: 100, // Confirmed 100
					MlNodes: []*types.MLNodeInfo{
						{
							NodeId:             "node3",
							PocWeight:          50,
							TimeslotAllocation: []bool{true, true}, // POC_SLOT=true (preserved=50)
						},
						{
							NodeId:             "node4",
							PocWeight:          100,
							TimeslotAllocation: []bool{true, false}, // POC_SLOT=false
						},
					},
				},
			},
		}

		participants := []types.Participant{
			{Address: "participant1", CoinBalance: 0, Status: types.ParticipantStatus_ACTIVE, CurrentEpochStats: &types.CurrentEpochStats{InferenceCount: 100, MissedRequests: 0}},
			{Address: "participant2", CoinBalance: 0, Status: types.ParticipantStatus_ACTIVE, CurrentEpochStats: &types.CurrentEpochStats{InferenceCount: 100, MissedRequests: 0}},
		}

		logger := createTestLogger(t)
		results, bitcoinResult, err := CalculateParticipantBitcoinRewards(participants, epochGroupData, bitcoinParams, nil, nil, logger)
		require.NoError(t, err)
		require.Equal(t, 2, len(results))

		// Effective weights:
		// participant1: preserved(100) + confirmed(150) = 250
		// participant2: preserved(50) + confirmed(100) = 150
		// Total = 400
		// participant1 has 250/400 = 62.5% which exceeds 50% cap (2 participants)
		// Power capping WILL apply

		// After capping to 50%: both get equal weights = 150 each
		// Total after cap = 300
		// participant1: 150/300 * 600 = 300
		// participant2: 150/300 * 600 = 300
		require.Equal(t, uint64(300), results[0].Settle.RewardCoins, "participant1 capped to 50%")
		require.Equal(t, uint64(300), results[1].Settle.RewardCoins, "participant2 also gets 50%")
		require.Equal(t, int64(600), bitcoinResult.Amount)
	})

	t.Run("Zero confirmation weight - only preserved nodes earn", func(t *testing.T) {
		bitcoinParams := &types.BitcoinRewardParams{
			GenesisEpoch:       1,
			InitialEpochReward: 300,
			DecayRate:          types.DecimalFromFloat(0.0),
		}

		epochGroupData := &types.EpochGroupData{
			EpochIndex: 1,
			ValidationWeights: []*types.ValidationWeight{
				{
					MemberAddress:      "participant1",
					Weight:             300,
					ConfirmationWeight: 0, // Failed confirmation PoC completely
					MlNodes: []*types.MLNodeInfo{
						{
							NodeId:             "node1",
							PocWeight:          100,
							TimeslotAllocation: []bool{true, true}, // POC_SLOT=true (preserved)
						},
						{
							NodeId:             "node2",
							PocWeight:          200,
							TimeslotAllocation: []bool{true, false}, // POC_SLOT=false
						},
					},
				},
				{
					MemberAddress:      "participant2",
					Weight:             200,
					ConfirmationWeight: 200, // Successful confirmation
					MlNodes: []*types.MLNodeInfo{
						{
							NodeId:             "node3",
							PocWeight:          200,
							TimeslotAllocation: []bool{true, false}, // POC_SLOT=false
						},
					},
				},
			},
		}

		participants := []types.Participant{
			{Address: "participant1", CoinBalance: 0, Status: types.ParticipantStatus_ACTIVE, CurrentEpochStats: &types.CurrentEpochStats{InferenceCount: 100, MissedRequests: 0}},
			{Address: "participant2", CoinBalance: 0, Status: types.ParticipantStatus_ACTIVE, CurrentEpochStats: &types.CurrentEpochStats{InferenceCount: 100, MissedRequests: 0}},
		}

		logger := createTestLogger(t)
		results, _, err := CalculateParticipantBitcoinRewards(participants, epochGroupData, bitcoinParams, nil, nil, logger)
		require.NoError(t, err)

		// Effective weights:
		// participant1: preserved(100) + confirmed(0) = 100
		// participant2: preserved(0) + confirmed(200) = 200
		// Total = 300
		// participant2 has 200/300 = 66.7% which exceeds 50% cap (2 participants)
		// Power capping WILL apply

		// After capping to 50%: both get equal weights = 100 each
		// Total after cap = 200
		// participant1: 100/200 * 300 = 150
		// participant2: 100/200 * 300 = 150
		require.Equal(t, uint64(150), results[0].Settle.RewardCoins, "participant1 gets capped rewards")
		require.Equal(t, uint64(150), results[1].Settle.RewardCoins, "participant2 capped to 50%")
	})
}

// Test confirmation capping WITH power capping
func TestCalculateParticipantBitcoinRewards_ConfirmationAndPowerCapping(t *testing.T) {
	t.Run("Power capping applies after confirmation capping", func(t *testing.T) {
		bitcoinParams := &types.BitcoinRewardParams{
			GenesisEpoch:       1,
			InitialEpochReward: 1000,
			DecayRate:          types.DecimalFromFloat(0.0),
		}

		epochGroupData := &types.EpochGroupData{
			EpochIndex: 1,
			ValidationWeights: []*types.ValidationWeight{
				{
					MemberAddress:      "participant1",
					Weight:             700,
					ConfirmationWeight: 600, // Large confirmed weight
					MlNodes: []*types.MLNodeInfo{
						{
							NodeId:             "node1",
							PocWeight:          100,
							TimeslotAllocation: []bool{true, true}, // POC_SLOT=true (preserved)
						},
						{
							NodeId:             "node2",
							PocWeight:          600,
							TimeslotAllocation: []bool{true, false}, // POC_SLOT=false
						},
					},
				},
				{
					MemberAddress:      "participant2",
					Weight:             100,
					ConfirmationWeight: 100,
					MlNodes: []*types.MLNodeInfo{
						{
							NodeId:             "node3",
							PocWeight:          100,
							TimeslotAllocation: []bool{true, false}, // POC_SLOT=false
						},
					},
				},
			},
		}

		participants := []types.Participant{
			{Address: "participant1", CoinBalance: 0, Status: types.ParticipantStatus_ACTIVE, CurrentEpochStats: &types.CurrentEpochStats{InferenceCount: 100, MissedRequests: 0}},
			{Address: "participant2", CoinBalance: 0, Status: types.ParticipantStatus_ACTIVE, CurrentEpochStats: &types.CurrentEpochStats{InferenceCount: 100, MissedRequests: 0}},
		}

		logger := createTestLogger(t)
		results, _, err := CalculateParticipantBitcoinRewards(participants, epochGroupData, bitcoinParams, nil, nil, logger)
		require.NoError(t, err)

		// Effective weights before power capping:
		// participant1: preserved(100) + confirmed(600) = 700
		// participant2: preserved(0) + confirmed(100) = 100
		// Total = 800

		// participant1 has 700/800 = 87.5% (exceeds 30% cap for 2 participants = 50%)
		// Power capping will reduce participant1's weight

		// With 2 participants, max allowed = 50%
		// Cap calculation: participant1 capped, participant2 unchanged
		// After capping, participant1 should be capped to at most 50% of total

		totalReward := results[0].Settle.RewardCoins + results[1].Settle.RewardCoins
		require.Equal(t, uint64(1000), totalReward, "Total should equal full reward")

		// Verify participant1 was capped (should get less than 87.5%)
		participant1Percentage := float64(results[0].Settle.RewardCoins) / float64(totalReward)
		require.LessOrEqual(t, participant1Percentage, 0.51, "participant1 should be capped below original 87.5%")
		require.GreaterOrEqual(t, participant1Percentage, 0.45, "participant1 should still get substantial reward")
	})
}

// Test edge cases
func TestCalculateParticipantBitcoinRewards_ConfirmationEdgeCases(t *testing.T) {
	t.Run("Single participant with confirmation capping", func(t *testing.T) {
		bitcoinParams := &types.BitcoinRewardParams{
			GenesisEpoch:       1,
			InitialEpochReward: 500,
			DecayRate:          types.DecimalFromFloat(0.0),
		}

		epochGroupData := &types.EpochGroupData{
			EpochIndex: 1,
			ValidationWeights: []*types.ValidationWeight{
				{
					MemberAddress:      "participant1",
					Weight:             300,
					ConfirmationWeight: 150,
					MlNodes: []*types.MLNodeInfo{
						{
							NodeId:             "node1",
							PocWeight:          100,
							TimeslotAllocation: []bool{true, true}, // POC_SLOT=true
						},
						{
							NodeId:             "node2",
							PocWeight:          200,
							TimeslotAllocation: []bool{true, false}, // POC_SLOT=false
						},
					},
				},
			},
		}

		participants := []types.Participant{
			{Address: "participant1", CoinBalance: 0, Status: types.ParticipantStatus_ACTIVE, CurrentEpochStats: &types.CurrentEpochStats{InferenceCount: 100, MissedRequests: 0}},
		}

		logger := createTestLogger(t)
		results, bitcoinResult, err := CalculateParticipantBitcoinRewards(participants, epochGroupData, bitcoinParams, nil, nil, logger)
		require.NoError(t, err)
		require.Equal(t, 1, len(results))

		// Single participant gets all rewards regardless of capping
		// effective weight: preserved(100) + confirmed(150) = 250
		require.Equal(t, uint64(500), results[0].Settle.RewardCoins, "Single participant gets full reward")
		require.Equal(t, int64(500), bitcoinResult.Amount)
	})

	t.Run("All participants have zero effective weight", func(t *testing.T) {
		bitcoinParams := &types.BitcoinRewardParams{
			GenesisEpoch:       1,
			InitialEpochReward: 1000,
			DecayRate:          types.DecimalFromFloat(0.0),
		}

		epochGroupData := &types.EpochGroupData{
			EpochIndex: 1,
			ValidationWeights: []*types.ValidationWeight{
				{
					MemberAddress:      "participant1",
					Weight:             100,
					ConfirmationWeight: 0,
					MlNodes: []*types.MLNodeInfo{
						{
							NodeId:             "node1",
							PocWeight:          100,
							TimeslotAllocation: []bool{true, false}, // POC_SLOT=false, no preserved
						},
					},
				},
			},
		}

		participants := []types.Participant{
			{Address: "participant1", CoinBalance: 100, Status: types.ParticipantStatus_ACTIVE, CurrentEpochStats: &types.CurrentEpochStats{InferenceCount: 100, MissedRequests: 0}},
		}

		logger := createTestLogger(t)
		results, _, err := CalculateParticipantBitcoinRewards(participants, epochGroupData, bitcoinParams, nil, nil, logger)
		require.NoError(t, err)

		// With zero effective weight, participant gets no reward coins (but still gets work coins)
		require.Equal(t, uint64(0), results[0].Settle.RewardCoins, "Zero effective weight means no reward")
		require.Equal(t, uint64(100), results[0].Settle.WorkCoins, "Work coins still distributed")
	})
}

// Test RecomputeEffectiveWeightFromMLNodes helper function
func TestRecomputeEffectiveWeightFromMLNodes(t *testing.T) {
	t.Run("Mixed POC_SLOT allocations", func(t *testing.T) {
		vw := &types.ValidationWeight{
			MemberAddress:      "participant1",
			Weight:             450, // Total weight (for reference)
			ConfirmationWeight: 250, // Sum of POC_SLOT=false weights
			MlNodes: []*types.MLNodeInfo{
				{
					NodeId:             "node1",
					PocWeight:          100,
					TimeslotAllocation: []bool{true, false}, // POC_SLOT=false
				},
				{
					NodeId:             "node2",
					PocWeight:          200,
					TimeslotAllocation: []bool{true, true}, // POC_SLOT=true
				},
				{
					NodeId:             "node3",
					PocWeight:          150,
					TimeslotAllocation: []bool{true, false}, // POC_SLOT=false
				},
			},
		}

		effectiveWeight := RecomputeEffectiveWeightFromMLNodes(vw, nil)

		// Should be: preservedWeight (200) + confirmationWeight (250) = 450
		require.Equal(t, int64(450), effectiveWeight)
	})

	t.Run("Confirmation PoC revealed lower capacity", func(t *testing.T) {
		vw := &types.ValidationWeight{
			MemberAddress:      "participant1",
			Weight:             450, // Original total
			ConfirmationWeight: 180, // Lower than initial (250), after confirmation PoC
			MlNodes: []*types.MLNodeInfo{
				{
					NodeId:             "node1",
					PocWeight:          100,
					TimeslotAllocation: []bool{true, false}, // POC_SLOT=false
				},
				{
					NodeId:             "node2",
					PocWeight:          200,
					TimeslotAllocation: []bool{true, true}, // POC_SLOT=true
				},
				{
					NodeId:             "node3",
					PocWeight:          150,
					TimeslotAllocation: []bool{true, false}, // POC_SLOT=false
				},
			},
		}

		effectiveWeight := RecomputeEffectiveWeightFromMLNodes(vw, nil)

		// Should be: preservedWeight (200) + confirmationWeight (180) = 380
		// This is LESS than baseWeight (450) due to confirmation capping
		require.Equal(t, int64(380), effectiveWeight)
		require.Less(t, effectiveWeight, vw.Weight, "Effective weight should be less than base weight after capping")
	})

	t.Run("All nodes preserved (POC_SLOT=true)", func(t *testing.T) {
		vw := &types.ValidationWeight{
			MemberAddress:      "participant1",
			Weight:             300,
			ConfirmationWeight: 0, // No confirmation weight (all preserved)
			MlNodes: []*types.MLNodeInfo{
				{
					NodeId:             "node1",
					PocWeight:          100,
					TimeslotAllocation: []bool{true, true}, // POC_SLOT=true
				},
				{
					NodeId:             "node2",
					PocWeight:          200,
					TimeslotAllocation: []bool{true, true}, // POC_SLOT=true
				},
			},
		}

		effectiveWeight := RecomputeEffectiveWeightFromMLNodes(vw, nil)

		// Should be: preservedWeight (300) + confirmationWeight (0) = 300
		require.Equal(t, int64(300), effectiveWeight)
	})

	t.Run("All nodes subject to confirmation (POC_SLOT=false)", func(t *testing.T) {
		vw := &types.ValidationWeight{
			MemberAddress:      "participant1",
			Weight:             300,
			ConfirmationWeight: 300, // All weight subject to confirmation
			MlNodes: []*types.MLNodeInfo{
				{
					NodeId:             "node1",
					PocWeight:          100,
					TimeslotAllocation: []bool{true, false}, // POC_SLOT=false
				},
				{
					NodeId:             "node2",
					PocWeight:          200,
					TimeslotAllocation: []bool{true, false}, // POC_SLOT=false
				},
			},
		}

		effectiveWeight := RecomputeEffectiveWeightFromMLNodes(vw, nil)

		// Should be: preservedWeight (0) + confirmationWeight (300) = 300
		require.Equal(t, int64(300), effectiveWeight)
	})

	t.Run("Handles nil and invalid nodes gracefully", func(t *testing.T) {
		vw := &types.ValidationWeight{
			MemberAddress:      "participant1",
			Weight:             150,
			ConfirmationWeight: 100,
			MlNodes: []*types.MLNodeInfo{
				nil, // Nil node
				{
					NodeId:             "node1",
					PocWeight:          50,
					TimeslotAllocation: []bool{}, // Empty allocation
				},
				{
					NodeId:             "node2",
					PocWeight:          50,
					TimeslotAllocation: []bool{true}, // Too short
				},
				{
					NodeId:             "node3",
					PocWeight:          50,
					TimeslotAllocation: []bool{true, true}, // Valid POC_SLOT=true
				},
			},
		}

		effectiveWeight := RecomputeEffectiveWeightFromMLNodes(vw, nil)

		// Should be: preservedWeight (50) + confirmationWeight (100) = 150
		require.Equal(t, int64(150), effectiveWeight)
	})

}

// Test settlement matching regular weight when no confirmation PoC
func TestSettlementMatchesRegularWeightWithoutConfirmation(t *testing.T) {
	// Create epoch group data with participant having mixed timeslot allocations
	epochGroupData := &types.EpochGroupData{
		EpochIndex: 1,
		ValidationWeights: []*types.ValidationWeight{
			{
				MemberAddress:      "participant1",
				Weight:             450, // Total PoC weight (already capped)
				ConfirmationWeight: 250, // Sum of POC_SLOT=false (initialized, no confirmation PoC occurred)
				MlNodes: []*types.MLNodeInfo{
					{
						NodeId:             "node1",
						PocWeight:          100,
						TimeslotAllocation: []bool{true, false}, // POC_SLOT=false
					},
					{
						NodeId:             "node2",
						PocWeight:          200,
						TimeslotAllocation: []bool{true, true}, // POC_SLOT=true
					},
					{
						NodeId:             "node3",
						PocWeight:          150,
						TimeslotAllocation: []bool{true, false}, // POC_SLOT=false
					},
				},
			},
		},
	}

	// Verify effective weight matches base weight
	vw := epochGroupData.ValidationWeights[0]
	effectiveWeight := RecomputeEffectiveWeightFromMLNodes(vw, nil)

	// preservedWeight (200) + confirmationWeight (250) = 450 = baseWeight
	require.Equal(t, vw.Weight, effectiveWeight,
		"When no confirmation PoC occurs, effective weight should equal base weight")
}

// Test settlement with confirmation PoC capping
func TestSettlementWithConfirmationCapping(t *testing.T) {
	// Initial: POC_SLOT=true: 200, POC_SLOT=false: 250, Total: 450
	// Confirmation PoC revealed: 180 (< 250)
	epochGroupData := &types.EpochGroupData{
		EpochIndex: 1,
		ValidationWeights: []*types.ValidationWeight{
			{
				MemberAddress:      "participant1",
				Weight:             450, // Original total weight
				ConfirmationWeight: 180, // Updated after confirmation PoC (< 250)
				MlNodes: []*types.MLNodeInfo{
					{
						NodeId:             "node1",
						PocWeight:          100,
						TimeslotAllocation: []bool{true, false}, // POC_SLOT=false
					},
					{
						NodeId:             "node2",
						PocWeight:          200,
						TimeslotAllocation: []bool{true, true}, // POC_SLOT=true
					},
					{
						NodeId:             "node3",
						PocWeight:          150,
						TimeslotAllocation: []bool{true, false}, // POC_SLOT=false
					},
				},
			},
		},
	}

	vw := epochGroupData.ValidationWeights[0]
	effectiveWeight := RecomputeEffectiveWeightFromMLNodes(vw, nil)

	// Should be capped: preservedWeight (200) + confirmationWeight (180) = 380
	require.Equal(t, int64(380), effectiveWeight)
	require.Less(t, effectiveWeight, vw.Weight,
		"When confirmation PoC reveals lower capacity, effective weight should be capped")
}
