package user

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

func TestBootstrapFlushRestoresPendingAtOriginalIndex(t *testing.T) {
	session, _, _ := setupSession(t, 1, 100000, 100)
	confirm := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{InferenceId: 7}}}
	ack1 := &types.DevshardTx{Tx: &types.DevshardTx_HeightAck{HeightAck: &types.MsgHeightAck{RefNonce: 1, SlotId: 0}}}
	heartbeat := &types.DevshardTx{Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{SlotsNum: 1}}}
	finish := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{InferenceId: 7}}}
	ack2 := &types.DevshardTx{Tx: &types.DevshardTx_HeightAck{HeightAck: &types.MsgHeightAck{RefNonce: 2, SlotId: 0}}}

	session.mu.Lock()
	session.pendingTxs = []*types.DevshardTx{confirm, ack1, heartbeat, finish, ack2}
	session.reindexPendingLocked()
	split := session.detachNonAckPendingLocked()
	acks := append([]*types.DevshardTx(nil), session.pendingTxs...)
	// The flush applied the first ack. The others keep the index they had.
	session.pendingTxs = session.pendingTxs[1:]
	session.reindexPendingLocked()
	session.restoreParkedPendingLocked(split)
	got := append([]*types.DevshardTx(nil), session.pendingTxs...)
	session.mu.Unlock()

	require.Equal(t, []*types.DevshardTx{ack1, ack2}, acks)
	require.Equal(t, []*types.DevshardTx{confirm, heartbeat, finish, ack2}, got)
}

func TestBootstrapFlushClearsParkFlagWhenComposeFails(t *testing.T) {
	session, _, _ := setupSession(t, 1, 100000, 100)
	confirm := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{InferenceId: 7}}}
	ack := &types.DevshardTx{Tx: &types.DevshardTx_HeightAck{HeightAck: &types.MsgHeightAck{RefNonce: 1, SlotId: 0}}}
	finish := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{InferenceId: 7}}}

	session.mu.Lock()
	session.pendingTxs = []*types.DevshardTx{confirm, ack, finish}
	session.reindexPendingLocked()
	session.bootstrapAckFlush = true
	session.heartbeatFlushLeft = 1
	session.turnTracker.Observe(1, []*types.DevshardTx{{
		Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{SlotsNum: 1, Reason: "quiet_session"}},
	}}, 0)
	session.mu.Unlock()
	// The next nonce is past the active cap, so compose fails before it publishes.
	session.SetChainMaxNonce(1)

	err := session.flushHeartbeatAckRounds(context.Background())
	require.Error(t, err)

	session.mu.Lock()
	flag := session.bootstrapAckFlush
	got := append([]*types.DevshardTx(nil), session.pendingTxs...)
	session.mu.Unlock()
	require.False(t, flag)
	require.Equal(t, []*types.DevshardTx{confirm, ack, finish}, got)
}

func TestBootstrapFlushInFlightDoesNotClearTheParkFlag(t *testing.T) {
	session, _, _ := setupSession(t, 1, 100000, 100)
	session.ackFlushInFlight.Store(true)
	session.mu.Lock()
	session.bootstrapAckFlush = true
	session.mu.Unlock()

	require.NoError(t, session.flushHeartbeatAckRounds(context.Background()))

	session.mu.Lock()
	flag := session.bootstrapAckFlush
	session.mu.Unlock()
	require.True(t, flag, "a flush that does not own the park flag leaves it set")
}
