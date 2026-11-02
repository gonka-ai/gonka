package user

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"common/nodemanager/gen"
	"devshard/chainoracle/blocks/headertime"
	"devshard/heightsync"
	"devshard/host"
	"devshard/hostevents"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/testenv/mockdapi"
	"devshard/testenv/mockheights"
	"devshard/types"
)

type floorRig struct {
	height  atomic.Uint64
	oracles []*sessionOracle
	clock   atomic.Int64
	feed    *mockdapi.HeightFeed
	gateway *headertime.Source
	hosts   []*headertime.Source
}

func (r *floorRig) set(h uint64) {
	if r.feed != nil && h > 0 {
		r.feed.Progress(int64(h))
	}
	r.height.Store(h)
	for _, o := range r.oracles {
		o.height.Store(int64(h))
	}
	r.waitHeight(h)
}

func (r *floorRig) waitHeight(h uint64) {
	if h == 0 || r.gateway == nil {
		return
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if r.heightSynced(h) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (r *floorRig) heightSynced(h uint64) bool {
	if _, ok := r.gateway.StoredAt(h); !ok {
		return false
	}
	for _, src := range r.hosts {
		if _, ok := src.StoredAt(h); !ok {
			return false
		}
	}
	return true
}

// coverTimeout waits out window after stampMs, then moves the floor to a new
// height. The new header time is the wall clock at that progress, so the
// chain's timeout check and the gateway's wall clock agree.
func (r *floorRig) coverTimeout(t *testing.T, session *Session, stampMs int64, window time.Duration) {
	t.Helper()
	if stampMs <= 0 {
		t.Fatal("coverTimeout: missing header time")
	}
	target := time.UnixMilli(stampMs).Add(window)
	if d := time.Until(target); d > 0 {
		time.Sleep(d + 30*time.Millisecond)
	}
	r.raise(t, session, r.height.Load()+1)
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
	rig.feed = mockdapi.NewHeightFeed()
	rig.gateway = headertime.New()
	rig.hosts = make([]*headertime.Source, len(group))
	rig.feed.ServeChain(rig.gateway)
	for i := range rig.hosts {
		rig.hosts[i] = headertime.New()
		rig.feed.ServeChain(rig.hosts[i])
	}
	startHeaderPolls(t, rig)
	_, fresh := rig.feed.Progress(100)
	require.True(t, fresh)
	rig.waitHeight(100)
	require.True(t, rig.heightSynced(100), "pollers store the starting height")

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
		sm := statetest.MustStateMachine(t, "escrow-1", config, group, balance, user.Address(), verifier, state.WithHeaderTimes(rig.hosts[i]))
		h, err := host.NewHost(sm, slotSigner, stub.NewInferenceEngine(), "escrow-1", group, nil,
			host.WithGrace(grace), host.WithChainOracle(oracles[i]))
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}
	userSM := statetest.MustStateMachine(t, "escrow-1", config, group, balance, user.Address(), verifier, state.WithHeaderTimes(rig.gateway))
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

// polledHeaders is the header-time map for heartbeat and recovery fixtures.
// The first claim for a height progresses the mock chain at that claim's hash.
// HeaderTimeMs returns the stamp only after the production poller has stored it,
// which is before the state machine keeps the confirm or ack.
type polledHeaders struct {
	mu  sync.Mutex
	rig *floorRig
}

var polledByTest sync.Map // *testing.T -> *polledHeaders

func polledSource(t *testing.T) *polledHeaders {
	t.Helper()
	if v, ok := polledByTest.Load(t); ok {
		return v.(*polledHeaders)
	}
	p := &polledHeaders{rig: openPollFeed(t)}
	polledByTest.Store(t, p)
	t.Cleanup(func() { polledByTest.Delete(t) })
	return p
}

func (p *polledHeaders) HeaderTimeMs(height uint64, hash []byte) (int64, error) {
	if height == 0 || len(hash) == 0 {
		return p.rig.gateway.HeaderTimeMs(height, hash)
	}
	if ms, err := p.rig.gateway.HeaderTimeMs(height, hash); err == nil {
		return ms, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if ms, err := p.rig.gateway.HeaderTimeMs(height, hash); err == nil {
		return ms, nil
	}
	if row, ok := p.rig.gateway.StoredAt(height); ok && !bytes.Equal(row.Hash, hash) {
		return p.rig.gateway.HeaderTimeMs(height, hash)
	}
	p.rig.feed.Chain().SetHash(hash)
	p.rig.feed.Progress(int64(height))
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ms, err := p.rig.gateway.HeaderTimeMs(height, hash)
		if err == nil {
			return ms, nil
		}
		if row, ok := p.rig.gateway.StoredAt(height); ok && !bytes.Equal(row.Hash, hash) {
			return 0, err
		}
		time.Sleep(5 * time.Millisecond)
	}
	return p.rig.gateway.HeaderTimeMs(height, hash)
}

// StoredAt is the height-keyed read the execution deadline uses. It does not
// progress the chain. A height the poller has not stored yet stays a miss.
func (p *polledHeaders) StoredAt(height uint64) (headertime.Record, bool) {
	if p == nil || p.rig == nil || p.rig.gateway == nil {
		return headertime.Record{}, false
	}
	return p.rig.gateway.StoredAt(height)
}

// StoredHash is the polled hash already stored. It does not progress the chain.
// A miss means the poller has not reached that height, and the ack still applies.
func (p *polledHeaders) StoredHash(height uint64) ([]byte, bool) {
	if p == nil || p.rig == nil || p.rig.gateway == nil {
		return nil, false
	}
	return p.rig.gateway.StoredHash(height)
}

// HostAckHash is a hash already accepted from a host ack.
func (p *polledHeaders) HostAckHash(height uint64) ([]byte, bool) {
	if p == nil || p.rig == nil || p.rig.gateway == nil {
		return nil, false
	}
	return p.rig.gateway.HostAckHash(height)
}

// NoteHostAck records an accepted host hash on the gateway map.
func (p *polledHeaders) NoteHostAck(height uint64, hash []byte) {
	if p == nil || p.rig == nil || p.rig.gateway == nil {
		return
	}
	p.rig.gateway.NoteHostAck(height, hash)
}

// openPollFeed is a single Source filled by the production poller. Tests that
// apply chain timeouts without a session use it so the map is the poll's stamp.
func openPollFeed(t *testing.T) *floorRig {
	t.Helper()
	rig := &floorRig{feed: mockdapi.NewHeightFeed(), gateway: headertime.New()}
	rig.feed.ServeChain(rig.gateway)
	startHeaderPolls(t, rig)
	return rig
}

func (r *floorRig) progress(t *testing.T, h uint64) {
	t.Helper()
	_, fresh := r.feed.Progress(int64(h))
	require.True(t, fresh)
	r.waitHeight(h)
	require.True(t, r.heightSynced(h))
}

func startHeaderPolls(t *testing.T, rig *floorRig) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	gs := grpc.NewServer()
	gen.RegisterNodeManagerServer(gs, rig.feed.Server())
	go func() { _ = gs.Serve(lis) }()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		gs.Stop()
		_ = conn.Close()
	})
	client := gen.NewNodeManagerClient(conn)
	sources := append([]*headertime.Source{rig.gateway}, rig.hosts...)
	for _, src := range sources {
		go hostevents.NewPoller(hostevents.PollerConfig{
			Config: hostevents.Config{
				Client:          client,
				ServerMaxWait:   time.Second,
				ErrorBackoffMin: time.Millisecond,
				ErrorBackoffMax: 5 * time.Millisecond,
			},
			Source:         src,
			BlocksVerified: true,
		}).Run(ctx)
	}
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

func TestStoppedDAPIWritesGatewayAndHostMaps(t *testing.T) {
	_, _, _, rig := setupArmedSession(t, 3, 100000, 10)
	before, ok := rig.gateway.StoredAt(100)
	require.True(t, ok)
	require.Equal(t, headertime.OriginDAPI, before.Origin)

	rig.feed.StopClient()
	block, fresh := rig.feed.Progress(250)
	require.True(t, fresh)
	rig.waitHeight(250)

	for _, src := range append([]*headertime.Source{rig.gateway}, rig.hosts...) {
		got, ok := src.StoredAt(250)
		require.True(t, ok)
		require.Equal(t, headertime.OriginChain, got.Origin)
		require.Equal(t, block.TimeMs, got.TimeMs)
		require.Equal(t, mockheights.DefaultHash, got.Hash)
	}
	kept, ok := rig.gateway.StoredAt(100)
	require.True(t, ok)
	require.Equal(t, before.TimeMs, kept.TimeMs)
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
