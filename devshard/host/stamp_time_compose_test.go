package host

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/chainoracle/blocks/headertime"
	"devshard/heightsync"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

// raiseFloor applies one heartbeat turn and the host's own ack so F is set.
func raiseFloor(t *testing.T, h *Host, user *signing.Secp256k1Signer, hosts []*signing.Secp256k1Signer, height uint64, hash []byte) {
	t.Helper()
	raiseFloorTimed(t, h, user, hosts, height, hash, 0)
}

// raiseFloorTimed is raiseFloor with ObservedTimeMs on each heartbeat (required
// under StampRequiresTime when the signed heartbeat is not host-composed).
func raiseFloorTimed(t *testing.T, h *Host, user *signing.Secp256k1Signer, hosts []*signing.Secp256k1Signer, height uint64, hash []byte, timeMs int64) {
	t.Helper()
	slots := uint64(len(hosts))
	var diffs []types.Diff
	for n := uint64(1); n <= slots; n++ {
		d := stampedHeartbeatDiff(t, user, n, 1, height, slots, hash)
		if timeMs > 0 {
			d.Txs[0].GetHeartbeat().ObservedTimeMs = timeMs
			d = testutil.SignDiff(t, user, "escrow-1", n, d.Txs)
		}
		diffs = append(diffs, d)
	}
	resp, err := h.HandleRequest(context.Background(), HostRequest{Diffs: diffs})
	require.NoError(t, err)
	acks := mempoolHeightAcks(resp.Mempool)
	require.NotEmpty(t, acks, "this host must ack its heartbeat slot")
	ackTxs := make([]*types.DevshardTx, 0, len(acks))
	for _, ack := range acks {
		ackTxs = append(ackTxs, &types.DevshardTx{Tx: &types.DevshardTx_HeightAck{HeightAck: ack}})
	}
	ackDiff := testutil.SignDiff(t, user, "escrow-1", slots+1, ackTxs)
	_, err = h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{ackDiff}})
	require.NoError(t, err)
	floor, got, known := h.sm.HeightSyncFloorAsOf(h.sm.LatestNonce() + 1)
	require.True(t, known)
	require.Equal(t, height, floor)
	require.Equal(t, hash, got)
}

func TestConfirmStart_ComposeFillsTimeFromMap(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	hash := []byte{0xaa}
	const wantTime int64 = 1_700_000_000_000

	src := headertime.New()
	require.NoError(t, src.Seed(100, hash, wantTime))

	or := &fakeOracle{}
	or.setHeight(100)
	or.setHash(hash)
	h := newAckTestHost(t, 1, hosts, user, WithChainOracle(or))
	h.sm.SetHeaderTimes(src)
	raiseFloor(t, h, user, hosts, 100, hash)

	nonce := h.sm.LatestNonce() + 1
	// Pick a nonce whose executor is host 1.
	for nonce%uint64(len(hosts)) != 1 {
		pad := testutil.SignDiff(t, user, "escrow-1", nonce, nil)
		_, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{pad}})
		require.NoError(t, err)
		nonce = h.sm.LatestNonce() + 1
	}

	start := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{
		{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
			InferenceId: nonce, PromptHash: testutil.TestPromptHash[:], Model: "llama",
			InputLength: 100, MaxTokens: testutil.TestMaxTokens,
			ObservedHeight: 100, ObservedBlockHash: hash, ProtocolVersion: h.sm.ProtocolVersion(),
		}}},
	})
	resp, err := h.HandleRequest(context.Background(), HostRequest{
		Diffs: []types.Diff{start}, Nonce: nonce, Payload: defaultPayload(),
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Receipt)

	confirm := h.mempool.QueuedConfirmStart(nonce)
	require.NotNil(t, confirm)
	require.Equal(t, wantTime, confirm.ObservedTimeMs)

	content := &types.ExecutorReceiptContent{
		InferenceId: nonce, PromptHash: testutil.TestPromptHash[:], Model: "llama",
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, EscrowId: "escrow-1",
		ObservedHeight: 100, ObservedBlockHash: hash, ObservedTimeMs: wantTime,
	}
	data, err := proto.Marshal(content)
	require.NoError(t, err)
	addr, err := signing.NewSecp256k1Verifier().RecoverAddress(data, resp.Receipt)
	require.NoError(t, err)
	require.Equal(t, hosts[1].Address(), addr)
}

func TestConfirmStart_ComposeMapMissUsesTipTime(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	hash := []byte{0xaa}
	tipAt := time.UnixMilli(1_800_000_000_000)

	or := &fakeOracle{}
	or.setHeight(100)
	or.setHash(hash)
	or.setTime(tipAt)
	h := newAckTestHost(t, 1, hosts, user, WithChainOracle(or))
	h.sm.SetHeaderTimes(headertime.New())
	raiseFloor(t, h, user, hosts, 100, hash)

	nonce := h.sm.LatestNonce() + 1
	for nonce%uint64(len(hosts)) != 1 {
		pad := testutil.SignDiff(t, user, "escrow-1", nonce, nil)
		_, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{pad}})
		require.NoError(t, err)
		nonce = h.sm.LatestNonce() + 1
	}

	start := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{
		{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
			InferenceId: nonce, PromptHash: testutil.TestPromptHash[:], Model: "llama",
			InputLength: 100, MaxTokens: testutil.TestMaxTokens,
			ObservedHeight: 100, ObservedBlockHash: hash, ProtocolVersion: h.sm.ProtocolVersion(),
		}}},
	})
	resp, err := h.HandleRequest(context.Background(), HostRequest{
		Diffs: []types.Diff{start}, Nonce: nonce, Payload: defaultPayload(),
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Receipt)
	confirm := h.mempool.QueuedConfirmStart(nonce)
	require.NotNil(t, confirm)
	require.Equal(t, tipAt.UnixMilli(), confirm.ObservedTimeMs)
}

func TestConfirmStart_ComposeMapDisagreeRejects(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	hash := []byte{0xaa}

	src := headertime.New()
	require.NoError(t, src.Seed(100, hash, 5_000))

	or := &fakeOracle{}
	or.setHeight(100)
	or.setHash(hash)
	// Agreeing tip time while raising the floor; disagree only on confirm.
	or.setTime(time.UnixMilli(5_000))
	h := newAckTestHost(t, 1, hosts, user, WithChainOracle(or))
	h.sm.SetHeaderTimes(src)
	raiseFloor(t, h, user, hosts, 100, hash)

	or.setTime(time.UnixMilli(9_000))

	nonce := h.sm.LatestNonce() + 1
	for nonce%uint64(len(hosts)) != 1 {
		pad := testutil.SignDiff(t, user, "escrow-1", nonce, nil)
		_, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{pad}})
		require.NoError(t, err)
		nonce = h.sm.LatestNonce() + 1
	}

	start := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{
		{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
			InferenceId: nonce, PromptHash: testutil.TestPromptHash[:], Model: "llama",
			InputLength: 100, MaxTokens: testutil.TestMaxTokens,
			ObservedHeight: 100, ObservedBlockHash: hash, ProtocolVersion: h.sm.ProtocolVersion(),
		}}},
	})
	_, err := h.HandleRequest(context.Background(), HostRequest{
		Diffs: []types.Diff{start}, Nonce: nonce, Payload: defaultPayload(),
	})
	require.ErrorIs(t, err, heightsync.ErrStampTimeMismatch)
	require.Nil(t, h.mempool.QueuedConfirmStart(nonce))
}

func TestHeightAck_ComposeFillsTimeFromMap(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	hash := []byte{0xaa}
	const wantTime int64 = 2_000_000_000_000

	src := headertime.New()
	require.NoError(t, src.Seed(100, hash, wantTime))

	or := &fakeOracle{}
	or.setHeight(100)
	or.setHash(hash)
	// Host 0 owns nonce 3 (3%3==0).
	h := newAckTestHost(t, 0, hosts, user, WithChainOracle(or))
	h.sm.SetHeaderTimes(src)

	slots := uint64(len(hosts))
	resp, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{
		stampedHeartbeatDiff(t, user, 1, 1, 100, slots, hash),
		stampedHeartbeatDiff(t, user, 2, 1, 100, slots, hash),
		stampedHeartbeatDiff(t, user, 3, 1, 100, slots, hash),
	}})
	require.NoError(t, err)

	acks := mempoolHeightAcks(resp.Mempool)
	require.Len(t, acks, 1)
	require.Equal(t, wantTime, acks[0].ObservedTimeMs)
	require.NoError(t, heightsync.VerifyAck(signing.NewSecp256k1Verifier(), acks[0], hosts[0].Address()))
}

func TestConfirmStart_FloorLiftEmptyMapUsesFloorTime(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	hash := []byte{0xaa}
	const wantTime int64 = 1_700_000_000_000

	or := &fakeOracle{}
	or.setHeight(100)
	or.setHash(hash)
	or.setTime(time.UnixMilli(wantTime))
	h := newAckTestHost(t, 1, hosts, user, WithChainOracle(or))
	h.sm.SetHeaderTimes(headertime.New())
	raiseFloor(t, h, user, hosts, 100, hash)

	p, known := h.sm.HeightSyncFloorPointAsOf(h.sm.LatestNonce() + 1)
	require.True(t, known)
	require.Equal(t, wantTime, p.TimeMs)

	// Tip below F, empty map: confirm must lift and carry floor TimeMs.
	or.setHeight(90)
	or.setHash([]byte{0xbb})
	h.sm.SetHeaderTimes(headertime.New())

	nonce := h.sm.LatestNonce() + 1
	for nonce%uint64(len(hosts)) != 1 {
		pad := testutil.SignDiff(t, user, "escrow-1", nonce, nil)
		_, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{pad}})
		require.NoError(t, err)
		nonce = h.sm.LatestNonce() + 1
	}

	start := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{
		{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
			InferenceId: nonce, PromptHash: testutil.TestPromptHash[:], Model: "llama",
			InputLength: 100, MaxTokens: testutil.TestMaxTokens,
			ObservedHeight: 100, ObservedBlockHash: hash, ObservedTimeMs: wantTime,
			ProtocolVersion: h.sm.ProtocolVersion(),
		}}},
	})
	resp, err := h.HandleRequest(context.Background(), HostRequest{
		Diffs: []types.Diff{start}, Nonce: nonce, Payload: defaultPayload(),
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Receipt)
	confirm := h.mempool.QueuedConfirmStart(nonce)
	require.NotNil(t, confirm)
	require.Equal(t, uint64(100), confirm.ObservedHeight)
	require.Equal(t, wantTime, confirm.ObservedTimeMs)
}

func TestHeightAck_FloorLiftEmptyMapUsesFloorTime(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	hash := []byte{0xaa}
	const wantTime int64 = 2_100_000_000_000
	const slots = uint64(3)

	or := &fakeOracle{}
	or.setHeight(90)
	or.setHash([]byte{0xbb})
	h := newAckTestHost(t, 0, hosts, user, WithChainOracle(or))
	h.sm.SetHeaderTimes(headertime.New())

	d1 := stampedHeartbeatDiff(t, user, 1, 1, 100, slots, hash)
	d2 := testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{
		signedAckTx(t, hosts[1], 1, 1, 1, 100, hash, wantTime),
		stampedHeartbeatDiff(t, user, 2, 2, 100, slots, hash).Txs[0],
	})
	d3 := stampedHeartbeatDiff(t, user, 3, 3, 100, slots, hash)
	resp, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{d1, d2, d3}})
	require.NoError(t, err)

	acks := mempoolHeightAcks(resp.Mempool)
	require.Len(t, acks, 1)
	require.Equal(t, uint64(100), acks[0].ObservedHeight)
	require.Equal(t, wantTime, acks[0].ObservedTimeMs, "floor lift with empty map carries floor TimeMs")
}

func TestFinish_FloorLiftEmptyMapUsesFloorTime(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	hash := []byte{0xaa}
	const wantTime int64 = 2_200_000_000_000

	or := &fakeOracle{}
	or.setHeight(100)
	or.setHash(hash)
	or.setTime(time.UnixMilli(wantTime))
	h := newAckTestHostVersion(t, "v7", 1, hosts, user, WithChainOracle(or))
	require.True(t, types.StampRequiresTime(h.sm.ProtocolVersion()))
	h.sm.SetHeaderTimes(headertime.New())
	raiseFloorTimed(t, h, user, hosts, 100, hash, wantTime)

	p, known := h.sm.HeightSyncFloorPointAsOf(h.sm.LatestNonce() + 1)
	require.True(t, known)
	require.Equal(t, wantTime, p.TimeMs)

	// Tip below F, empty map: finish must lift and carry floor TimeMs.
	or.setHeight(90)
	or.setHash([]byte{0xbb})
	h.sm.SetHeaderTimes(headertime.New())

	nonce := h.sm.LatestNonce() + 1
	for nonce%uint64(len(hosts)) != 1 {
		pad := testutil.SignDiff(t, user, "escrow-1", nonce, nil)
		_, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{pad}})
		require.NoError(t, err)
		nonce = h.sm.LatestNonce() + 1
	}

	start := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{
		{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
			InferenceId: nonce, PromptHash: testutil.TestPromptHash[:], Model: "llama",
			InputLength: 100, MaxTokens: testutil.TestMaxTokens,
			ObservedHeight: 100, ObservedBlockHash: hash, ObservedTimeMs: wantTime,
			ProtocolVersion: h.sm.ProtocolVersion(),
		}}},
	})
	resp, err := h.HandleRequest(context.Background(), HostRequest{
		Diffs: []types.Diff{start}, Nonce: nonce, Payload: defaultPayload(),
	})
	require.NoError(t, err)
	require.NotNil(t, resp.ExecutionJob)

	_, err = h.RunExecution(context.Background(), resp.ExecutionJob)
	require.NoError(t, err)

	finishTx := findMempoolFinish(h.MempoolTxs())
	require.NotNil(t, finishTx, "mempool should contain MsgFinishInference")
	fin := finishTx.GetFinishInference()
	require.Equal(t, uint64(100), fin.ObservedHeight)
	require.Equal(t, wantTime, fin.ObservedTimeMs, "floor lift with empty map carries floor TimeMs on finish")
}
