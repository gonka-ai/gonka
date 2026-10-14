package host

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
)

// gatedBatchStore records every AppendDiffBatch and, while gate is set, holds
// each call until the gate closes, like a pool with no free connection.
type gatedBatchStore struct {
	*storage.Memory
	mu      sync.Mutex
	batches [][2]uint64
	gate    chan struct{}
	entered chan struct{}
	// before runs inside the call, ahead of the write.
	before func(ctx context.Context) error
}

func (s *gatedBatchStore) AppendDiffBatch(ctx context.Context, escrowID string, recs []types.DiffRecord) error {
	s.mu.Lock()
	s.batches = append(s.batches, [2]uint64{recs[0].Nonce, recs[len(recs)-1].Nonce})
	gate, entered, before := s.gate, s.entered, s.before
	s.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return fmt.Errorf("%w: %w", storage.ErrConnWait, ctx.Err())
		}
	}
	if before != nil {
		if err := before(ctx); err != nil {
			return err
		}
	}
	return s.Memory.AppendDiffBatch(ctx, escrowID, recs)
}

func (s *gatedBatchStore) calls() [][2]uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][2]uint64(nil), s.batches...)
}

type writerFixture struct {
	host  *Host
	store *gatedBatchStore
	diffs []types.Diff // diffs[i] has nonce i+1
}

func newWriterFixture(t *testing.T, n int) *writerFixture {
	t.Helper()
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	inner := storage.NewMemory()
	require.NoError(t, inner.CreateSession(storage.CreateSessionParams{
		EscrowID: "escrow-1", Version: testutil.RuntimeTestVersion,
		CreatorAddr: user.Address(), Config: config, Group: group, InitialBalance: 10000,
	}))

	probe, err := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier,
		testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 10000))
	require.NoError(t, err)
	diffs := make([]types.Diff, 0, n)
	for nonce := uint64(1); nonce <= uint64(n); nonce++ {
		root, err := probe.ApplyLocal(nonce, nil)
		require.NoError(t, err)
		diffs = append(diffs, testutil.SignDiffWithRoot(t, user, "escrow-1", nonce, nil, root))
	}

	store := &gatedBatchStore{Memory: inner}
	sm, err := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier, inner)
	require.NoError(t, err)
	h, err := NewHost(sm, hosts[0], stub.NewInferenceEngine(), "escrow-1", group, nil,
		WithGrace(uint64(n)+10), WithStorage(store), WithVerifier(verifier))
	require.NoError(t, err)
	return &writerFixture{host: h, store: store, diffs: diffs}
}

func (f *writerFixture) request(from, to uint64) HostRequest {
	return HostRequest{Diffs: f.diffs[from-1 : to], Nonce: to}
}

func (f *writerFixture) storedLatest(t *testing.T) uint64 {
	t.Helper()
	meta, err := f.store.GetSessionMeta("escrow-1")
	require.NoError(t, err)
	return meta.LatestNonce
}

func (f *writerFixture) queued() int {
	f.host.writer.mu.Lock()
	defer f.host.writer.mu.Unlock()
	return len(f.host.writer.queue)
}

func TestHost_ChunkIsOneBatchWrite(t *testing.T) {
	f := newWriterFixture(t, 200)
	resp, err := f.host.HandleRequest(context.Background(), f.request(1, 200))
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, uint64(200), f.host.LatestNonce())
	require.Equal(t, [][2]uint64{{1, 200}}, f.store.calls(), "200 diffs are written in one batch")
	require.Equal(t, uint64(200), f.storedLatest(t))
}

// Test flow:
//  1. Hold the first request's batch inside the store.
//  2. Queue two overlapping catch-up chunks behind it.
//  3. Release; the two queued requests share one batch and every request
//     gets its reply.
func TestHost_QueuedRequestsShareOneBatch(t *testing.T) {
	f := newWriterFixture(t, 30)
	f.store.gate = make(chan struct{})
	f.store.entered = make(chan struct{}, 1)

	type result struct {
		resp *HostResponse
		err  error
	}
	send := func(from, to uint64) <-chan result {
		out := make(chan result, 1)
		go func() {
			resp, err := f.host.HandleRequest(context.Background(), f.request(from, to))
			out <- result{resp, err}
		}()
		return out
	}

	a := send(1, 10)
	<-f.store.entered
	b := send(1, 20)
	c := send(11, 30)
	require.Eventually(t, func() bool { return f.queued() == 2 }, 2*time.Second, time.Millisecond)

	f.store.mu.Lock()
	gate := f.store.gate
	f.store.gate = nil
	f.store.mu.Unlock()
	close(gate)

	for _, ch := range []<-chan result{a, b, c} {
		select {
		case r := <-ch:
			require.NoError(t, r.err)
			require.NotNil(t, r.resp)
		case <-time.After(5 * time.Second):
			t.Fatal("a queued request got no reply")
		}
	}
	require.Equal(t, [][2]uint64{{1, 10}, {11, 30}}, f.store.calls())
	require.Equal(t, uint64(30), f.host.LatestNonce())
	require.Equal(t, uint64(30), f.storedLatest(t))
}

// Test flow:
//  1. Hold a batch for nonces 1..5 inside the store.
//  2. Queue 1..20, then 11..30, behind it; they share one batch.
//  3. Each applied nonce is reported as newly applied to one request only,
//     the first queued that carried it, so heartbeat acks and receipts are
//     not issued twice.
func TestHost_SharedBatchReportsEachNonceOnce(t *testing.T) {
	f := newWriterFixture(t, 30)
	f.store.gate = make(chan struct{})
	f.store.entered = make(chan struct{}, 1)

	type result struct {
		newly []types.Diff
		err   error
	}
	apply := func(from, to uint64) <-chan result {
		out := make(chan result, 1)
		go func() {
			f.host.mu.Lock()
			newly, err := f.host.applyDiffsLocked(context.Background(), f.diffs[from-1:to])
			f.host.mu.Unlock()
			out <- result{newly, err}
		}()
		return out
	}
	nonces := func(diffs []types.Diff) [2]uint64 {
		require.NotEmpty(t, diffs)
		for i := 1; i < len(diffs); i++ {
			require.Equal(t, diffs[i-1].Nonce+1, diffs[i].Nonce)
		}
		return [2]uint64{diffs[0].Nonce, diffs[len(diffs)-1].Nonce}
	}

	a := apply(1, 5)
	<-f.store.entered
	b := apply(1, 20)
	require.Eventually(t, func() bool { return f.queued() == 1 }, 2*time.Second, time.Millisecond)
	c := apply(11, 30)
	require.Eventually(t, func() bool { return f.queued() == 2 }, 2*time.Second, time.Millisecond)

	f.store.mu.Lock()
	gate := f.store.gate
	f.store.gate = nil
	f.store.mu.Unlock()
	close(gate)

	want := [][2]uint64{{1, 5}, {6, 20}, {21, 30}}
	for i, ch := range []<-chan result{a, b, c} {
		select {
		case r := <-ch:
			require.NoError(t, r.err)
			require.Equal(t, want[i], nonces(r.newly))
		case <-time.After(5 * time.Second):
			t.Fatal("a queued request got no reply")
		}
	}
}

func TestHost_CanceledQueuedRequestWritesNothing(t *testing.T) {
	f := newWriterFixture(t, 20)
	f.store.gate = make(chan struct{})
	f.store.entered = make(chan struct{}, 1)

	aDone := make(chan error, 1)
	go func() {
		_, err := f.host.HandleRequest(context.Background(), f.request(1, 10))
		aDone <- err
	}()
	<-f.store.entered

	ctx, cancel := context.WithCancel(context.Background())
	bDone := make(chan struct{})
	var bResp *HostResponse
	var bErr error
	go func() {
		defer close(bDone)
		bResp, bErr = f.host.HandleRequest(ctx, f.request(1, 20))
	}()
	require.Eventually(t, func() bool { return f.queued() == 1 }, 2*time.Second, time.Millisecond)
	cancel()
	<-bDone
	require.ErrorIs(t, bErr, context.Canceled)
	require.Nil(t, bResp, "a canceled request gets no receipt")
	require.Zero(t, f.queued(), "the canceled request left the queue")

	close(f.store.gate)
	require.NoError(t, <-aDone)
	require.Equal(t, [][2]uint64{{1, 10}}, f.store.calls(), "nothing of the canceled request reached the store")
	require.Equal(t, uint64(10), f.host.LatestNonce())
	require.Equal(t, uint64(10), f.storedLatest(t))
}

// A request whose batch is already being written completes the write but,
// once its client is gone, returns no receipt. The resend is a no-op write.
func TestHost_CancelDuringWriteCommitsButReturnsNoReceipt(t *testing.T) {
	f := newWriterFixture(t, 10)
	ctx, cancel := context.WithCancel(context.Background())
	f.store.before = func(context.Context) error { cancel(); return nil }

	resp, err := f.host.HandleRequest(ctx, f.request(1, 10))
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, resp)
	require.Equal(t, uint64(10), f.host.LatestNonce(), "a taken batch is written whole")
	require.Equal(t, uint64(10), f.storedLatest(t))

	f.store.before = nil
	resp, err = f.host.HandleRequest(context.Background(), f.request(1, 10))
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Len(t, f.store.calls(), 1, "the resend found every diff already applied")
}

func TestHost_StorageBusySurfacesAndLeavesMemoryUnchanged(t *testing.T) {
	f := newWriterFixture(t, 10)
	f.store.gate = make(chan struct{})
	defer close(f.store.gate)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	resp, err := f.host.HandleRequest(ctx, f.request(1, 10))
	require.ErrorIs(t, err, storage.ErrStorageBusy)
	require.Nil(t, resp)
	require.Zero(t, f.host.LatestNonce())
	require.Zero(t, f.storedLatest(t))
}
