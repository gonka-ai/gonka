package host

import (
	"context"
	"errors"
	"testing"

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
			TurnSeq:        turnSeq,
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
	require.Equal(t, uint64(1), ack.TurnSeq)
	require.Equal(t, uint64(3), ack.RefNonce)
	require.Equal(t, uint32(0), ack.SlotId)
	require.Equal(t, uint64(100), ack.ObservedHeight)
	require.Equal(t, []byte{0xaa}, ack.ObservedBlockHash)
	require.Equal(t, types.SyncState_SYNCED, ack.SyncState)
	require.NotEmpty(t, ack.PeerSeen)
	require.Equal(t, byte(1<<0|1<<1|1<<2), ack.PeerSeen[0], "peer_seen from Diff heartbeats")
	require.Equal(t, int64(1), or.latestCalls.Load(), "one oracle read per exchange")

	err = heightsync.VerifyAck(signing.NewSecp256k1Verifier(), ack, hosts[0].Address())
	require.NoError(t, err)
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
		require.Nil(t, tx.GetHeartbeat(), "H33: busy stamped session emits no heartbeat")
		require.Nil(t, tx.GetHeightAck(), "H33: acks exist only inside heartbeat turns")
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

// TestHost_HeartbeatAck_LagsButClearsSolicitingFloor pins the producing-nonce
// basis for acks. The host is really behind at 90 and the only stamped height in
// the log is the 100 on the heartbeat it is answering, so the bar the verifier
// applies — F(ref_nonce+1), which folds in that heartbeat — is above the host's
// raw tip. The honest ack is therefore the lift to 100, with the lag moved into
// sync_state.
//
// The slot is picked so ref_nonce is the first nonce: reading F(ref_nonce)
// instead drops the soliciting heartbeat, because AsOf is exclusive, and leaves
// nothing behind it. That is how an honest host ends up authoring an L0-invalid
// ack, and no later heartbeat in the span can paper over it.
func TestHost_HeartbeatAck_LagsButClearsSolicitingFloor(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	or := &fakeOracle{}
	or.setHeight(90)
	or.setHash([]byte{0xbb})
	h := newAckTestHost(t, 1, hosts, user, WithChainOracle(or)) // executor(1) = 1

	const slots = uint64(3)
	top := []byte{0xaa}
	d1 := stampedHeartbeatDiff(t, user, 1, 1, 100, slots, top)
	d2 := stampedHeartbeatDiff(t, user, 2, 1, 100, slots, top)
	d3 := stampedHeartbeatDiff(t, user, 3, 1, 100, slots, top)
	resp, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{d1, d2, d3}})
	require.NoError(t, err)

	acks := mempoolHeightAcks(resp.Mempool)
	require.Len(t, acks, 1)
	require.Equal(t, uint64(1), acks[0].RefNonce, "F(1) is empty; only F(2) holds the heartbeat's 100")
	require.Equal(t, uint64(100), acks[0].ObservedHeight, "lifted to F(ref_nonce+1), not F(ref_nonce)")
	require.Equal(t, top, acks[0].ObservedBlockHash, "the carried pair must be the floor's, not the host's")
	require.Equal(t, types.SyncState_CATCHING_UP, acks[0].SyncState,
		"the lag is not hidden: it moves to the label the gateway monitors")
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
