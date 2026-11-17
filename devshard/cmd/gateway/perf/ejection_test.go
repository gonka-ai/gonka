package perf

import (
	"testing"
	"time"
)

func recordFailures(h *hostPerf, count int, at time.Time) {
	for range count {
		h.recordSample(Sample{Responsive: false}, at)
	}
}

// Test flow:
//  1. Build an ejection policy with a consecutive-fail threshold of 3.
//  2. Record 3 consecutive failures on a host.
//  3. Evaluate the policy.
//  4. Assert the host is ejected.
func TestEjectionPolicyEvaluateConsecutiveFailThresholdEjects(t *testing.T) {
	policy := newEjectionPolicy(3, 0.99, 1e9, time.Minute, 10*time.Minute)
	h := newHostPerf(10 * time.Minute)
	state := &ejectionState{}
	recordFailures(h, 3, testEpoch)

	policy.evaluate(h, state, testEpoch)

	if !state.ejected(testEpoch) {
		t.Fatalf("ejected() after %d consecutive failures (threshold 3) = false, want true", h.consecutiveFail)
	}
}

// Test flow:
//  1. Build an ejection policy with a consecutive-fail threshold of 3.
//  2. Record 2 consecutive failures on a host.
//  3. Evaluate the policy.
//  4. Assert the host is not ejected and the ejection count stays 0.
func TestEjectionPolicyEvaluateBelowConsecutiveFailThresholdDoesNotEject(t *testing.T) {
	policy := newEjectionPolicy(3, 0.99, 1e9, time.Minute, 10*time.Minute)
	h := newHostPerf(10 * time.Minute)
	state := &ejectionState{}
	recordFailures(h, 2, testEpoch)

	policy.evaluate(h, state, testEpoch)

	if state.ejected(testEpoch) {
		t.Fatalf("ejected() after 2 consecutive failures (threshold 3) = true, want false")
	}
	if state.ejectionCount != 0 {
		t.Fatalf("ejectionCount after a non-trigger = %d, want 0", state.ejectionCount)
	}
}

// Test flow:
//  1. Build an ejection policy with a minimum volume of 5.
//  2. Record 3 failures on a host: a 100% failure rate but below the minimum volume.
//  3. Evaluate the policy.
//  4. Assert the host is not ejected.
func TestEjectionPolicyEvaluateFailureRateBelowMinVolumeNeverEjects(t *testing.T) {
	policy := newEjectionPolicy(1000, 0.5, 5, time.Minute, 10*time.Minute)
	h := newHostPerf(10 * time.Minute)
	state := &ejectionState{}
	recordFailures(h, 3, testEpoch)

	policy.evaluate(h, state, testEpoch)

	if state.ejected(testEpoch) {
		t.Fatalf("ejected() at 100%% failure but volume 3 < minVolume 5 = true, want false")
	}
}

// Test flow:
//  1. Build an ejection policy with a minimum volume of 5.
//  2. Record 5 failures on a host: a 100% failure rate at the minimum volume.
//  3. Evaluate the policy.
//  4. Assert the host is ejected.
func TestEjectionPolicyEvaluateFailureRateAboveMinVolumeAndThresholdEjects(t *testing.T) {
	policy := newEjectionPolicy(1000, 0.5, 5, time.Minute, 10*time.Minute)
	h := newHostPerf(10 * time.Minute)
	state := &ejectionState{}
	recordFailures(h, 5, testEpoch)

	policy.evaluate(h, state, testEpoch)

	if !state.ejected(testEpoch) {
		t.Fatalf("ejected() at 100%% failure with volume 5 >= minVolume 5 = false, want true")
	}
}

// Test flow:
//  1. Build an ejection policy with a 100-second base backoff and a 250-second cap.
//  2. Record one failure that never resets, then evaluate exactly when each prior ejection window expires, three times.
//  3. Assert the ejected-until doubles from base to 2x base, then is clamped at the 250-second cap (3x base would exceed it).
//  4. Assert the ejection count increments to 1, 2, then 3 across the three evaluations.
func TestEjectionPolicyEvaluateBackoffLadderDoublesThenCaps(t *testing.T) {
	const base = 100 * time.Second
	const max = 250 * time.Second
	policy := newEjectionPolicy(1, 0.99, 1e9, base, max)
	h := newHostPerf(10 * time.Minute)
	state := &ejectionState{}
	recordFailures(h, 1, testEpoch)

	step1 := testEpoch
	policy.evaluate(h, state, step1)
	if want := step1.Add(base); !state.ejectedUntil.Equal(want) || state.ejectionCount != 1 {
		t.Fatalf("after 1st ejection: ejectedUntil=%v count=%d, want %v count=1", state.ejectedUntil, state.ejectionCount, want)
	}

	step2 := state.ejectedUntil
	policy.evaluate(h, state, step2)
	if want := step2.Add(2 * base); !state.ejectedUntil.Equal(want) || state.ejectionCount != 2 {
		t.Fatalf("after 2nd ejection: ejectedUntil=%v count=%d, want %v count=2", state.ejectedUntil, state.ejectionCount, want)
	}

	step3 := state.ejectedUntil
	policy.evaluate(h, state, step3)
	if want := step3.Add(max); !state.ejectedUntil.Equal(want) || state.ejectionCount != 3 {
		t.Fatalf("after 3rd ejection: ejectedUntil=%v count=%d, want %v count=3 (capped at max)", state.ejectedUntil, state.ejectionCount, want)
	}
}

// Test flow:
//  1. Build an ejection policy and eject a host with one failure.
//  2. Re-evaluate the policy midway through the ejection window while the host is still failing.
//  3. Assert the ejected-until and ejection count are unchanged.
func TestEjectionPolicyEvaluateDoesNotReExtendWhileAlreadyEjected(t *testing.T) {
	const base = 100 * time.Second
	policy := newEjectionPolicy(1, 0.99, 1e9, base, 250*time.Second)
	h := newHostPerf(10 * time.Minute)
	state := &ejectionState{}
	recordFailures(h, 1, testEpoch)
	policy.evaluate(h, state, testEpoch)
	firstEjectedUntil := state.ejectedUntil

	midWindow := testEpoch.Add(50 * time.Second)
	policy.evaluate(h, state, midWindow)

	if !state.ejectedUntil.Equal(firstEjectedUntil) || state.ejectionCount != 1 {
		t.Fatalf("re-evaluate mid-window: ejectedUntil=%v count=%d, want unchanged %v count=1", state.ejectedUntil, state.ejectionCount, firstEjectedUntil)
	}
}

// Test flow:
//  1. Build a zero-value ejection state.
//  2. Assert `ejected` reports false.
func TestEjectionStateEjectedFalseBeforeAnyEjection(t *testing.T) {
	state := &ejectionState{}
	if state.ejected(testEpoch) {
		t.Fatalf("ejected() on a zero-value state = true, want false")
	}
}

// Test flow:
//  1. Build an ejection state with an ejected-until 30 seconds after the epoch.
//  2. Assert `ejected` is true one second before expiry.
//  3. Assert `ejected` is false exactly at expiry and one second after expiry.
func TestEjectionStateEjectedRecoversWhenTimerExpires(t *testing.T) {
	state := &ejectionState{ejectedUntil: testEpoch.Add(30 * time.Second)}

	if !state.ejected(testEpoch.Add(29 * time.Second)) {
		t.Fatalf("ejected() 1s before expiry = false, want true")
	}
	if state.ejected(testEpoch.Add(30 * time.Second)) {
		t.Fatalf("ejected() exactly at expiry = true, want false (rejoined)")
	}
	if state.ejected(testEpoch.Add(31 * time.Second)) {
		t.Fatalf("ejected() 1s after expiry = true, want false (rejoined)")
	}
}

// Test flow:
//  1. Build an ejection policy with a 100-second base backoff and a 200-second healthy window, then eject a host with one failure.
//  2. Record one successful sample just after the ejection timer expires, resetting the consecutive-failure streak, and re-evaluate.
//  3. Assert the host is no longer ejected.
//  4. Assert the ejection count still holds at 1 because the healthy window has not elapsed yet.
func TestEjectionPolicyEvaluateDoesNotDecayBeforeHealthyWindowElapses(t *testing.T) {
	const base = 100 * time.Second
	const window = 200 * time.Second
	policy := newEjectionPolicy(1, 0.99, 1e9, base, window)
	h := newHostPerf(10 * time.Minute)
	state := &ejectionState{}
	recordFailures(h, 1, testEpoch)
	policy.evaluate(h, state, testEpoch)

	recoverTime := state.ejectedUntil.Add(time.Millisecond)
	success := Sample{Responsive: true}
	h.recordSample(success, recoverTime)
	policy.evaluate(h, state, recoverTime)

	if state.ejected(recoverTime) {
		t.Fatalf("ejected() just after timer expiry = true, want false (host rejoins immediately)")
	}
	if state.ejectionCount != 1 {
		t.Fatalf("ejectionCount 1ms after recovery = %d, want 1 (healthy window not elapsed yet)", state.ejectionCount)
	}
}

// Test flow:
//  1. Build an ejection policy with a 100-second base backoff and a 200-second healthy window, then eject a host with one failure.
//  2. Record a successful sample right after expiry, and evaluate again once a full healthy window has passed.
//  3. Assert the ejection count resets to 0.
//  4. Record a fresh single failure and evaluate again.
//  5. Assert it re-ejects at the base backoff, with the ejection count back to 1 rather than continuing the old multiplier.
func TestEjectionPolicyEvaluateDecaysEjectionCountAndResetsLadderAfterHealthyWindow(t *testing.T) {
	const base = 100 * time.Second
	const window = 200 * time.Second
	policy := newEjectionPolicy(1, 0.99, 1e9, base, window)
	h := newHostPerf(10 * time.Minute)
	state := &ejectionState{}
	recordFailures(h, 1, testEpoch)
	policy.evaluate(h, state, testEpoch)
	firstEjectedUntil := state.ejectedUntil

	recoverTime := firstEjectedUntil.Add(time.Millisecond)
	success := Sample{Responsive: true}
	h.recordSample(success, recoverTime)

	pastWindow := firstEjectedUntil.Add(window + time.Second)
	policy.evaluate(h, state, pastWindow)
	if state.ejectionCount != 0 {
		t.Fatalf("ejectionCount after a full healthy window = %d, want 0 (relaxed)", state.ejectionCount)
	}

	recordFailures(h, 1, pastWindow)
	policy.evaluate(h, state, pastWindow)
	if want := pastWindow.Add(base); !state.ejectedUntil.Equal(want) || state.ejectionCount != 1 {
		t.Fatalf("re-ejection after decay: ejectedUntil=%v count=%d, want %v count=1 (base, not punished at the old multiplier)", state.ejectedUntil, state.ejectionCount, want)
	}
}

// Test flow:
//  1. Build an ejection policy driven by failure rate, with a minimum volume of 20.
//  2. Record 25 failures (above the minimum volume, 100% failure rate) and evaluate.
//  3. Assert the host is ejected by the rate trigger.
//  4. Record one successful sample right after rejoin and evaluate again.
//  5. Assert the host is not re-ejected: stale decayed failures must not re-trigger the rate check.
func TestEjectionPolicyEvaluateDoesNotReEjectOnFirstSuccessAfterRateTrigger(t *testing.T) {
	policy := newEjectionPolicy(1000, 0.5, 20, 30*time.Second, 300*time.Second)
	h := newHostPerf(10 * time.Minute)
	state := &ejectionState{}
	recordFailures(h, 25, testEpoch)

	policy.evaluate(h, state, testEpoch)
	if !state.ejected(testEpoch) {
		t.Fatal("ejected() right after the rate trigger = false, want true")
	}

	rejoinTime := state.ejectedUntil.Add(time.Millisecond)
	success := Sample{Responsive: true}
	h.recordSample(success, rejoinTime)

	policy.evaluate(h, state, rejoinTime)
	if state.ejected(rejoinTime) {
		t.Fatal("ejected() after one success right after rejoin = true, want false (stale decayed failures must not re-trigger the rate check)")
	}
}

// Test flow:
//  1. Build an ejection policy with a consecutive-fail threshold of 5 and a failure-rate threshold of 0.15 over a minimum volume of 20.
//  2. Record 5 failures and evaluate, so the consecutive trigger ejects the host at rung 1.
//  3. Record 4 more failures while the host is still ejected, as attempts in flight at the trigger would.
//  4. After the ejection lapses, record 30 successes, evaluating after each.
//  5. Assert the host is never ejected again and stays at rung 1.
func TestEjectionPolicyEvaluateJudgesReleasedHostOnlyOnOutcomesAfterRelease(t *testing.T) {
	policy := newEjectionPolicy(5, 0.15, 20, 30*time.Second, 120*time.Second)
	h := newHostPerf(10 * time.Minute)
	state := &ejectionState{}
	recordFailures(h, 5, testEpoch)
	policy.evaluate(h, state, testEpoch)
	if !state.ejected(testEpoch) {
		t.Fatal("ejected() after 5 consecutive failures = false, want true")
	}

	duringEjection := testEpoch.Add(10 * time.Second)
	for range 4 {
		h.recordSample(Sample{Responsive: false}, duringEjection)
		policy.evaluate(h, state, duringEjection)
	}

	released := state.ejectedUntil
	for index := range 30 {
		at := released.Add(time.Duration(index+1) * 500 * time.Millisecond)
		h.recordSample(Sample{Responsive: true}, at)
		policy.evaluate(h, state, at)
		if state.ejected(at) {
			t.Fatalf("re-ejected at rung %d after %d successes since release, want no ejection", state.ejectionCount, index+1)
		}
	}
	if state.ejectionCount != 1 {
		t.Fatalf("ejectionCount after a clean release = %d, want 1", state.ejectionCount)
	}
}

// Test flow:
//  1. Build an ejection policy driven only by failure rate, with a minimum volume of 20.
//  2. Record 25 failures and evaluate, so the host is ejected at rung 1.
//  3. Record 4 more failures while the host is still ejected.
//  4. After the ejection lapses, record 19 failures at one instant, evaluating after each, and assert the host is not ejected below the minimum volume.
//  5. Record a 20th failure and assert the host is ejected again at rung 2.
func TestEjectionPolicyEvaluateReEjectsHostStillFailingAfterRelease(t *testing.T) {
	policy := newEjectionPolicy(1000, 0.15, 20, 30*time.Second, 120*time.Second)
	h := newHostPerf(10 * time.Minute)
	state := &ejectionState{}
	recordFailures(h, 25, testEpoch)
	policy.evaluate(h, state, testEpoch)

	duringEjection := testEpoch.Add(10 * time.Second)
	for range 4 {
		h.recordSample(Sample{Responsive: false}, duringEjection)
		policy.evaluate(h, state, duringEjection)
	}

	at := state.ejectedUntil.Add(time.Second)
	for range 19 {
		h.recordSample(Sample{Responsive: false}, at)
		policy.evaluate(h, state, at)
	}
	if state.ejected(at) {
		t.Fatal("ejected() after 19 failures since release (minimum volume 20) = true, want false")
	}
	h.recordSample(Sample{Responsive: false}, at)
	policy.evaluate(h, state, at)
	if !state.ejected(at) || state.ejectionCount != 2 {
		t.Fatalf("after 20 failures since release: ejected=%v count=%d, want ejected at rung 2", state.ejected(at), state.ejectionCount)
	}
}
