package user

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/types"
)

// A completed request queues ConfirmStart and Finish after the Start diff.
// With the height-sync cadence off, nothing else composes them, so the
// inference stays pending and the reservation stays held. DrainUnpinnedPending
// is that compose. It must do the same when the cadence is on, without
// opening a heartbeat turn.
func TestDrainUnpinnedPending_PublishesReceiptAndFinish(t *testing.T) {
	for _, heartbeatOff := range []bool{false, true} {
		name := "heartbeat_on"
		if heartbeatOff {
			name = "heartbeat_off"
		}
		t.Run(name, func(t *testing.T) {
			session, nonce := sessionWithQueuedReceiptAndFinish(t, heartbeatOff)
			rec, ok := session.StateMachine().Inference(nonce)
			require.True(t, ok)
			require.Equal(t, types.StatusPending, rec.Status)
			held := session.StateMachine().Balance()
			nonceBefore := session.Nonce()
			require.NotNil(t, pendingFinishFor(session, nonce))

			// Heartbeat off returns immediately. Heartbeat on still skips here:
			// this session has no floor, so the cadence refuses to open and
			// does not drain. Either way the receipt stays queued until Drain.
			require.NoError(t, session.MaybeHeartbeat(context.Background()))
			still, ok := session.StateMachine().Inference(nonce)
			require.True(t, ok)
			require.Equal(t, types.StatusPending, still.Status, "the cadence must not be what publishes this receipt")
			require.Equal(t, nonceBefore, session.Nonce())

			require.NoError(t, session.DrainUnpinnedPending(context.Background()))

			got, ok := session.StateMachine().Inference(nonce)
			require.True(t, ok)
			require.Equal(t, types.StatusFinished, got.Status)
			require.Nil(t, pendingFinishFor(session, nonce))
			require.Greater(t, session.StateMachine().Balance(), held, "finish must return the unused reservation")
			require.Zero(t, session.HeartbeatTurnovers(), "draining a receipt must not open a heartbeat turn")

			settled := session.Nonce()
			require.NoError(t, session.DrainUnpinnedPending(context.Background()))
			require.Equal(t, settled, session.Nonce(), "an empty queue must not burn a nonce")
		})
	}
}

// An error-miss Finish is pinned so the drain cannot apply it as a normal
// finish. The receipt still has to land, or the record stays pending and the
// execution timeout cannot be voted.
func TestDrainUnpinnedPending_PinnedFinishStaysQueued(t *testing.T) {
	for _, heartbeatOff := range []bool{false, true} {
		name := "heartbeat_on"
		if heartbeatOff {
			name = "heartbeat_off"
		}
		t.Run(name, func(t *testing.T) {
			session, nonce := sessionWithQueuedReceiptAndFinish(t, heartbeatOff)
			session.PinPendingFinish(nonce)
			held := session.StateMachine().Balance()

			require.NoError(t, session.DrainUnpinnedPending(context.Background()))

			got, ok := session.StateMachine().Inference(nonce)
			require.True(t, ok)
			require.Equal(t, types.StatusStarted, got.Status, "the receipt must apply while the finish stays pinned")
			require.NotNil(t, pendingFinishFor(session, nonce))
			require.Equal(t, held, session.StateMachine().Balance(), "a confirm does not release the reservation")
			require.Zero(t, session.HeartbeatTurnovers())
		})
	}
}

func sessionWithQueuedReceiptAndFinish(t *testing.T, heartbeatOff bool) (*Session, uint64) {
	t.Helper()
	var opts []SessionOption
	if heartbeatOff {
		opts = append(opts, WithDisableHeightSyncHeartbeat(true))
	}
	session, hosts, _ := setupSessionWithOptions(t, 3, 1_000_000, 0, opts...)
	nonce := prepareNonce(t, session)
	rec, ok := session.StateMachine().Inference(nonce)
	require.True(t, ok)
	confirmedAt := time.Now().Unix()
	slot := rec.ExecutorSlot
	require.NoError(t, session.ProcessResponse(int(slot), &host.HostResponse{
		Nonce:       nonce,
		Receipt:     signedExecutorReceipt(t, session, hosts, nonce, confirmedAt),
		ConfirmedAt: confirmedAt,
		Mempool: []*types.DevshardTx{
			finishTxFor(t, hosts[int(slot)], nonce, slot, testutil.TestResponseHash),
		},
	}, nonce))
	require.Equal(t, types.StatusPending, mustInference(t, session, nonce).Status)
	return session, nonce
}

func mustInference(t *testing.T, session *Session, nonce uint64) *types.InferenceRecord {
	t.Helper()
	rec, ok := session.StateMachine().Inference(nonce)
	require.True(t, ok)
	return rec
}
