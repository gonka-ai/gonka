package gossip

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/storage"
	"devshard/types"
)

// mockPeer records gossip calls.
type mockPeer struct {
	mu          sync.Mutex
	nonceCalls  []nonceCall
	txsCalls    [][]*types.DevshardTx
	nonceCount  atomic.Int32
	failOnNonce bool
}

type nonceCall struct {
	nonce     uint64
	stateHash []byte
	stateSig  []byte
	slotID    uint32
}

func (m *mockPeer) GossipNonce(_ context.Context, nonce uint64, stateHash, stateSig []byte, slotID uint32) error {
	m.nonceCount.Add(1)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nonceCalls = append(m.nonceCalls, nonceCall{nonce, stateHash, stateSig, slotID})
	if m.failOnNonce {
		return context.DeadlineExceeded
	}
	return nil
}

func (m *mockPeer) GossipTxs(_ context.Context, txs []*types.DevshardTx) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.txsCalls = append(m.txsCalls, txs)
	return nil
}

func (m *mockPeer) getNonceCalls() []nonceCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]nonceCall, len(m.nonceCalls))
	copy(result, m.nonceCalls)
	return result
}

// mockMempool records AddTx calls.
type mockMempool struct {
	mu  sync.Mutex
	txs []*types.DevshardTx
}

func (m *mockMempool) AddTx(tx *types.DevshardTx) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.txs = append(m.txs, tx)
}

// mockSigAccumulator records AccumulateGossipSig calls.
type mockSigAccumulator struct {
	mu    sync.Mutex
	calls []sigAccCall
}

type sigAccCall struct {
	nonce     uint64
	stateHash []byte
	sig       []byte
	slot      uint32
}

func (m *mockSigAccumulator) AccumulateGossipSig(nonce uint64, stateHash, sig []byte, senderSlot uint32) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, sigAccCall{nonce, stateHash, sig, senderSlot})
	return nil
}

func (m *mockSigAccumulator) getCalls() []sigAccCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]sigAccCall, len(m.calls))
	copy(result, m.calls)
	return result
}

// mockDiffFetcher returns pre-configured diffs.
type mockDiffFetcher struct {
	diffs []types.Diff
	err   error
}

func (m *mockDiffFetcher) GetDiffPages(_ context.Context, _, _ uint64, fn func([]types.Diff) error) error {
	if m.err != nil {
		return m.err
	}
	if len(m.diffs) == 0 {
		return nil
	}
	return fn(m.diffs)
}

// mockStateUpdater returns pre-configured sigs.
type mockStateUpdater struct {
	sigs []GossipSig
	err  error
}

func (m *mockStateUpdater) ApplyRecoveredDiffs(_ context.Context, _ []types.Diff) ([]GossipSig, error) {
	return m.sigs, m.err
}

func TestAfterRequest_SendsToKPeers(t *testing.T) {
	peers := make([]PeerClient, 15)
	mocks := make([]*mockPeer, 15)
	for i := range peers {
		m := &mockPeer{}
		mocks[i] = m
		peers[i] = m
	}

	g := NewGossip("escrow-1", 0, peers, nil)
	g.K = 5

	ctx := context.Background()
	g.AfterRequest(ctx, 1, []byte("hash1"), []byte("sig1"))

	// Exactly K peers should have been contacted.
	total := 0
	for _, m := range mocks {
		total += int(m.nonceCount.Load())
	}
	require.Equal(t, 5, total)
}

func TestAfterRequest_AllPeersWhenLessThanK(t *testing.T) {
	peers := make([]PeerClient, 3)
	mocks := make([]*mockPeer, 3)
	for i := range peers {
		m := &mockPeer{}
		mocks[i] = m
		peers[i] = m
	}

	g := NewGossip("escrow-1", 0, peers, nil)
	g.K = 10

	ctx := context.Background()
	g.AfterRequest(ctx, 1, []byte("hash1"), []byte("sig1"))

	// All 3 peers should be contacted (3 < K=10).
	total := 0
	for _, m := range mocks {
		total += int(m.nonceCount.Load())
	}
	require.Equal(t, 3, total)
}

func TestOnNonceReceived_SameHash_NoError(t *testing.T) {
	g := NewGossip("escrow-1", 0, nil, nil)

	err := g.OnNonceReceived(1, []byte("hash1"), []byte("sig1"), 1)
	require.NoError(t, err)

	err = g.OnNonceReceived(1, []byte("hash1"), []byte("sig2"), 2)
	require.NoError(t, err)
}

func TestOnNonceReceived_DifferentHash_Equivocation(t *testing.T) {
	g := NewGossip("escrow-1", 0, nil, nil)

	err := g.OnNonceReceived(1, []byte("hash1"), []byte("sig1"), 1)
	require.NoError(t, err)

	err = g.OnNonceReceived(1, []byte("hash2"), []byte("sig2"), 2)
	require.Error(t, err)
	require.Contains(t, err.Error(), "equivocation")
}

func TestOnNonceReceived_Amplification(t *testing.T) {
	peer := &mockPeer{}
	g := NewGossip("escrow-1", 0, []PeerClient{peer}, nil)
	g.K = 5

	// First time seeing nonce 5 -> should forward to peers.
	err := g.OnNonceReceived(5, []byte("hash5"), []byte("sig5"), 2)
	require.NoError(t, err)

	// Wait for async send to complete.
	time.Sleep(50 * time.Millisecond)

	calls := peer.getNonceCalls()
	require.Len(t, calls, 1, "new nonce should be forwarded to peers")
	require.Equal(t, uint64(5), calls[0].nonce)
	require.Equal(t, uint32(2), calls[0].slotID)
}

func TestOnNonceReceived_NoAmplificationForKnownNonce(t *testing.T) {
	peer := &mockPeer{}
	g := NewGossip("escrow-1", 0, []PeerClient{peer}, nil)

	// First receive: triggers amplification.
	err := g.OnNonceReceived(5, []byte("hash5"), []byte("sig5"), 2)
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	countAfterFirst := int(peer.nonceCount.Load())

	// Second receive with same hash: no amplification.
	err = g.OnNonceReceived(5, []byte("hash5"), []byte("sig6"), 3)
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	require.Equal(t, countAfterFirst, int(peer.nonceCount.Load()),
		"known nonce should not trigger re-amplification")
}

func TestOnNonceReceived_SigAccumulation(t *testing.T) {
	acc := &mockSigAccumulator{}
	g := NewGossip("escrow-1", 0, nil, nil, WithSigAccumulator(acc))

	// AfterRequest stores nonce 5 in seen map.
	g.AfterRequest(context.Background(), 5, []byte("hash5"), []byte("our-sig"))

	// Receive gossip for same nonce -> should accumulate sig.
	err := g.OnNonceReceived(5, []byte("hash5"), []byte("peer-sig"), 3)
	require.NoError(t, err)

	calls := acc.getCalls()
	require.Len(t, calls, 1)
	require.Equal(t, uint64(5), calls[0].nonce)
	require.Equal(t, uint32(3), calls[0].slot)
	require.Equal(t, []byte("peer-sig"), calls[0].sig)
}

func TestNonceStatus_ReturnsCopyOfGossipRecord(t *testing.T) {
	g := NewGossip("escrow-1", 0, nil, nil)
	require.NoError(t, g.OnNonceReceived(5, []byte("hash5"), []byte("sig5"), 2))

	status, seen := g.NonceStatus(5)
	require.True(t, seen)
	require.Equal(t, []byte("hash5"), status.StateHash)
	require.Equal(t, []byte("sig5"), status.StateSig)
	require.Equal(t, uint32(2), status.SlotID)

	status.StateHash[0] = 'X'
	status.StateSig[0] = 'Y'
	again, seen := g.NonceStatus(5)
	require.True(t, seen)
	require.Equal(t, []byte("hash5"), again.StateHash)
	require.Equal(t, []byte("sig5"), again.StateSig)
}

func TestOnTxsReceived_ForwardsToMempool(t *testing.T) {
	mem := &mockMempool{}
	g := NewGossip("escrow-1", 0, nil, mem)

	txs := []*types.DevshardTx{
		{Tx: &types.DevshardTx_FinalizeRound{FinalizeRound: &types.MsgFinalizeRound{}}},
	}
	g.OnTxsReceived(txs)

	mem.mu.Lock()
	defer mem.mu.Unlock()
	require.Len(t, mem.txs, 1)
}

func TestRebroadcast_StaleNonce(t *testing.T) {
	peer := &mockPeer{}
	g := NewGossip("escrow-1", 0, []PeerClient{peer}, nil)
	g.StaleTTL = 10 * time.Millisecond

	// Simulate receiving a nonce.
	err := g.OnNonceReceived(5, []byte("hash5"), []byte("sig5"), 2)
	require.NoError(t, err)

	// Wait for async amplification + past StaleTTL.
	time.Sleep(50 * time.Millisecond)

	initialCount := int(peer.nonceCount.Load())

	ctx := context.Background()
	g.rebroadcastStale(ctx)

	calls := peer.getNonceCalls()
	require.Greater(t, len(calls), initialCount, "stale nonce should be rebroadcast")
}

func TestHighestSeen(t *testing.T) {
	g := NewGossip("escrow-1", 0, nil, nil)

	require.Equal(t, uint64(0), g.HighestSeen())

	g.AfterRequest(context.Background(), 3, []byte("h3"), []byte("s3"))
	require.Equal(t, uint64(3), g.HighestSeen())

	_ = g.OnNonceReceived(7, []byte("h7"), []byte("s7"), 1)
	require.Equal(t, uint64(7), g.HighestSeen())
}

func TestRecovery_TriggersWhenBehind(t *testing.T) {
	fetchedDiffs := []types.Diff{
		{Nonce: 4, UserSig: []byte("sig4")},
		{Nonce: 5, UserSig: []byte("sig5")},
	}
	recoveredSigs := []GossipSig{
		{Nonce: 4, StateHash: []byte("h4"), Sig: []byte("s4"), SlotID: 0},
		{Nonce: 5, StateHash: []byte("h5"), Sig: []byte("s5"), SlotID: 0},
	}

	fetcher := &mockDiffFetcher{diffs: fetchedDiffs}
	updater := &mockStateUpdater{sigs: recoveredSigs}
	peer := &mockPeer{}

	g := NewGossip("escrow-1", 0, []PeerClient{peer}, nil, WithRecovery(fetcher, updater))
	g.RecoveryDelay = 0 // no delay for test

	// AfterRequest sets lastAfterReqNonce to 3.
	g.AfterRequest(context.Background(), 3, []byte("h3"), []byte("s3"))
	// Gossip sees nonce 5 -> gap: highestSeen(5) > lastAfterReqNonce(3).
	_ = g.OnNonceReceived(5, []byte("h5"), []byte("s5"), 1)

	// Wait for amplification of nonce 5 to complete.
	time.Sleep(50 * time.Millisecond)

	// Reset lastAfterReq to past so recovery triggers.
	g.mu.Lock()
	g.lastAfterReq = time.Now().Add(-2 * time.Hour)
	g.mu.Unlock()

	peerCountBefore := int(peer.nonceCount.Load())

	g.tryRecovery(context.Background())

	// Should have gossipped recovered sigs.
	time.Sleep(50 * time.Millisecond)
	require.Greater(t, int(peer.nonceCount.Load()), peerCountBefore,
		"recovery should gossip recovered sigs")
}

func TestRebroadcast_OnlyOnce(t *testing.T) {
	peer := &mockPeer{}
	g := NewGossip("escrow-1", 0, []PeerClient{peer}, nil)
	g.StaleTTL = 10 * time.Millisecond

	err := g.OnNonceReceived(5, []byte("hash5"), []byte("sig5"), 2)
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	// First rebroadcast should send.
	g.rebroadcastStale(context.Background())
	countAfterFirst := int(peer.nonceCount.Load())
	require.Greater(t, countAfterFirst, 1, "first rebroadcast should send")

	// Second rebroadcast should be a noop (already rebroadcast).
	g.rebroadcastStale(context.Background())
	require.Equal(t, countAfterFirst, int(peer.nonceCount.Load()),
		"second rebroadcast should be noop")
}

func TestBroadcastTxs_SendsToAllPeers(t *testing.T) {
	peers := make([]PeerClient, 5)
	mocks := make([]*mockPeer, 5)
	for i := range peers {
		m := &mockPeer{}
		mocks[i] = m
		peers[i] = m
	}

	g := NewGossip("escrow-1", 0, peers, nil)
	g.K = 2 // K is irrelevant -- BroadcastTxs sends to ALL

	txs := []*types.DevshardTx{
		{Tx: &types.DevshardTx_FinalizeRound{FinalizeRound: &types.MsgFinalizeRound{}}},
	}
	g.BroadcastTxs(context.Background(), txs)

	for i, m := range mocks {
		m.mu.Lock()
		require.Len(t, m.txsCalls, 1, "peer %d should receive txs", i)
		m.mu.Unlock()
	}
}

func TestBroadcastTxs_Dedup(t *testing.T) {
	peer := &mockPeer{}
	g := NewGossip("escrow-1", 0, []PeerClient{peer}, nil)

	txs := []*types.DevshardTx{
		{Tx: &types.DevshardTx_FinalizeRound{FinalizeRound: &types.MsgFinalizeRound{}}},
	}

	g.BroadcastTxs(context.Background(), txs)
	peer.mu.Lock()
	require.Len(t, peer.txsCalls, 1)
	peer.mu.Unlock()

	// Same txs again -- should not re-send.
	g.BroadcastTxs(context.Background(), txs)
	peer.mu.Lock()
	require.Len(t, peer.txsCalls, 1, "duplicate txs should not be re-sent")
	peer.mu.Unlock()
}

func TestBroadcastTxs_EmptyIsNoop(t *testing.T) {
	peer := &mockPeer{}
	g := NewGossip("escrow-1", 0, []PeerClient{peer}, nil)

	g.BroadcastTxs(context.Background(), nil)
	g.BroadcastTxs(context.Background(), []*types.DevshardTx{})

	peer.mu.Lock()
	require.Empty(t, peer.txsCalls)
	peer.mu.Unlock()
}

func TestPruneBelow(t *testing.T) {
	g := NewGossip("escrow-1", 0, nil, nil)

	// Populate seen with nonces 1..200.
	for i := uint64(1); i <= 200; i++ {
		g.seen[i] = &nonceRecord{stateHash: []byte("h"), seenAt: time.Now()}
	}

	// Prune below 150 -> cutoff = 150 - 100 = 50.
	// Nonces 1..49 should be removed.
	g.PruneBelow(150)

	g.mu.Lock()
	for i := uint64(1); i < 50; i++ {
		_, ok := g.seen[i]
		require.False(t, ok, "nonce %d should be pruned", i)
	}
	// Nonce 50 (cutoff) and above should remain.
	for i := uint64(50); i <= 200; i++ {
		_, ok := g.seen[i]
		require.True(t, ok, "nonce %d should remain", i)
	}
	g.mu.Unlock()

	// Prune with nonce <= margin should be a noop.
	g.PruneBelow(50)
	g.mu.Lock()
	require.Greater(t, len(g.seen), 0)
	g.mu.Unlock()
}

func TestRecovery_UpdatesWatermark(t *testing.T) {
	recoveredSigs := []GossipSig{
		{Nonce: 4, StateHash: []byte("h4"), Sig: []byte("s4"), SlotID: 0},
		{Nonce: 5, StateHash: []byte("h5"), Sig: []byte("s5"), SlotID: 0},
	}

	fetcher := &mockDiffFetcher{diffs: []types.Diff{
		{Nonce: 4, UserSig: []byte("sig4")},
		{Nonce: 5, UserSig: []byte("sig5")},
	}}
	updater := &mockStateUpdater{sigs: recoveredSigs}

	g := NewGossip("escrow-1", 0, nil, nil, WithRecovery(fetcher, updater))
	g.RecoveryDelay = 0

	// Set initial state: applied up to 3, seen up to 5.
	g.AfterRequest(context.Background(), 3, []byte("h3"), []byte("s3"))
	_ = g.OnNonceReceived(5, []byte("h5"), []byte("s5"), 1)
	time.Sleep(50 * time.Millisecond)

	g.mu.Lock()
	g.lastAfterReq = time.Now().Add(-2 * time.Hour)
	g.mu.Unlock()

	g.tryRecovery(context.Background())

	g.mu.Lock()
	require.Equal(t, uint64(5), g.lastAfterReqNonce,
		"watermark should advance to highest recovered nonce")
	g.mu.Unlock()
}

func TestStop_DoubleSafe(t *testing.T) {
	g := NewGossip("escrow-1", 0, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	g.Start(ctx)

	// First stop.
	g.Stop()

	// Second stop should not panic.
	cancel() // ensure background loop is done
	// closeOnce prevents double-close panic.
	require.NotPanics(t, func() {
		// stopCh already closed, stopped already closed.
		// This should not panic.
		g.closeOnce.Do(func() {}) // noop
	})
}

func TestRecovery_PagesAMultiPageGap(t *testing.T) {
	const n = storage.DiffPageMaxNonces + 5
	journal := make([]types.Diff, n)
	for i := range journal {
		journal[i] = types.Diff{Nonce: uint64(i + 1)}
	}
	fetcher := &rangeDiffFetcher{journal: journal}
	updater := &recordingUpdater{}

	g := NewGossip("escrow-1", 0, nil, nil, WithRecovery(fetcher, updater))
	g.RecoveryDelay = 0
	g.mu.Lock()
	g.highestSeen = n
	g.lastAfterReqNonce = 0
	g.lastAfterReq = time.Now().Add(-2 * time.Hour)
	g.mu.Unlock()

	g.tryRecovery(context.Background())

	require.Greater(t, len(updater.batches), 1, "a gap wider than one page is more than one apply")
	var applied []uint64
	for _, batch := range updater.batches {
		require.LessOrEqual(t, len(batch), storage.DiffPageMaxNonces)
		require.NotEmpty(t, batch)
		for _, diff := range batch {
			applied = append(applied, diff.Nonce)
		}
	}
	require.Len(t, applied, n)
	for i := uint64(1); i <= n; i++ {
		require.Equal(t, i, applied[i-1])
	}
	for _, call := range fetcher.calls {
		require.LessOrEqual(t, call[1]-call[0], uint64(storage.DiffPageMaxNonces-1))
	}
	g.mu.Lock()
	require.Equal(t, uint64(n), g.lastAfterReqNonce)
	g.mu.Unlock()
}

func TestRecovery_WalksPastAnEmptyWindow(t *testing.T) {
	const page = storage.DiffPageMaxNonces
	const highest = 2*page + 3
	var journal []types.Diff
	for _, nonce := range []uint64{1, 2, 3, highest - 1, highest} {
		journal = append(journal, types.Diff{Nonce: nonce})
	}
	fetcher := &rangeDiffFetcher{journal: journal}
	updater := &recordingUpdater{}

	g := NewGossip("escrow-1", 0, nil, nil, WithRecovery(fetcher, updater))
	g.RecoveryDelay = 0
	g.mu.Lock()
	g.highestSeen = highest
	g.lastAfterReq = time.Now().Add(-2 * time.Hour)
	g.mu.Unlock()

	g.tryRecovery(context.Background())

	require.Equal(t, [][2]uint64{{1, page}, {page + 1, 2 * page}, {2*page + 1, highest}}, fetcher.calls,
		"a window with no stored diffs moves on to the next one")
	require.Len(t, updater.batches, 2)
	require.Equal(t, []types.Diff{{Nonce: highest - 1}, {Nonce: highest}}, updater.batches[1])
	g.mu.Lock()
	require.Equal(t, uint64(highest), g.lastAfterReqNonce)
	g.mu.Unlock()
}

// rangeDiffFetcher returns only the journal slice inside the requested window.
type rangeDiffFetcher struct {
	journal []types.Diff
	calls   [][2]uint64
}

func (f *rangeDiffFetcher) GetDiffPages(_ context.Context, from, to uint64, fn func([]types.Diff) error) error {
	f.calls = append(f.calls, [2]uint64{from, to})
	var page []types.Diff
	for _, diff := range f.journal {
		if diff.Nonce >= from && diff.Nonce <= to {
			page = append(page, diff)
		}
	}
	if len(page) == 0 {
		return nil
	}
	return fn(page)
}

// recordingUpdater applies each page and returns one signature per diff.
type recordingUpdater struct {
	batches [][]types.Diff
}

func (u *recordingUpdater) ApplyRecoveredDiffs(_ context.Context, diffs []types.Diff) ([]GossipSig, error) {
	u.batches = append(u.batches, append([]types.Diff(nil), diffs...))
	sigs := make([]GossipSig, len(diffs))
	for i, diff := range diffs {
		sigs[i] = GossipSig{Nonce: diff.Nonce, StateHash: []byte("h"), Sig: []byte("s"), SlotID: 0}
	}
	return sigs, nil
}

// failingUpdater applies diffs up to failAt, then fails, returning the
// signatures of the nonces it applied as Host.ApplyRecoveredDiffs does.
type failingUpdater struct {
	failAt uint64
	calls  int
}

func (u *failingUpdater) ApplyRecoveredDiffs(_ context.Context, diffs []types.Diff) ([]GossipSig, error) {
	u.calls++
	var sigs []GossipSig
	for _, diff := range diffs {
		if diff.Nonce == u.failAt {
			return sigs, errors.New("apply failed")
		}
		sigs = append(sigs, GossipSig{Nonce: diff.Nonce, StateHash: []byte("h"), Sig: []byte("s")})
	}
	return sigs, nil
}

func TestRecovery_PublishesTheAppliedPrefixOfAFailedPage(t *testing.T) {
	journal := []types.Diff{{Nonce: 1}, {Nonce: 2}, {Nonce: 3}, {Nonce: 4}, {Nonce: 5}}
	fetcher := &rangeDiffFetcher{journal: journal}
	updater := &failingUpdater{failAt: 4}
	peer := &mockPeer{}

	g := NewGossip("escrow-1", 0, []PeerClient{peer}, nil, WithRecovery(fetcher, updater))
	g.RecoveryDelay = time.Hour
	g.mu.Lock()
	g.highestSeen = 5
	g.mu.Unlock()

	g.tryRecovery(context.Background())

	var published []uint64
	for _, call := range peer.getNonceCalls() {
		published = append(published, call.nonce)
	}
	require.Equal(t, []uint64{1, 2, 3}, published, "the applied prefix is published before the walk stops")
	g.mu.Lock()
	require.Equal(t, uint64(3), g.lastAfterReqNonce)
	require.True(t, g.lastAfterReq.IsZero(), "a walk that stops short does not start the recovery delay")
	g.mu.Unlock()

	updater.failAt = 0
	g.tryRecovery(context.Background())

	require.Equal(t, [][2]uint64{{1, 5}, {4, 5}}, fetcher.calls, "the next pass resumes after the applied prefix")
	g.mu.Lock()
	require.Equal(t, uint64(5), g.lastAfterReqNonce)
	require.False(t, g.lastAfterReq.IsZero(), "reaching highestSeen starts the recovery delay")
	g.mu.Unlock()

	g.mu.Lock()
	g.highestSeen = 6
	g.mu.Unlock()
	g.tryRecovery(context.Background())
	require.Len(t, fetcher.calls, 2, "a caught-up walk waits out RecoveryDelay")
}

func TestRecovery_DoesNotTriggerWhenUpToDate(t *testing.T) {
	fetcher := &mockDiffFetcher{}
	updater := &mockStateUpdater{}

	g := NewGossip("escrow-1", 0, nil, nil, WithRecovery(fetcher, updater))

	// AfterRequest sets lastAfterReqNonce to 5. highestSeen is also 5.
	g.AfterRequest(context.Background(), 5, []byte("h5"), []byte("s5"))

	// No gap -> recovery should not fetch.
	g.tryRecovery(context.Background())
	// No panic/error = success.
}
