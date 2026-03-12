package calculations

import (
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

var (
	zeroStats = types.CurrentEpochStats{
		InvalidLLR:  types.DecimalFromFloat(0),
		InactiveLLR: types.DecimalFromFloat(0),
	}

	testPrecomputed = types.SPRTPrecomputedValues{
		InvalidationLogFail: types.DecimalFromFloat(0.69314718056),   // ln(0.1/0.05)
		InvalidationLogPass: types.DecimalFromFloat(-0.05406722127),  // ln(0.9/0.95)
		InactiveLogFail:     types.DecimalFromFloat(0.69314718056),   // ln(0.2/0.1)
		InactiveLogPass:     types.DecimalFromFloat(-0.117783035656), // ln(0.8/0.9)
	}
)

func TestComputeStatus(t *testing.T) {
	tests := []struct {
		name        string
		params      *types.ValidationParams
		participant types.Participant
		wantStatus  types.ParticipantStatus
		wantReason  ParticipantStatusReason
	}{
		{
			name:        "nil validation parameters returns active",
			params:      nil,
			participant: types.Participant{},
			wantStatus:  types.ParticipantStatus_ACTIVE,
			wantReason:  NoReason,
		},
		{
			name: "consecutive failures returns invalid",
			params: &types.ValidationParams{
				FalsePositiveRate:              types.DecimalFromFloat(0.05),
				BadParticipantInvalidationRate: types.DecimalFromFloat(0.1),
				InvalidationHThreshold:         types.DecimalFromFloat(4),
				DowntimeGoodPercentage:         types.DecimalFromFloat(0.1),
				DowntimeBadPercentage:          types.DecimalFromFloat(0.2),
				DowntimeHThreshold:             types.DecimalFromFloat(4),
				QuickFailureThreshold:          types.DecimalFromFloat(0.000001),
			},
			participant: types.Participant{
				ConsecutiveInvalidInferences: 20,
			},
			wantStatus: types.ParticipantStatus_INVALID,
			wantReason: ConsecutiveFailures,
		},
		{
			name: "statistical invalidations returns invalid",
			params: &types.ValidationParams{
				BadParticipantInvalidationRate: types.DecimalFromFloat(0.1),
				InvalidationHThreshold:         types.DecimalFromFloat(4),
				FalsePositiveRate:              types.DecimalFromFloat(0.05),
				DowntimeGoodPercentage:         types.DecimalFromFloat(0.1),
				DowntimeBadPercentage:          types.DecimalFromFloat(0.2),
				DowntimeHThreshold:             types.DecimalFromFloat(4),
				QuickFailureThreshold:          types.DecimalFromFloat(0.000001),
			},
			participant: types.Participant{
				CurrentEpochStats: &types.CurrentEpochStats{
					ValidatedInferences:   7,
					InvalidatedInferences: 7,
				},
			},
			wantStatus: types.ParticipantStatus_INVALID,
			wantReason: StatisticalInvalidations,
		},
		{
			name: "normal operation returns active",
			params: &types.ValidationParams{
				BadParticipantInvalidationRate: types.DecimalFromFloat(0.1),
				InvalidationHThreshold:         types.DecimalFromFloat(4),
				FalsePositiveRate:              types.DecimalFromFloat(0.05),
				DowntimeGoodPercentage:         types.DecimalFromFloat(0.1),
				DowntimeBadPercentage:          types.DecimalFromFloat(0.2),
				DowntimeHThreshold:             types.DecimalFromFloat(4),
				QuickFailureThreshold:          types.DecimalFromFloat(0.000001),
			},
			participant: types.Participant{
				CurrentEpochStats: &types.CurrentEpochStats{
					ValidatedInferences:   95,
					InvalidatedInferences: 5,
				},
			},
			wantStatus: types.ParticipantStatus_ACTIVE,
			wantReason: NoReason,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, reason, _ := ComputeStatus(tt.params, nil, tt.participant, zeroStats, testPrecomputed)
			require.Equal(t, tt.wantStatus, status)
			require.Equal(t, tt.wantReason, reason)
		})
	}
}

func TestDowntimeTriggersInactive(t *testing.T) {
	params := &types.ValidationParams{
		FalsePositiveRate:              types.DecimalFromFloat(0.05),
		BadParticipantInvalidationRate: types.DecimalFromFloat(0.1),
		InvalidationHThreshold:         types.DecimalFromFloat(4),
		DowntimeGoodPercentage:         types.DecimalFromFloat(0.1), // P0
		DowntimeBadPercentage:          types.DecimalFromFloat(0.2), // P1
		DowntimeHThreshold:             types.DecimalFromFloat(4),   // H
		QuickFailureThreshold:          types.DecimalFromFloat(0.000001),
	}

	participant := types.Participant{
		CurrentEpochStats: &types.CurrentEpochStats{
			InferenceCount:        50, // passes
			MissedRequests:        60, // failures
			ValidatedInferences:   0,
			InvalidatedInferences: 0,
		},
	}

	status, reason, _ := ComputeStatus(params, nil, participant, zeroStats, testPrecomputed)
	require.Equal(t, types.ParticipantStatus_INACTIVE, status)
	require.Equal(t, Downtime, reason)
}

func TestDowntimeParamsOutOfRangeReturnAlgorithmError(t *testing.T) {
	badVals := []struct{ good, bad float64 }{
		{0, 0.2},    // good == 0
		{1, 0.2},    // good == 1
		{-0.1, 0.2}, // good < 0
		{0.1, 0},    // bad == 0
		{0.1, 1},    // bad == 1
		{0.1, 1.1},  // bad > 1
	}

	for _, v := range badVals {
		params := &types.ValidationParams{
			FalsePositiveRate:              types.DecimalFromFloat(0.05),
			BadParticipantInvalidationRate: types.DecimalFromFloat(0.1),
			InvalidationHThreshold:         types.DecimalFromFloat(4),
			DowntimeGoodPercentage:         types.DecimalFromFloat(v.good),
			DowntimeBadPercentage:          types.DecimalFromFloat(v.bad),
			DowntimeHThreshold:             types.DecimalFromFloat(4),
			QuickFailureThreshold:          types.DecimalFromFloat(0.000001),
		}
		participant := types.Participant{CurrentEpochStats: &types.CurrentEpochStats{}}
		status, reason, _ := ComputeStatus(params, nil, participant, zeroStats, types.SPRTPrecomputedValues{})
		require.Equal(t, types.ParticipantStatus_ACTIVE, status)
		require.Equal(t, AlgorithmError, reason)
	}
}

func TestProbabilityOfConsecutiveFailures_PanicOnBadRate(t *testing.T) {
	// Test that invalid rates (< 0 or > 1) return zero instead of panicking
	result := probabilityOfConsecutiveFailures(types.DecimalFromFloat(1.5).ToDecimal(), 1)
	require.True(t, result.IsZero(), "Expected zero for invalid rate > 1")

	result = probabilityOfConsecutiveFailures(types.DecimalFromFloat(-0.5).ToDecimal(), 1)
	require.True(t, result.IsZero(), "Expected zero for invalid rate < 0")
}

func TestGetStats(t *testing.T) {
	part := &types.Participant{
		CurrentEpochStats: &types.CurrentEpochStats{
			InvalidLLR:  types.DecimalFromFloat(1.5),
			InactiveLLR: types.DecimalFromFloat(2.0),
		},
	}

	result := getStats(part)
	require.NotNil(t, result.InvalidLLR)
	require.NotNil(t, result.InactiveLLR)

	// Test with nil participant
	part2 := &types.Participant{}
	result2 := getStats(part2)
	require.NotNil(t, result2.InvalidLLR)
	require.NotNil(t, result2.InactiveLLR)
	require.Equal(t, int64(0), result2.InvalidLLR.Value)
	require.Equal(t, int64(0), result2.InactiveLLR.Value)
}

func TestComputeStatus_Stability(t *testing.T) {
	params := &types.ValidationParams{
		FalsePositiveRate:              types.DecimalFromFloat(0.05),
		BadParticipantInvalidationRate: types.DecimalFromFloat(0.1),
		InvalidationHThreshold:         types.DecimalFromFloat(4),
		DowntimeGoodPercentage:         types.DecimalFromFloat(0.1),
		DowntimeBadPercentage:          types.DecimalFromFloat(0.2),
		DowntimeHThreshold:             types.DecimalFromFloat(4),
		QuickFailureThreshold:          types.DecimalFromFloat(0.000001),
	}
	confirmationParams := &types.ConfirmationPoCParams{
		AlphaThreshold: types.DecimalFromFloat(0.5),
	}

	tests := []struct {
		name               string
		participant        types.Participant
		oldStats           types.CurrentEpochStats
		confirmationParams *types.ConfirmationPoCParams
	}{
		{
			name: "active path remains stable",
			participant: types.Participant{
				CurrentEpochStats: &types.CurrentEpochStats{
					ValidatedInferences:   95,
					InvalidatedInferences: 5,
					InferenceCount:        100,
					MissedRequests:        2,
				},
			},
			oldStats: zeroStats,
		},
		{
			name: "consecutive failure invalid path remains stable",
			participant: types.Participant{
				ConsecutiveInvalidInferences: 20,
			},
			oldStats: zeroStats,
		},
		{
			name: "statistical invalidation path remains stable",
			participant: types.Participant{
				CurrentEpochStats: &types.CurrentEpochStats{
					ValidatedInferences:   7,
					InvalidatedInferences: 7,
				},
			},
			oldStats: zeroStats,
		},
		{
			name: "downtime inactive path remains stable",
			participant: types.Participant{
				CurrentEpochStats: &types.CurrentEpochStats{
					InferenceCount: 50,
					MissedRequests: 60,
				},
			},
			oldStats: zeroStats,
		},
		{
			name: "already invalid path remains stable",
			participant: types.Participant{
				Status: types.ParticipantStatus_INVALID,
				CurrentEpochStats: &types.CurrentEpochStats{
					ValidatedInferences:   7,
					InvalidatedInferences: 7,
				},
			},
			oldStats: zeroStats,
		},
		{
			name: "confirmation poc inactive path remains stable",
			participant: types.Participant{
				CurrentEpochStats: &types.CurrentEpochStats{
					InferenceCount:        10,
					MissedRequests:        0,
					ValidatedInferences:   10,
					InvalidatedInferences: 0,
					ConfirmationPoCRatio:  types.DecimalFromFloat(0.1),
				},
			},
			oldStats:           zeroStats,
			confirmationParams: confirmationParams,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actualStatus, actualReason, actualStats := ComputeStatus(params, tt.confirmationParams, tt.participant, tt.oldStats, testPrecomputed)

			require.NotNil(t, actualStats.InvalidLLR)
			require.NotNil(t, actualStats.InactiveLLR)

			shouldBeActive := tt.name == "active path remains stable"
			if shouldBeActive {
				require.Equal(t, types.ParticipantStatus_ACTIVE, actualStatus, "Expected status ACTIVE for %s", tt.name)
			} else {
				require.NotEqual(t, types.ParticipantStatus_ACTIVE, actualStatus, "Expected status NOT ACTIVE for %s", tt.name)
			}
			_ = actualReason
		})
	}
}

func BenchmarkComputeStatus(b *testing.B) {
	params := &types.ValidationParams{
		FalsePositiveRate:              types.DecimalFromFloat(0.05),
		BadParticipantInvalidationRate: types.DecimalFromFloat(0.1),
		InvalidationHThreshold:         types.DecimalFromFloat(4),
		DowntimeGoodPercentage:         types.DecimalFromFloat(0.1),
		DowntimeBadPercentage:          types.DecimalFromFloat(0.2),
		DowntimeHThreshold:             types.DecimalFromFloat(4),
		QuickFailureThreshold:          types.DecimalFromFloat(0.000001),
	}
	participant := types.Participant{
		CurrentEpochStats: &types.CurrentEpochStats{
			InferenceCount:        50,
			MissedRequests:        60,
			ValidatedInferences:   7,
			InvalidatedInferences: 7,
		},
	}
	old := zeroStats

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = ComputeStatus(params, nil, participant, old, testPrecomputed)
	}
}
