package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

// Crowned winner that streamed clean content but never produced an applied finish; old sendTime so the failure branch skips HandleTimeout deterministically.
func deliveredWinnerInflight(nonce uint64) *inflight {
	inf := &inflight{
		hostIdx:      0,
		hostID:       "host-0",
		nonce:        nonce,
		escrowID:     "escrow-proxy",
		sendTime:     time.Now().Add(-longResponseFailureExemption - time.Second),
		receiptTime:  time.Now().Add(-longResponseFailureExemption),
		done:         make(chan struct{}),
		receiptCh:    make(chan struct{}),
		firstTokenCh: make(chan struct{}),
	}
	inf.outputChunks.Store(2)
	inf.contentChunks.Store(1)
	return inf
}

func TestWinnerDeliveredPendingSettlement(t *testing.T) {
	env := setupTestProxy(t, 2, nil, true)
	env.proxy.redundancy.picker.stop()
	redundancy := env.proxy.redundancy

	brokeMidStream := deliveredWinnerInflight(7)
	brokeMidStream.err = errSimulatedWinnerTransport

	errorStream := deliveredWinnerInflight(7)
	errorStream.errorSource = "error.BadRequestError"

	emptyStream := deliveredWinnerInflight(7)
	emptyStream.contentChunks.Store(0)

	stateDiverged := deliveredWinnerInflight(7)
	stateDiverged.processErr = types.ErrStateHashMismatch

	probeWinner := deliveredWinnerInflight(7)
	probeWinner.probe = true

	cases := []struct {
		name        string
		attempts    []*inflight
		winnerNonce uint64
		want        bool
	}{
		{name: "delivered_content_not_finished", attempts: []*inflight{deliveredWinnerInflight(7)}, winnerNonce: 7, want: true},
		{name: "no_crowned_winner", attempts: []*inflight{deliveredWinnerInflight(7)}, winnerNonce: 0, want: false},
		{name: "broke_mid_stream", attempts: []*inflight{brokeMidStream}, winnerNonce: 7, want: false},
		{name: "error_stream", attempts: []*inflight{errorStream}, winnerNonce: 7, want: false},
		{name: "empty_stream", attempts: []*inflight{emptyStream}, winnerNonce: 7, want: false},
		{name: "state_hash_mismatch", attempts: []*inflight{stateDiverged}, winnerNonce: 7, want: false},
		{name: "probe_winner", attempts: []*inflight{probeWinner}, winnerNonce: 7, want: false},
		{name: "winner_nonce_absent", attempts: []*inflight{deliveredWinnerInflight(7)}, winnerNonce: 99, want: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			require.Equal(t, testCase.want, redundancy.winnerDeliveredPendingSettlement(testCase.attempts, testCase.winnerNonce))
		})
	}
}

func deliveredPendingSettlementEnv(t *testing.T) func(winner *inflight, opts raceFinishOptions) RequestAccountingRecord {
	t.Helper()
	env := setupTestProxy(t, 2, nil, true)
	env.proxy.redundancy.picker.stop()

	store, err := NewPerfStore(filepath.Join(t.TempDir(), "perf.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	redundancy := env.proxy.redundancy
	redundancy.perf = NewPerfTracker(store)
	redundancy.devshardID = "escrow-proxy"

	return func(winner *inflight, opts raceFinishOptions) RequestAccountingRecord {
		winner.hostID = env.session.HostLabel(0)
		ctx, requestID := ensureRequestLogContext(context.Background())
		redundancy.perf.RecordAccountingRequestStart(requestID, "escrow-proxy", "llama", time.Unix(1000, 0))
		require.False(t, env.session.IsNonceFinished(winner.nonce))
		require.Error(t, redundancy.finishRaceOutcome(ctx, []*inflight{winner}, defaultParams(), Decision{Reason: "primary_only"}, winner.nonce, opts))
		record, ok, findErr := redundancy.perf.FindAccountingRequest(requestID, "escrow-proxy")
		require.NoError(t, findErr)
		require.True(t, ok)
		return record
	}
}

// Winner delivered content but no nonce finished, reached via forceTreatAsFailure (the #1387 route): accounted with the real nonce and delivered_pending_settlement, not failed/0.
func TestFinishRaceOutcome_ForcedDeliveredWinnerRecordsPendingSettlement(t *testing.T) {
	run := deliveredPendingSettlementEnv(t)
	record := run(deliveredWinnerInflight(7), raceFinishOptions{forceTreatAsFailure: true})
	require.Equal(t, "delivered_pending_settlement", record.Outcome)
	require.Equal(t, uint64(7), record.WinnerNonce)
}

func TestFinishRaceOutcome_BrokenWinnerStaysFailed(t *testing.T) {
	run := deliveredPendingSettlementEnv(t)
	winner := deliveredWinnerInflight(7)
	winner.err = errSimulatedWinnerTransport
	record := run(winner, raceFinishOptions{forceTreatAsFailure: true})
	require.Equal(t, "failed", record.Outcome)
	require.Equal(t, uint64(0), record.WinnerNonce)
}
