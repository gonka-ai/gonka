package main

import (
	"bytes"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/user"
)

// ghostMissObservationWindow outlasts the test session's refusal deadline (RefusalTimeout plus
// TimeoutBuffer). A shorter window would let "no miss" assertions pass before a miss could land.
const ghostMissObservationWindow = 1500 * time.Millisecond

// shortRefusalWindow shrinks the wait between burning a nonce and voting on it, so a test can
// observe the miss instead of the intent to raise one.
func shortRefusalWindow(t *testing.T) {
	t.Helper()
	saved := user.TimeoutBuffer
	user.TimeoutBuffer = 50 * time.Millisecond
	t.Cleanup(func() { user.TimeoutBuffer = saved })
}

// Accountability is on by default; both helpers restore that default so global state cannot leak
// between tests.
func enableGhostAccountability(t *testing.T) {
	t.Helper()
	ConfigureGhostAccountability("")
	t.Cleanup(func() { ConfigureGhostAccountability("") })
}

func disableGhostAccountability(t *testing.T) {
	t.Helper()
	ConfigureGhostAccountability("false")
	t.Cleanup(func() { ConfigureGhostAccountability("") })
}

func missesForSlot(t *testing.T, env *testProxyEnv, slot int) uint32 {
	t.Helper()
	stats, ok := env.sm.HostStatsFor(uint32(slot))
	require.True(t, ok, "slot %d has no host stats", slot)
	return stats.Missed
}

func waitForMiss(t *testing.T, env *testProxyEnv, slot int) {
	t.Helper()
	require.Eventually(t, func() bool { return missesForSlot(t, env, slot) > 0 },
		5*time.Second, 20*time.Millisecond,
		"burned nonce never produced a miss for slot %d", slot)
}

// A host that refuses everything is never dispatched to, so the only record of its refusal is the
// burned nonce. Unless that nonce is voted on, refusing costs the host nothing.
func TestGhostAccountability_ChargesTheHostForANonceItRefused(t *testing.T) {
	shortRefusalWindow(t)
	enableGhostAccountability(t)
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()

	prepared := prepareForGhost(t, env.session, "llama")
	slot := prepared.HostIdx()
	require.Zero(t, missesForSlot(t, env, slot), "precondition: no miss before the burn")

	env.proxy.redundancy.runGhostProbe(prepared, ghostThrottled, ghostThrottled.reason())

	waitForMiss(t, env, slot)
}

// The refusing host must not be contacted again: verifiers run the challenge, we do not.
func TestGhostAccountability_NeverContactsTheRefusingHost(t *testing.T) {
	shortRefusalWindow(t)
	enableGhostAccountability(t)
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()

	prepared := prepareForGhost(t, env.session, "llama")
	slot := prepared.HostIdx()

	env.proxy.redundancy.runGhostProbe(prepared, ghostCapability, ghostCapability.reason())
	waitForMiss(t, env, slot)

	require.Nil(t, env.killables[slot].LastRequest(),
		"accountability must not re-send to the host that refused")
}

// A host that serves nothing has to end up as visibly missed as one that accepts work and drops it.
// Charging one burn per window would leave the first at a fraction of the second's miss rate.
func TestGhostAccountability_ChargesEveryBurnNotJustTheFirst(t *testing.T) {
	shortRefusalWindow(t)
	enableGhostAccountability(t)
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()

	first := prepareForGhost(t, env.session, "llama")
	slot := first.HostIdx()
	var second *user.PreparedInference
	for attempt := 0; attempt < len(env.group) && second == nil; attempt++ {
		if candidate := prepareForGhost(t, env.session, "llama"); candidate.HostIdx() == slot {
			second = candidate
		}
	}
	require.NotNil(t, second, "a group of %d must reassign the slot within that many nonces", len(env.group))

	env.proxy.redundancy.runGhostProbe(first, ghostThrottled, ghostThrottled.reason())
	env.proxy.redundancy.runGhostProbe(second, ghostThrottled, ghostThrottled.reason())

	require.Eventually(t, func() bool { return missesForSlot(t, env, slot) >= 2 },
		5*time.Second, 20*time.Millisecond,
		"the second burn on the same host must be charged too")
}

// Charging hosts for undispatched nonces changes what the network bills for. It is on by default,
// but an operator who sees it misfire must be able to stop it without a rebuild.
func TestGhostAccountability_CanBeTurnedOff(t *testing.T) {
	shortRefusalWindow(t)
	disableGhostAccountability(t)
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()

	prepared := prepareForGhost(t, env.session, "llama")
	slot := prepared.HostIdx()

	env.proxy.redundancy.runGhostProbe(prepared, ghostThrottled, ghostThrottled.reason())

	require.Never(t, func() bool { return missesForSlot(t, env, slot) > 0 },
		ghostMissObservationWindow, 25*time.Millisecond,
		"an explicit off switch must stop the charging")
}

// A gateway started with nothing configured must already be charging.
func TestGhostAccountability_OnByDefault(t *testing.T) {
	ConfigureGhostAccountability("")
	require.True(t, ghostAccountabilityEnabled(), "unset means on")

	ConfigureGhostAccountability("false")
	require.False(t, ghostAccountabilityEnabled())

	ConfigureGhostAccountability("")
	require.True(t, ghostAccountabilityEnabled())
}

// PoC is unavailability the protocol grants and an empty queue is our own scheduling, so neither is
// the host's fault.
func TestGhostAccountability_SparesBurnsTheHostDidNotCause(t *testing.T) {
	for _, testCase := range []struct {
		name string
		kind ghostKind
	}{
		{"poc", ghostPoC},
		{"exclude", ghostExclude},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			shortRefusalWindow(t)
			enableGhostAccountability(t)
			env := setupTestProxy(t, 3, nil, true)
			env.proxy.redundancy.picker.stop()

			prepared := prepareForGhost(t, env.session, "llama")
			slot := prepared.HostIdx()

			env.proxy.redundancy.runGhostProbe(prepared, testCase.kind, testCase.kind.reason())

			require.Never(t, func() bool { return missesForSlot(t, env, slot) > 0 },
				ghostMissObservationWindow, 25*time.Millisecond,
				"%s burn must not charge the host", testCase.name)
		})
	}
}

func TestGhostAccountability_AccountableKinds(t *testing.T) {
	require.True(t, ghostThrottled.accountable())
	require.True(t, ghostCapability.accountable())
	require.False(t, ghostPoC.accountable())
	require.False(t, ghostExclude.accountable())
	require.False(t, ghostNone.accountable())
}

// stageRecorder captures what logInferenceStage wrote. Stage lines go through the standard logger in
// this mode, and the timeout runs on its own goroutine, so the buffer needs its own lock.
type stageRecorder struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (r *stageRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(p)
}

func (r *stageRecorder) saw(stage string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Contains(r.buf.String(), "stage="+stage)
}

func captureStages(t *testing.T) *stageRecorder {
	t.Helper()
	recorder := &stageRecorder{}
	previous := log.Writer()
	log.SetOutput(recorder)
	t.Cleanup(func() { log.SetOutput(previous) })
	return recorder
}

// HandleTimeout signals an applied timeout by returning an error, so branching on err reported every
// success as a failure: one production day logged 45,909 ghost_timeout_failed of which 44,896 had
// applied. A log that says the mechanism is broken while it works is worse than no log.
func TestGhostAccountability_ASuccessfulTimeoutIsNotLoggedAsAFailure(t *testing.T) {
	shortRefusalWindow(t)
	enableGhostAccountability(t)
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()
	captured := captureStages(t)

	prepared := prepareForGhost(t, env.session, "llama")
	env.proxy.redundancy.runGhostProbe(prepared, ghostThrottled, ghostThrottled.reason())
	waitForMiss(t, env, prepared.HostIdx())

	require.Eventually(t, func() bool { return captured.saw("ghost_timeout_applied") },
		2*time.Second, 25*time.Millisecond, "an applied timeout must say so")
	require.False(t, captured.saw("ghost_timeout_failed"), "and must not also say it failed")
}
