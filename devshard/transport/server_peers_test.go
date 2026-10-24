package transport

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/bridge"
	"devshard/heightsync"
	"devshard/host"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
)

const peerTestEscrow = "escrow-peers"

// countingPeer is an executor that never answers a challenge.
type countingPeer struct {
	HostPeerClient
	challenges atomic.Int32
	closed     atomic.Bool
}

func (p *countingPeer) ChallengeReceipt(context.Context, uint64, *host.InferencePayload, []types.Diff) ([]byte, []*types.DevshardTx, error) {
	p.challenges.Add(1)
	return nil, nil, errors.New("executor down")
}

func (p *countingPeer) Close() { p.closed.Store(true) }

// countingResolver hands out one countingPeer per call, or err.
type countingResolver struct {
	calls atomic.Int32
	err   error
	gate  chan struct{} // when non-nil, each call blocks until it is closed
	mu    sync.Mutex
	made  []*countingPeer
}

func (r *countingResolver) resolve(int) (HostPeerClient, error) {
	r.calls.Add(1)
	if r.gate != nil {
		<-r.gate
	}
	if r.err != nil {
		return nil, r.err
	}
	pc := &countingPeer{}
	r.mu.Lock()
	r.made = append(r.made, pc)
	r.mu.Unlock()
	return pc, nil
}

func (r *countingResolver) last() *countingPeer {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.made) == 0 {
		return nil
	}
	return r.made[len(r.made)-1]
}

// newPeerTestServer is slot 0 of a two-host group holding a pending
// inference 1 whose refusal deadline has passed. Slot 1 is its executor.
func newPeerTestServer(t *testing.T) *Server {
	t.Helper()
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(group))
	verifier := signing.NewSecp256k1Verifier()

	sm, err := state.NewStateMachine(peerTestEscrow, config, group, 100000, user.Address(), verifier,
		testutil.MustMemoryStore(t, peerTestEscrow, user.Address(), config, group, 100000))
	require.NoError(t, err)
	store := storage.NewMemory()
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       peerTestEscrow,
		Version:        testutil.RuntimeTestVersion,
		Config:         config,
		Group:          group,
		InitialBalance: 100000,
	}))
	h, err := host.NewHost(sm, hosts[0], stub.NewInferenceEngine(), peerTestEscrow, group, nil, host.WithStorage(store))
	require.NoError(t, err)
	t.Cleanup(h.Close)

	start := testutil.SignDiff(t, user, peerTestEscrow, 1, []*types.DevshardTx{testutil.StartTx(1)})
	_, err = h.HandleRequest(context.Background(), host.HostRequest{Diffs: []types.Diff{start}})
	require.NoError(t, err)
	require.Equal(t, types.StatusPending, h.SnapshotState().Inferences[1].Status)
	statetest.ArmRefusalDue(t, sm, 1)

	srv, err := NewServer(h, store, verifier, user.Address())
	require.NoError(t, err)
	return srv
}

func refusedVerifyRequest() VerifyTimeoutRequest {
	return VerifyTimeoutRequest{
		InferenceID: 1,
		Reason:      TimeoutReasonToString(types.TimeoutReason_TIMEOUT_REASON_REFUSED),
		Payload: PayloadToJSON(&host.InferencePayload{
			Prompt:      testutil.TestPrompt,
			Model:       "llama",
			InputLength: 100,
			MaxTokens:   testutil.TestMaxTokens,
			StartedAt:   1000,
		}),
	}
}

// Tests that a verifier opened without the executor's client builds it on the first timeout vote and keeps it.
//  1. Open the server with an empty roster and a resolver, as a bind that could not reach the chain leaves it.
//  2. Vote a refused timeout twice.
//  3. Expect the resolver to run once and the executor to be challenged on both votes.
func TestServeVerifyTimeout_ResolvesMissingExecutorOnFirstVote(t *testing.T) {
	srv := newPeerTestServer(t)
	r := &countingResolver{}
	srv.SetPeerClients(map[int]HostPeerClient{})
	srv.SetPeerResolver(r.resolve)

	for i := 0; i < 2; i++ {
		resp, err := srv.ServeVerifyTimeout(context.Background(), refusedVerifyRequest())
		require.NoError(t, err)
		require.True(t, resp.Accept, "an executor that does not answer the challenge supports the timeout")
	}
	require.EqualValues(t, 1, r.calls.Load(), "the resolved client is kept for the next vote")
	require.EqualValues(t, 2, r.last().challenges.Load(), "each vote challenges the executor")
	require.Contains(t, srv.PeerClients(), 1)
}

// Tests that a verifier that cannot look up the executor abstains instead of voting.
//  1. Give the server a resolver whose chain query fails.
//  2. Vote a refused timeout.
//  3. Expect ErrPeerUnresolved and no vote: accepting would count against an executor nobody challenged.
func TestServeVerifyTimeout_UnresolvedExecutorAbstains(t *testing.T) {
	srv := newPeerTestServer(t)
	r := &countingResolver{err: fmt.Errorf("get host info: %w", bridge.ErrChainUnavailable)}
	srv.SetPeerResolver(r.resolve)

	resp, err := srv.ServeVerifyTimeout(context.Background(), refusedVerifyRequest())
	require.ErrorIs(t, err, ErrPeerUnresolved)
	require.ErrorIs(t, err, bridge.ErrChainUnavailable)
	require.Nil(t, resp)
}

// Tests that an executor publishing no URL is treated as unreachable.
//  1. Give the server a resolver that reports ErrPeerURLMissing for the executor.
//  2. Vote a refused timeout.
//  3. Expect accept: no one can dial that executor, the gateway included.
func TestServeVerifyTimeout_ExecutorWithoutURLIsUnreachable(t *testing.T) {
	srv := newPeerTestServer(t)
	r := &countingResolver{err: fmt.Errorf("slot 1: %w", ErrPeerURLMissing)}
	srv.SetPeerResolver(r.resolve)

	resp, err := srv.ServeVerifyTimeout(context.Background(), refusedVerifyRequest())
	require.NoError(t, err)
	require.True(t, resp.Accept)
}

// Tests that a failed lookup is reused for peerLookupRetry before the chain is asked again.
//  1. Fail the resolver and ask for the slot twice.
//  2. Expire the cached failure and let the resolver succeed.
//  3. Expect one query for the first two calls, and the slot filled by the third.
func TestPeerClient_FailedLookupWaitsForRetryWindow(t *testing.T) {
	srv := newPeerTestServer(t)
	r := &countingResolver{err: bridge.ErrChainUnavailable}
	srv.SetPeerResolver(r.resolve)

	for i := 0; i < 2; i++ {
		_, err := srv.peerClient(context.Background(), 1)
		require.ErrorIs(t, err, bridge.ErrChainUnavailable)
	}
	require.EqualValues(t, 1, r.calls.Load(), "the failure answers until the retry window ends")

	srv.peers.mu.Lock()
	l := srv.peers.slots[1]
	srv.peers.mu.Unlock()
	expireLazyFailure(l)
	r.err = nil

	pc, err := srv.peerClient(context.Background(), 1)
	require.NoError(t, err)
	require.NotNil(t, pc)
	require.EqualValues(t, 2, r.calls.Load())
	require.Contains(t, srv.PeerClients(), 1)
}

// Tests that concurrent callers for one slot share a single lookup.
//  1. Block the resolver and ask for the slot from eight goroutines.
//  2. Release the resolver.
//  3. Expect one query and every caller to get the same client.
func TestPeerClient_ConcurrentCallersShareOneLookup(t *testing.T) {
	srv := newPeerTestServer(t)
	r := &countingResolver{gate: make(chan struct{})}
	srv.SetPeerResolver(r.resolve)

	type result struct {
		pc  HostPeerClient
		err error
	}
	const callers = 8
	got := make(chan result, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pc, err := srv.peerClient(context.Background(), 1)
			got <- result{pc: pc, err: err}
		}()
	}
	require.Eventually(t, func() bool { return r.calls.Load() == 1 }, time.Second, time.Millisecond)
	close(r.gate)
	wg.Wait()
	close(got)

	require.EqualValues(t, 1, r.calls.Load())
	first := r.last()
	for res := range got {
		require.NoError(t, res.err)
		require.Same(t, first, res.pc)
	}
}

// Tests that a hung chain query does not hold the caller past its context.
//  1. Block the resolver and ask for the slot with a short deadline.
//  2. Expect the caller to return the deadline, not wait for the query.
//  3. Release the resolver and expect the slot to be filled for the next caller.
func TestPeerClient_HungLookupHonoursContext(t *testing.T) {
	srv := newPeerTestServer(t)
	r := &countingResolver{gate: make(chan struct{})}
	srv.SetPeerResolver(r.resolve)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := srv.peerClient(ctx, 1)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	close(r.gate)
	require.Eventually(t, func() bool {
		_, ok := srv.PeerClients()[1]
		return ok
	}, time.Second, time.Millisecond)
}

// Tests that a client resolved after the session closed is closed, not installed.
//  1. Start a lookup that blocks in the resolver.
//  2. Close the server's outbound clients, then release the resolver.
//  3. Expect the late client to be closed and the slot to report the server closed.
func TestPeerClient_LookupFinishingAfterCloseIsDiscarded(t *testing.T) {
	srv := newPeerTestServer(t)
	r := &countingResolver{gate: make(chan struct{})}
	srv.SetPeerResolver(r.resolve)

	done := make(chan error, 1)
	go func() {
		_, err := srv.peerClient(context.Background(), 1)
		done <- err
	}()
	require.Eventually(t, func() bool { return r.calls.Load() == 1 }, time.Second, time.Millisecond)

	srv.CloseOutbound()
	close(r.gate)
	require.ErrorIs(t, <-done, errPeerClientsClosed)
	require.True(t, r.last().closed.Load(), "the late client must not leak its PeerConn")
	require.Nil(t, srv.PeerClients())

	_, err := srv.peerClient(context.Background(), 1)
	require.ErrorIs(t, err, errPeerClientsClosed)
}

// Tests that CloseOutbound closes clients the resolver installed.
//  1. Resolve slot 1 lazily.
//  2. Close the server's outbound clients.
//  3. Expect that client to be closed.
func TestCloseOutbound_ClosesResolvedClients(t *testing.T) {
	srv := newPeerTestServer(t)
	r := &countingResolver{}
	srv.SetPeerResolver(r.resolve)

	_, err := srv.peerClient(context.Background(), 1)
	require.NoError(t, err)
	srv.CloseOutbound()
	require.True(t, r.last().closed.Load())
}

// Tests that StartPeerLookups resolves slots without anyone waiting on them.
//  1. Give the server a resolver and start lookups for slots 0 and 1.
//  2. Expect both slots to appear in PeerClients with no probe or vote.
//  3. Vote a refused timeout and expect it to reuse slot 1 without another lookup.
func TestStartPeerLookups_FillsSlotsInBackground(t *testing.T) {
	srv := newPeerTestServer(t)
	r := &countingResolver{}
	srv.SetPeerResolver(r.resolve)

	srv.StartPeerLookups(0, 1)
	require.Eventually(t, func() bool { return len(srv.PeerClients()) == 2 }, time.Second, time.Millisecond)

	_, err := srv.ServeVerifyTimeout(context.Background(), refusedVerifyRequest())
	require.NoError(t, err)
	require.EqualValues(t, 2, r.calls.Load())
}

// Tests that replacing the roster releases a lookup that was still running.
//  1. Start a lookup for slot 1 that blocks in the resolver.
//  2. Replace the roster with SetPeerClients, then release the resolver.
//  3. Expect the late client to be closed and the new roster to be untouched.
func TestSetPeerClients_ReleasesReplacedLookup(t *testing.T) {
	srv := newPeerTestServer(t)
	r := &countingResolver{gate: make(chan struct{})}
	srv.SetPeerResolver(r.resolve)
	srv.StartPeerLookups(1)
	require.Eventually(t, func() bool { return r.calls.Load() == 1 }, time.Second, time.Millisecond)

	replacement := &countingPeer{}
	srv.SetPeerClients(map[int]HostPeerClient{1: replacement})
	close(r.gate)

	require.Eventually(t, func() bool {
		late := r.last()
		return late != nil && late.closed.Load()
	}, time.Second, time.Millisecond)
	require.Same(t, replacement, srv.PeerClients()[1])
	require.False(t, replacement.closed.Load())
}

// Tests that a repair probe to a slot whose URL cannot be resolved fails as a probe error.
//  1. Give the server a resolver whose chain query fails.
//  2. Probe slot 1.
//  3. Expect an error carrying the lookup cause; the host records that as UNREACHABLE.
func TestRepairProbe_UnresolvedSlotIsProbeError(t *testing.T) {
	srv := newPeerTestServer(t)
	r := &countingResolver{err: bridge.ErrChainUnavailable}
	srv.SetPeerResolver(r.resolve)

	_, err := srv.RepairProbe(context.Background(), 1, &heightsync.RepairRequest{})
	require.ErrorIs(t, err, bridge.ErrChainUnavailable)
}
