package session

import (
	"context"
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
	return NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil,
		testutil.RuntimeTestVersion, &mockBridge{
			escrow: &bridge.EscrowInfo{EscrowID: "1", Amount: 100000, CreatorAddress: user.Address(), Slots: addresses},
		}, nil, nil)
}

func (m *HostManager) loadedSessionCount() int {
	m.sessionsMutex.RLock()
	defer m.sessionsMutex.RUnlock()
	return len(m.sessions)
}

func TestRecoveryGate_ParksBackgroundWorkerUntilOnDemandDone(t *testing.T) {
	var gate recoveryGate
	gate.begin()

	parked := make(chan struct{})
	go func() {
		gate.waitTurn()
		close(parked)
	}()

	select {
	case <-parked:
		t.Fatal("worker passed the gate while an on-demand load was in flight")
	case <-time.After(50 * time.Millisecond):
	}

	gate.end()
	select {
	case <-parked:
	case <-time.After(3 * time.Second):
		t.Fatal("worker was not released after the on-demand load finished")
	}
}

func TestRecoveryGate_NestedOnDemandLoadsHoldTheGate(t *testing.T) {
	var gate recoveryGate
	gate.begin()
	gate.begin()

	parked := make(chan struct{})
	go func() {
		gate.waitTurn()
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
		t.Fatal("worker was not released after the last on-demand load finished")
	}
}

// stop must release parked workers so shutdown cannot deadlock behind the gate.
func TestRecoveryGate_StopReleasesParkedWorker(t *testing.T) {
	var gate recoveryGate
	gate.begin()

	parked := make(chan struct{})
	go func() {
		gate.waitTurn()
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
		gate.waitTurn()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("idle gate blocked a background worker")
	}
}

// A session a live caller is waiting on must not queue behind the backlog: the
// background workers park while an on-demand load holds the gate.
func TestRecoverSessions_OnDemandLoadParksBackgroundWorkers(t *testing.T) {
	inner := newManagerTestStore(t)
	mgr := seedRecoveryManager(t, inner, 16)

	mgr.recoveryGate.begin()

	done := make(chan error, 1)
	go func() { done <- mgr.RecoverSessions() }()

	select {
	case err := <-done:
		t.Fatalf("recovery drained the backlog while an on-demand load held the gate: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	require.Zero(t, mgr.loadedSessionCount(), "workers must not recover sessions while parked")

	mgr.recoveryGate.end()
	require.NoError(t, <-done)
	require.Equal(t, 16, mgr.loadedSessionCount())
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

	mgr.recoveryGate.begin()
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

	mgr.recoveryGate.begin()
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
