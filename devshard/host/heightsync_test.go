package host

import (
	"context"
	"errors"
	"testing"
	"time"

	"common/chainoracle/blocks"
	"github.com/stretchr/testify/require"

	"devshard/heightsync"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/types"
)

func newAckTestHost(t *testing.T, hostIdx int, hosts []*signing.Secp256k1Signer, user *signing.Secp256k1Signer, opts ...HostOption) *Host {
	t.Helper()
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	sm, err := state.NewStateMachine("escrow-1", config, group, 100000, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 100000))
	require.NoError(t, err)
	all := append([]HostOption{WithGrace(100)}, opts...)
	h, err := NewHost(sm, hosts[hostIdx], stub.NewInferenceEngine(), "escrow-1", group, nil, all...)
	require.NoError(t, err)
	return h
}

func heartbeatDiff(t *testing.T, user signing.Signer, nonce, turnSeq, height, slots uint64) types.Diff {
	t.Helper()
	return testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{
		{Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
			ObservedHeight: height,
			SlotsNum:       slots,
			Reason:         string(heightsync.ReasonQuietSession),
		}}},
	})
}

func stampedHeartbeatDiff(t *testing.T, user signing.Signer, nonce, turnSeq, height, slots uint64, hash []byte) types.Diff {
	t.Helper()
	d := heartbeatDiff(t, user, nonce, turnSeq, height, slots)
	d.Txs[0].GetHeartbeat().ObservedBlockHash = hash
	return testutil.SignDiff(t, user, "escrow-1", nonce, d.Txs)
}

func signedAckTx(t *testing.T, signer *signing.Secp256k1Signer, turn, ref uint64, slot uint32, height uint64, hash []byte) *types.DevshardTx {
	t.Helper()
	ack := &types.MsgHeightAck{
		RefNonce:          ref,
		SlotId:            slot,
		ObservedHeight:    height,
		ObservedBlockHash: append([]byte(nil), hash...),
		SyncState:         types.SyncState_SYNCED,
		PeerSeen:          []byte{0xff},
	}
	require.NoError(t, heightsync.SignAck(signer, ack))
	return &types.DevshardTx{Tx: &types.DevshardTx_HeightAck{HeightAck: ack}}
}

func mempoolHeightAcks(txs []*types.DevshardTx) []*types.MsgHeightAck {
	var out []*types.MsgHeightAck
	for _, tx := range txs {
		if ack := tx.GetHeightAck(); ack != nil {
			out = append(out, ack)
		}
	}
	return out
}

func TestHost_HeartbeatAck_OwnSlotIntoMempool(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	or := &fakeOracle{}
	or.setHeight(100)
	or.setHash([]byte{0xaa})
	h := newAckTestHost(t, 0, hosts, user, WithChainOracle(or))

	const slots = uint64(3)
	d1 := heartbeatDiff(t, user, 1, 1, 100, slots)
	d2 := heartbeatDiff(t, user, 2, 1, 100, slots)
	d3 := heartbeatDiff(t, user, 3, 1, 100, slots)
	resp, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{d1, d2, d3}})
	require.NoError(t, err)

	acks := mempoolHeightAcks(resp.Mempool)
	require.Len(t, acks, 1)
	ack := acks[0]
	require.Equal(t, uint64(3), ack.RefNonce)
	require.Equal(t, uint32(0), ack.SlotId)
	require.Equal(t, uint64(100), ack.ObservedHeight)
	require.Equal(t, []byte{0xaa}, ack.ObservedBlockHash)
	require.Equal(t, types.SyncState_SYNCED, ack.SyncState)
	require.NotEmpty(t, ack.PeerSeen)
	require.Equal(t, byte(1<<0), ack.PeerSeen[0], "peer_seen is this host's own slot, not sequencer heartbeats")
	require.True(t, h.PeerSeenHas(0))
	require.False(t, h.PeerSeenHas(1), "sequencer heartbeat is not a claim from slot 1")
	require.False(t, h.PeerSeenHas(2))
	require.Equal(t, int64(1), or.latestCalls.Load(), "one oracle read per exchange")

	err = heightsync.VerifyAck(signing.NewSecp256k1Verifier(), ack, hosts[0].Address())
	require.NoError(t, err)
}

func TestHost_PeerSeenMarksAcksNotHeartbeats(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	or := &fakeOracle{}
	or.setHeight(100)
	or.setHash([]byte{0xaa})
	h := newAckTestHost(t, 0, hosts, user, WithChainOracle(or))

	const slots = uint64(3)
	d1 := heartbeatDiff(t, user, 1, 1, 100, slots)
	d2 := heartbeatDiff(t, user, 2, 1, 100, slots)
	d3 := heartbeatDiff(t, user, 3, 1, 100, slots)
	_, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{d1, d2, d3}})
	require.NoError(t, err)
	require.False(t, h.PeerSeenHas(1))
	require.False(t, h.PeerSeenHas(2))

	ackDiff := testutil.SignDiff(t, user, "escrow-1", 4, []*types.DevshardTx{
		signedAckTx(t, hosts[1], 1, 2, 1, 100, []byte{0xaa}),
	})
	_, err = h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{ackDiff}})
	require.NoError(t, err)
	require.True(t, h.PeerSeenHas(1), "peer_seen from a host-signed ack")
	require.Equal(t, uint64(100), h.PeerSeenHeight(1))
	require.False(t, h.PeerSeenHas(2))
}

func TestHost_HeartbeatAck_WrongSlotSilent(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	or := &fakeOracle{}
	or.setHeight(100)
	h := newAckTestHost(t, 0, hosts, user, WithChainOracle(or))

	d1 := heartbeatDiff(t, user, 1, 1, 100, 3)
	resp, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{d1}})
	require.NoError(t, err)
	require.Empty(t, mempoolHeightAcks(resp.Mempool))
	require.Equal(t, int64(0), or.latestCalls.Load())
}

func TestHost_HeartbeatAck_NoHeartbeatNoAck(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	or := &fakeOracle{}
	or.setHeight(100)
	h := newAckTestHost(t, 0, hosts, user, WithChainOracle(or))

	diff := testutil.SignDiff(t, user, "escrow-1", 1, nil)
	resp, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff}})
	require.NoError(t, err)
	require.Empty(t, mempoolHeightAcks(resp.Mempool))
}

func TestHost_HeartbeatAck_OracleUnavailableStillRequired(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	h := newAckTestHost(t, 0, hosts, user)

	const slots = uint64(3)
	d1 := heartbeatDiff(t, user, 1, 1, 100, slots)
	d2 := heartbeatDiff(t, user, 2, 1, 100, slots)
	d3 := heartbeatDiff(t, user, 3, 1, 100, slots)
	resp, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{d1, d2, d3}})
	require.NoError(t, err)

	acks := mempoolHeightAcks(resp.Mempool)
	require.Len(t, acks, 1)
	require.Equal(t, types.SyncState_ORACLE_UNAVAILABLE, acks[0].SyncState)
	require.Equal(t, uint64(0), acks[0].ObservedHeight)
	require.Empty(t, acks[0].ObservedBlockHash)
	require.NoError(t, heightsync.VerifyAck(signing.NewSecp256k1Verifier(), acks[0], hosts[0].Address()))
}

func TestHost_HeartbeatAck_OracleErrorStillRequired(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	or := &fakeOracle{}
	or.setErr(errors.New("upstream unreachable"))
	h := newAckTestHost(t, 0, hosts, user, WithChainOracle(or))

	const slots = uint64(3)
	d1 := heartbeatDiff(t, user, 1, 1, 100, slots)
	d2 := heartbeatDiff(t, user, 2, 1, 100, slots)
	d3 := heartbeatDiff(t, user, 3, 1, 100, slots)
	resp, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{d1, d2, d3}})
	require.NoError(t, err)
	acks := mempoolHeightAcks(resp.Mempool)
	require.Len(t, acks, 1)
	require.Equal(t, types.SyncState_ORACLE_UNAVAILABLE, acks[0].SyncState)
	require.Equal(t, int64(1), or.latestCalls.Load())
}

func TestHeartbeat_StampedBusySessionEmitsNoAcks(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	or := &fakeOracle{}
	or.setHeight(100)
	or.setHash([]byte{0xaa})
	h := newAckTestHost(t, 1, hosts, user, WithChainOracle(or))

	start := testutil.StartTx(1)
	start.GetStartInference().ObservedHeight = 100
	start.GetStartInference().ObservedBlockHash = []byte{0xaa}
	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{start})
	resp, err := h.HandleRequest(context.Background(), HostRequest{
		Diffs: []types.Diff{diff}, Nonce: 1, Payload: defaultPayload(),
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	for _, tx := range resp.Mempool {
		require.Nil(t, tx.GetHeartbeat(), "busy stamped session emits no heartbeat")
		require.Nil(t, tx.GetHeightAck(), "acks exist only inside heartbeat turns")
	}
	require.Zero(t, countHeartbeatsInMempool(h.MempoolTxs()))
}

func countHeartbeatsInMempool(txs []*types.DevshardTx) int {
	n := 0
	for _, tx := range txs {
		if tx.GetHeartbeat() != nil || tx.GetHeightAck() != nil {
			n++
		}
	}
	return n
}

func TestHost_HeartbeatAck_CatchingUp(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	or := &fakeOracle{}
	or.setHeight(90)
	or.setHash([]byte{0xbb})
	h := newAckTestHost(t, 0, hosts, user, WithChainOracle(or))

	const slots = uint64(3)
	d1 := heartbeatDiff(t, user, 1, 1, 100, slots)
	d2 := heartbeatDiff(t, user, 2, 1, 100, slots)
	d3 := heartbeatDiff(t, user, 3, 1, 100, slots)
	resp, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{d1, d2, d3}})
	require.NoError(t, err)
	acks := mempoolHeightAcks(resp.Mempool)
	require.Len(t, acks, 1)
	require.Equal(t, types.SyncState_CATCHING_UP, acks[0].SyncState)
	require.Equal(t, uint64(90), acks[0].ObservedHeight)
}

// TestHost_HeartbeatAck_LagsButClearsHostFloor is the producer-side lift: a host
// whose own tip sits below a floor *host acks already established* stamps F(m),
// not its raw tip. A soliciting MsgHeartbeat does not itself raise F (spec §14
// rule 3), so the bar comes from a prior host-signed stamp, not from the
// heartbeat the ack answers.
func TestHost_HeartbeatAck_LagsButClearsHostFloor(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	or := &fakeOracle{}
	or.setHeight(90)
	or.setHash([]byte{0xbb})
	h := newAckTestHost(t, 0, hosts, user, WithChainOracle(or)) // executor(3) = 0

	const slots = uint64(3)
	top := []byte{0xaa}
	d1 := stampedHeartbeatDiff(t, user, 1, 1, 100, slots, top)
	d2 := testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{
		signedAckTx(t, hosts[1], 1, 1, 1, 100, top),
		stampedHeartbeatDiff(t, user, 2, 2, 100, slots, top).Txs[0],
	})
	d3 := stampedHeartbeatDiff(t, user, 3, 3, 100, slots, top)
	resp, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{d1, d2, d3}})
	require.NoError(t, err)

	acks := mempoolHeightAcks(resp.Mempool)
	require.Len(t, acks, 1)
	require.Equal(t, uint64(3), acks[0].RefNonce)
	require.Equal(t, uint64(100), acks[0].ObservedHeight, "lifted to the host-established floor, not own tip 90")
	require.Equal(t, top, acks[0].ObservedBlockHash, "the carried pair must be the floor's, not the host's")
	require.Equal(t, types.SyncState_CATCHING_UP, acks[0].SyncState,
		"the lag is not hidden: it moves to the label the gateway monitors")
}

// TestHost_HeartbeatAck_CarriesAFloorFarAboveOwnTip pins what replaced the
// producer rule's second escape.
//
// The escape said: if F is more than W_conf above my own tip, omit rather than
// carry, because no plausible chain advance explains the distance. Read from the
// other side it said something much worse — the first party to stamp a height
// nobody else can reach silences every other host's stamp for the rest of the
// session, and nothing lowers a floor. Distance is not evidence, and the carrier
// is not where a bad height is judged: the floor is visibly in the log with its
// author attached (L6), and the height only got there through an envelope that
// had to clear |Δ| > D at admission (§8/§15).
//
// So a lagging host carries, at any distance, and reports its real position in
// sync_state instead.
func TestHost_HeartbeatAck_CarriesAFloorFarAboveOwnTip(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	or := &fakeOracle{}
	or.setHeight(1)
	or.setHash([]byte{0xbb})
	h := newAckTestHost(t, 0, hosts, user, WithChainOracle(or)) // executor(3) = 0

	// A peer host-ack sets F = 10_000 in one step against a host still at 1.
	const slots = uint64(3)
	const far = uint64(10_000)
	top := []byte{0xaa}
	d1 := heartbeatDiff(t, user, 1, 1, 0, slots)
	d2 := testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{
		signedAckTx(t, hosts[1], 1, 1, 1, far, top),
		heartbeatDiff(t, user, 2, 2, 0, slots).Txs[0],
	})
	// The soliciting heartbeat carries F, as §10.3.1 requires of a user stamp, so
	// the host is asked to ack a height 10_000 blocks above anything it has seen.
	d3 := stampedHeartbeatDiff(t, user, 3, 3, far, slots, top)
	resp, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{d1, d2, d3}})
	require.NoError(t, err)

	acks := mempoolHeightAcks(resp.Mempool)
	require.Len(t, acks, 1)
	require.Equal(t, uint64(3), acks[0].RefNonce)
	require.Equal(t, far, acks[0].ObservedHeight, "the floor is carried however far above own tip 1 it is")
	require.Equal(t, top, acks[0].ObservedBlockHash, "the carried pair is the floor's, not the host's")
	require.Equal(t, types.SyncState_CATCHING_UP, acks[0].SyncState,
		"the host's real position still reaches the gateway through the label")
}

func TestHost_HeartbeatAck_OracleStale(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	or := &fakeOracle{}
	or.setHeight(100)
	or.setStale(true)
	h := newAckTestHost(t, 0, hosts, user, WithChainOracle(or))

	const slots = uint64(3)
	d1 := heartbeatDiff(t, user, 1, 1, 100, slots)
	d2 := heartbeatDiff(t, user, 2, 1, 100, slots)
	d3 := heartbeatDiff(t, user, 3, 1, 100, slots)
	resp, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{d1, d2, d3}})
	require.NoError(t, err)
	acks := mempoolHeightAcks(resp.Mempool)
	require.Len(t, acks, 1)
	require.Equal(t, types.SyncState_ORACLE_STALE, acks[0].SyncState)
	require.Equal(t, uint64(100), acks[0].ObservedHeight)
}

func TestHost_HeartbeatAck_AlreadyAppliedDoesNotRereadOracle(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	or := &fakeOracle{}
	or.setHeight(100)
	h := newAckTestHost(t, 0, hosts, user, WithChainOracle(or))

	const slots = uint64(3)
	d1 := heartbeatDiff(t, user, 1, 1, 100, slots)
	d2 := heartbeatDiff(t, user, 2, 1, 100, slots)
	d3 := heartbeatDiff(t, user, 3, 1, 100, slots)
	_, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{d1, d2, d3}})
	require.NoError(t, err)
	require.Equal(t, int64(1), or.latestCalls.Load())

	resp, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{d1, d2, d3}})
	require.NoError(t, err)
	require.Equal(t, int64(1), or.latestCalls.Load(), "stale catch-up must not re-ack")
	require.Len(t, mempoolHeightAcks(resp.Mempool), 1)
}

func TestHost_BlockedOracleDoesNotHoldMutex(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	or := &blockingOracle{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		hdr:     &blocks.Header{Height: 100, ChainID: "fake-chain", BlockHash: []byte{0xaa}},
	}
	h := newAckTestHost(t, 0, hosts, user, WithChainOracle(or))

	const slots = uint64(3)
	d1 := heartbeatDiff(t, user, 1, 1, 100, slots)
	d2 := heartbeatDiff(t, user, 2, 1, 100, slots)
	d3 := heartbeatDiff(t, user, 3, 1, 100, slots)
	done := make(chan error, 1)
	go func() {
		_, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{d1, d2, d3}})
		done <- err
	}()
	select {
	case <-or.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Latest was not entered")
	}
	unblocked := make(chan struct{})
	go func() {
		_ = h.LatestNonce()
		close(unblocked)
	}()
	select {
	case <-unblocked:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("host mutex held during oracle I/O")
	}
	close(or.release)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("HandleRequest did not return")
	}
}
