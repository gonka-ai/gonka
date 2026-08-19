package user

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/types"
)

func TestUser_ForceHeightSyncTurn_AppearsOnlyInTriggerDiff(t *testing.T) {
	const numHosts = 3
	const k = uint64(10)
	const slots = uint64(numHosts)
	session, _, _ := setupSessionWithOptions(t, numHosts, 100000, 100, WithHeightSyncCadence(k, slots))

	ctx := context.Background()
	base := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: 50, StartedAt: 1000,
	}
	forced := base
	forced.ForceHeightSyncAnchor = true

	countForceTxs := func(d types.Diff) int {
		n := 0
		for _, tx := range d.Txs {
			if tx.GetForceHeightSyncTurn() != nil {
				n++
			}
		}
		return n
	}
	findForceTx := func(d types.Diff) *types.MsgForceHeightSyncTurn {
		for _, tx := range d.Txs {
			if inner := tx.GetForceHeightSyncTurn(); inner != nil {
				return inner
			}
		}
		return nil
	}

	_, err := session.SendInference(ctx, forced)
	require.NoError(t, err, "trigger inference at nonce 1")
	require.Equal(t, uint64(1), session.Nonce())

	for n := 2; n <= int(slots); n++ {
		_, err = session.SendInference(ctx, forced)
		require.NoError(t, err, "in-window inference at nonce %d", n)
	}
	require.Equal(t, slots, session.Nonce(), "session advanced through the full forced window")

	diffs := session.Diffs()
	require.Len(t, diffs, int(slots))
	require.Equal(t, 1, countForceTxs(diffs[0]),
		"trigger diff at nonce 1 must contain exactly one MsgForceHeightSyncTurn")
	tx := findForceTx(diffs[0])
	require.NotNil(t, tx, "trigger diff must carry the force-turn tx")
	require.Equal(t, uint64(1), tx.TriggerNonce)
	require.Equal(t, slots, tx.SlotsNum)
	require.Equal(t, slots, tx.EndNonce)
	require.Equal(t, k, tx.AnchorK)

	for i := 1; i < int(slots); i++ {
		require.Equal(t, 0, countForceTxs(diffs[i]),
			"diff at nonce %d (in-window) must NOT contain MsgForceHeightSyncTurn", diffs[i].Nonce)
	}

	require.True(t, session.StateMachine().HeightSyncForcedTurnActive(slots),
		"sanity: forced turn still active at the last in-window nonce")
	require.False(t, session.StateMachine().HeightSyncForcedTurnActive(slots+1),
		"sanity: forced turn closed for the next nonce")

	_, err = session.SendInference(ctx, forced)
	require.NoError(t, err, "next forced trigger after window closes")
	diffs = session.Diffs()
	require.Len(t, diffs, int(slots)+1)
	require.Equal(t, 1, countForceTxs(diffs[int(slots)]),
		"a fresh ForceHeightSyncAnchor after the previous window closes must re-open a new turn")
	tx2 := findForceTx(diffs[int(slots)])
	require.NotNil(t, tx2)
	require.Equal(t, slots+1, tx2.TriggerNonce)
	require.Equal(t, 2*slots, tx2.EndNonce)
}
