package state

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

func TestSnapshotWithInferenceSkipsTheMap(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	sm, err := NewStateMachine("escrow-snapshot", config, group, 100000, user.Address(), signing.NewSecp256k1Verifier(),
		testutil.MustMemoryStore(t, "escrow-snapshot", user.Address(), config, group, 100000))
	require.NoError(t, err)

	plant := func(n int) {
		t.Helper()
		inferences := make(map[uint64]*types.InferenceRecord, n)
		for id := 1; id <= n; id++ {
			inferences[uint64(id)] = &types.InferenceRecord{
				Status:     types.StatusPending,
				PromptHash: []byte{1, 2, 3, 4},
				Model:      "llama",
			}
		}
		sm.mu.Lock()
		sm.state.Inferences = inferences
		sm.mu.Unlock()
	}

	plant(8)
	st, rec, ok := sm.SnapshotWithInference(1)
	require.True(t, ok)
	require.Nil(t, st.Inferences)
	require.Equal(t, "llama", rec.Model)
	sm.mu.Lock()
	sm.state.Inferences[1].Model = "changed"
	sm.mu.Unlock()
	require.Equal(t, "llama", rec.Model)
	_, _, ok = sm.SnapshotWithInference(0)
	require.False(t, ok)

	plant(8)
	small := testing.AllocsPerRun(20, func() { sm.SnapshotWithInference(1) })
	plant(4000)
	large := testing.AllocsPerRun(20, func() { sm.SnapshotWithInference(1) })
	require.Equal(t, small, large)
}
