package protocol

import (
	"bytes"
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"common/chainoracle/blocks"
	"devshard/chainoracle/blocks/headertime"
	"devshard/heightsync"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/types"
	"devshard/user"
)

type rigOracle struct {
	height *atomic.Int64
	rig    *protocolRig
}

func (o rigOracle) header(height int64) *blocks.Header {
	if height <= 0 && o.height != nil {
		height = o.height.Load()
	}
	at := time.Unix(1, 0).UTC()
	if o.rig != nil && height > 0 {
		if ms, ok := o.rig.storedTime(uint64(height)); ok {
			at = time.UnixMilli(ms).UTC()
		}
	}
	return blocks.HashOnlyHeader(height, at, "fake-chain", []byte{0xaa})
}

func (o rigOracle) Latest(context.Context) (*blocks.Header, error) {
	return o.header(0), nil
}
func (o rigOracle) At(_ context.Context, height int64) (*blocks.Header, error) {
	return o.header(height), nil
}
func (o rigOracle) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, blocks.ErrProveNotImplemented
}
func (o rigOracle) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	ch := make(chan *blocks.Header)
	close(ch)
	return ch, nil
}

type protocolRig struct {
	height  atomic.Int64
	clock   atomic.Int64
	oracles []blocks.BlockOracle
	mu      sync.Mutex
	diffs   []types.Diff
	times   map[uint64]int64
}

// protocolHeaders is the stored map the HTTP stack's timeout votes read.
// A height is recorded the first time the rig's clock sits on it.
type protocolHeaders struct{ rig *protocolRig }

func (p protocolHeaders) StoredAt(height uint64) (headertime.Record, bool) {
	if p.rig == nil || height == 0 {
		return headertime.Record{}, false
	}
	ms, ok := p.rig.storedTime(height)
	if !ok {
		return headertime.Record{}, false
	}
	return headertime.Record{Hash: []byte{0xaa}, TimeMs: ms}, true
}

func (p protocolHeaders) HeaderTimeMs(height uint64, hash []byte) (int64, error) {
	rec, ok := p.StoredAt(height)
	if !ok {
		return 0, types.ErrHeaderPending
	}
	if len(hash) > 0 && len(rec.Hash) > 0 && !bytes.Equal(hash, rec.Hash) {
		return 0, types.ErrHeaderMismatch
	}
	return rec.TimeMs, nil
}

func (r *protocolRig) remember(height uint64) {
	if r == nil || height == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.times == nil {
		r.times = map[uint64]int64{}
	}
	if _, ok := r.times[height]; ok {
		return
	}
	ms := time.Unix(0, r.clock.Load()).UnixMilli()
	if ms <= 0 {
		ms = time.Now().UnixMilli()
	}
	r.times[height] = ms
}

func (r *protocolRig) storedTime(height uint64) (int64, bool) {
	if r == nil || height == 0 || height > uint64(r.height.Load()) {
		return 0, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ms, ok := r.times[height]
	return ms, ok && ms > 0
}

func (r *protocolRig) observe(session *user.Session) {
	session.SetDiffObserver(func(d types.Diff) {
		r.mu.Lock()
		r.diffs = append(r.diffs, d)
		r.mu.Unlock()
	})
}

func (r *protocolRig) journal() []types.Diff {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]types.Diff(nil), r.diffs...)
}

func (r *protocolRig) peerBefore(t *testing.T, env *httpTestEnv, nonce uint64) *state.StateMachine {
	t.Helper()
	peer := newPeerSM(t, env, 1000000)
	for _, d := range r.journal() {
		if d.Nonce >= nonce {
			break
		}
		_, err := peer.ApplyDiff(d)
		require.NoError(t, err)
	}
	return peer
}

func (r *protocolRig) diffAt(t *testing.T, nonce uint64) types.Diff {
	t.Helper()
	for _, d := range r.journal() {
		if d.Nonce == nonce {
			return d
		}
	}
	require.FailNow(t, "no journaled diff", "nonce %d", nonce)
	return types.Diff{}
}

func stampedReceiptResponse(receipt []byte, confirm *types.DevshardTx) *host.HostResponse {
	cs := confirm.GetConfirmStart()
	return &host.HostResponse{
		Receipt:           receipt,
		ConfirmedAt:       time.Now().Unix(),
		ObservedHeight:    cs.GetObservedHeight(),
		ObservedBlockHash: cs.GetObservedBlockHash(),
		ObservedTimeMs:    cs.GetObservedTimeMs(),
	}
}

func newProtocolRig(n int) *protocolRig {
	r := &protocolRig{}
	r.height.Store(100)
	// The refusal window is 60s and the verifier reads the start block's wall
	// time. A stamp from this instant is already past that window.
	r.clock.Store(time.Now().Add(-2 * time.Minute).UnixNano())
	r.remember(100)
	for range n {
		r.oracles = append(r.oracles, rigOracle{height: &r.height, rig: r})
	}
	return r
}

func (r *protocolRig) sessionOptions(numHosts int) []user.SessionOption {
	return []user.SessionOption{
		user.WithHeightSyncCadence(10, uint64(numHosts)),
		user.WithHeartbeatClock(func() time.Time { return time.Unix(0, r.clock.Load()) }),
		user.WithObservedHeight(func() (uint64, []byte, bool) {
			return uint64(r.height.Load()), []byte{0xaa}, true
		}),
	}
}

func (r *protocolRig) seed(t *testing.T, session *user.Session) {
	t.Helper()
	ctx := context.Background()
	_, err := session.SendInference(ctx, user.InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})
	require.NoError(t, err)
	require.NoError(t, session.SendPendingDiff(ctx))
	f, _, _ := session.StateMachine().HeightSyncFloorAsOf(session.Nonce() + 1)
	require.NotZero(t, f, "the bootstrap inference's finish seeds the floor")
}

func (r *protocolRig) raise(t *testing.T, session *user.Session, h uint64) {
	t.Helper()
	r.height.Store(int64(h))
	ctx := context.Background()
	for range 8 {
		r.clock.Add(int64(time.Hour))
		r.remember(h)
		_ = session.MaybeHeartbeat(ctx)
		_ = session.SendPendingDiff(ctx)
		if f, _, _ := session.StateMachine().HeightSyncFloorAsOf(session.Nonce() + 1); f >= h {
			return
		}
	}
	f, _, _ := session.StateMachine().HeightSyncFloorAsOf(session.Nonce() + 1)
	require.GreaterOrEqual(t, f, h, "host acks must carry the floor to the raised height")
}

func TestProtocolRigRaisesTheFloor(t *testing.T) {
	env, rig := setupArmedHTTPEnv(t, 3, 1_000_000, 100)
	rig.raise(t, env.session, 500)
	rig.raise(t, env.session, 5000)
}

func signedReceiptResponse(t *testing.T, env *httpTestEnv, executorIdx int, nonce uint64) *host.HostResponse {
	t.Helper()
	rec, ok := env.session.StateMachine().GetInference(nonce)
	require.True(t, ok)
	stamp := testutil.ReceiptStamp{}
	if rec.StartedAtHeight > 0 {
		stamp = testutil.ReceiptStamp{Height: rec.StartedAtHeight, Hash: []byte{0xaa}}
	}
	receipt := testutil.SignExecutorReceipt(t, env.signers[executorIdx], "escrow-1", nonce,
		rec.PromptHash, rec.Model, rec.InputLength, rec.MaxTokens, stamp)
	return &host.HostResponse{Receipt: receipt, ConfirmedAt: time.Now().Unix(), ObservedHeight: stamp.Height, ObservedBlockHash: stamp.Hash}
}

func floorDiffs(t *testing.T, user, slot0 *signing.Secp256k1Signer, ref, slots, height uint64) []types.Diff {
	t.Helper()
	hb := &types.DevshardTx{Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{SlotsNum: slots, Reason: string(heightsync.ReasonQuietSession)}}}
	ack := &types.MsgHeightAck{RefNonce: ref, SlotId: 0, ObservedHeight: height, ObservedBlockHash: []byte{0xaa},
		ObservedTimeMs: int64(height) * testutil.HeightClockBlockMs,
		SyncState:      types.SyncState_SYNCED, PeerSeen: []byte{0xff}}
	require.NoError(t, heightsync.SignAck(slot0, ack))
	return []types.Diff{
		testutil.SignDiff(t, user, "escrow-1", ref, []*types.DevshardTx{hb}),
		testutil.SignDiff(t, user, "escrow-1", ref+1, []*types.DevshardTx{{Tx: &types.DevshardTx_HeightAck{HeightAck: ack}}}),
	}
}
