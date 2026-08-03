package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"devshard/accounting"
	"devshard/types"
	"devshard/user"

	"github.com/stretchr/testify/require"
)

func TestGatewayAccountingAdapterRecordsEvents(t *testing.T) {
	tracker, err := accounting.OpenTracker(filepath.Join(t.TempDir(), "accounting.db"), 0, time.Hour)
	require.NoError(t, err)
	defer tracker.Close()
	recorder := accounting.NewRecorder(tracker, nil)
	require.NotNil(t, recorder)
	require.NoError(t, tracker.RegisterEscrow(accounting.EscrowMetadata{
		EscrowID:      "e1",
		CreationEpoch: 1,
		Model:         "m",
		Phase:         accounting.EscrowActive,
		Slots: []types.SlotAssignment{
			{SlotID: 0, ValidatorAddress: "p0"},
			{SlotID: 1, ValidatorAddress: "p1"},
		},
	}))
	require.NoError(t, tracker.RecordDiff("e1", 1, true))
	recorder.Ghost("e1", 1, "participant_throttled_no_send", "probe")
	require.NoError(t, tracker.RecordDiff("e1", 2, true))
	recorder.RealSend("e1", 2, time.Now().Add(-time.Second), "shadow")
	recorder.TimeoutResult("e1", 2, "refused", "failed", "insufficient_votes", "no_receipt", "")

	records := tracker.Query(accounting.QueryFilter{EpochIndex: 1})
	var ghost, unfinished uint64
	for _, record := range records {
		ghost += record.Dispositions[accounting.DispositionGhost]
		unfinished += record.Dispositions[accounting.DispositionUnfinishedRefused]
	}
	require.Equal(t, uint64(1), ghost)
	require.Equal(t, uint64(1), unfinished)
}

func TestAccountingObserverTracksCommittedSessionDiffs(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	tracker, err := accounting.OpenTracker(filepath.Join(t.TempDir(), "accounting.db"), 0, time.Hour)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tracker.Close()) })
	recorder := accounting.NewRecorder(tracker, nil)
	recorder.Attach(accounting.RuntimeMetadata{
		EscrowID:      "escrow-proxy",
		CreationEpoch: 21,
		Model:         "llama",
		TimeoutBuffer: user.TimeoutBuffer,
	}, env.session, env.sm)

	_, err = env.session.SendInference(context.Background(), defaultParams())
	require.NoError(t, err)
	recorder.RealSend("escrow-proxy", 1, time.Now(), "")
	recorder.Usage("escrow-proxy", 1, 1)
	_, err = env.session.PrepareInference(defaultParams())
	require.NoError(t, err)

	var finished uint64
	for _, record := range tracker.Query(accounting.QueryFilter{EpochIndex: 21}) {
		finished += record.Dispositions[accounting.DispositionFinishedUsed]
	}
	require.Equal(t, uint64(1), finished)
}
