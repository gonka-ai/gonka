package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/user"
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

func TestThrottleProbeGate_SendsOnlyTheFirstBurnOfAWindow(t *testing.T) {
	var gate throttleProbeGate
	now := time.Unix(1_700_000_000, 0)

	require.Equal(t, throttleProbeSend, gate.decide("host-a", now, nil))
	require.Equal(t, throttleProbeWait, gate.decide("host-a", now.Add(time.Hour), nil),
		"a probe still in flight must not be joined by another, however long it takes")
	require.Equal(t, throttleProbeSend, gate.decide("host-b", now, nil), "the bound is per participant")
}

func windowAnswers(t *testing.T, served bool, inherited throttleProbeVerdict) {
	t.Helper()
	var gate throttleProbeGate
	now := time.Unix(1_700_000_000, 0)
	answers := []bool{}

	require.Equal(t, throttleProbeSend, gate.decide("host-a", now, nil))
	require.Equal(t, throttleProbeWait, gate.decide("host-a", now, func(answer bool) { answers = append(answers, answer) }))
	require.Equal(t, throttleProbeWait, gate.decide("host-a", now, func(answer bool) { answers = append(answers, answer) }))

	gate.release("host-a", now, served)

	require.Equal(t, []bool{served, served}, answers, "every waiter is handed the probe's own answer")
	require.Equal(t, inherited, gate.decide("host-a", now.Add(throttleProbeMinInterval-time.Millisecond), nil),
		"a burn inside the interval inherits the answer instead of polling the group")
	require.Equal(t, throttleProbeSend, gate.decide("host-a", now.Add(throttleProbeMinInterval), nil))
}

func TestThrottleProbeGate_AServedAnswerCarriesTheWholeWindow(t *testing.T) {
	windowAnswers(t, true, throttleProbeServed)
}

func TestThrottleProbeGate_AnUnservedAnswerCarriesTheWholeWindow(t *testing.T) {
	windowAnswers(t, false, throttleProbeUnserved)
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

func TestThrottleProbe_AnUnservedProbeHonoursTheAccountabilitySwitch(t *testing.T) {
	shortRefusalWindow(t)
	disableGhostAccountability(t)
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()

	prepared := prepareForGhost(t, env.session, "llama")
	slot := prepared.HostIdx()
	env.killables[slot].ForceError(errors.New("503 over capacity"))

	env.proxy.redundancy.runGhostProbe(prepared, ghostThrottled, ghostThrottled.reason())
	waitForHostContact(t, env, slot)

	require.Never(t, func() bool { return missesForSlot(t, env, slot) > 0 },
		ghostMissObservationWindow, 25*time.Millisecond,
		"accountability is off, so an unserved probe must not charge the host either")
}

// refuseAfterClient holds the probe open until the test releases it, so the second burn is decided
// by this probe rather than one of its own, and then refuses it.
type refuseAfterClient struct {
	releaseCh chan struct{}
}

func (c *refuseAfterClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error) {
	select {
	case <-c.releaseCh:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return nil, errors.New("host refuses the probe")
}

// twoBurnsOnOneSlot returns two prepared burns bound to the same host, so the second is decided by
// the probe the first sends rather than by a probe of its own.
func twoBurnsOnOneSlot(t *testing.T, env *testProxyEnv) (*user.PreparedInference, *user.PreparedInference, int) {
	t.Helper()
	first := prepareForGhost(t, env.session, "llama")
	slot := first.HostIdx()
	for attempt := 0; attempt < len(env.group); attempt++ {
		if candidate := prepareForGhost(t, env.session, "llama"); candidate.HostIdx() == slot {
			return first, candidate, slot
		}
	}
	t.Fatalf("a group of %d must reassign slot %d within that many nonces", len(env.group), slot)
	return nil, nil, 0
}

// A burn that waits on someone else's probe still has to be charged when that probe goes unanswered,
// or letting one probe speak for the window would quietly forgive every burn behind it.
func TestThrottleProbe_AnUnansweredProbeChargesTheBurnsWaitingOnIt(t *testing.T) {
	shortRefusalWindow(t)
	enableGhostAccountability(t)
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()
	first, second, slot := twoBurnsOnOneSlot(t, env)

	holdProbe := make(chan struct{})
	env.killables[slot].inner = &refuseAfterClient{releaseCh: holdProbe}

	env.proxy.redundancy.runGhostProbe(first, ghostThrottled, ghostThrottled.reason())
	env.proxy.redundancy.runGhostProbe(second, ghostThrottled, ghostThrottled.reason())
	close(holdProbe)

	require.Eventually(t, func() bool { return missesForSlot(t, env, slot) >= 2 },
		5*time.Second, 20*time.Millisecond,
		"the burn that waited on the unanswered probe must be charged too")
}

// The probe exists to tell a host that is refusing from one that is merely busy. A host that answers
// must not be charged for the burns that were waiting on that answer.
func TestThrottleProbe_AnAnsweredProbeClearsTheBurnsWaitingOnIt(t *testing.T) {
	shortRefusalWindow(t)
	enableGhostAccountability(t)
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()
	first, second, slot := twoBurnsOnOneSlot(t, env)

	holdProbe := make(chan struct{})
	env.killables[slot].inner = &releaseAfterClient{releaseCh: holdProbe}

	env.proxy.redundancy.runGhostProbe(first, ghostThrottled, ghostThrottled.reason())
	env.proxy.redundancy.runGhostProbe(second, ghostThrottled, ghostThrottled.reason())
	close(holdProbe)

	require.Never(t, func() bool { return missesForSlot(t, env, slot) > 0 },
		ghostMissObservationWindow, 20*time.Millisecond,
		"a host that answered the probe owes nothing for the burns that waited on it")
}
