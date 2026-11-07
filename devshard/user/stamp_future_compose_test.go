package user

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/types"
)

func TestComposeOmitsFutureHostAck(t *testing.T) {
	wall := time.UnixMilli(1_700_000_000_000)
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	userKey := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	clients := make([]HostClient, len(hosts))
	userSM := sessionMachine(t, config, group, 100000, userKey.Address(), verifier,
		state.WithStampWallClock(func() time.Time { return wall }))
	session, err := NewSession(userSM, userKey, "escrow-1", group, clients, verifier)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })

	hb := &types.DevshardTx{Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
		SlotsNum: uint64(len(group)),
		Reason:   string(heightsync.ReasonQuietSession),
	}}}
	session.mu.Lock()
	hbDiff, _, err := session.composeDiffLocked(context.Background(), []*types.DevshardTx{hb})
	session.mu.Unlock()
	require.NoError(t, err)

	ack := func(slot uint32, height uint64, stamp int64) *types.DevshardTx {
		t.Helper()
		msg := &types.MsgHeightAck{
			RefNonce:          hbDiff.Nonce,
			SlotId:            slot,
			ObservedHeight:    height,
			ObservedBlockHash: []byte{byte(height)},
			ObservedTimeMs:    stamp,
			SyncState:         types.SyncState_SYNCED,
			PeerSeen:          []byte{0xff},
		}
		require.NoError(t, heightsync.SignAck(hosts[slot], msg))
		return &types.DevshardTx{Tx: &types.DevshardTx_HeightAck{HeightAck: msg}}
	}
	future := ack(0, 10, wall.Add(31*time.Second).UnixMilli())
	near := ack(1, 11, wall.Add(10*time.Second).UnixMilli())

	session.mu.Lock()
	diff, _, err := session.composeDiffLocked(context.Background(), []*types.DevshardTx{future, near})
	session.mu.Unlock()
	require.NoError(t, err)

	var sawNear bool
	for _, tx := range diff.Txs {
		got := tx.GetHeightAck()
		if got == nil {
			continue
		}
		require.NotEqual(t, future.GetHeightAck().GetObservedTimeMs(), got.GetObservedTimeMs())
		if got.GetObservedTimeMs() == near.GetHeightAck().GetObservedTimeMs() {
			sawNear = true
		}
	}
	require.True(t, sawNear, "an ack inside the wall skew is signed")
}
