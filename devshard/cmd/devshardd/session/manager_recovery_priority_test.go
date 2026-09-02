package session

import (
	"context"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/bridge"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/storage"
	"devshard/stub"
)

// seedRecoveryManager creates count active sessions and a manager bound to them.
func seedRecoveryManager(t *testing.T, store storage.Storage, count int) *HostManager {
	t.Helper()
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	group := makeGroup(hosts)
	for i := 1; i <= count; i++ {
		require.NoError(t, store.CreateSession(storage.CreateSessionParams{
			EscrowID:       strconv.Itoa(i),
			EpochID:        7,
			Version:        testutil.RuntimeTestVersion,
			CreatorAddr:    user.Address(),
			Config:         defaultConfig(3),
			Group:          group,
			InitialBalance: 100000,
		}))
	}
	addresses := make([]string, len(group))
	for i, slot := range group {
		addresses[i] = slot.ValidatorAddress
	}
	return waitObsRepairsOnCleanup(t, NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil,
		testutil.RuntimeTestVersion, &mockBridge{
			escrow: &bridge.EscrowInfo{EscrowID: "1", Amount: 100000, CreatorAddress: user.Address(), Slots: addresses},
		}, nil, nil))
}

func (m *HostManager) loadedSessionCount() int {
	m.sessionsMutex.RLock()
	defer m.sessionsMutex.RUnlock()
	return len(m.sessions)
}

func (m *HostManager) loadedSessionIDs() []string {
	m.sessionsMutex.RLock()
	defer m.sessionsMutex.RUnlock()
	out := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// waitFor polls until cond holds, so tests do not depend on worker scheduling.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRecoveryQueue_HandsOutRequestedSessionsFirst(t *testing.T) {
	requested := map[string]bool{"4": true}
	q := &recoveryQueue{
		pending: []storage.ActiveSession{
			{EscrowID: "1"}, {EscrowID: "2"}, {EscrowID: "3"}, {EscrowID: "4"}, {EscrowID: "5"},
		},
		prioritize: func(id string) bool { return requested[id] },
	}

	sess, ok := q.next()
	require.True(t, ok)
	require.Equal(t, "4", sess.EscrowID, "a demanded escrow must be handed out before cold ones")

	// A request arriving mid-recovery reorders whatever is left.
	requested["3"] = true
	sess, ok = q.next()
	require.True(t, ok)
	require.Equal(t, "3", sess.EscrowID, "demand arriving mid-drain must overtake the remaining backlog")

	var rest []string
	for {
		sess, ok := q.next()
		if !ok {
			break
		}
		rest = append(rest, sess.EscrowID)
	}
	require.Equal(t, []string{"1", "2", "5"}, rest, "cold sessions keep list order")
	require.Zero(t, q.remaining())
}

func TestRecoveryQueue_DrainsWithoutPrioritizer(t *testing.T) {
	q := &recoveryQueue{pending: []storage.ActiveSession{{EscrowID: "1"}, {EscrowID: "2"}}}
	first, ok := q.next()
	require.True(t, ok)
	require.Equal(t, "1", first.EscrowID)
	second, ok := q.next()
	require.True(t, ok)
	require.Equal(t, "2", second.EscrowID)
	_, ok = q.next()
	require.False(t, ok)
}

func TestRecoveryGate_ParksColdWorkerUntilOnDemandDone(t *testing.T) {
	var gate recoveryGate
	gate.begin("requested")

	parked := make(chan struct{})
	go func() {
		gate.waitTurn("cold")
		close(parked)
	}()

	select {
	case <-parked:
		t.Fatal("cold worker passed the gate while an on-demand load was in flight")
	case <-time.After(50 * time.Millisecond):
	}

	gate.end()
	select {
	case <-parked:
	case <-time.After(3 * time.Second):
		t.Fatal("cold worker was not released after the on-demand load finished")
	}
}

// Work on an already-demanded escrow is what the caller is waiting for, so it
// must never be parked, however many on-demand loads are in flight.
func TestRecoveryGate_DoesNotParkRequestedEscrow(t *testing.T) {
	var gate recoveryGate
	gate.begin("a")
	gate.begin("b")

	done := make(chan struct{})
	go func() {
		gate.waitTurn("a")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("a worker recovering a demanded escrow was parked")
	}
}

// A cold worker that parks must resume as soon as its own escrow is demanded,
// without waiting for the unrelated request to finish.
func TestRecoveryGate_PromotesParkedWorkerWhenItsEscrowIsRequested(t *testing.T) {
	var gate recoveryGate
	gate.begin("other")

	resumed := make(chan struct{})
	go func() {
		gate.waitTurn("target")
		close(resumed)
	}()

	select {
	case <-resumed:
		t.Fatal("cold worker ran while an unrelated request was in flight")
	case <-time.After(50 * time.Millisecond):
	}

	gate.begin("target")
	defer gate.end()
	select {
	case <-resumed:
	case <-time.After(3 * time.Second):
		t.Fatal("parked worker was not promoted when its escrow became demanded")
	}
}

// The demand marker outlives the request so a later backlog pass still treats
// the escrow as warm.
func TestRecoveryGate_RequestedMarkerIsSticky(t *testing.T) {
	var gate recoveryGate
	gate.begin("5")
	require.True(t, gate.isRequested("5"))
	gate.end()
	require.True(t, gate.isRequested("5"), "demand must survive the request that recorded it")
	require.False(t, gate.isRequested("6"))
}

func TestRecoveryGate_NestedOnDemandLoadsHoldTheGate(t *testing.T) {
	var gate recoveryGate
	gate.begin("a")
	gate.begin("b")

	parked := make(chan struct{})
	go func() {
		gate.waitTurn("cold")
		close(parked)
	}()

	gate.end()
	select {
	case <-parked:
		t.Fatal("gate released while a second on-demand load was still in flight")
	case <-time.After(50 * time.Millisecond):
	}

	gate.end()
	select {
	case <-parked:
	case <-time.After(3 * time.Second):
		t.Fatal("cold worker was not released after the last on-demand load finished")
	}
}

// stop must release parked workers so shutdown cannot deadlock behind the gate.
func TestRecoveryGate_StopReleasesParkedWorker(t *testing.T) {
	var gate recoveryGate
	gate.begin("requested")

	parked := make(chan struct{})
	go func() {
		gate.waitTurn("cold")
		close(parked)
	}()

	gate.stop()
	select {
	case <-parked:
	case <-time.After(3 * time.Second):
		t.Fatal("stop did not release the parked worker")
	}
}

func TestRecoveryGate_IdleGateDoesNotBlock(t *testing.T) {
	var gate recoveryGate
	done := make(chan struct{})
	go func() {
		gate.waitTurn("cold")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("idle gate blocked a background worker")
	}
}

func TestRecoveryGate_RequestedSetIsBounded(t *testing.T) {
	var gate recoveryGate
	for i := 0; i < maxRequestedRecoveryEscrows+10; i++ {
		gate.begin(strconv.Itoa(i))
		gate.end()
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	require.Len(t, gate.requested, maxRequestedRecoveryEscrows,
		"demand set must not grow without bound")
}

// The end-to-end priority contract: demanded sessions are dequeued first and
// run to completion while a request is in flight, and only cold sessions wait.
func TestRecoverSessions_RunsRequestedSessionsWhileColdOnesWait(t *testing.T) {
	inner := newManagerTestStore(t)
	mgr := seedRecoveryManager(t, inner, 16)

	// Two live callers are waiting on these; neither request has finished.
	mgr.recoveryGate.begin("12")
	mgr.recoveryGate.begin("15")

	done := make(chan error, 1)
	go func() { done <- mgr.RecoverSessions() }()

	waitFor(t, "demanded sessions to be recovered", func() bool {
		return mgr.loadedSessionCount() >= 2
	})
	require.Equal(t, []string{"12", "15"}, mgr.loadedSessionIDs(),
		"only the demanded sessions may be recovered while requests are in flight")

	select {
	case err := <-done:
		t.Fatalf("recovery drained cold sessions while requests were in flight: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	require.Equal(t, []string{"12", "15"}, mgr.loadedSessionIDs(),
		"cold sessions must stay parked until the requests finish")

	mgr.recoveryGate.end()
	mgr.recoveryGate.end()
	require.NoError(t, <-done)
	require.Equal(t, 16, mgr.loadedSessionCount())
}

// With every worker busy on demanded sessions, nothing should be parked: the
// backlog drains even though on-demand loads are in flight the whole time.
func TestRecoverSessions_AllRequestedBacklogNeverParks(t *testing.T) {
	const sessions = 6
	inner := newManagerTestStore(t)
	mgr := seedRecoveryManager(t, inner, sessions)

	for i := 1; i <= sessions; i++ {
		mgr.recoveryGate.begin(strconv.Itoa(i))
	}
	// Every remaining session is demanded, so recovery must complete without
	// waiting for these requests to finish.
	defer func() {
		for i := 1; i <= sessions; i++ {
			mgr.recoveryGate.end()
		}
	}()

	done := make(chan error, 1)
	go func() { done <- mgr.RecoverSessions() }()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("recovery parked workers even though every session was demanded")
	}
	require.Equal(t, sessions, mgr.loadedSessionCount())
}

func TestRecoverSessionsContext_CancelledSkipsRemaining(t *testing.T) {
	inner := newManagerTestStore(t)
	mgr := seedRecoveryManager(t, inner, 8)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.NoError(t, mgr.RecoverSessionsContext(ctx), "cancellation is not a recovery failure")
	require.Zero(t, mgr.loadedSessionCount(), "cancelled recovery must leave sessions to lazy recovery")
}

func TestStartRecovery_MarksCompleteWhenBacklogDrains(t *testing.T) {
	inner := newManagerTestStore(t)
	mgr := seedRecoveryManager(t, inner, 4)

	mgr.recoveryGate.begin("cold-request")
	wait := mgr.StartRecovery(context.Background())
	require.False(t, mgr.RecoveryComplete(), "recovery must not report complete while the backlog is pending")

	mgr.recoveryGate.end()
	wait()
	require.True(t, mgr.RecoveryComplete())
	require.Equal(t, 4, mgr.loadedSessionCount())
}

// The wait func returned by StartRecovery is the shutdown hook, so it has to
// return even when workers are parked behind the gate.
func TestStartRecovery_WaitReleasesParkedWorkers(t *testing.T) {
	inner := newManagerTestStore(t)
	mgr := seedRecoveryManager(t, inner, 16)

	mgr.recoveryGate.begin("cold-request")
	defer mgr.recoveryGate.end()
	wait := mgr.StartRecovery(context.Background())

	returned := make(chan struct{})
	go func() {
		wait()
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown blocked behind the recovery gate")
	}
	require.True(t, mgr.RecoveryComplete())
}
