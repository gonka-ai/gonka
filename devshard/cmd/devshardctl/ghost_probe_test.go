package main

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func disableThrottleProbe(t *testing.T) {
	t.Helper()
	throttleProbeEnabled.Store(false)
	t.Cleanup(func() { throttleProbeEnabled.Store(true) })
}

func waitForHostContact(t *testing.T, env *testProxyEnv, slot int) {
	t.Helper()
	require.Eventually(t, func() bool { return env.killables[slot].LastRequest() != nil },
		5*time.Second, 20*time.Millisecond,
		"throttled burn never reached the host on slot %d", slot)
}

func TestThrottleProbe_ContactsTheHost(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()

	prepared := prepareForGhost(t, env.session, "llama")
	slot := prepared.HostIdx()
	require.Nil(t, env.killables[slot].LastRequest(), "precondition: no host contact before the burn")

	env.proxy.redundancy.runGhostProbe(prepared, ghostThrottled, ghostThrottled.reason())

	waitForHostContact(t, env, slot)
}

func TestThrottleProbe_AServedProbeRaisesNoMiss(t *testing.T) {
	shortRefusalWindow(t)
	enableGhostAccountability(t)
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()

	prepared := prepareForGhost(t, env.session, "llama")
	slot := prepared.HostIdx()

	env.proxy.redundancy.runGhostProbe(prepared, ghostThrottled, ghostThrottled.reason())
	waitForHostContact(t, env, slot)

	require.Never(t, func() bool { return missesForSlot(t, env, slot) > 0 },
		ghostMissObservationWindow, 25*time.Millisecond,
		"a served probe must not charge the host")
}

func TestThrottleProbe_AnUnservedProbeStillMisses(t *testing.T) {
	shortRefusalWindow(t)
	enableGhostAccountability(t)
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()

	prepared := prepareForGhost(t, env.session, "llama")
	slot := prepared.HostIdx()
	env.killables[slot].ForceError(errors.New("503 over capacity"))

	env.proxy.redundancy.runGhostProbe(prepared, ghostThrottled, ghostThrottled.reason())

	waitForMiss(t, env, slot)
}

func TestThrottleProbe_OffMeansTheSilentBurn(t *testing.T) {
	shortRefusalWindow(t)
	enableGhostAccountability(t)
	disableThrottleProbe(t)
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()

	prepared := prepareForGhost(t, env.session, "llama")
	slot := prepared.HostIdx()

	env.proxy.redundancy.runGhostProbe(prepared, ghostThrottled, ghostThrottled.reason())
	waitForMiss(t, env, slot)

	require.Nil(t, env.killables[slot].LastRequest(),
		"the probe is off, so the burn must stay silent")
}

func TestThrottleProbe_OnByDefault(t *testing.T) {
	require.True(t, throttleProbeEnabled.Load())
}

func TestThrottleProbeGate_AdmitsOneProbeAtATimePerParticipant(t *testing.T) {
	var gate throttleProbeGate
	now := time.Unix(1_700_000_000, 0)

	require.True(t, gate.admit("host-a", now))
	require.False(t, gate.admit("host-a", now.Add(time.Hour)),
		"a probe still in flight must not be joined by another, however long it takes")
	require.True(t, gate.admit("host-b", now), "the bound is per participant, not global")
}

func TestThrottleProbeGate_HoldsTheIntervalAfterRelease(t *testing.T) {
	var gate throttleProbeGate
	now := time.Unix(1_700_000_000, 0)

	require.True(t, gate.admit("host-a", now))
	gate.release("host-a", now)

	require.False(t, gate.admit("host-a", now.Add(throttleProbeMinInterval-time.Millisecond)),
		"a fast-failing host must not be retried sooner than a slow one")
	require.True(t, gate.admit("host-a", now.Add(throttleProbeMinInterval)))
}

func TestThrottleProbeGate_RefusesAnEmptyParticipant(t *testing.T) {
	var gate throttleProbeGate
	require.False(t, gate.admit("", time.Unix(1_700_000_000, 0)))
}

func TestThrottleProbe_AServedProbeReleasesTheHostFromQuarantine(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()

	limiter := NewParticipantRequestLimiter(10, 10)
	env.proxy.redundancy.participantLimiter = limiter

	prepared := prepareForGhost(t, env.session, "llama")
	slot := prepared.HostIdx()
	participantKey := env.proxy.redundancy.participantKeyForHost(slot)
	limiter.ObserveResult(participantKey, "/inference", http.StatusServiceUnavailable)
	require.True(t, limiter.IsBlocked(participantKey), "precondition: the host is quarantined")

	env.proxy.redundancy.runGhostProbe(prepared, ghostThrottled, ghostThrottled.reason())

	require.Eventually(t, func() bool { return !limiter.IsBlocked(participantKey) },
		5*time.Second, 20*time.Millisecond,
		"a host that served the probe must not stay quarantined")
}
