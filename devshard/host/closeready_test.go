package host

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/gossip"
	"devshard/heightsync"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

func TestCloseReady_ArmsAfterIdle(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	or := &fakeOracle{}
	or.setHeight(100)
	or.setHash([]byte{0xaa})
	h := newAckTestHost(t, 0, hosts, user, WithChainOracle(or))
	now := time.Unix(1_700_000_000, 0)
	h.SetCloseReadyClock(func() time.Time { return now })
	peer := &recordingPeer{}
	g := gossip.NewGossip("escrow-1", 0, []gossip.PeerClient{peer}, h.HostMempool())
	WithGossip(g)(h)

	ctx := context.Background()
	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	_, err := h.HandleRequest(ctx, HostRequest{Diffs: []types.Diff{diff}})
	require.NoError(t, err)

	afterContact := cloneMempool(h.MempoolTxs())
	armed, _ := h.CloseReadyView().Armed()
	require.False(t, armed, "still inside T_idle")

	now = now.Add(heightsync.DefaultHeartbeatIdleTimeout + time.Second)
	or.setHeight(104)
	h.EvaluateCloseReady(ctx)
	armed, at := h.CloseReadyView().Armed()
	require.True(t, armed, "H21: silence past T_idle arms")
	require.Equal(t, uint64(104), at, "evidence cites the newest height this host knows")
	require.Equal(t, afterContact, cloneMempool(h.MempoolTxs()), "arming emits nothing into the mempool")
	require.Zero(t, peer.txsCount.Load(), "arming emits nothing on gossip")
}

func TestCloseReady_SilenceArmsWithoutOracleProgress(t *testing.T) {
	// The host learns height only from traffic, so a partitioned host sees a
	// frozen oracle. Silence alone must still arm it.
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	or := &fakeOracle{}
	or.setHeight(100)
	or.setHash([]byte{0xaa})
	h := newAckTestHost(t, 0, hosts, user, WithChainOracle(or))
	now := time.Unix(1_700_000_000, 0)
	h.SetCloseReadyClock(func() time.Time { return now })

	ctx := context.Background()
	_, err := h.HandleRequest(ctx, HostRequest{Diffs: []types.Diff{
		testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)}),
	}})
	require.NoError(t, err)

	now = now.Add(heightsync.DefaultHeartbeatIdleTimeout + time.Second)
	armed, _ := h.CloseReadyView().Armed()
	require.True(t, armed, "no tick and no new block: elapsed silence is enough")
	require.Equal(t, heightsync.DefaultHeartbeatIdleTimeout+time.Second, h.CloseReadySilentFor())
}

func TestCloseReady_DisarmsOnContact(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	or := &fakeOracle{}
	or.setHeight(100)
	or.setHash([]byte{0xaa})
	h := newAckTestHost(t, 0, hosts, user, WithChainOracle(or))
	now := time.Unix(1_700_000_000, 0)
	h.SetCloseReadyClock(func() time.Time { return now })
	ctx := context.Background()

	_, err := h.HandleRequest(ctx, HostRequest{Diffs: []types.Diff{
		testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)}),
	}})
	require.NoError(t, err)
	now = now.Add(heightsync.DefaultHeartbeatIdleTimeout + time.Second)
	or.setHeight(104)
	h.EvaluateCloseReady(ctx)
	armed, _ := h.CloseReadyView().Armed()
	require.True(t, armed)

	now = now.Add(time.Second)
	or.setHeight(105)
	_, err = h.HandleRequest(ctx, HostRequest{Diffs: []types.Diff{
		testutil.SignDiff(t, user, "escrow-1", 2, nil),
	}})
	require.NoError(t, err)
	armed, _ = h.CloseReadyView().Armed()
	require.False(t, armed, "H22: contact disarms")

	ivs := h.CloseReadyIntervals()
	require.Len(t, ivs, 1)
	require.Equal(t, uint64(104), ivs[0].ArmedAt)
	require.Equal(t, heightsync.DefaultHeartbeatIdleTimeout+2*time.Second, ivs[0].SilentFor)
}

func TestCloseReady_MinorityCannotClose(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	ctx := context.Background()

	now := time.Unix(1_700_000_000, 0)
	clock := func() time.Time { return now }

	orA := &fakeOracle{}
	orA.setHeight(100)
	orA.setHash([]byte{0xaa})
	a := newAckTestHost(t, 0, hosts, user, WithChainOracle(orA))
	a.SetCloseReadyClock(clock)
	peerA := &recordingPeer{}
	WithGossip(gossip.NewGossip("escrow-1", 0, []gossip.PeerClient{peerA}, a.HostMempool()))(a)

	orB := &fakeOracle{}
	orB.setHeight(100)
	orB.setHash([]byte{0xaa})
	b := newAckTestHost(t, 1, hosts, user, WithChainOracle(orB))
	b.SetCloseReadyClock(clock)
	peerB := &recordingPeer{}
	WithGossip(gossip.NewGossip("escrow-1", 1, []gossip.PeerClient{peerB}, b.HostMempool()))(b)

	d1 := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	_, err := a.HandleRequest(ctx, HostRequest{Diffs: []types.Diff{d1}})
	require.NoError(t, err)
	_, err = b.HandleRequest(ctx, HostRequest{Diffs: []types.Diff{d1}})
	require.NoError(t, err)

	// A is partitioned: no further user diffs. B keeps being served.
	now = now.Add(heightsync.DefaultHeartbeatIdleTimeout + time.Second)
	orA.setHeight(104)
	a.EvaluateCloseReady(ctx)
	armedA, _ := a.CloseReadyView().Armed()
	require.True(t, armedA, "partitioned host arms")

	orB.setHeight(104)
	d2 := testutil.SignDiff(t, user, "escrow-1", 2, nil)
	_, err = b.HandleRequest(ctx, HostRequest{Diffs: []types.Diff{d2}})
	require.NoError(t, err)
	armedB, _ := b.CloseReadyView().Armed()
	require.False(t, armedB, "served host stays unarmed")

	require.False(t, mempoolHasCloseTx(a.MempoolTxs()), "H23: armed minority emits no close tx")
	require.False(t, mempoolHasCloseTx(b.MempoolTxs()), "H23: unarmed host emits no close tx")
	require.Zero(t, peerA.txsCount.Load())
	require.Zero(t, peerB.txsCount.Load())
}

func cloneMempool(txs []*types.DevshardTx) []string {
	out := make([]string, 0, len(txs))
	for _, tx := range txs {
		if tx == nil || tx.GetTx() == nil {
			continue
		}
		out = append(out, fmt.Sprintf("%T", tx.GetTx()))
	}
	return out
}

func mempoolHasCloseTx(txs []*types.DevshardTx) bool {
	for _, tx := range txs {
		if tx == nil {
			continue
		}
		if tx.GetFinalizeRound() != nil || tx.GetTimeoutInference() != nil {
			return true
		}
	}
	return false
}
