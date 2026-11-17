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

func sessionWithQueuedReceiptAndFinish(t *testing.T, heartbeatOff bool, extra ...SessionOption) (*Session, uint64) {
	t.Helper()
	opts := append([]SessionOption{}, extra...)
	if heartbeatOff {
		opts = append(opts, WithDisableHeightSyncHeartbeat(true))
	}
	session, hosts, _ := setupSessionWithOptions(t, 3, 1_000_000, 0, opts...)
	t.Cleanup(func() { _ = session.Close() })
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

// A following chat carries the queued receipt and Finish inside its Start
// diff. That is one nonce for the new inference, not a second nonce for the
// previous one. The quiet timer is armed the way the gateway arms it, and
// must not fire once PrepareInference has taken the queue.
func TestQuietSettle_SuccessorStartCarriesTheFinish(t *testing.T) {
	for _, heartbeatOff := range []bool{false, true} {
		name := "heartbeat_on"
		if heartbeatOff {
			name = "heartbeat_off"
		}
		t.Run(name, func(t *testing.T) {
			session, first := sessionWithQueuedReceiptAndFinish(t, heartbeatOff, WithQuietSettleDelay(200*time.Millisecond))
			before := session.Nonce()
			session.ArmQuietSettle()

			second := prepareNonce(t, session)
			time.Sleep(400 * time.Millisecond)

			require.Equal(t, before+1, session.Nonce(), "the successor Start is the only new nonce")
			require.Equal(t, second, session.Nonce())
			require.Equal(t, types.StatusFinished, mustInference(t, session, first).Status)
			require.Nil(t, pendingFinishFor(session, first))
			require.Equal(t, types.StatusPending, mustInference(t, session, second).Status)

			diff := diffByNonce(t, session.Diffs(), second)
			require.Equal(t, 1, diffStartCount(diff))
			require.True(t, diffHasFinish(diff, first), "Finish of %d must share nonce %d", first, second)
			require.False(t, isHeartbeatPlaneDiff(diff))
			require.Zero(t, session.HeartbeatTurnovers())
		})
	}
}

// With no later chat, one quiet compose publishes the leftover. That nonce
// is not an inference and does not open a heartbeat turn.
func TestQuietSettle_PublishesLeftoverWithoutASuccessor(t *testing.T) {
	for _, heartbeatOff := range []bool{false, true} {
		name := "heartbeat_on"
		if heartbeatOff {
			name = "heartbeat_off"
		}
		t.Run(name, func(t *testing.T) {
			session, nonce := sessionWithQueuedReceiptAndFinish(t, heartbeatOff, WithQuietSettleDelay(40*time.Millisecond))
			before := session.Nonce()
			held := session.StateMachine().Balance()
			session.ArmQuietSettle()

			require.Eventually(t, func() bool {
				return session.Nonce() == before+1
			}, 2*time.Second, 10*time.Millisecond)

			require.Equal(t, types.StatusFinished, mustInference(t, session, nonce).Status)
			require.Nil(t, pendingFinishFor(session, nonce))
			require.Greater(t, session.StateMachine().Balance(), held)
			require.Zero(t, session.HeartbeatTurnovers())

			diff := diffByNonce(t, session.Diffs(), before+1)
			require.Zero(t, diffStartCount(diff), "the quiet nonce is not an inference")
			require.True(t, diffHasFinish(diff, nonce))
			require.False(t, isHeartbeatPlaneDiff(diff))
			require.Equal(t, 1, countStartDiffs(session.Diffs()))
		})
	}
}

func TestQuietSettle_PinnedFinishStaysQueued(t *testing.T) {
	for _, heartbeatOff := range []bool{false, true} {
		name := "heartbeat_on"
		if heartbeatOff {
			name = "heartbeat_off"
		}
		t.Run(name, func(t *testing.T) {
			session, nonce := sessionWithQueuedReceiptAndFinish(t, heartbeatOff, WithQuietSettleDelay(40*time.Millisecond))
			session.PinPendingFinish(nonce)
			held := session.StateMachine().Balance()
			before := session.Nonce()
			session.ArmQuietSettle()

			require.Eventually(t, func() bool {
				return session.Nonce() == before+1
			}, 2*time.Second, 10*time.Millisecond)

			require.Equal(t, types.StatusStarted, mustInference(t, session, nonce).Status)
			require.NotNil(t, pendingFinishFor(session, nonce))
			require.Equal(t, held, session.StateMachine().Balance())
			require.Zero(t, session.HeartbeatTurnovers())
			diff := diffByNonce(t, session.Diffs(), before+1)
			require.Zero(t, diffStartCount(diff))
			require.False(t, diffHasFinish(diff, nonce))
		})
	}
}

// Each inference is one nonce. The receipt and Finish of inference K ride
// the Start diff of inference K+1. A heartbeat span may add nonces, and
// those diffs are not inferences.
func TestOneNoncePerInferenceExceptHeartbeat(t *testing.T) {
	params := InferenceParams{
		Prompt:      testutil.TestPrompt,
		Model:       "llama",
		InputLength: uint64(len(testutil.TestPrompt)),
		MaxTokens:   testutil.TestMaxTokens,
		StartedAt:   time.Now().Unix(),
	}
	const n = 3

	for _, heartbeatOff := range []bool{false, true} {
		name := "heartbeat_on"
		if heartbeatOff {
			name = "heartbeat_off"
		}
		t.Run(name, func(t *testing.T) {
			var opts []SessionOption
			opts = append(opts, WithQuietSettleDelay(time.Hour))
			if heartbeatOff {
				opts = append(opts, WithDisableHeightSyncHeartbeat(true))
			}
			session, _, _ := setupSessionWithOptions(t, 3, 1_000_000, 0, opts...)
			rememberJournal(session)
			t.Cleanup(func() { _ = session.Close() })

			for i := 0; i < n; i++ {
				_, err := session.SendInference(context.Background(), params)
				require.NoError(t, err)
				session.ArmQuietSettle()
			}

			require.Equal(t, uint64(n), session.Nonce())
			diffs := seenDiffs(session)
			require.Len(t, diffs, n)
			for i, diff := range diffs {
				require.Equal(t, uint64(i+1), diff.Nonce)
				require.Equal(t, 1, diffStartCount(diff), "nonce %d must be one inference", diff.Nonce)
				require.False(t, isHeartbeatPlaneDiff(diff))
				if i == 0 {
					require.False(t, diffHasFinish(diff, 1))
					continue
				}
				require.True(t, diffHasFinish(diff, uint64(i)),
					"Finish of inference %d must share the Start nonce %d", i, diff.Nonce)
			}
			require.NotNil(t, pendingFinishFor(session, uint64(n)),
				"the last Finish stays queued until a successor or the quiet settle")
			require.Equal(t, n, countStartDiffs(diffs))
		})
	}

	t.Run("heartbeat_span", func(t *testing.T) {
		var height uint64 = 100
		session := setupHeartbeatSession(t, &height)
		t.Cleanup(func() { _ = session.Close() })
		starts := countStartDiffs(seenDiffs(session))
		base := session.Nonce()

		require.NoError(t, session.MaybeHeartbeat(context.Background()))

		require.Greater(t, session.Nonce(), base, "a heartbeat span adds nonces")
		require.Greater(t, countHeartbeats(seenDiffs(session)), 0)
		require.Equal(t, starts, countStartDiffs(seenDiffs(session)),
			"heartbeat nonces must not be counted as inferences")
		for _, diff := range seenDiffs(session) {
			if diff.Nonce <= base {
				continue
			}
			require.Zero(t, diffStartCount(diff), "nonce %d is a heartbeat, not an inference", diff.Nonce)
			require.True(t, isHeartbeatPlaneDiff(diff), "nonce %d has no heartbeat tx", diff.Nonce)
		}
	})
}

func diffByNonce(t *testing.T, diffs []types.Diff, nonce uint64) types.Diff {
	t.Helper()
	for _, diff := range diffs {
		if diff.Nonce == nonce {
			return diff
		}
	}
	t.Fatalf("nonce %d not in the log", nonce)
	return types.Diff{}
}

func diffStartCount(diff types.Diff) int {
	n := 0
	for _, tx := range diff.Txs {
		if tx.GetStartInference() != nil {
			n++
		}
	}
	return n
}

func diffHasFinish(diff types.Diff, inferenceID uint64) bool {
	for _, tx := range diff.Txs {
		if finish := tx.GetFinishInference(); finish != nil && finish.InferenceId == inferenceID {
			return true
		}
	}
	return false
}

func countStartDiffs(diffs []types.Diff) int {
	n := 0
	for _, diff := range diffs {
		if diffStartCount(diff) > 0 {
			n++
		}
	}
	return n
}

func isHeartbeatPlaneDiff(diff types.Diff) bool {
	for _, tx := range diff.Txs {
		if tx.GetHeartbeat() != nil || tx.GetHeightAck() != nil {
			return true
		}
		if force := tx.GetForceHeightSyncTurn(); force != nil && force.Reason == heartbeatForceReason {
			return true
		}
	}
	return false
}
