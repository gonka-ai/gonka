package user

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
	"devshard/transport"
	"devshard/types"
)

func TestCachedHeightSyncView_DoesNotTakeSessionLock(t *testing.T) {
	session, _, _ := setupSessionWithOptions(t, 3, 100000, 100, WithHeightSyncCadence(10, 3))
	session.SetHeightSyncPeerTips(transport.NewHeightSyncPeerTips())
	require.True(t, session.HeightSyncWired())
	require.Len(t, session.CachedHeightSyncView().Slots, 3)

	session.mu.Lock()
	defer session.mu.Unlock()

	done := make(chan heightsync.OperatorView, 1)
	go func() { done <- session.CachedHeightSyncView() }()
	select {
	case v := <-done:
		require.Len(t, v.Slots, 3)
		require.Equal(t, "escrow-1", v.DevshardID)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("CachedHeightSyncView blocked on s.mu — scrape must not take the producer lock")
	}
}

func TestCachedHeightSyncView_UnwiredStaysEmpty(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 100)
	require.False(t, session.HeightSyncWired())
	session.publishHeightSyncView()
	require.Empty(t, session.CachedHeightSyncView().Slots)
	require.Empty(t, session.CachedHeightSyncView().DevshardID)
}

func TestCachedHeightSyncView_PublishAfterUnlock(t *testing.T) {
	session, _, _ := setupSessionWithOptions(t, 2, 100000, 100, WithHeightSyncCadence(10, 2))
	require.True(t, session.HeightSyncWired())
	require.Empty(t, session.CachedHeightSyncView().Slots, "wired but not yet published")

	session.SetHeightSyncPeerTips(transport.NewHeightSyncPeerTips())
	view := session.CachedHeightSyncView()
	require.Len(t, view.Slots, 2)
	require.Equal(t, "escrow-1", view.DevshardID)
}

func TestNoteAckObs_RejectsForeignSlot(t *testing.T) {
	session, _, _ := setupSessionWithOptions(t, 3, 100000, 100, WithHeightSyncCadence(10, 3))
	session.SetHeightSyncPeerTips(transport.NewHeightSyncPeerTips())

	session.mu.Lock()
	session.noteAckObsLocked(&types.MsgHeightAck{
		SlotId:            1 << 20,
		SyncState:         types.SyncState_SYNCED,
		PeerSeen:          []byte{0x07},
		ObservedHeight:    42,
		ObservedBlockHash: []byte{1},
	})
	require.Empty(t, session.lastSyncState)
	require.Empty(t, session.lastPeerSeen)
	require.Zero(t, session.anchors.OpenLen(), "forged slot must not allocate an anchor bucket")
	session.mu.Unlock()

	view := session.SnapshotHeightSync()
	require.Empty(t, view.SyncStates)
	require.Empty(t, view.PeerSeen)
}

func TestNoteAckObs_DropsOversizedPeerSeen(t *testing.T) {
	session, _, _ := setupSessionWithOptions(t, 3, 100000, 100, WithHeightSyncCadence(10, 3))
	session.SetHeightSyncPeerTips(transport.NewHeightSyncPeerTips())
	slot := session.group[0].SlotID

	session.mu.Lock()
	session.noteAckObsLocked(&types.MsgHeightAck{
		SlotId:    slot,
		SyncState: types.SyncState_SYNCED,
		PeerSeen:  make([]byte, 1024),
	})
	require.Equal(t, "SYNCED", session.lastSyncState[slot], "sync_state still records for a roster slot")
	require.Nil(t, session.lastPeerSeen[slot], "oversized PeerSeen must not be retained")

	bits := []byte{0x05}
	session.noteAckObsLocked(&types.MsgHeightAck{
		SlotId:    session.group[1].SlotID,
		SyncState: types.SyncState_CATCHING_UP,
		PeerSeen:  bits,
	})
	require.Equal(t, bits, session.lastPeerSeen[session.group[1].SlotID])
	require.Equal(t, "CATCHING_UP", session.lastSyncState[session.group[1].SlotID])
	session.mu.Unlock()

	view := session.SnapshotHeightSync()
	require.Len(t, view.PeerSeen, 1)
	require.Equal(t, session.group[1].SlotID, view.PeerSeen[0].Observer)
	require.Equal(t, bits, view.PeerSeen[0].Bits)
}

func TestPublishHeightSyncView_ThrottlesRapidUpdates(t *testing.T) {
	session, _, _ := setupSessionWithOptions(t, 2, 100000, 100, WithHeightSyncCadence(10, 2))
	session.TestingOnlySetHeightSyncMinPublish(50 * time.Millisecond)
	session.SetHeightSyncPeerTips(transport.NewHeightSyncPeerTips())
	before := session.TestingOnlyHeightSyncPublishCount()
	require.GreaterOrEqual(t, before, uint64(1), "force publish on wire")

	for i := 0; i < 20; i++ {
		session.NoteExchangeOverlap(true, true, true)
	}
	after := session.TestingOnlyHeightSyncPublishCount()
	require.LessOrEqual(t, after-before, uint64(2),
		"rapid publishes must coalesce within the min interval; got %d", after-before)
	session.TestingOnlyFlushHeightSyncView()
	require.Equal(t, uint64(20), session.CachedHeightSyncView().Overlap.Total)
}

func TestPublishHeightSyncView_TrailingEdgeFlushesLastUpdate(t *testing.T) {
	session, _, _ := setupSessionWithOptions(t, 2, 100000, 100, WithHeightSyncCadence(10, 2))
	session.TestingOnlySetHeightSyncMinPublish(20 * time.Millisecond)
	session.SetHeightSyncPeerTips(transport.NewHeightSyncPeerTips())

	// A burst coalesces, so the last mutation is not in the cache yet. Nothing
	// else will mutate this session: without a trailing publish the cached view
	// would stay behind until the next heartbeat.
	for i := 0; i < 10; i++ {
		session.NoteExchangeOverlap(true, true, true)
	}

	require.Eventually(t, func() bool {
		return session.CachedHeightSyncView().Overlap.Total == 10
	}, 2*time.Second, 5*time.Millisecond,
		"the throttled tail must reach the cache on its own")
}

func TestPublishHeightSyncView_CloseCancelsTrailingFlush(t *testing.T) {
	session, _, _ := setupSessionWithOptions(t, 2, 100000, 100, WithHeightSyncCadence(10, 2))
	session.TestingOnlySetHeightSyncMinPublish(time.Hour)
	session.SetHeightSyncPeerTips(transport.NewHeightSyncPeerTips())
	session.NoteExchangeOverlap(true, true, true)

	session.heightSyncPublishMu.Lock()
	armed := session.heightSyncFlush != nil
	session.heightSyncPublishMu.Unlock()
	require.True(t, armed, "a coalesced publish must arm the trailing edge")

	require.NoError(t, session.Close())
	session.heightSyncPublishMu.Lock()
	defer session.heightSyncPublishMu.Unlock()
	require.Nil(t, session.heightSyncFlush, "Close must not leave a timer behind")
	require.True(t, session.heightSyncFlushClosed)
}

func TestSnapshotHeightSync_UndatedClaimIsNotFresh(t *testing.T) {
	session, _, _ := setupSessionWithOptions(t, 2, 100000, 100, WithHeightSyncCadence(10, 2))
	tips := transport.NewHeightSyncPeerTips()
	session.SetHeightSyncPeerTips(tips)

	// A host that omits its observation time would otherwise show age 0 and
	// count as the freshest claim in the roster, forever.
	tips.RecordOriginWithBlob(&heightsync.HeightSyncSection{
		ProofType:           heightsync.AnchorProofType,
		MainnetHeight:       500,
		MainnetBlockHashHex: "deadbeef",
		OriginatorSenderID:  session.group[0].ValidatorAddress,
	}, []byte("blob"), []byte{1})

	view := session.SnapshotHeightSync()
	require.Len(t, view.Tips, 1)
	require.Equal(t, uint64(500), view.Tips[0].Height)
	require.False(t, view.Tips[0].AgeKnown)
	require.False(t, view.Tips[0].Fresh,
		"an undated claim must not drive host_height or host_height_lag")
}

func TestPublishHeightSyncView_MonotonicUnderConcurrency(t *testing.T) {
	session, _, _ := setupSessionWithOptions(t, 2, 100000, 100, WithHeightSyncCadence(10, 2))
	session.TestingOnlySetHeightSyncMinPublish(-1) // disable throttle so every mutation tries
	session.SetHeightSyncPeerTips(transport.NewHeightSyncPeerTips())

	const n = 50
	done := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		go func() {
			session.NoteExchangeOverlap(true, false, false)
			done <- struct{}{}
		}()
	}
	for i := 0; i < n; i++ {
		<-done
	}
	session.TestingOnlyFlushHeightSyncView()

	var last uint64
	for i := 0; i < 30; i++ {
		total := session.CachedHeightSyncView().Overlap.Total
		require.GreaterOrEqual(t, total, last, "cached counter must not go backwards")
		last = total
	}
	require.Equal(t, uint64(n), session.CachedHeightSyncView().Overlap.Total)
}

func TestSnapshotHeightSync_DoesNotSealAnchors(t *testing.T) {
	session, _, _ := setupSessionWithOptions(t, 2, 100000, 100, WithHeightSyncCadence(10, 2))
	session.SetHeightSyncPeerTips(transport.NewHeightSyncPeerTips())

	session.mu.Lock()
	session.anchors.Record(10, heightsync.AnchorKindResponse)
	require.Equal(t, 1, session.anchors.OpenLen())
	session.mu.Unlock()

	_ = session.SnapshotHeightSync()
	require.Equal(t, 1, session.anchors.OpenLen(), "Snapshot must be read-only")

	session.mu.Lock()
	session.anchors.ObserveTip(100)
	session.mu.Unlock()
	require.Zero(t, session.anchors.OpenLen(), "producer ObserveTip seals")
}
