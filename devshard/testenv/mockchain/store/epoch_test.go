package store_test

import (
	"testing"

	"devshard/testenv/mockchain/seed"

	"github.com/stretchr/testify/require"
)

func TestPlanEpochAdvance_TargetsNextPocStart(t *testing.T) {
	st := seed.Defaults()
	require.Equal(t, int64(500), st.GetNextPocStartBlockHeight())

	plan := st.PlanEpochAdvance()
	require.Equal(t, int64(150), plan.FromHeight)
	require.Equal(t, int64(500), plan.ToHeight)
	require.Equal(t, uint64(2), plan.NewEpoch.Index)
	require.Equal(t, int64(500), plan.NewEpoch.PocStartBlockHeight)
	require.Equal(t, int64(900), plan.NewNextPoc)
}

func TestApplyEpochAdvance_UpdatesNextPocStart(t *testing.T) {
	st := seed.Defaults()
	plan := st.PlanEpochAdvance()
	for h := plan.FromHeight + 1; h <= plan.ToHeight; h++ {
		st.SetBlockHeight(h)
	}
	st.ApplyEpochAdvance(plan)
	require.Equal(t, uint64(2), st.GetEpoch().Index)
	require.Equal(t, int64(500), st.GetBlockHeight())
	require.Equal(t, int64(500), st.GetEpoch().PocStartBlockHeight)
	require.Equal(t, int64(900), st.GetNextPocStartBlockHeight())
}
