package calculations

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestShareRewards(t *testing.T) {
	tests := []struct {
		name            string
		existingWorkers []string
		newWorkers      []string
		actualCost      int64
		expectedActions []Adjustment
	}{
		{
			name:            "No workers",
			existingWorkers: []string{},
			newWorkers:      []string{},
			actualCost:      100,
			expectedActions: []Adjustment{},
		},
		{
			name:            "Only existing workers",
			existingWorkers: []string{"worker1", "worker2"},
			newWorkers:      []string{},
			actualCost:      100,
			expectedActions: []Adjustment{},
		},
		{
			name:            "Only new workers",
			existingWorkers: []string{},
			newWorkers:      []string{"worker1", "worker2"},
			actualCost:      100,
			expectedActions: []Adjustment{
				{WorkAdjustment: 50, ParticipantId: "worker1"},
				{WorkAdjustment: 50, ParticipantId: "worker2"},
			},
		},
		{
			name:            "Existing and new workers",
			existingWorkers: []string{"worker1"},
			newWorkers:      []string{"worker2", "worker3"},
			actualCost:      100,
			expectedActions: []Adjustment{
				// Note the extra going to the first worker (the one who did the initial work)
				{WorkAdjustment: -66, ParticipantId: "worker1"},
				{WorkAdjustment: 33, ParticipantId: "worker2"},
				{WorkAdjustment: 33, ParticipantId: "worker3"},
			},
		},
		{
			name:            "One existing, one new, cost 100",
			existingWorkers: []string{"worker1"},
			newWorkers:      []string{"worker2"},
			actualCost:      100,
			expectedActions: []Adjustment{
				{WorkAdjustment: -50, ParticipantId: "worker1"},
				{WorkAdjustment: 50, ParticipantId: "worker2"},
			},
		},
		{
			name:            "Very uneven distribution",
			existingWorkers: []string{"worker1", "worker2", "worker3", "worker4", "worker5", "worker6", "worker7", "worker8"},
			newWorkers:      []string{"worker9"},
			actualCost:      100,
			expectedActions: []Adjustment{
				{WorkAdjustment: -4, ParticipantId: "worker1"},
				{WorkAdjustment: -1, ParticipantId: "worker2"},
				{WorkAdjustment: -1, ParticipantId: "worker3"},
				{WorkAdjustment: -1, ParticipantId: "worker4"},
				{WorkAdjustment: -1, ParticipantId: "worker5"},
				{WorkAdjustment: -1, ParticipantId: "worker6"},
				{WorkAdjustment: -1, ParticipantId: "worker7"},
				{WorkAdjustment: -1, ParticipantId: "worker8"},
				{WorkAdjustment: 11, ParticipantId: "worker9"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShareWork(tt.existingWorkers, tt.newWorkers, tt.actualCost)
			require.ElementsMatch(t, tt.expectedActions, got)
		})
	}
}
