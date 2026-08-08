package accounting

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

// A nonce that settled as the winner and streamed nothing is one we paid for and could not use.
// Winner-versus-loser alone cannot tell it from a healthy one.
func TestTrackerDelivery_SeparatesAPaidNonceThatReturnedNothing(t *testing.T) {
	tracker := newTestTracker(t)
	registerEscrow(t, tracker, "e1", 7, "m")

	attempts := []struct {
		nonce    uint64
		delivery string
	}{
		{nonce: 1, delivery: ""},
		{nonce: 2, delivery: "empty_stream"},
	}
	for _, attempt := range attempts {
		require.NoError(t, tracker.RecordDiff("e1", attempt.nonce, true))
		require.NoError(t, tracker.RecordRealSend("e1", attempt.nonce, accountingTestNow, PhaseNormal, QuarantineNone))
		require.NoError(t, tracker.RecordUsage("e1", attempt.nonce, UsageWinner, attempt.delivery))
		require.NoError(t, tracker.RecordProtocol("e1", attempt.nonce, 0, ProtocolFinishApplied, types.HostStats{}))
	}

	delivered := map[string]uint64{}
	paidFor := uint64(0)
	for _, record := range tracker.Query(QueryFilter{EpochIndex: 7}) {
		for _, slot := range record.Slots {
			paidFor += slot.Dispositions[DispositionFinishedUsed]
			for reason, count := range slot.DeliveryReasons {
				delivered[reason] += count
			}
		}
	}

	require.Equal(t, uint64(2), paidFor)
	require.Equal(t, map[string]uint64{"empty_stream": 1}, delivered)
}

// The reason vocabulary is closed, so a host cannot inflate the counter map with its own strings.
func TestTrackerDelivery_CollapsesAReasonItDoesNotKnow(t *testing.T) {
	tracker := newTestTracker(t)
	registerEscrow(t, tracker, "e1", 7, "m")
	require.NoError(t, tracker.RecordDiff("e1", 1, true))
	require.NoError(t, tracker.RecordRealSend("e1", 1, accountingTestNow, PhaseNormal, QuarantineNone))
	require.NoError(t, tracker.RecordUsage("e1", 1, UsageLoser, "whatever-a-host-felt-like"))
	require.NoError(t, tracker.RecordProtocol("e1", 1, 0, ProtocolFinishApplied, types.HostStats{}))

	delivered := map[string]uint64{}
	for _, record := range tracker.Query(QueryFilter{EpochIndex: 7}) {
		for _, slot := range record.Slots {
			for reason, count := range slot.DeliveryReasons {
				delivered[reason] += count
			}
		}
	}

	require.Equal(t, map[string]uint64{"unknown": 1}, delivered)
}
