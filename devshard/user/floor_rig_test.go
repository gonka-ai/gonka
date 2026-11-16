package user

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
	"devshard/host"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/types"
)

type floorRig struct {
	height  atomic.Uint64
	oracles []*sessionOracle
	clock   atomic.Int64
}

func (r *floorRig) set(h uint64) {
	r.height.Store(h)
	for _, o := range r.oracles {
		o.height.Store(int64(h))
	}
}

func (r *floorRig) tick() {
	r.clock.Add(int64(time.Hour))
}

func (r *floorRig) step(session *Session, h uint64) bool {
	ctx := context.Background()
	r.tick()
	_ = session.MaybeHeartbeat(ctx)
	_ = session.SendPendingDiff(ctx)
	f, _, _ := session.StateMachine().HeightSyncFloorAsOf(session.Nonce() + 1)
	return f >= h
}

func (r *floorRig) raiseOnceStarted(session *Session, nonce, h uint64) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if rec, ok := session.StateMachine().GetInference(nonce); ok && rec.Status != types.StatusPending {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		r.set(h)
		for time.Now().Before(deadline) && !r.step(session, h) {
		}
	}()
	return done
}

func setupArmedSession(t *testing.T, numHosts int, balance uint64, grace uint64, opts ...SessionOption) (*Session, []*signing.Secp256k1Signer, *signing.Secp256k1Signer, *floorRig) {
	t.Helper()
	hosts := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	group := testutil.MakeGroup(hosts)
	session, user, rig := armedSessionFor(t, group, hosts, testutil.DefaultConfig(numHosts), balance, grace, opts...)
	return session, hosts, user, rig
}

func armedSessionFor(t *testing.T, group []types.SlotAssignment, signers []*signing.Secp256k1Signer, config types.SessionConfig, balance, grace uint64, opts ...SessionOption) (*Session, *signing.Secp256k1Signer, *floorRig) {
	t.Helper()
	rig := &floorRig{}
	rig.height.Store(100)
	rig.clock.Store(time.Now().UnixNano())
	oracles, own := sessionOracles(len(group), 100)
	rig.oracles = own
	user := testutil.MustGenerateKey(t)
	verifier := signing.NewSecp256k1Verifier()
	clients := make([]HostClient, len(group))
	for i, slot := range group {
		var slotSigner *signing.Secp256k1Signer
		for _, s := range signers {
			if s.Address() == slot.ValidatorAddress {
				slotSigner = s
				break
			}
		}
		sm := statetest.MustStateMachine(t, "escrow-1", config, group, balance, user.Address(), verifier)
		h, err := host.NewHost(sm, slotSigner, stub.NewInferenceEngine(), "escrow-1", group, nil,
			host.WithGrace(grace), host.WithChainOracle(oracles[i]))
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}
	userSM := statetest.MustStateMachine(t, "escrow-1", config, group, balance, user.Address(), verifier)
	all := append([]SessionOption{
		WithHeightSyncCadence(10, uint64(len(group))),
		WithHeartbeatClock(func() time.Time { return time.Unix(0, rig.clock.Load()) }),
		WithObservedHeight(func() (uint64, []byte, bool) {
			h := rig.height.Load()
			if h == 0 {
				return 0, nil, false
			}
			return h, []byte{0xaa}, true
		}),
	}, opts...)
	session, err := NewSession(userSM, user, "escrow-1", group, clients, verifier, all...)
	require.NoError(t, err)
	seedFloorByInference(t, session)
	return session, user, rig
}

func (r *floorRig) raise(t *testing.T, session *Session, h uint64) {
	t.Helper()
	r.set(h)
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		r.tick()
		require.NoError(t, session.MaybeHeartbeat(ctx))
		require.NoError(t, session.SendPendingDiff(ctx))
		if f, _, _ := session.StateMachine().HeightSyncFloorAsOf(session.Nonce() + 1); f >= h {
			return
		}
	}
	f, _, _ := session.StateMachine().HeightSyncFloorAsOf(session.Nonce() + 1)
	require.GreaterOrEqual(t, f, h, "host acks must carry the floor to the raised height")
}

func TestFloorRigRaisesTheFloor(t *testing.T) {
	session, _, _, rig := setupArmedSession(t, 3, 100000, 10)
	rig.raise(t, session, 500)
	rig.raise(t, session, 2000)
}

func floorStampOf(rec *types.InferenceRecord) testutil.ReceiptStamp {
	if rec == nil || rec.StartedAtHeight == 0 {
		return testutil.ReceiptStamp{}
	}
	return testutil.ReceiptStamp{Height: rec.StartedAtHeight, Hash: []byte{0xaa}}
}

func stampedReceiptResponse(t *testing.T, session *Session, signer signing.Signer, nonce uint64) (*host.HostResponse, *types.DevshardTx) {
	t.Helper()
	rec, ok := session.StateMachine().GetInference(nonce)
	require.True(t, ok)
	stamp := floorStampOf(&rec)
	sig := testutil.SignExecutorReceipt(t, signer, session.escrowID, nonce, rec.PromptHash, rec.Model, rec.InputLength, rec.MaxTokens, stamp)
	resp := &host.HostResponse{Nonce: nonce, Receipt: sig, ConfirmedAt: time.Now().Unix(), ObservedHeight: stamp.Height, ObservedBlockHash: stamp.Hash}
	tx := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: nonce, ExecutorSig: sig, ObservedHeight: stamp.Height, ObservedBlockHash: stamp.Hash,
	}}}
	return resp, tx
}

type noFinishClient struct{ HostClient }

func (c noFinishClient) Send(ctx context.Context, req host.HostRequest, w io.Writer, receipt func(*host.HostResponse)) (*host.HostResponse, error) {
	resp, err := c.HostClient.Send(ctx, req, w, receipt)
	if resp != nil {
		kept := resp.Mempool[:0:0]
		for _, tx := range resp.Mempool {
			if tx.GetFinishInference() == nil {
				kept = append(kept, tx)
			}
		}
		resp.Mempool = kept
	}
	return resp, err
}

type noReceiptClient struct{ HostClient }

func (c noReceiptClient) Send(ctx context.Context, req host.HostRequest, w io.Writer, receipt func(*host.HostResponse)) (*host.HostResponse, error) {
	resp, err := c.HostClient.Send(ctx, req, w, nil)
	if resp != nil {
		resp.Receipt = nil
		kept := resp.Mempool[:0:0]
		for _, tx := range resp.Mempool {
			if tx.GetFinishInference() == nil && tx.GetConfirmStart() == nil {
				kept = append(kept, tx)
			}
		}
		resp.Mempool = kept
	}
	return resp, err
}

func raiseRawFloor(t *testing.T, sm *state.StateMachine, slotSigner *signing.Secp256k1Signer, slots, height uint64) {
	t.Helper()
	ref := sm.LatestNonce() + 1
	_, applied, err := sm.ApplyLocalBestEffort(ref, []*types.DevshardTx{{Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
		SlotsNum: slots, Reason: string(heightsync.ReasonQuietSession),
	}}}})
	require.NoError(t, err)
	require.Len(t, applied, 1)
	ack := &types.MsgHeightAck{RefNonce: ref, SlotId: 0, ObservedHeight: height, ObservedBlockHash: []byte{0xaa},
		SyncState: types.SyncState_SYNCED, PeerSeen: []byte{0xff}}
	require.NoError(t, heightsync.SignAck(slotSigner, ack))
	_, applied, err = sm.ApplyLocalBestEffort(ref+1, []*types.DevshardTx{{Tx: &types.DevshardTx_HeightAck{HeightAck: ack}}})
	require.NoError(t, err)
	require.Len(t, applied, 1)
	f, _, _ := sm.HeightSyncFloorAsOf(sm.LatestNonce() + 1)
	require.Equal(t, height, f)
}
