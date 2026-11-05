package registry

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	gatewayescrow "devshard/cmd/gateway/escrow"
	"devshard/host"
	"devshard/types"
	"devshard/user"
)

// Test flow:
//  1. Build a registry whose serving factory rebuilds escrow 5, which the registry does not hold, and share the chain's max_nonce of 1700.
//  2. Run the pre-finalize sweep and then Finalize for escrow 5, recording the max_nonce the rebuilt session holds when each one's work runs and clearing it after the sweep.
//  3. Assert the factory rebuilt the session for each call and both saw 1700.
func TestARebuiltSettlementSessionHoldsTheChainMaxNonceBeforeItsWorkRuns(t *testing.T) {
	t.Parallel()
	session := settleableSession(9)
	var sweptUnder, finalizedUnder uint32
	session.onSweep = func() {
		sweptUnder = session.chainMaxNonce.Load()
		session.chainMaxNonce.Store(0)
	}
	session.onFinalize = func() { finalizedUnder = session.chainMaxNonce.Load() }
	serving := newSessions(map[string]*fakeSession{"5": session})
	registry := New(Deps{ServingSessions: serving.open, Now: fixedClock()})
	registry.ShareChainMaxNonce(1_700)

	if _, _, _, err := registry.SweepBeforeFinalize(context.Background(), "5", 8); err != nil {
		t.Fatalf("SweepBeforeFinalize = %v, want nil", err)
	}
	if err := registry.Finalize(context.Background(), "5"); err != nil {
		t.Fatalf("Finalize = %v, want nil", err)
	}

	if got := serving.calls.Load(); got != 2 {
		t.Fatalf("serving factory calls = %d, want 2", got)
	}
	if sweptUnder != 1_700 {
		t.Errorf("max_nonce during the pre-finalize sweep = %d, want 1700", sweptUnder)
	}
	if finalizedUnder != 1_700 {
		t.Errorf("max_nonce during Finalize = %d, want 1700", finalizedUnder)
	}
}

type keptOpenSession struct {
	EscrowSession
}

func (keptOpenSession) Close() error { return nil }

type hostsMaxNonce struct {
	atomic.Uint32
}

func (m *hostsMaxNonce) MaxNonce() uint32 { return m.Load() }

type diffOnlyVoter struct {
	acceptingVoter
}

func (v diffOnlyVoter) Send(ctx context.Context, request host.HostRequest, stream io.Writer, receipt func(*host.HostResponse)) (*host.HostResponse, error) {
	request.Payload = nil
	return v.acceptingVoter.Send(ctx, request, stream, receipt)
}

func escrowAtTheHostsActiveCap(t *testing.T) (longResponseEscrow, uint32) {
	t.Helper()
	maxNonce := &hostsMaxNonce{}
	escrow := newVotingEscrow(t, func(voter acceptingVoter) user.HostClient { return diffOnlyVoter{voter} }, host.WithMaxNonceProvider(maxNonce))
	startedConfirmedAt(t, escrow, time.Now().Add(-2*time.Hour).Unix(), true)
	maxNonce.Store(uint32(escrow.session.Nonce() + types.FinalizeNonceReserve(len(escrow.signers))))
	escrow.registry = New(Deps{
		ServingSessions: func(context.Context, string) (EscrowSession, error) {
			return keptOpenSession{EscrowSession: escrow.handle}, nil
		},
		Now: fixedClock(),
	})
	return escrow, maxNonce.Load()
}

// Test flow:
//  1. Build an escrow on three in-process hosts, start one inference its executor confirmed two hours ago, then set the hosts' max_nonce so their active cap is exactly the escrow's nonce; the registry does not hold the escrow and rebuilds it to settle.
//  2. Share the chain's max_nonce with the registry, then run the pre-finalize sweep for the escrow.
//  3. Assert the sweep left the nonce at the hosts' active cap and returned no error.
//  4. Finalize the escrow; assert it succeeds, reaches settlement and holds a signature quorum at a nonce no higher than max_nonce.
func TestASettlementAtTheHostsActiveCapStaysUnderItAndReachesQuorum(t *testing.T) {
	escrow, maxNonce := escrowAtTheHostsActiveCap(t)
	activeCap := types.MaxActiveNonce(maxNonce, len(escrow.signers))
	escrow.registry.ShareChainMaxNonce(uint64(maxNonce))

	_, _, _, err := escrow.registry.SweepBeforeFinalize(context.Background(), longResponseEscrowID, 16)
	require.LessOrEqual(t, escrow.session.Nonce(), activeCap, "the sweep wrote diffs past the hosts' active cap")
	require.NoError(t, err)

	require.NoError(t, escrow.registry.Finalize(context.Background(), longResponseEscrowID))
	require.Equal(t, types.PhaseSettlement, escrow.machine.Phase())
	require.LessOrEqual(t, escrow.session.Nonce(), uint64(maxNonce))
	require.True(t, escrow.session.HasQuorumAt(escrow.session.Nonce()), "no signature quorum at nonce %d", escrow.session.Nonce())
}

// Test flow:
//  1. Build the escrow at the hosts' active cap that the registry rebuilds to settle, and never share the chain's max_nonce.
//  2. Run the pre-finalize sweep, then Finalize.
//  3. Assert both return ErrDevshardBusy, leave the nonce at the hosts' active cap and leave the escrow out of settlement.
func TestASettlementBeforeTheChainMaxNonceIsKnownWaitsInsteadOfPassingTheHostsCap(t *testing.T) {
	escrow, maxNonce := escrowAtTheHostsActiveCap(t)
	activeCap := types.MaxActiveNonce(maxNonce, len(escrow.signers))

	_, _, _, err := escrow.registry.SweepBeforeFinalize(context.Background(), longResponseEscrowID, 16)
	require.LessOrEqual(t, escrow.session.Nonce(), activeCap, "the sweep wrote diffs past the hosts' active cap")
	require.ErrorIs(t, err, gatewayescrow.ErrDevshardBusy)

	require.ErrorIs(t, escrow.registry.Finalize(context.Background(), longResponseEscrowID), gatewayescrow.ErrDevshardBusy)
	require.LessOrEqual(t, escrow.session.Nonce(), activeCap, "finalize wrote diffs past the hosts' active cap")
	require.NotEqual(t, types.PhaseSettlement, escrow.machine.Phase())
}

// Test flow:
//  1. Build a registry whose serving factory opens escrow 5, and share the chain's max_nonce of 1700.
//  2. Add escrow 5.
//  3. Assert the published session holds 1700.
func TestAnEscrowAddedAfterTheChainMaxNonceIsSharedPublishesWithIt(t *testing.T) {
	t.Parallel()
	session := newFakeSession("hostA")
	registry := New(Deps{ServingSessions: newSessions(map[string]*fakeSession{"5": session}).open, Now: fixedClock()})
	registry.ShareChainMaxNonce(1_700)

	require.NoError(t, registry.Add(context.Background(), "5", "llama"))

	require.Equal(t, uint32(1_700), session.chainMaxNonce.Load())
}

// Test flow:
//  1. Build the escrow at the hosts' active cap, publish it with Add as the boot does, and never share the chain's max_nonce.
//  2. Run the pre-finalize sweep, then Finalize, on the held live escrow.
//  3. Assert both return ErrDevshardBusy, leave the nonce at the hosts' active cap and leave the escrow out of settlement.
func TestAHeldSettlementBeforeTheChainMaxNonceIsKnownWaitsInsteadOfPassingTheHostsCap(t *testing.T) {
	escrow, maxNonce := escrowAtTheHostsActiveCap(t)
	require.NoError(t, escrow.registry.Add(context.Background(), longResponseEscrowID, "llama"))
	activeCap := types.MaxActiveNonce(maxNonce, len(escrow.signers))

	_, _, _, err := escrow.registry.SweepBeforeFinalize(context.Background(), longResponseEscrowID, 16)
	require.LessOrEqual(t, escrow.session.Nonce(), activeCap, "the sweep wrote diffs past the hosts' active cap")
	require.ErrorIs(t, err, gatewayescrow.ErrDevshardBusy)

	require.ErrorIs(t, escrow.registry.Finalize(context.Background(), longResponseEscrowID), gatewayescrow.ErrDevshardBusy)
	require.LessOrEqual(t, escrow.session.Nonce(), activeCap, "finalize wrote diffs past the hosts' active cap")
	require.NotEqual(t, types.PhaseSettlement, escrow.machine.Phase())
}

// Test flow:
//  1. Build the escrow at the hosts' active cap, publish it with Add, and never share the chain's max_nonce.
//  2. Take a settlement hold with Inspect, then retire the escrow so it stays draining.
//  3. Run the pre-finalize sweep, then Finalize, on the held draining escrow, and release the hold.
//  4. Assert both return ErrDevshardBusy, leave the nonce at the hosts' active cap and leave the escrow out of settlement.
func TestAHeldDrainingSettlementBeforeTheChainMaxNonceIsKnownWaitsInsteadOfPassingTheHostsCap(t *testing.T) {
	escrow, maxNonce := escrowAtTheHostsActiveCap(t)
	require.NoError(t, escrow.registry.Add(context.Background(), longResponseEscrowID, "llama"))
	activeCap := types.MaxActiveNonce(maxNonce, len(escrow.signers))
	_, release, err := escrow.registry.Inspect(context.Background(), longResponseEscrowID)
	require.NoError(t, err)
	require.NoError(t, escrow.registry.Retire(longResponseEscrowID))

	_, _, _, sweepErr := escrow.registry.SweepBeforeFinalize(context.Background(), longResponseEscrowID, 16)
	sweptNonce := escrow.session.Nonce()
	finalizeErr := escrow.registry.Finalize(context.Background(), longResponseEscrowID)
	release()

	require.LessOrEqual(t, sweptNonce, activeCap, "the sweep wrote diffs past the hosts' active cap")
	require.ErrorIs(t, sweepErr, gatewayescrow.ErrDevshardBusy)
	require.ErrorIs(t, finalizeErr, gatewayescrow.ErrDevshardBusy)
	require.LessOrEqual(t, escrow.session.Nonce(), activeCap, "finalize wrote diffs past the hosts' active cap")
	require.NotEqual(t, types.PhaseSettlement, escrow.machine.Phase())
}

// Test flow:
//  1. Build the escrow at the hosts' active cap, publish it with Add, and never share the chain's max_nonce.
//  2. Run the routine execution-timeout sweep with a minute of race grace.
//  3. Assert it swept nothing and left the nonce at the hosts' active cap.
//  4. Share the chain's max_nonce and sweep again; assert the due timeout is counted and the nonce stays at the cap.
func TestTheRoutineTimeoutSweepBeforeTheChainMaxNonceIsKnownWaitsInsteadOfPassingTheHostsCap(t *testing.T) {
	escrow, maxNonce := escrowAtTheHostsActiveCap(t)
	require.NoError(t, escrow.registry.Add(context.Background(), longResponseEscrowID, "llama"))
	activeCap := types.MaxActiveNonce(maxNonce, len(escrow.signers))

	due, applied, failed := escrow.registry.SweepExecutionTimeouts(context.Background(), time.Minute, 16)
	require.LessOrEqual(t, escrow.session.Nonce(), activeCap, "the routine sweep wrote diffs past the hosts' active cap")
	require.Equal(t, [3]int{0, 0, 0}, [3]int{due, applied, failed})

	escrow.registry.ShareChainMaxNonce(uint64(maxNonce))
	due, _, _ = escrow.registry.SweepExecutionTimeouts(context.Background(), time.Minute, 16)
	require.Equal(t, 1, due)
	require.LessOrEqual(t, escrow.session.Nonce(), activeCap, "the routine sweep wrote diffs past the hosts' active cap")
}

type sharingDuringAdd struct {
	keptOpenSession
	share func()
}

func (s sharingDuringAdd) UserSession() *user.Session {
	s.share()
	return s.keptOpenSession.UserSession()
}

func addRacingTheFirstShare(t *testing.T) (longResponseEscrow, uint32) {
	t.Helper()
	escrow, maxNonce := escrowAtTheHostsActiveCap(t)
	var firstShare sync.Once
	var registry *Registry
	registry = New(Deps{
		ServingSessions: func(context.Context, string) (EscrowSession, error) {
			return sharingDuringAdd{keptOpenSession: keptOpenSession{EscrowSession: escrow.handle}, share: func() {
				firstShare.Do(func() { registry.ShareChainMaxNonce(uint64(maxNonce)) })
			}}, nil
		},
		Now: fixedClock(),
	})
	escrow.registry = registry
	require.NoError(t, registry.Add(context.Background(), longResponseEscrowID, "llama"))
	return escrow, maxNonce
}

// Test flow:
//  1. Build the escrow at the hosts' active cap and Add it through a session that shares the chain's max_nonce for the first time while Add opens it, after Add read the registry's value and before it publishes.
//  2. Run the routine execution-timeout sweep with a minute of race grace.
//  3. Assert the due timeout is counted and the nonce stays at the hosts' active cap.
func TestTheRoutineTimeoutSweepAfterAnAddRacingTheFirstShareStaysUnderTheHostsCap(t *testing.T) {
	escrow, maxNonce := addRacingTheFirstShare(t)
	activeCap := types.MaxActiveNonce(maxNonce, len(escrow.signers))

	due, _, _ := escrow.registry.SweepExecutionTimeouts(context.Background(), time.Minute, 16)

	require.Equal(t, 1, due)
	require.LessOrEqual(t, escrow.session.Nonce(), activeCap, "the routine sweep wrote diffs past the hosts' active cap")
}

// Test flow:
//  1. Build the escrow at the hosts' active cap and Add it through a session that shares the chain's max_nonce for the first time while Add opens it, after Add read the registry's value and before it publishes.
//  2. Run the pre-finalize sweep, then Finalize, on the held live escrow.
//  3. Assert the sweep left the nonce at the hosts' active cap, and Finalize reached settlement at a nonce no higher than max_nonce.
func TestAHeldSettlementAfterAnAddRacingTheFirstShareStaysUnderTheHostsCap(t *testing.T) {
	escrow, maxNonce := addRacingTheFirstShare(t)
	activeCap := types.MaxActiveNonce(maxNonce, len(escrow.signers))

	_, _, _, err := escrow.registry.SweepBeforeFinalize(context.Background(), longResponseEscrowID, 16)
	require.NoError(t, err)
	require.LessOrEqual(t, escrow.session.Nonce(), activeCap, "the sweep wrote diffs past the hosts' active cap")

	require.NoError(t, escrow.registry.Finalize(context.Background(), longResponseEscrowID))
	require.Equal(t, types.PhaseSettlement, escrow.machine.Phase())
	require.LessOrEqual(t, escrow.session.Nonce(), uint64(maxNonce))
}
