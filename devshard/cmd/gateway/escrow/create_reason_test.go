package escrow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Test flow:
//  1. Table-driven: the two create paths outside the funding planner — a bridge temp through fillToLabel and the operator's create — run once on a rotation manager with a recording narrator.
//  2. Assert the one escrow it created was narrated with the reason of its path.
func TestEveryCreateNarratesTheReasonItWasMadeFor(t *testing.T) {
	model := ModelConfig{ModelID: "model-a", Amount: 1000, PrivateKeyEnv: "MODEL_A_KEY", TargetCount: 1, TempCount: 1, ReserveCount: 1}
	snapshot := servedSnapshot(7, 500, "model-a")
	ctx := context.Background()
	testCases := []struct {
		name   string
		create func(manager *Manager) error
		want   string
	}{
		{name: "a bridge temp", want: "bridge", create: func(manager *Manager) error {
			return manager.fillToLabel(ctx, roleTemp, snapshot.EpochIndex, []ModelConfig{model}, func(ModelConfig) int { return 1 }, snapshot, nil)[0].err
		}},
		{name: "the operator's create", want: "operator", create: func(manager *Manager) error {
			_, err := manager.CreateEscrow(ctx, model)
			return err
		}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			txClient := &fakeTxClient{createEscrowFn: succeedingCreateEscrowFn(100)}
			manager := newRotationManager(t, newFakeStore(), txClient, false)
			narrator := &recordingLifecycleNarrator{}
			manager.narrator, manager.snapshots = narrator, &fakeSnapshotSource{snapshot: snapshot}

			require.NoError(t, testCase.create(manager))

			require.Equal(t, map[string]string{"100": testCase.want}, narrator.createdReasons(), "created reasons after %s", testCase.name)
		})
	}
}
