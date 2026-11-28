package harness

import (
	"encoding/json"
	"testing"

	"devshard/heightsync"

	"github.com/stretchr/testify/require"
)

func TestHeightSyncViewReadsTheDebugEntry(t *testing.T) {
	raw := `{"devshard_id":"7","tips":[{"slot":0,"height":120,"fresh":true},{"slot":1,"height":100,"fresh":false}],` +
		`"cadence_counts":{"heartbeat_opened":3},"abandoned_turns":1,"peer_seen":[{"observer":0,"count":2}],"anchors_last_sealed":null,"blocks_without_anchor":4}`
	var view HeightSyncView
	require.NoError(t, json.Unmarshal([]byte(raw), &view))

	spread, claimed := view.HeightSpread()
	require.True(t, claimed)
	require.Equal(t, uint64(20), spread)
	require.Equal(t, 1, view.FreshTips())
	require.Equal(t, 1, view.StaleTips())
	require.Equal(t, uint64(3), view.CadenceCount(heightsync.CadenceHeartbeatOpened))
	require.Zero(t, view.CadenceCount(heightsync.CadenceTurnSettledDegraded))
	require.Nil(t, view.AnchorsLastSealed)
	require.Equal(t, uint64(4), view.BlocksWithoutAnchor)
}

func TestHeightSyncViewWithoutTipsHasNoSpread(t *testing.T) {
	_, claimed := HeightSyncView{}.HeightSpread()
	require.False(t, claimed)
}
