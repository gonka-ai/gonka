package calculations

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
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

func TestSequentialWorkShare(t *testing.T) {
	tests := []struct {
		name       string
		totalCost  int64
		numWorkers int
	}{
		{
			name:       "Real-world scenario",
			totalCost:  1136000,
			numWorkers: 4,
		},
		{
			name:       "Simple case",
			totalCost:  100,
			numWorkers: 2,
		},
		{
			name:       "Large group",
			totalCost:  1000000,
			numWorkers: 10,
		},
		{
			name:       "Small amount",
			totalCost:  7,
			numWorkers: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Track balances
			balances := make(map[string]int64)
			balances["worker1"] = tt.totalCost

			// Sequentially add workers
			existingWorkers := []string{"worker1"}
			for i := 2; i <= tt.numWorkers; i++ {
				newWorker := fmt.Sprintf("worker%d", i)
				adjustments := ShareWork(existingWorkers, []string{newWorker}, tt.totalCost)

				// Apply adjustments
				for _, adj := range adjustments {
					balances[adj.ParticipantId] += adj.WorkAdjustment
				}

				// Add new worker to existing workers list
				existingWorkers = append(existingWorkers, newWorker)
			}

			// Calculate total
			var total int64
			for _, balance := range balances {
				total += balance
			}

			// Verify total balance
			require.Equal(t, tt.totalCost, total, "Total balance must be preserved")
		})
	}
}

// TestShareWorkComprehensive - Tests a wide range of costs and worker counts
func TestShareWorkComprehensive(t *testing.T) {
	// Test costs from 1000 to 100000 with step 1000
	costs := []int64{}
	for cost := int64(1000); cost <= 100000; cost += 1000 {
		costs = append(costs, cost)
	}

	// Test worker counts: 1, 2, 3, 4, 5, 10, 20, 50, 100, 200, 500, 1000
	workerCounts := []int{1, 2, 3, 4, 5, 10, 20, 50, 100, 200, 500, 1000}

	totalTests := len(costs) * len(workerCounts)
	t.Logf("Running %d comprehensive balance tests...", totalTests)

	for _, cost := range costs {
		for _, numWorkers := range workerCounts {
			// Skip single worker case (nothing to test)
			if numWorkers == 1 {
				continue
			}

			// Track balances
			balances := make(map[string]int64)
			balances["worker1"] = cost

			// Sequentially add workers
			existingWorkers := []string{"worker1"}
			for i := 2; i <= numWorkers; i++ {
				newWorker := fmt.Sprintf("worker%d", i)
				adjustments := ShareWork(existingWorkers, []string{newWorker}, cost)

				// Apply adjustments
				for _, adj := range adjustments {
					balances[adj.ParticipantId] += adj.WorkAdjustment
				}

				// Add new worker to existing workers list
				existingWorkers = append(existingWorkers, newWorker)
			}

			// Calculate total
			var total int64
			for _, balance := range balances {
				total += balance
			}

			// Verify total balance
			if total != cost {
				t.Errorf("Balance not preserved: cost=%d, workers=%d, expected=%d, actual=%d",
					cost, numWorkers, cost, total)
			}
		}
	}

	t.Logf("All %d comprehensive tests passed!", totalTests-len(costs)) // -len(costs) because we skip single worker cases
}
