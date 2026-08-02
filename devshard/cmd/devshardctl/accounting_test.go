package main

import (
	"path/filepath"
	"testing"
	"time"

	"devshard/accounting"
	"devshard/types"

	"github.com/stretchr/testify/require"
)

func TestGatewayAccountingAdapterRecordsEvents(t *testing.T) {
	tracker, err := accounting.OpenTracker(filepath.Join(t.TempDir(), "accounting.db"), 0, time.Hour)
	require.NoError(t, err)
	defer tracker.Close()
	recorder := newGatewayAccountingRecorder(tracker)
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
	recorder.recordGhost("e1", 1, "participant_throttled_no_send", "probe")
	require.NoError(t, tracker.RecordDiff("e1", 2, true))
	recorder.recordRealSend("e1", 2, "shadow")
	recorder.recordTimeout("e1", 2, "refused", "failed", "insufficient_votes", "no_receipt", "")

	records := tracker.Query(accounting.QueryFilter{EpochIndex: 1})
	var ghost, unfinished uint64
	for _, record := range records {
		ghost += record.Dispositions[accounting.DispositionGhost]
		unfinished += record.Dispositions[accounting.DispositionUnfinishedRefused]
	}
	require.Equal(t, uint64(1), ghost)
	require.Equal(t, uint64(1), unfinished)
}
