package calculations

import (
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

var zeroStats = types.CurrentEpochStats{
	InvalidLLR:  types.DecimalFromFloat(0),
	InactiveLLR: types.DecimalFromFloat(0),
}

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
			status, reason, _ := ComputeStatus(tt.params, nil, tt.participant, zeroStats, 0)
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

	status, reason, _ := ComputeStatus(params, nil, participant, zeroStats, 0)
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
		status, reason, _ := ComputeStatus(params, nil, participant, zeroStats, 0)
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

// TestSkipComputeStatusOnCompletionBreaksLLR shows that if we skip calling ComputeStatus on
// successful completions (e.g. SetParticipantReasonNone from MsgFinishInference), the inactive
// LLR is wrong when we later run ComputeStatus on a miss: we only apply miss deltas, not the
// completion deltas, so the LLR can flip from ACTIVE to INACTIVE for the same event sequence.
func TestSkipComputeStatusOnCompletionBreaksLLR(t *testing.T) {
	params := types.DefaultValidationParams()
	completions := 50
	misses := 6

	emptyStats := func() types.CurrentEpochStats {
		return types.CurrentEpochStats{
			InvalidLLR:  types.DecimalFromFloat(0),
			InactiveLLR: types.DecimalFromFloat(0),
		}
	}

	scope := StatusCheckScope(0) // RunAll so inactive check runs

	// A: simulate 50 successful inferences + 6 misses (calling ComputeStatus every time)
	stored := emptyStats()
	for i := 0; i < completions; i++ {
		_, _, stored = ComputeStatus(params, nil, types.Participant{
			CurrentEpochStats: &types.CurrentEpochStats{
				InferenceCount:  stored.InferenceCount + 1,
				MissedRequests:  stored.MissedRequests,
				InactiveLLR:     stored.InactiveLLR,
				InvalidLLR:      stored.InvalidLLR,
			},
		}, stored, scope)
	}
	var st types.ParticipantStatus
	for i := 0; i < misses; i++ {
		st, _, stored = ComputeStatus(params, nil, types.Participant{
			CurrentEpochStats: &types.CurrentEpochStats{
				InferenceCount:  stored.InferenceCount,
				MissedRequests:  stored.MissedRequests + 1,
				InactiveLLR:     stored.InactiveLLR,
				InvalidLLR:      stored.InvalidLLR,
			},
		}, stored, scope)
	}
	t.Logf("always compute: LLR=%s status=%s", stored.InactiveLLR.ToDecimal(), st)
	require.Equal(t, types.ParticipantStatus_ACTIVE, st)

	// B: same event sequence but skip ComputeStatus on completions (only run on misses)
	skipped := emptyStats()
	skipped.InferenceCount = uint64(completions)
	var st2 types.ParticipantStatus
	for i := 0; i < misses; i++ {
		st2, _, skipped = ComputeStatus(params, nil, types.Participant{
			CurrentEpochStats: &types.CurrentEpochStats{
				InferenceCount: uint64(completions),
				MissedRequests: skipped.MissedRequests + 1,
				InactiveLLR:    skipped.InactiveLLR,
				InvalidLLR:     skipped.InvalidLLR,
			},
		}, skipped, scope)
	}
	t.Logf("skip completions: LLR=%s status=%s", skipped.InactiveLLR.ToDecimal(), st2)

	// Same participant, same event sequence (50 completions, 6 misses), but B is marked INACTIVE
	// because InactiveLLR never received the 50 * logPass contributions from completions.
	require.Equal(t, types.ParticipantStatus_INACTIVE, st2)

	// C: batch 50 completions (one ComputeStatus with delta (50,0)), then apply 6 misses.
	// Same net deltas as A, so InactiveLLR and status must match A.
	skipped2 := emptyStats()
	var st3 types.ParticipantStatus
	st3, _, skipped2 = ComputeStatus(params, nil, types.Participant{
		CurrentEpochStats: &types.CurrentEpochStats{
			InferenceCount: uint64(completions),
			MissedRequests: 0,
			InactiveLLR:    skipped2.InactiveLLR,
			InvalidLLR:     skipped2.InvalidLLR,
		},
	}, skipped2, scope)
	for i := 0; i < misses; i++ {
		st3, _, skipped2 = ComputeStatus(params, nil, types.Participant{
			CurrentEpochStats: &types.CurrentEpochStats{
				InferenceCount: skipped2.InferenceCount,
				MissedRequests: skipped2.MissedRequests + 1,
				InactiveLLR:    skipped2.InactiveLLR,
				InvalidLLR:     skipped2.InvalidLLR,
			},
		}, skipped2, scope)
	}
	t.Logf("batch completions then misses: LLR=%s status=%s", skipped2.InactiveLLR.ToDecimal(), st3)
	require.Equal(t, types.ParticipantStatus_ACTIVE, st3, "same event sequence as A should stay ACTIVE")
	require.Equal(t, stored.InactiveLLR.ToDecimal().String(), skipped2.InactiveLLR.ToDecimal().String(),
		"InactiveLLR should equal case A when we batch completions then apply misses")
}
